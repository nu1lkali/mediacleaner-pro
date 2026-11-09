package api

import (
	"context"
	"net/http"
	"sort"
)

type kindStat struct {
	Kind  string `json:"kind"`
	Files int64  `json:"files"`
	Bytes int64  `json:"bytes"`
}

// handleDashboard 汇总首页所需的全部指标（单次请求，前端无需拼装）。
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 文件总览
	var totalFiles, totalBytes int64
	_ = s.st.Get(ctx, []any{&totalFiles, &totalBytes},
		`SELECT COUNT(*), COALESCE(SUM(size),0) FROM files WHERE state='active'`)

	rows, err := s.st.Select(ctx, `
		SELECT media_kind, COUNT(*), COALESCE(SUM(size),0)
		FROM files WHERE state='active' GROUP BY media_kind ORDER BY 3 DESC`)
	if err != nil {
		fail(w, err)
		return
	}
	kinds := []kindStat{}
	kindMap := map[string]kindStat{}
	for rows.Next() {
		var k kindStat
		if err := rows.Scan(&k.Kind, &k.Files, &k.Bytes); err != nil {
			rows.Close()
			fail(w, err)
			return
		}
		kinds = append(kinds, k)
		kindMap[k.Kind] = k
	}
	rows.Close()

	// 重复统计
	type dupStat struct {
		Groups    int64
		Removable int64
		Bytes     int64
	}
	var exact, image, video dupStat
	dupRows, err := s.st.Select(ctx, `
		SELECT kind, COUNT(*), COALESCE(SUM(member_count-1),0), COALESCE(SUM(reclaimable_bytes),0)
		FROM dup_groups WHERE member_count>1 GROUP BY kind`)
	if err == nil {
		for dupRows.Next() {
			var (
				kind string
				st   dupStat
			)
			if err := dupRows.Scan(&kind, &st.Groups, &st.Removable, &st.Bytes); err != nil {
				break
			}
			switch kind {
			case "exact":
				exact = st
			case "image":
				image = st
			case "video":
				video = st
			}
		}
		dupRows.Close()
	}

	// 清理统计
	var cleanedFiles, cleanedBytes int64
	_ = s.st.Get(ctx, []any{&cleanedFiles, &cleanedBytes},
		`SELECT COUNT(*), COALESCE(SUM(size),0) FROM quarantine_items WHERE status IN ('quarantined','purged','restored')`)

	qCount, qBytes, _ := s.quar.Stats(ctx)

	// 最近的扫描与清理任务
	recentScans, _ := s.recentScans(ctx, 5)
	recentCleans, _ := s.recentCleanTasks(ctx, 5)
	// 存储卷
	roots, _ := s.roots.List(ctx)

	ok(w, map[string]any{
		"files": map[string]any{
			"total": totalFiles,
			"bytes": totalBytes,
			"by_kind": kinds,
			"videos": kindMap["video"].Files,
			"images": kindMap["image"].Files,
			"audio":  kindMap["audio"].Files,
			"docs":   kindMap["document"].Files,
		},
		"duplicates": map[string]any{
			"exact_groups":  exact.Groups,
			"exact_files":   exact.Removable,
			"exact_bytes":   exact.Bytes,
			"image_groups":  image.Groups,
			"image_files":   image.Removable,
			"image_bytes":   image.Bytes,
			"video_groups":  video.Groups,
			"video_files":   video.Removable,
			"video_bytes":   video.Bytes,
			"reclaimable":   exact.Bytes + image.Bytes + video.Bytes,
		},
		"cleaned": map[string]any{
			"files": cleanedFiles,
			"bytes": cleanedBytes,
		},
		"quarantine": map[string]any{
			"count": qCount,
			"bytes": qBytes,
		},
		"roots":         roots,
		"recent_scans":  recentScans,
		"recent_cleans": recentCleans,
	})
}

func (s *Server) recentScans(ctx context.Context, limit int) ([]map[string]any, error) {
	rows, err := s.st.Select(ctx, `
		SELECT id, name, status, phase, files_seen, bytes_scanned, error_count, queued_at, finished_at
		FROM scan_tasks ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			id, seen, bytes, errs, queued int64
			name, status, phase          string
			finished                     *int64
		)
		var fi any
		if err := rows.Scan(&id, &name, &status, &phase, &seen, &bytes, &errs, &queued, &fi); err != nil {
			return nil, err
		}
		finished = toNullInt(fi)
		out = append(out, map[string]any{
			"id": id, "name": name, "status": status, "phase": phase,
			"files_seen": seen, "bytes_scanned": bytes, "error_count": errs,
			"queued_at": queued, "finished_at": finished,
		})
	}
	return out, rows.Err()
}

func (s *Server) recentCleanTasks(ctx context.Context, limit int) ([]map[string]any, error) {
	tasks, _, err := s.exec.ListTasks(ctx, limit, 0)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, map[string]any{
			"id": t.ID, "name": t.Name, "status": t.Status, "mode": t.ExecuteMode,
			"stats": t.Stats, "created_at": t.CreatedAt, "message": t.Message,
		})
	}
	return out, nil
}

// handleLibraryStats 媒体库构成分析。
func (s *Server) handleLibraryStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	extRows, err := s.st.Select(ctx, `
		SELECT ext, COUNT(*), COALESCE(SUM(size),0) FROM files
		WHERE state='active' GROUP BY ext ORDER BY 3 DESC LIMIT 25`)
	if err != nil {
		fail(w, err)
		return
	}
	type extStat struct {
		Ext   string `json:"ext"`
		Files int64  `json:"files"`
		Bytes int64  `json:"bytes"`
	}
	exts := []extStat{}
	for extRows.Next() {
		var e extStat
		if err := extRows.Scan(&e.Ext, &e.Files, &e.Bytes); err != nil {
			extRows.Close()
			fail(w, err)
			return
		}
		exts = append(exts, e)
	}
	extRows.Close()

	resRows, err := s.st.Select(ctx, `
		SELECT
		  CASE
		    WHEN width=0 OR height=0 THEN '未知'
		    WHEN width*height>=3840*2160 THEN '4K 及以上'
		    WHEN width*height>=2560*1440 THEN '2K'
		    WHEN width*height>=1920*1080 THEN '1080P'
		    WHEN width*height>=1280*720  THEN '720P'
		    WHEN width*height>=640*480   THEN '480P 及以下'
		    ELSE '更低'
		  END AS bucket,
		  COUNT(*), COALESCE(SUM(f.size),0)
		FROM files f JOIN file_media m ON m.file_id=f.id
		WHERE f.state='active' AND f.media_kind IN ('video','image')
		GROUP BY bucket ORDER BY 3 DESC`)
	if err != nil {
		fail(w, err)
		return
	}
	type resStat struct {
		Bucket string `json:"bucket"`
		Files  int64  `json:"files"`
		Bytes  int64  `json:"bytes"`
	}
	res := []resStat{}
	for resRows.Next() {
		var x resStat
		if err := resRows.Scan(&x.Bucket, &x.Files, &x.Bytes); err != nil {
			resRows.Close()
			fail(w, err)
			return
		}
		res = append(res, x)
	}
	resRows.Close()

	// 按存储卷
	rootRows, err := s.st.Select(ctx, `
		SELECT COALESCE(rt.name,'未知'), COALESCE(rt.volume,''), COUNT(f.id), COALESCE(SUM(f.size),0)
		FROM files f LEFT JOIN roots rt ON rt.id=f.root_id
		WHERE f.state='active' GROUP BY rt.id ORDER BY 4 DESC`)
	if err != nil {
		fail(w, err)
		return
	}
	type rootStat struct {
		Name   string `json:"name"`
		Volume string `json:"volume"`
		Files  int64  `json:"files"`
		Bytes  int64  `json:"bytes"`
	}
	rootStats := []rootStat{}
	for rootRows.Next() {
		var x rootStat
		if err := rootRows.Scan(&x.Name, &x.Volume, &x.Files, &x.Bytes); err != nil {
			rootRows.Close()
			fail(w, err)
			return
		}
		rootStats = append(rootStats, x)
	}
	rootRows.Close()

	ok(w, map[string]any{
		"by_ext":        exts,
		"by_resolution": res,
		"by_root":       rootStats,
	})
}

// handleSuggestions 生成"空间清理建议"，只做分析，不自动删除。
func (s *Server) handleSuggestions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	type suggestion struct {
		Key      string  `json:"key"`
		Title    string  `json:"title"`
		Detail   string  `json:"detail"`
		Files    int64   `json:"files"`
		Bytes    int64   `json:"bytes"`
		Severity string  `json:"severity"` // info | warn | ok
	}
	out := []suggestion{}

	// 1. 完全重复
	var f, b int64
	_ = s.st.Get(ctx, []any{&f, &b}, `
		SELECT COALESCE(SUM(member_count-1),0), COALESCE(SUM(reclaimable_bytes),0)
		FROM dup_groups WHERE kind='exact' AND member_count>1`)
	out = append(out, suggestion{
		Key: "exact", Title: "完全重复文件",
		Detail: "内容逐字节相同，删除副本不影响任何数据。建议保留路径层级最浅的一份。",
		Files:  f, Bytes: b, Severity: severityFor(b),
	})

	// 2. 相似图片 / 视频
	for _, k := range []struct{ key, title, detail string }{
		{"image", "视觉相似的图片", "感知哈希相近，多为同图不同压缩/尺寸。删除前请核对分辨率。"},
		{"video", "内容相似的视频", "关键帧匹配，多为转码或不同码率版本。建议保留分辨率与码率最高者。"},
	} {
		var nf, nb int64
		_ = s.st.Get(ctx, []any{&nf, &nb}, `
			SELECT COALESCE(SUM(member_count-1),0), COALESCE(SUM(reclaimable_bytes),0)
			FROM dup_groups WHERE kind=? AND member_count>1`, k.key)
		out = append(out, suggestion{Key: k.key, Title: k.title, Detail: k.detail,
			Files: nf, Bytes: nb, Severity: severityFor(nb)})
	}

	// 3. 低分辨率视频
	var lf, lb int64
	_ = s.st.Get(ctx, []any{&lf, &lb}, `
		SELECT COUNT(*), COALESCE(SUM(f.size),0)
		FROM files f JOIN file_media m ON m.file_id=f.id
		WHERE f.state='active' AND f.media_kind='video'
		  AND m.width>0 AND m.width*m.height < 1280*720`)
	out = append(out, suggestion{
		Key: "low_res", Title: "低分辨率视频（低于 720P）",
		Detail: "若已有高清版本，这些文件可考虑清理；单独存在时请谨慎，可能是唯一副本。",
		Files: lf, Bytes: lb, Severity: severityFor(lb),
	})

	// 4. 孤立附属文件（同目录同名主体已无媒体文件）
	var orphanFiles, orphanBytes int64
	_ = s.st.Get(ctx, []any{&orphanFiles, &orphanBytes}, `
		SELECT COUNT(*), COALESCE(SUM(f.size),0)
		FROM files f
		WHERE f.state='active' AND f.ext IN ('.nfo','.srt','.ass','.ssa','.sub','.idx','.sfv')
		  AND NOT EXISTS (
		    SELECT 1 FROM files t
		    WHERE t.state='active' AND t.dir_path=f.dir_path AND t.stem=f.stem
		      AND t.media_kind IN ('video','audio','image')
		  )`)
	out = append(out, suggestion{
		Key: "orphan_sidecar", Title: "孤立的字幕/信息文件",
		Detail: "所在目录已没有同名媒体文件，这些附属文件大概率已无用处。",
		Files: orphanFiles, Bytes: orphanBytes, Severity: severityFor(orphanBytes),
	})

	// 5. 长期未修改的大文件（>1GB 且 2 年未动）
	var sf, sb int64
	_ = s.st.Get(ctx, []any{&sf, &sb}, `
		SELECT COUNT(*), COALESCE(SUM(size),0) FROM files
		WHERE state='active' AND size > 1073741824
		  AND mtime_ns < ?`, (nowMS()-2*365*86400000)*1000000)
	out = append(out, suggestion{
		Key: "stale_big", Title: "两年未修改的超大文件（>1GB）",
		Detail: "仅提示，不建议自动处理。可人工确认是否为归档素材。",
		Files: sf, Bytes: sb, Severity: "info",
	})

	sort.SliceStable(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })

	// 6. 可释放空间排行榜（按目录聚合）
	rankRows, err := s.st.Select(ctx, `
		SELECT dir_path, COUNT(*), COALESCE(SUM(size),0) FROM files
		WHERE state='active' GROUP BY dir_path ORDER BY 3 DESC LIMIT 15`)
	type dirRank struct {
		Dir   string `json:"dir"`
		Files int64  `json:"files"`
		Bytes int64  `json:"bytes"`
	}
	ranks := []dirRank{}
	if err == nil {
		for rankRows.Next() {
			var d dirRank
			if err := rankRows.Scan(&d.Dir, &d.Files, &d.Bytes); err == nil {
				ranks = append(ranks, d)
			}
		}
		rankRows.Close()
	}

	ok(w, map[string]any{"items": out, "top_dirs": ranks})
}

func severityFor(bytes int64) string {
	const gb = int64(1) << 30
	switch {
	case bytes >= 50*gb:
		return "warn"
	case bytes > 0:
		return "info"
	default:
		return "ok"
	}
}

