package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mediacleaner/internal/media"
	"mediacleaner/internal/scan"
	"mediacleaner/internal/store"
)

func dedupeInt64(in []int64) []int64 {
	seen := make(map[int64]bool, len(in))
	out := make([]int64, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// handleScanProfiles 返回可选的扫描预设（前端渲染选择卡片）。
func (s *Server) handleScanProfiles(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]any{
		"items": scan.Profiles(),
		"capabilities": media.Capabilities(),
	})
}

// ScanTask 是扫描任务的完整视图。
type ScanTask struct {
	ID           int64          `json:"id"`
	Name         string         `json:"name"`
	Mode         string         `json:"mode"`
	Status       string         `json:"status"`
	Phase        string         `json:"phase"`
	PhaseLabel   string         `json:"phase_label"`
	Priority     int            `json:"priority"`
	Options      scan.Options   `json:"options"`
	RootIDs      []int64        `json:"root_ids"`
	Progress     map[string]any `json:"progress"`
	CurrentPath  string         `json:"current_path"`
	FilesSeen    int64          `json:"files_seen"`
	FilesIndexed int64          `json:"files_indexed"`
	BytesScanned int64          `json:"bytes_scanned"`
	ErrorCount   int64          `json:"error_count"`
	QueuedAt     int64          `json:"queued_at"`
	StartedAt    *int64         `json:"started_at"`
	FinishedAt   *int64         `json:"finished_at"`
	Message      string         `json:"message"`
}

func (s *Server) handleListScans(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var total int
	if err := s.st.Get(r.Context(), &total, `SELECT COUNT(*) FROM scan_tasks`); err != nil {
		fail(w, err)
		return
	}
	rows, err := s.st.Select(r.Context(), `
		SELECT id, name, mode, status, phase, priority, options_json, root_ids_json, progress_json,
		       COALESCE(current_path,''), files_seen, files_indexed, bytes_scanned, error_count,
		       queued_at, started_at, finished_at, COALESCE(message,'')
		FROM scan_tasks ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		fail(w, err)
		return
	}
	defer rows.Close()
	items := []ScanTask{}
	for rows.Next() {
		t, err := scanTaskFromRows(rows)
		if err != nil {
			fail(w, err)
			return
		}
		items = append(items, *t)
	}
	ok(w, map[string]any{"items": items, "total": total})
}

type createScanReq struct {
	Name        string   `json:"name"`
	Mode        string   `json:"mode"`
	Profile     string   `json:"profile"`
	RootIDs     []int64  `json:"root_ids"`
	ExtraPaths  []string `json:"extra_paths"` // 手动输入的目录，未注册则自动注册
	OnlyKinds   []string `json:"only_kinds"`
	MinFileSize int64    `json:"min_file_size"`
	ExcludeDirs []string `json:"exclude_dirs"`
	ExcludeNames []string `json:"exclude_names"`
	MaxDepth    int      `json:"max_depth"`

	// 三个阶段能力可独立开关；传 null 表示按 Profile 预设
	DetectExact        *bool `json:"detect_exact"`
	DetectSimilarImage *bool `json:"detect_similar_image"`
	DetectSimilarVideo *bool `json:"detect_similar_video"`
	// 延迟校验：null 表示按 Profile 预设（极速=开，其余=关）
	DeferVerify *bool `json:"defer_verify"`
	// 完全免校验：扫描不读全文，清理时也不与保留副本比对。
	// 隐含 defer_verify=true；必须由界面二次确认后置 ack_skip_verify=true。
	SkipVerify    *bool `json:"skip_verify"`
	AckSkipVerify bool  `json:"ack_skip_verify"`

	DeepProbe      bool `json:"deep_probe"`
	FollowSymlinks bool `json:"follow_symlinks"`
	IncludeOther   bool `json:"include_other"`
	Threads        int  `json:"threads"`
	LimitMBps      int  `json:"limit_mbps"`
	Priority       int  `json:"priority"`
}

func (s *Server) handleCreateScan(w http.ResponseWriter, r *http.Request) {
	var req createScanReq
	if err := decodeBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	if req.Mode != "full" {
		req.Mode = "incremental"
	}

	// 手动输入的目录：能自动注册就注册（受 DataRoot 约束），不能注册则明确报错
	for _, p := range req.ExtraPaths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		root, err := s.roots.Add(r.Context(), scan.AddRootInput{Path: p, Writable: false})
		if err != nil {
			// 已存在则复用
			if existing, e2 := s.roots.FindByPath(r.Context(), p); e2 == nil {
				req.RootIDs = append(req.RootIDs, existing.ID)
				continue
			}
			fail(w, badRequest("路径 "+p+" 无法加入扫描: "+err.Error()))
			return
		}
		req.RootIDs = append(req.RootIDs, root.ID)
	}

	if len(req.RootIDs) == 0 {
		roots, err := s.roots.Enabled(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		for _, rs := range roots {
			req.RootIDs = append(req.RootIDs, rs.ID)
		}
	}
	if len(req.RootIDs) == 0 {
		fail(w, badRequest("请先添加至少一个存储卷，或在任务中指定扫描目录"))
		return
	}
	req.RootIDs = dedupeInt64(req.RootIDs)
	if req.Name == "" {
		req.Name = "扫描 " + time.Now().Format("2006-01-02 15:04")
	}
	opts := scan.Options{
		Mode:           req.Mode,
		Profile:        req.Profile,
		RootIDs:        req.RootIDs,
		OnlyKinds:      req.OnlyKinds,
		MinFileSize:    req.MinFileSize,
		ExcludeDirs:    req.ExcludeDirs,
		ExcludeNames:   req.ExcludeNames,
		MaxDepth:       req.MaxDepth,
		DeepProbe:      req.DeepProbe,
		FollowSymlinks: req.FollowSymlinks,
		IncludeOther:   req.IncludeOther,
		Threads:        req.Threads,
		LimitMBps:      req.LimitMBps,
	}
	// 显式传入的开关优先于预设
	explicit := req.DetectExact != nil || req.DetectSimilarImage != nil || req.DetectSimilarVideo != nil
	if req.DetectExact != nil {
		opts.DetectExact = *req.DetectExact
	}
	if req.DetectSimilarImage != nil {
		opts.DetectSimilarImage = *req.DetectSimilarImage
	}
	if req.DetectSimilarVideo != nil {
		opts.DetectSimilarVideo = *req.DetectSimilarVideo
	}
	opts.ApplyProfile(explicit)
	// 延迟校验：极速预设默认开启（分组只到采样证据，清理前再比对）。
	// 这个字段不参与 ApplyProfile，避免任务重载时被预设覆盖掉用户显式选择。
	if req.DeferVerify != nil {
		opts.DeferVerify = *req.DeferVerify
	} else if opts.Profile == scan.ProfileFast {
		opts.DeferVerify = true
	}
	// 完全免校验：扫描不读全文，清理时也不比对。
	// 必须带界面二次确认标记，防止脚本/误调用悄悄打开高风险选项。
	if req.SkipVerify != nil && *req.SkipVerify {
		if !req.AckSkipVerify {
			fail(w, badRequest("启用「完全免校验」前请先确认风险提示"))
			return
		}
		opts.SkipVerify = true
		opts.DeferVerify = true
	}
	// 视频关键帧需要 ffmpeg，缺失时明确降级而不是静默失败
	if opts.DetectSimilarVideo && !media.FFmpegAvailable() {
		fail(w, badRequest("当前环境未检测到 ffmpeg，无法启用视频相似度检测。可改用「仅完全重复」预设。"))
		return
	}
	optJSON, _ := json.Marshal(opts)
	rootJSON, _ := json.Marshal(req.RootIDs)
	prio := req.Priority
	if prio <= 0 {
		prio = 5
	}
	res, err := s.st.Exec(r.Context(), `
		INSERT INTO scan_tasks(name, mode, status, phase, priority, options_json, root_ids_json,
		                       progress_json, queued_at)
		VALUES(?,?,'pending','index',?,?,?,'{}',?)`,
		req.Name, req.Mode, prio, string(optJSON), string(rootJSON), store.NowMS())
	if err != nil {
		fail(w, err)
		return
	}
	id, _ := res.LastInsertId()
	auditDetail := map[string]any{
		"mode": req.Mode, "roots": req.RootIDs,
		"defer_verify": opts.DeferVerify, "skip_verify": opts.SkipVerify,
	}
	if opts.SkipVerify {
		// 高风险选择必须留下可追溯记录：谁、什么时候、对哪一批关掉了校验
		auditDetail["risk_accepted"] = "清理时不与保留副本逐字节比对，可能误删同大小不同内容的文件"
	}
	s.st.Audit(r.Context(), actorOf(r), "scan.create", "scan_task", fmt.Sprint(id),
		auditDetail, clientIP(r))
	if err := s.eng.Enqueue(id); err != nil {
		_, _ = s.st.Exec(r.Context(), `UPDATE scan_tasks SET status='failed', message=? WHERE id=?`, err.Error(), id)
		fail(w, conflict(err.Error()))
		return
	}
	task, err := s.getScanTask(r, id)
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, task)
}

func (s *Server) handleGetScan(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	task, err := s.getScanTask(r, id)
	if err != nil {
		fail(w, notFound("扫描任务不存在"))
		return
	}
	ok(w, task)
}

func (s *Server) handleCurrentScan(w http.ResponseWriter, r *http.Request) {
	var id int64
	err := s.st.Get(r.Context(), &id,
		`SELECT id FROM scan_tasks WHERE status IN ('running','paused') ORDER BY id DESC LIMIT 1`)
	if err != nil {
		ok(w, map[string]any{"task": nil})
		return
	}
	task, err := s.getScanTask(r, id)
	if err != nil {
		ok(w, map[string]any{"task": nil})
		return
	}
	ok(w, map[string]any{"task": task})
}

type scanControlReq struct {
	Action string `json:"action"` // pause | resume | cancel
}

func (s *Server) handleScanControl(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	var req scanControlReq
	if err := decodeBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	if err := s.eng.Control(id, req.Action); err != nil {
		fail(w, conflict(err.Error()))
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "scan."+req.Action, "scan_task", fmt.Sprint(id), nil, clientIP(r))
	task, err := s.getScanTask(r, id)
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, task)
}

func (s *Server) handleScanErrors(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	limit := queryInt(r, "limit", 200)
	rows, err := s.st.Select(r.Context(), `
		SELECT COALESCE(path,''), COALESCE(code,''), COALESCE(message,''), ts
		FROM scan_errors WHERE task_id=? ORDER BY id DESC LIMIT ?`, id, limit)
	if err != nil {
		fail(w, err)
		return
	}
	defer rows.Close()
	type errRow struct {
		Path    string `json:"path"`
		Code    string `json:"code"`
		Message string `json:"message"`
		TS      int64  `json:"ts"`
	}
	items := []errRow{}
	for rows.Next() {
		var e errRow
		if err := rows.Scan(&e.Path, &e.Code, &e.Message, &e.TS); err != nil {
			fail(w, err)
			return
		}
		items = append(items, e)
	}
	ok(w, map[string]any{"items": items})
}

func (s *Server) handleDeleteScan(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	var status string
	if err := s.st.Get(r.Context(), &status, `SELECT status FROM scan_tasks WHERE id=?`, id); err != nil {
		fail(w, notFound("扫描任务不存在"))
		return
	}
	if status == "running" || status == "paused" {
		fail(w, conflict("任务正在执行，请先取消"))
		return
	}
	if _, err := s.st.Exec(r.Context(), `DELETE FROM scan_tasks WHERE id=?`, id); err != nil {
		fail(w, err)
		return
	}
	_, _ = s.st.Exec(r.Context(), `DELETE FROM scan_errors WHERE task_id=?`, id)
	ok(w, map[string]any{"ok": true})
}

// handleScanEvents 用 SSE 推送实时进度。
func (s *Server) handleScanEvents(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	flusher, okFlush := w.(http.Flusher)
	if !okFlush {
		fail(w, serverError("当前服务器不支持流式响应"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, unsubscribe := s.eng.Subscribe()
	defer unsubscribe()

	send := func(p scan.Progress) bool {
		b, err := json.Marshal(p)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: progress\ndata: %s\n\n", b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// 先推一帧当前快照，避免打开页面时空等
	if task, err := s.getScanTask(r, id); err == nil {
		send(scan.Progress{
			TaskID: task.ID, Status: task.Status, Phase: task.Phase,
			PhaseLabel: task.PhaseLabel, CurrentPath: task.CurrentPath,
			FilesSeen: task.FilesSeen, FilesIndexed: task.FilesIndexed,
			BytesScanned: task.BytesScanned, ErrorCount: task.ErrorCount,
			Message: task.Message,
		})
	}

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case p, open := <-ch:
			if !open {
				return
			}
			if p.TaskID != id {
				continue
			}
			if !send(p) {
				return
			}
			if p.Status == scan.StatusSuccess || p.Status == scan.StatusFailed || p.Status == scan.StatusCanceled {
				// 终态：再发一次 done 事件让前端停止重连
				_, _ = fmt.Fprintf(w, "event: done\ndata: %s\n\n", p.Status)
				flusher.Flush()
				return
			}
		case <-ticker.C:
			// 有些阶段（例如给大文件算全文哈希）可能几分钟没有新进度事件，
			// 这时顺手把库里的快照再推一次，前端的数字就不会一直停着。
			if task, err := s.getScanTask(r, id); err == nil {
				if !send(scan.Progress{
					TaskID: task.ID, Status: task.Status, Phase: task.Phase,
					PhaseLabel: task.PhaseLabel, CurrentPath: task.CurrentPath,
					FilesSeen: task.FilesSeen, FilesIndexed: task.FilesIndexed,
					BytesScanned: task.BytesScanned, ErrorCount: task.ErrorCount,
					Message: task.Message,
				}) {
					return
				}
				if task.Status == scan.StatusSuccess || task.Status == scan.StatusFailed || task.Status == scan.StatusCanceled {
					_, _ = fmt.Fprintf(w, "event: done\ndata: %s\n\n", task.Status)
					flusher.Flush()
					return
				}
			} else {
				_, _ = fmt.Fprintf(w, ": keepalive\n\n")
				flusher.Flush()
			}
		}
	}
}

// ---------- 组装 ----------

func (s *Server) getScanTask(r *http.Request, id int64) (*ScanTask, error) {
	rows, err := s.st.Select(r.Context(), `
		SELECT id, name, mode, status, phase, priority, options_json, root_ids_json, progress_json,
		       COALESCE(current_path,''), files_seen, files_indexed, bytes_scanned, error_count,
		       queued_at, started_at, finished_at, COALESCE(message,'')
		FROM scan_tasks WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, fmt.Errorf("not found")
	}
	return scanTaskFromRows(rows)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTaskFromRows(rows rowScanner) (*ScanTask, error) {
	var (
		t                    ScanTask
		optRaw, rootRaw, prg string
		si, fi               any
	)
	if err := rows.Scan(&t.ID, &t.Name, &t.Mode, &t.Status, &t.Phase, &t.Priority,
		&optRaw, &rootRaw, &prg, &t.CurrentPath, &t.FilesSeen, &t.FilesIndexed,
		&t.BytesScanned, &t.ErrorCount, &t.QueuedAt, &si, &fi, &t.Message); err != nil {
		return nil, err
	}
	t.StartedAt = toNullInt(si)
	t.FinishedAt = toNullInt(fi)
	_ = json.Unmarshal([]byte(optRaw), &t.Options)
	_ = json.Unmarshal([]byte(rootRaw), &t.RootIDs)
	t.Progress = map[string]any{}
	_ = json.Unmarshal([]byte(prg), &t.Progress)
	t.PhaseLabel = scan.PhaseLabel(t.Phase)
	return &t, nil
}

func toNullInt(v any) *int64 {
	switch x := v.(type) {
	case nil:
		return nil
	case int64:
		if x == 0 {
			return nil
		}
		return &x
	case []byte:
		if n, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return &n
		}
	case string:
		if n, err := strconv.ParseInt(x, 10, 64); err == nil {
			return &n
		}
	}
	return nil
}
