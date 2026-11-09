package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"mediacleaner/internal/export"
)

type exportReq struct {
	Kind        string `json:"kind"` // clean | groups | scan
	Format      string `json:"format"`
	CleanTaskID int64  `json:"clean_task_id"`
	ScanTaskID  int64  `json:"scan_task_id"`
	KindFilter  string `json:"kind_filter"`
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	var req exportReq
	if err := decodeBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	if req.Format == "" {
		req.Format = "csv"
	}

	var (
		rows  []export.Row
		base  string
		err   error
	)
	switch req.Kind {
	case "clean":
		rows, base, err = s.exportClean(r, req.CleanTaskID)
	case "groups":
		rows, base, err = s.exportGroups(r, req.KindFilter)
	case "scan":
		rows, base, err = s.exportFiles(r, req.ScanTaskID)
	default:
		fail(w, badRequest("不支持的导出类型: "+req.Kind))
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	if len(rows) == 0 {
		fail(w, badRequest("没有可导出的数据"))
		return
	}

	path, count, err := export.Write(s.cfg.ExportDir(), base, req.Format, rows)
	if err != nil {
		fail(w, serverError("生成导出文件失败: "+err.Error()))
		return
	}
	name := filepath.Base(path)
	if _, err := s.st.Exec(r.Context(), `
		INSERT INTO export_jobs(kind, fmt, scope_json, file_path, rows, status, created_at)
		VALUES(?,?,?,?,?, 'ready', ?)`,
		req.Kind, req.Format, fmt.Sprintf(`{"clean_task_id":%d,"scan_task_id":%d}`,
			req.CleanTaskID, req.ScanTaskID), path, count, nowMS()); err != nil {
		// 记录失败不影响导出本身
		_ = err
	}
	s.st.Audit(r.Context(), actorOf(r), "export.create", "export", name,
		map[string]any{"kind": req.Kind, "format": req.Format, "rows": count}, clientIP(r))
	ok(w, map[string]any{
		"file":         name,
		"path":         path,
		"rows":         count,
		"download_url": "/api/export/download/" + name,
	})
}

func (s *Server) exportClean(r *http.Request, taskID int64) ([]export.Row, string, error) {
	q := `SELECT i.path, i.name, i.size, i.media_kind, COALESCE(i.group_id,0),
	             COALESCE(i.keeper_path,''), COALESCE(i.reason,''), COALESCE(i.hit_rules,''),
	             COALESCE(i.assoc_of,0), COALESCE(i.error,''), COALESCE(i.executed_at,0),
	             COALESCE(qi.quarantined_at,0), COALESCE(t.created_at,0), i.action
	      FROM clean_items i
	      LEFT JOIN clean_tasks t ON t.id=i.task_id
	      LEFT JOIN quarantine_items qi ON qi.id=i.quarantine_id`
	args := []any{}
	if taskID > 0 {
		q += ` WHERE i.task_id=?`
		args = append(args, taskID)
	}
	q += ` ORDER BY i.size DESC`
	rows, err := s.st.Select(r.Context(), q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	out := []export.Row{}
	for rows.Next() {
		var (
			r        export.Row
			groupID  int64
			assocOf  int64
			executed int64
			quarAt   int64
			created  int64
			action   string
		)
		if err := rows.Scan(&r.Path, &r.Name, &r.Size, &r.Kind, &groupID, &r.KeeperPath,
			&r.Reason, &r.Rule, &assocOf, &r.Error, &executed, &quarAt, &created, &action); err != nil {
			return nil, "", err
		}
		r.ScanTime = created
		r.CleanTime = quarAt
		if r.CleanTime == 0 {
			r.CleanTime = executed
		}
		if groupID > 0 {
			r.GroupNo = fmt.Sprintf("#%03d", groupID)
		}
		if assocOf > 0 {
			r.Assoc = "是"
		}
		r.Result = actionLabel(action)
		if action == "quarantined" || action == "deleted" {
			r.Released = r.Size
		}
		out = append(out, r)
	}
	base := "clean-result"
	if taskID > 0 {
		base = fmt.Sprintf("clean-task-%d", taskID)
	}
	return out, base, rows.Err()
}

func actionLabel(a string) string {
	switch a {
	case "quarantined":
		return "已移入隔离区"
	case "deleted":
		return "已永久删除"
	case "pending":
		return "待执行"
	case "failed":
		return "失败"
	case "skipped":
		return "已跳过"
	}
	return a
}

func (s *Server) exportGroups(r *http.Request, kind string) ([]export.Row, string, error) {
	q := `SELECT g.id, g.kind, f.path, f.name, f.size, f.media_kind,
	             COALESCE(k.path,''), m.similarity, m.is_keeper, f.mtime_ns
	      FROM dup_groups g
	      JOIN dup_group_members m ON m.group_id=g.id
	      JOIN files f ON f.id=m.file_id
	      LEFT JOIN dup_group_members km ON km.group_id=g.id AND km.is_keeper=1
	      LEFT JOIN files k ON k.id=km.file_id`
	args := []any{}
	if kind != "" && kind != "all" {
		q += ` WHERE g.kind=?`
		args = append(args, kind)
	}
	q += ` ORDER BY g.reclaimable_bytes DESC, g.id, m.is_keeper DESC`
	rows, err := s.st.Select(r.Context(), q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []export.Row{}
	for rows.Next() {
		var (
			gid      int64
			gkind    string
			r        export.Row
			sim      float64
			isKeeper int
			mtime    int64
		)
		if err := rows.Scan(&gid, &gkind, &r.Path, &r.Name, &r.Size, &r.Kind,
			&r.KeeperPath, &sim, &isKeeper, &mtime); err != nil {
			return nil, "", err
		}
		r.GroupNo = fmt.Sprintf("#%03d", gid)
		r.ScanTime = mtime / 1e6
		if isKeeper == 1 {
			r.Reason = "分组保留文件"
			r.Result = "保留"
		} else {
			r.Reason = fmt.Sprintf("%s 类型重复，相似度 %.1f%%", gkindLabel(gkind), sim*100)
			r.Result = "候选删除"
			r.Released = r.Size
		}
		out = append(out, r)
	}
	return out, "duplicate-groups", rows.Err()
}

func gkindLabel(k string) string {
	switch k {
	case "exact":
		return "完全重复"
	case "image":
		return "相似图片"
	case "video":
		return "相似视频"
	}
	return k
}

func (s *Server) exportFiles(r *http.Request, scanTaskID int64) ([]export.Row, string, error) {
	q := `SELECT path, name, size, media_kind, mtime_ns, hash_status,
	             COALESCE(content_hash,'') FROM files WHERE state='active'`
	args := []any{}
	if scanTaskID > 0 {
		q += ` AND scan_task_id=?`
		args = append(args, scanTaskID)
	}
	q += ` ORDER BY size DESC LIMIT 200000`
	rows, err := s.st.Select(r.Context(), q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []export.Row{}
	for rows.Next() {
		var (
			r     export.Row
			mtime int64
			hs    string
			hash  string
		)
		if err := rows.Scan(&r.Path, &r.Name, &r.Size, &r.Kind, &mtime, &hs, &hash); err != nil {
			return nil, "", err
		}
		r.ScanTime = mtime / 1e6
		r.Result = "已索引"
		r.Reason = "校验状态: " + hs
		if hash != "" {
			r.Assoc = "BLAKE3 " + hash[:16]
		}
		out = append(out, r)
	}
	return out, "scan-index", rows.Err()
}

// handleDownload 只允许下载导出目录内的文件，文件名做严格校验防穿越。
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		fail(w, badRequest("非法文件名"))
		return
	}
	full := filepath.Join(s.cfg.ExportDir(), name)
	if !strings.HasPrefix(filepath.Clean(full), filepath.Clean(s.cfg.ExportDir())) {
		fail(w, forbidden("越权访问"))
		return
	}
	if _, err := os.Stat(full); err != nil {
		fail(w, notFound("导出文件不存在或已清理"))
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	http.ServeFile(w, r, full)
}
