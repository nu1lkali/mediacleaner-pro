package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"mediacleaner/internal/scan"
	"mediacleaner/internal/store"
)

// BatchInfo 是一次扫描任务产出的独立分组快照。
// 每个任务各留一份结果，界面可切换查看；同一时刻只有一个批次是激活状态。
type BatchInfo struct {
	ScanTaskID int64  `json:"scan_task_id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	FinishedAt int64  `json:"finished_at"`
	Groups     int64  `json:"groups"`
	Removable  int64  `json:"removable"`
	Bytes      int64  `json:"bytes"`
	Verified   int64  `json:"verified"`   // evidence=exact，扫描时全文校验过
	Unverified int64  `json:"unverified"` // evidence=sampled，清理前会比对
	Trusted    int64  `json:"trusted"`    // evidence=trusted，用户确认免比对
	SkipVerify bool   `json:"skip_verify"`
	Active     bool   `json:"active"`
}

// handleListBatches 列出可切换的扫描批次。
//
// 以 scan_tasks 为主表 LEFT JOIN：即使某次扫描 0 个重复组，只要它是当前
// 激活批次也会列出，用户才能明确「现在看的是哪一批」。已被归档清理的历史
// 任务（没有任何分组、也不是当前批次）不显示，避免下拉框里全是空条目。
func (s *Server) handleListBatches(w http.ResponseWriter, r *http.Request) {
	activeID := s.st.ActiveBatch(r.Context())
	rows, err := s.st.Select(r.Context(), `
		SELECT t.id, t.name, t.status, COALESCE(t.finished_at, t.queued_at, 0),
		       COALESCE(t.options_json,'{}'),
		       COUNT(g.id),
		       COALESCE(SUM(CASE WHEN g.member_count>1 THEN g.member_count-1 ELSE 0 END),0),
		       COALESCE(SUM(g.reclaimable_bytes),0),
		       COALESCE(SUM(CASE WHEN g.evidence='exact'   THEN 1 ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN g.evidence='sampled' THEN 1 ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN g.evidence='trusted' THEN 1 ELSE 0 END),0),
		       COALESCE(MAX(g.batch_active),0)
		FROM scan_tasks t
		LEFT JOIN dup_groups g ON g.scan_task_id=t.id
		GROUP BY t.id
		HAVING COUNT(g.id)>0 OR t.id=?
		ORDER BY t.id DESC LIMIT 50`, activeID)
	if err != nil {
		fail(w, err)
		return
	}
	defer rows.Close()
	items := []BatchInfo{}
	for rows.Next() {
		var (
			b       BatchInfo
			optRaw  string
			activeF int
		)
		if err := rows.Scan(&b.ScanTaskID, &b.Name, &b.Status, &b.FinishedAt, &optRaw,
			&b.Groups, &b.Removable, &b.Bytes, &b.Verified, &b.Unverified, &b.Trusted,
			&activeF); err != nil {
			fail(w, err)
			return
		}
		b.Active = activeF == 1
		var opts scan.Options
		_ = json.Unmarshal([]byte(optRaw), &opts)
		b.SkipVerify = opts.SkipVerify
		items = append(items, b)
	}
	if err := rows.Err(); err != nil {
		fail(w, err)
		return
	}
	ok(w, map[string]any{"items": items, "active_task_id": activeID, "total": len(items)})
}

type switchBatchReq struct {
	ScanTaskID int64 `json:"scan_task_id"`
}

// handleSwitchBatch 把界面切换到某次扫描的结果批次。
// 分组数据不做任何重算，只是切换 batch_active 标记；历史批次仍完整保留。
func (s *Server) handleSwitchBatch(w http.ResponseWriter, r *http.Request) {
	var req switchBatchReq
	if err := decodeBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	if req.ScanTaskID <= 0 {
		fail(w, badRequest("请指定扫描任务 ID"))
		return
	}
	var exists int
	if err := s.st.Get(r.Context(), &exists,
		`SELECT COUNT(*) FROM scan_tasks WHERE id=?`, req.ScanTaskID); err != nil {
		fail(w, err)
		return
	}
	if exists == 0 {
		fail(w, notFound("扫描任务不存在"))
		return
	}
	// 与 scan.groupBuilder.activate 同样的写法：全程复用同一个事务连接，
	// 写连接池 MaxOpenConns=1，事务内再调 st.Exec 会死锁。
	err := s.st.Tx(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(),
			`UPDATE dup_groups SET batch_active=0 WHERE batch_active=1`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(),
			`UPDATE dup_groups SET batch_active=1 WHERE scan_task_id=?`, req.ScanTaskID); err != nil {
			return err
		}
		_, err := tx.ExecContext(r.Context(),
			`INSERT INTO settings(key, value_json, updated_at) VALUES(?,?,?)
			 ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json, updated_at=excluded.updated_at`,
			store.ActiveBatchKey, fmt.Sprint(req.ScanTaskID), store.NowMS())
		return err
	})
	if err != nil {
		fail(w, err)
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "group.switch_batch", "scan_task",
		fmt.Sprint(req.ScanTaskID), map[string]any{"active_task_id": req.ScanTaskID}, clientIP(r))
	ok(w, map[string]any{"ok": true, "active_task_id": req.ScanTaskID})
}

type trustAllReq struct {
	// Ack 是界面二次确认标记，防止误触/脚本调用直接关闭全部校验。
	Ack bool `json:"ack"`
	// Restore 为 true 时反向操作：把免校验分组退回「未校验」，恢复清理前比对。
	Restore bool `json:"restore"`
}

// handleTrustAllGroups 把当前激活批次的所有「未校验」分组一键标记为免校验
// （或撤销回来）。这不重新扫描全文，只改变执行清理时是否比对保留副本。
//
// 用户诉求是「对未校验的也能批量删除」：默认 sampled 分组在清理时会逐字节
// 比对，不希望被拦下时可以整体信任采样结果。风险必须显式确认并留审计。
func (s *Server) handleTrustAllGroups(w http.ResponseWriter, r *http.Request) {
	var req trustAllReq
	if err := decodeBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	if !req.Ack {
		fail(w, badRequest("请先确认风险提示"))
		return
	}
	from, to, action := store.EvidenceSampled, store.EvidenceTrusted, "group.trust_all"
	if req.Restore {
		from, to, action = store.EvidenceTrusted, store.EvidenceSampled, "group.untrust_all"
	}
	res, err := s.st.Exec(r.Context(),
		`UPDATE dup_groups SET evidence=?, updated_at=? WHERE batch_active=1 AND evidence=?`,
		to, store.NowMS(), from)
	if err != nil {
		fail(w, err)
		return
	}
	changed, _ := res.RowsAffected()
	activeID := s.st.ActiveBatch(r.Context())
	detail := map[string]any{
		"batch": from, "to": to, "changed": changed, "active_task_id": activeID,
	}
	if !req.Restore {
		detail["risk_accepted"] = "未全文校验的分组清理时不再与保留副本比对，可能误删同大小不同内容的文件"
	}
	s.st.Audit(r.Context(), actorOf(r), action, "dup_group", "", detail, clientIP(r))
	ok(w, map[string]any{
		"ok": true, "changed": changed, "restore": req.Restore,
		"active_task_id": activeID,
	})
}
