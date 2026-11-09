package api

import (
	"context"
	"net/http"
	"strings"

	"mediacleaner/internal/cleaner"
	"mediacleaner/internal/rules"
)

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
	FileIDs     []int64 `json:"file_ids"` // 手动勾选的文件（来自重复组页面）
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
		it := rules.PlanItem{
			FileID: id, Path: path, Name: name, Ext: ext, Size: size, Kind: kind,
			MtimeNS: mtimeNS, RootID: rootID, Reason: "用户手动选择", RuleName: "手动清理",
		}
		// 附上所属分组的保留文件，便于核对
		var keeperPath string
		_ = s.st.Get(ctx, &keeperPath, `
			SELECT f.path FROM dup_group_members m
			JOIN files f ON f.id=m.file_id
			WHERE m.is_keeper=1 AND m.group_id IN (
			  SELECT group_id FROM dup_group_members WHERE file_id=?)
			LIMIT 1`, id)
		it.KeeperPath = keeperPath
		plan.Items = append(plan.Items, it)
		plan.TotalBytes += size
		plan.ByKind[kind]++
		plan.BytKindBytes[kind] += size
	}
	if len(plan.Items) == 0 {
		return nil, errNoItems
	}
	return plan, nil
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

func (s *Server) handleCleanItems(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	items, err := s.exec.Items(r.Context(), id, false)
	if err != nil {
		fail(w, err)
		return
	}
	var total, selected int64
	var bytes, selectedBytes int64
	for _, it := range items {
		total++
		bytes += it.Size
		if it.Selected {
			selected++
			selectedBytes += it.Size
		}
	}
	ok(w, map[string]any{
		"items":          items,
		"total":          total,
		"selected":       selected,
		"total_bytes":    bytes,
		"selected_bytes": selectedBytes,
		"by_kind":        summarize(items),
	})
}

func summarize(items []cleaner.Item) map[string]map[string]int64 {
	out := map[string]map[string]int64{}
	for _, it := range items {
		m, ok := out[it.Kind]
		if !ok {
			m = map[string]int64{"count": 0, "bytes": 0}
			out[it.Kind] = m
		}
		m["count"]++
		m["bytes"] += it.Size
	}
	return out
}

func (s *Server) handleExecuteClean(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	// 执行是长任务，放到后台，前端轮询任务状态
	go func() {
		_ = s.exec.Execute(context.Background(), id, actorOf(r), clientIP(r))
	}()
	ok(w, map[string]any{"ok": true, "message": "清理任务已开始执行"})
}

func (s *Server) handleCancelClean(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.exec.Cancel(id); err != nil {
		fail(w, conflict(err.Error()))
		return
	}
	ok(w, map[string]any{"ok": true})
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
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)
	items, total, err := s.quar.List(r.Context(), status, limit, offset)
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
