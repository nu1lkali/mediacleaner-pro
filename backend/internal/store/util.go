package store

import (
	"context"
	"encoding/json"
	"os"
)

func mkdirAll(dir string) error { return os.MkdirAll(dir, 0o755) }

// Audit 写审计日志（调用方负责传入已脱敏的 detail）。
func (s *Store) Audit(ctx context.Context, actor, action, targetType, targetID string, detail any, ip string) {
	var raw string
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			if len(b) > 8000 {
				b = b[:8000]
			}
			raw = string(b)
		}
	}
	_, _ = s.rw.ExecContext(ctx,
		`INSERT INTO audit_log(ts, actor, action, target_type, target_id, detail_json, ip) VALUES(?,?,?,?,?,?,?)`,
		NowMS(), actor, action, targetType, targetID, raw, ip)
}

// Setting 读取设置值；不存在返回 def。
func (s *Store) Setting(ctx context.Context, key, def string) string {
	var v string
	err := s.ro.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key=?`, key).Scan(&v)
	if err != nil {
		return def
	}
	return v
}

func (s *Store) SetSetting(ctx context.Context, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.rw.ExecContext(ctx,
		`INSERT INTO settings(key, value_json, updated_at) VALUES(?,?,?)
		 ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json, updated_at=excluded.updated_at`,
		key, string(b), NowMS())
	return err
}
