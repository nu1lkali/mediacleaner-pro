package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"mediacleaner/internal/cleaner"
	"mediacleaner/internal/media"
	"mediacleaner/internal/rules"
)

// writableRootIDs 返回已开启「允许清理」的存储卷 ID 集合。
//
// rules.Engine 有同一条闸门，但一键清理（planFromGroups / planFromFiles）直接读分组，
// 绕过了规则引擎。缺了这一步的后果是：先生成一张几千项的清单，执行阶段才逐条
// ErrReadOnly 全跳过 —— 用户等半天，看到「成功 0 个，跳过 N 个」，以为功能坏了。
func (s *Server) writableRootIDs(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.st.Select(ctx, `SELECT id FROM roots WHERE writable=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			out[id] = true
		}
	}
	return out, rows.Err()
}

func (s *Server) handleListCleanTasks(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)
	items, total, err := s.exec.ListTasks(r.Context(), limit, offset)
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, map[string]any{"items": items, "total": total})
}

type createCleanReq struct {
	Name        string  `json:"name"`
	RuleIDs     []int64 `json:"rule_ids"`
	FileIDs     []int64 `json:"file_ids"`   // 手动勾选的文件（来自重复组页面）
	GroupKind   string  `json:"group_kind"` // 一键清理：把该类型分组里的非保留副本全部纳入
	ExecuteMode string  `json:"execute_mode"`
}

func (s *Server) handleCreateCleanTask(w http.ResponseWriter, r *http.Request) {
	var req createCleanReq
	if err := decodeBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	var (
		plan *rules.Plan
		err  error
	)
	switch {
	case len(req.FileIDs) > 0:
		plan, err = s.planFromFiles(r.Context(), req.FileIDs)
	case req.GroupKind != "":
		plan, err = s.planFromGroups(r.Context(), req.GroupKind)
	default:
		plan, err = s.ruleEngine.Evaluate(r.Context(), req.RuleIDs)
	}
	if err != nil {
		fail(w, badRequest(err.Error()))
		return
	}
	if len(plan.Items) == 0 {
		fail(w, badRequest("没有匹配到可清理的文件，请调整规则或先执行扫描"))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "清理任务"
	}
	task, err := s.exec.CreateFromPlan(r.Context(), name, req.RuleIDs, plan, req.ExecuteMode)
	if err != nil {
		fail(w, badRequest(err.Error()))
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "clean.plan", "clean_task", "", map[string]any{
		"task_id": task.ID, "items": len(plan.Items), "bytes": plan.TotalBytes,
	}, clientIP(r))
	ok(w, task)
}

// planFromFiles 把用户手动勾选的文件转成清理计划。
func (s *Server) planFromFiles(ctx context.Context, ids []int64) (*rules.Plan, error) {
	plan := &rules.Plan{
		Items:        []rules.PlanItem{},
		ByKind:       map[string]int{},
		BytKindBytes: map[string]int64{},
	}
	// 与 rules.Engine 保持同一道只读卷闸门：执行阶段的 ErrReadOnly 兜底仍然保留，
	// 但那只是兜底，不能用来代替计划阶段的前置判断。
	writable, err := s.writableRootIDs(ctx)
	if err != nil {
		return nil, err
	}
	readOnlySkipped := 0
	for _, id := range ids {
		var (
			path, name, ext, kind, hashStatus string
			size, mtimeNS, rootID             int64
		)
		err := s.st.Get(ctx, []any{&path, &name, &ext, &kind, &size, &mtimeNS, &rootID, &hashStatus},
			`SELECT path, name, ext, media_kind, size, mtime_ns, root_id, hash_status
			 FROM files WHERE id=? AND state='active'`, id)
		if err != nil {
			continue // 文件已被其他任务处理，跳过
		}
		if !writable[rootID] {
			readOnlySkipped++ // 只读卷：不纳入计划，避免生成一张必然全跳过的清单
			continue
		}
		it := rules.PlanItem{
			FileID: id, Path: path, Name: name, Ext: ext, Size: size, Kind: kind,
			MtimeNS: mtimeNS, RootID: rootID, Reason: "用户手动选择", RuleName: "手动清理",
		}
		// 附上所属分组的保留文件，便于核对
		var keeperPath string
		_ = s.st.Get(ctx, &keeperPath, `
			SELECT f.path FROM dup_group_members m
			JOIN dup_groups g ON g.id=m.group_id AND g.batch_active=1
			JOIN files f ON f.id=m.file_id
			WHERE m.is_keeper=1 AND m.group_id IN (
			  SELECT gm.group_id FROM dup_group_members gm
			  JOIN dup_groups g2 ON g2.id=gm.group_id AND g2.batch_active=1
			  WHERE gm.file_id=?)
			LIMIT 1`, id)
		it.KeeperPath = keeperPath
		plan.Items = append(plan.Items, it)
		plan.TotalBytes += size
		plan.ByKind[kind]++
		plan.BytKindBytes[kind] += size
	}
	if len(plan.Items) == 0 {
		if readOnlySkipped > 0 {
			return nil, fmt.Errorf(
				"这 %d 个文件所在的存储卷还没有开启「允许清理」，所以一个都没有纳入计划。"+
					"请先到「系统设置 → 存储卷」打开该卷的「允许清理」开关，再回来重新清理",
				readOnlySkipped)
		}
		return nil, errNoItems
	}
	if readOnlySkipped > 0 {
		plan.Notes = append(plan.Notes, fmt.Sprintf(
			"%d 个文件因所在存储卷未开启「允许清理」未纳入本次计划；确认无误后可在系统设置中开启该开关",
			readOnlySkipped))
	}
	return plan, nil
}

// planFromGroups 一键清理：把某一类重复组里的所有非保留副本纳入计划。
// 排除「在任意分组中被标记为保留」的文件，避免跨组误删；
// 标为 sampled 的分组会在执行清理时自动与保留副本逐字节比对。
func (s *Server) planFromGroups(ctx context.Context, kind string) (*rules.Plan, error) {
	rows, err := s.st.Select(ctx, `
		SELECT DISTINCT m.file_id
		FROM dup_group_members m
		JOIN dup_groups g ON g.id=m.group_id
		JOIN files f ON f.id=m.file_id
		WHERE g.kind=? AND g.batch_active=1 AND m.is_keeper=0 AND f.state='active'
		  AND NOT EXISTS (
		    SELECT 1 FROM dup_group_members k
		    JOIN dup_groups kg ON kg.id=k.group_id AND kg.batch_active=1
		    WHERE k.file_id=m.file_id AND k.is_keeper=1
		  )
		ORDER BY m.file_id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return s.planFromFiles(ctx, ids)
}

func (s *Server) handleGetCleanTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	task, err := s.exec.GetTask(r.Context(), id)
	if err != nil {
		fail(w, notFound("清理任务不存在"))
		return
	}
	ok(w, task)
}

// handleCleanItems 分页返回任务明细。
//
// 参数：filter=pending|done|failed|skipped|all（默认 all），limit（默认 200，0=全部），offset。
// 以前一次性返回全部行，几千项的清单在浏览器里渲染非常卡；现在界面只取
// 「正在执行 + 即将执行」的那一段，其余按需懒加载。计数始终是全量的。
func (s *Server) handleCleanItems(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	filter := strings.TrimSpace(r.URL.Query().Get("filter"))
	if filter == "" {
		filter = cleaner.ItemsAll
	}
	limit := queryInt(r, "limit", 200)
	if limit > 500 {
		limit = 500
	}
	if limit < 0 {
		limit = 0
	}
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	items, counts, err := s.exec.ItemsPage(r.Context(), id, filter, limit, offset)
	if err != nil {
		fail(w, err)
		return
	}
	matched := counts.Total
	switch filter {
	case cleaner.ItemsPending:
		matched = counts.Pending
	case cleaner.ItemsDone:
		matched = counts.Done
	case cleaner.ItemsFailed:
		matched = counts.Failed
	case cleaner.ItemsSkipped:
		matched = counts.Skipped
	}
	ok(w, map[string]any{
		"items":  items,
		"filter": filter,
		"limit":  limit,
		"offset": offset,
		"total":  counts.Total, "selected": counts.Selected,
		"pending": counts.Pending, "done": counts.Done,
		"failed": counts.Failed, "skipped": counts.Skipped,
		"total_bytes": counts.TotalBytes, "selected_bytes": counts.SelectedBytes,
		"matched":  matched,
		"has_more": limit > 0 && int64(offset+len(items)) < matched,
	})
}

func (s *Server) handleExecuteClean(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	// 可选的执行选项：skip_verify 跳过"与保留副本逐字节比对"。
	// 请求体可能为空，解析失败一律按「不跳过」处理，保证默认安全。
	var req struct {
		SkipVerify bool `json:"skip_verify"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	opts := cleaner.ExecOptions{SkipVerify: req.SkipVerify}
	// 执行是长任务，放到后台，前端轮询任务状态
	go func() {
		_ = s.exec.ExecuteWith(context.Background(), id, opts, actorOf(r), clientIP(r))
	}()
	ok(w, map[string]any{"ok": true, "message": "清理任务已开始执行"})
}

// handlePauseClean 暂停执行中的任务。暂停只是挂起，已完成项与隔离区记录都保留，
// 继续后从下一个文件接着做；想放弃请用取消。
func (s *Server) handlePauseClean(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.exec.Pause(r.Context(), id); err != nil {
		fail(w, conflict(err.Error()))
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "clean.pause", "clean_task", fmt.Sprint(id), nil, clientIP(r))
	ok(w, map[string]any{"ok": true, "message": "任务已暂停，点「继续」可接着执行"})
}

func (s *Server) handleResumeClean(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.exec.Resume(r.Context(), id); err != nil {
		fail(w, conflict(err.Error()))
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "clean.resume", "clean_task", fmt.Sprint(id), nil, clientIP(r))
	ok(w, map[string]any{"ok": true, "message": "任务已继续"})
}

func (s *Server) handleCancelClean(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.exec.Cancel(r.Context(), id); err != nil {
		fail(w, conflict(err.Error()))
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "clean.cancel", "clean_task", fmt.Sprint(id), nil, clientIP(r))
	ok(w, map[string]any{"ok": true, "message": "已取消执行，未处理的文件保持原样"})
}

func (s *Server) handleDeleteCleanTask(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.exec.DeleteTask(r.Context(), id); err != nil {
		fail(w, conflict(err.Error()))
		return
	}
	ok(w, map[string]any{"ok": true})
}

type selectItemReq struct {
	Selected bool `json:"selected"`
}

func (s *Server) handleSelectItem(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	var req selectItemReq
	if err := decodeBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	if err := s.exec.SetSelected(r.Context(), id, req.Selected); err != nil {
		fail(w, conflict(err.Error()))
		return
	}
	ok(w, map[string]any{"ok": true})
}

// ---------- 隔离区 ----------

func (s *Server) handleListQuarantine(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "quarantined"
	}
	kind := r.URL.Query().Get("kind")
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)
	items, total, err := s.quar.List(r.Context(), status, kind, limit, offset)
	if err != nil {
		fail(w, err)
		return
	}
	count, bytes, _ := s.quar.Stats(r.Context())
	ok(w, map[string]any{
		"items": items, "total": total,
		"quarantined_count": count, "quarantined_bytes": bytes,
		"retention_days": s.cfg.QuarantineRetentionDays,
	})
}

func (s *Server) handleGetQuarantine(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	item, err := s.quar.Get(r.Context(), id)
	if err != nil {
		fail(w, notFound("隔离记录不存在"))
		return
	}
	ok(w, item)
}

// handleQuarantineOrigin 反查隔离文件的主本（仍留在库里的同内容文件）。
//
// 两种用法：
//
//	GET /api/quarantine/{id}/origin        按隔离记录 id
//	GET /api/quarantine/origin?hash=xxx    按隔离区文件名（即内容哈希）
//
// 不需要重新扫描：优先走隔离时的分组关系（keeper），分组被重建后回落到
// 全文 BLAKE3 匹配。免校验隔离且分组已重建时才会找不到。
func (s *Server) handleQuarantineOrigin(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		id = 0
	}
	hash := strings.TrimSpace(r.URL.Query().Get("hash"))
	if hash != "" {
		// 支持把整个文件名（hash.mp4）粘进来，顺手去掉扩展名
		hash = strings.TrimSuffix(hash, filepath.Ext(hash))
	}
	info, err := s.quar.Origin(r.Context(), id, hash)
	if err != nil {
		fail(w, notFound("找不到对应的隔离记录或主本：" + err.Error()))
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "quarantine.origin", "quarantine", fmt.Sprint(info.QuarantineID),
		map[string]any{"method": info.Method, "matches": len(info.Matches)}, clientIP(r))
	ok(w, info)
}

// quarantinePathOK 校验 stored_path 确实落在隔离目录内。
// 该值来自自家 DB，正常不可能越界；这一步只是防止意外数据把任意路径变成下载口。
func (s *Server) quarantinePathOK(p string) error {
	qdir := filepath.Clean(s.cfg.QuarantineDir())
	cp := filepath.Clean(p)
	if cp == qdir || strings.HasPrefix(cp, qdir+string(filepath.Separator)) {
		return nil
	}
	return fmt.Errorf("存储路径不在隔离目录内: %s", p)
}

// handleQuarantineFile 直接输出隔离区里的实体文件（预览 / 下载）。
//
//	GET /api/quarantine/{id}/file
//
// stored_path 是入库时写好的 /cache 视角规范路径，容器内可直接读；
// http.ServeContent 自带 Range，视频预览可拖进度条。已永久删除或磁盘上
// 已不存在的记录返回 404，前端据此给明确提示。
func (s *Server) handleQuarantineFile(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	item, err := s.quar.Get(r.Context(), id)
	if err != nil {
		fail(w, notFound("隔离记录不存在"))
		return
	}
	if err := s.quarantinePathOK(item.StoredPath); err != nil {
		fail(w, badRequest(err.Error()))
		return
	}
	if _, err := os.Stat(item.StoredPath); err != nil {
		fail(w, notFound("隔离文件已不在磁盘上（可能已被永久删除或后台清理）"))
		return
	}
	name := filepath.Base(item.OrigPath)
	ct := media.ContentType(filepath.Ext(item.StoredPath))
	if err := media.ServeFile(w, r, item.StoredPath, name, ct); err != nil {
		// ServeContent 已写过响应头，这里只记日志，不再重复输出错误体
		log.Printf("serve quarantine file %d: %v", id, err)
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "quarantine.preview", "quarantine", fmt.Sprint(id),
		map[string]any{"kind": item.MediaKind}, clientIP(r))
}

// handleQuarantineThumb 输出隔离文件的缩略图（图片直接缩，视频 ffmpeg 抽帧）。
//
//	GET /api/quarantine/{id}/thumb?w=360
//
// 缓存键用负数 id —— 与文件表的正数 id 空间天然隔离，互不覆盖。
func (s *Server) handleQuarantineThumb(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	item, err := s.quar.Get(r.Context(), id)
	if err != nil {
		fail(w, notFound("隔离记录不存在"))
		return
	}
	if err := s.quarantinePathOK(item.StoredPath); err != nil {
		fail(w, badRequest(err.Error()))
		return
	}
	width := queryInt(r, "w", s.cfg.ThumbSize)
	if width <= 0 || width > 1600 {
		width = s.cfg.ThumbSize
	}
	// mtimeNS 只是缓存键，用隔离时间（毫秒 -> 纳秒）即可；时长未知传 0，
	// 抽帧时间会退到默认的第 1 秒。
	thumb, err := media.Thumb(s.cfg, item.MediaKind, item.StoredPath, -item.ID, item.QuarantinedAt*1000000, width, 0)
	if err != nil {
		var un *media.ThumbUnavailable
		if errors.As(err, &un) {
			writeJSON(w, http.StatusNotImplemented, &apiError{
				Status: 501, Code: "thumb_unavailable", Msg: un.Reason,
			})
			return
		}
		fail(w, serverError("生成缩略图失败: "+err.Error()))
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, thumb)
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.quar.Restore(r.Context(), id); err != nil {
		fail(w, conflict("恢复失败: "+err.Error()))
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "quarantine.restore", "quarantine", "", map[string]any{"id": id}, clientIP(r))
	ok(w, map[string]any{"ok": true})
}

func (s *Server) handlePurgeQuarantine(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.quar.Purge(r.Context(), id); err != nil {
		fail(w, conflict(err.Error()))
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "quarantine.purge", "quarantine", "", map[string]any{"id": id}, clientIP(r))
	ok(w, map[string]any{"ok": true})
}

func (s *Server) handlePurgeExpired(w http.ResponseWriter, r *http.Request) {
	n, err := s.quar.PurgeExpired(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "quarantine.purge_expired", "quarantine", "",
		map[string]any{"count": n}, clientIP(r))
	ok(w, map[string]any{"ok": true, "purged": n})
}

var errNoItems = &apiError{Status: 400, Code: "no_items", Msg: "没有可清理的文件"}
