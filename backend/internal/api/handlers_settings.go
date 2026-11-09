package api

import (
	"encoding/json"
	"net/http"

	"mediacleaner/internal/media"
	"mediacleaner/internal/scan"
)

// settingsKeys 是允许通过 API 写入的设置项白名单。
// 未列入的键一律拒绝，避免前端任意写库。
var settingsKeys = map[string]string{
	"default_profile":        "默认扫描预设",
	"default_threads":        "默认并发线程数",
	"default_limit_mbps":     "默认 IO 限速（MB/s，0 为不限）",
	"default_min_file_size":  "默认最小文件大小（字节）",
	"default_exclude_names":  "默认排除文件名通配",
	"quarantine_retention_days": "隔离区保留天数",
	"auto_purge_expired":     "自动清理过期隔离文件",
	"scheduled_scan_enabled": "启用定时扫描",
	"scheduled_scan_freq":    "定时扫描频率（daily/weekly/monthly）",
	"scheduled_scan_at":      "定时扫描时间（HH:MM）",
	"theme":                  "界面主题（light/dark/system）",
	"confirm_before_execute": "执行清理前强制二次确认",
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.Select(r.Context(), `SELECT key, value_json FROM settings ORDER BY key`)
	if err != nil {
		fail(w, err)
		return
	}
	defer rows.Close()
	values := map[string]any{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			fail(w, err)
			return
		}
		var decoded any
		if err := json.Unmarshal([]byte(v), &decoded); err == nil {
			values[k] = decoded
		} else {
			values[k] = v
		}
	}
	ok(w, map[string]any{
		"values": values,
		"schema": settingsKeys,
		"defaults": map[string]any{
			"default_profile":           scan.ProfileStandard,
			"default_threads":           s.cfg.Threads,
			"default_limit_mbps":        0,
			"default_min_file_size":     1024,
			"default_exclude_names":     []string{"Thumbs.db", "desktop.ini", ".DS_Store"},
			"quarantine_retention_days": s.cfg.QuarantineRetentionDays,
			"auto_purge_expired":        true,
			"scheduled_scan_enabled":    false,
			"scheduled_scan_freq":       "weekly",
			"scheduled_scan_at":         "03:00",
			"theme":                     "dark",
			"confirm_before_execute":    true,
		},
		"runtime": map[string]any{
			"version":      s.cfg.Version,
			"data_root":    s.cfg.DataRoot,
			"cache_dir":    s.cfg.CacheDir,
			"db_path":      s.cfg.DBPath,
			"threads":      s.cfg.Threads,
			"max_threads":  s.cfg.MaxThreads,
			"capabilities": media.Capabilities(),
			"probe":        media.ProbeStatus(),
		},
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	if err := decodeBody(r, &body); err != nil {
		fail(w, err)
		return
	}
	applied := []string{}
	for k, raw := range body {
		if _, ok := settingsKeys[k]; !ok {
			fail(w, badRequest("不支持的设置项: "+k))
			return
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			fail(w, badRequest("设置项 "+k+" 的值无效"))
			return
		}
		if err := s.st.SetSetting(r.Context(), k, v); err != nil {
			fail(w, serverError("保存 "+k+" 失败: "+err.Error()))
			return
		}
		applied = append(applied, k)
	}
	s.st.Audit(r.Context(), actorOf(r), "settings.update", "settings", "",
		map[string]any{"keys": applied}, clientIP(r))
	ok(w, map[string]any{"ok": true, "applied": applied})
}
