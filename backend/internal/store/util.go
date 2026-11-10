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

// ActiveBatchKey 记录当前界面正在查看的扫描批次（scan_tasks.id）。
// 即使某次扫描没有产出任何分组，也要能明确「现在看的是哪一批结果」。
const ActiveBatchKey = "active_batch_task_id"

// 分组证据等级，写入 dup_groups.evidence。放在 store 包是为了让
// scan（生产）与 cleaner（消费）共用同一套取值，不必互相 import。
const (
	EvidenceExact   = "exact"   // 扫描时全文校验过，content_hash 相同
	EvidenceSampled = "sampled" // 只比 size + 4 段采样，清理前需逐字节复验
	EvidenceSimilar = "similar" // 感知哈希相似，本就允许内容不同
	EvidenceTrusted = "trusted" // 用户确认信任采样结果，清理时也不比对
)

func (s *Store) ActiveBatch(ctx context.Context) int64 {
	var n int64
	if err := json.Unmarshal([]byte(s.Setting(ctx, ActiveBatchKey, "0")), &n); err != nil {
		return 0
	}
	return n
}

func (s *Store) SetActiveBatch(ctx context.Context, taskID int64) error {
	return s.SetSetting(ctx, ActiveBatchKey, taskID)
}
