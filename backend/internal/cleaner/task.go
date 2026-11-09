package cleaner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"mediacleaner/internal/config"
	"mediacleaner/internal/rules"
	"mediacleaner/internal/store"
)

// 清理任务状态
const (
	TaskDraft     = "draft"
	TaskRunning   = "running"
	TaskSucceeded = "succeeded"
	TaskFailed    = "failed"
	TaskCanceled  = "canceled"
)

// Task 是一次清理作业。
type Task struct {
	ID        int64          `json:"id"`
	Name      string         `json:"name"`
	RuleID    int64          `json:"rule_id"`
	Status    string         `json:"status"`
	ExecuteMode string       `json:"execute_mode"`
	Stats     map[string]any `json:"stats"`
	CreatedAt int64          `json:"created_at"`
	StartedAt *int64         `json:"started_at"`
	FinishedAt *int64        `json:"finished_at"`
	Message   string         `json:"message"`
}

// Item 是清理任务中的一条明细（对应 clean_items）。
type Item struct {
	ID         int64   `json:"id"`
	TaskID     int64   `json:"task_id"`
	GroupID    int64   `json:"group_id"`
	FileID     int64   `json:"file_id"`
	Path       string  `json:"path"`
	Name       string  `json:"name"`
	Ext        string  `json:"ext"`
	Size       int64   `json:"size"`
	Kind       string  `json:"media_kind"`
	Reason     string  `json:"reason"`
	RuleID     int64   `json:"rule_id"`
	HitRules   string  `json:"hit_rules"`
	KeeperPath string  `json:"keeper_path"`
	AssocOf    int64   `json:"assoc_of"`
	Action     string  `json:"action"`
	Error      string  `json:"error"`
	QuarantineID int64 `json:"quarantine_id"`
	ExecutedAt *int64  `json:"executed_at"`
	Selected   bool    `json:"selected"`
	MtimeNS    int64   `json:"expected_mtime_ns"`
}

// Executor 负责把"计划"落成"任务"再执行。
type Executor struct {
	st  *store.Store
	cfg *config.Config
	q   *Quarantine

	mu      sync.Mutex
	running map[int64]context.CancelFunc
}

func NewExecutor(st *store.Store, cfg *config.Config, q *Quarantine) *Executor {
	return &Executor{st: st, cfg: cfg, q: q, running: map[int64]context.CancelFunc{}}
}

// CreateFromPlan 把规则求值结果固化成一份可审阅的清理任务（status=draft）。
func (x *Executor) CreateFromPlan(ctx context.Context, name string, ruleIDs []int64, plan *rules.Plan, mode string) (*Task, error) {
	if len(plan.Items) == 0 {
		return nil, errors.New("计划为空，没有可清理的文件")
	}
	if mode != "permanent" {
		mode = "quarantine"
	}
	var ruleID int64
	if len(ruleIDs) == 1 {
		ruleID = ruleIDs[0]
	}
	now := store.NowMS()
	stats := map[string]any{
		"items":      len(plan.Items),
		"bytes":      plan.TotalBytes,
		"by_kind":    plan.ByKind,
		"groups":     plan.GroupCount,
		"rule_stats": plan.RuleStats,
		"notes":      plan.Notes,
	}
	statsJSON, _ := json.Marshal(stats)

	var taskID int64
	err := x.st.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO clean_tasks(name, rule_id, status, execute_mode, stats_json, created_at)
			VALUES(?,?,?,?,?,?)`,
			name, nullInt(ruleID), TaskDraft, mode, string(statsJSON), now)
		if err != nil {
			return err
		}
		taskID, _ = res.LastInsertId()

		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO clean_items(task_id, group_id, file_id, path, name, ext, size, media_kind,
			                        reason, rule_id, hit_rules, keeper_path, assoc_of, action,
			                        selected, expected_mtime_ns)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?, 'pending', ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		// 先插入主文件，再回填关联文件的 assoc_of（依赖主项的 rowid）
		mainIndex := map[string]int64{}
		for _, it := range plan.Items {
			var groupID any
			if it.GroupID > 0 {
				groupID = it.GroupID
			}
			var assocOf any
			if it.AssocOf != "" {
				if v, ok := mainIndex[it.AssocOf]; ok {
					assocOf = v
				}
			}
			res, err := stmt.ExecContext(ctx, taskID, groupID, it.FileID, it.Path, it.Name, it.Ext,
				it.Size, it.Kind, it.Reason, it.RuleID, joinRules(it.HitRules), it.KeeperPath,
				assocOf, 1, it.MtimeNS)
			if err != nil {
				return err
			}
			if it.AssocOf == "" {
				id, _ := res.LastInsertId()
				mainIndex[it.Path] = id
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return x.GetTask(ctx, taskID)
}

// Execute 执行清理任务。
func (x *Executor) Execute(ctx context.Context, taskID int64, actor, ip string) error {
	task, err := x.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task.Status == TaskRunning {
		return errors.New("任务已在执行中")
	}
	if task.Status == TaskSucceeded || task.Status == TaskCanceled {
		return errors.New("任务已结束，无法重复执行")
	}
	items, err := x.Items(ctx, taskID, true)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return errors.New("没有勾选任何待清理文件")
	}

	runCtx, cancel := context.WithCancel(ctx)
	x.mu.Lock()
	x.running[taskID] = cancel
	x.mu.Unlock()
	defer func() {
		x.mu.Lock()
		delete(x.running, taskID)
		x.mu.Unlock()
		cancel()
	}()

	now := store.NowMS()
	if _, err := x.st.Exec(ctx,
		`UPDATE clean_tasks SET status=?, started_at=? WHERE id=?`,
		TaskRunning, now, taskID); err != nil {
		return err
	}

	var (
		done, failed, skipped int64
		freed                 int64
		quarantined           int64
		mu                    sync.Mutex
		firstErr              error
	)
	// 串行执行：批量删除本身是 IO 密集操作，并发反而加重 NAS 负载
	for _, it := range items {
		if runCtx.Err() != nil {
			break
		}
		err := x.processOne(runCtx, task, it)
		mu.Lock()
		switch {
		case err == nil:
			done++
			freed += it.Size
			if task.ExecuteMode == "quarantine" {
				quarantined++
			}
		case errors.Is(err, ErrReadOnly):
			// 只读卷：既不算成功也不算失败，不计入释放空间
			skipped++
		default:
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
		mu.Unlock()
	}

	final := TaskSucceeded
	msg := fmt.Sprintf("成功 %d 个，失败 %d 个，释放 %s", done, failed, humanSize(freed))
	if skipped > 0 {
		msg = fmt.Sprintf("成功 %d 个，失败 %d 个，跳过 %d 个（只读卷），释放 %s",
			done, failed, skipped, humanSize(freed))
	}
	if runCtx.Err() != nil {
		final = TaskCanceled
		msg = "已取消：" + msg
	} else if failed > 0 && done == 0 {
		final = TaskFailed
		msg = "全部失败：" + msg
		if firstErr != nil {
			msg += "；首个错误：" + firstErr.Error()
		}
	}
	stats := map[string]any{
		"items":       len(items),
		"done":        done,
		"failed":      failed,
		"skipped":     skipped,
		"bytes":       freed,
		"quarantined": quarantined,
	}
	sj, _ := json.Marshal(stats)
	if _, err := x.st.Exec(context.Background(),
		`UPDATE clean_tasks SET status=?, finished_at=?, message=?, stats_json=? WHERE id=?`,
		final, store.NowMS(), msg, string(sj), taskID); err != nil {
		return err
	}
	x.st.Audit(context.Background(), actor, "clean.execute", "clean_task", fmt.Sprint(taskID), stats, ip)
	return nil
}

// processOne 处理单个文件：复核 → 隔离/删除 → 回写结果。
func (x *Executor) processOne(ctx context.Context, task *Task, it Item) error {
	fail := func(err error) error {
		_, _ = x.st.Exec(context.Background(),
			`UPDATE clean_items SET action='failed', error=?, executed_at=? WHERE id=?`,
			err.Error(), store.NowMS(), it.ID)
		return err
	}

	// 1. 复核文件状态：必须仍是普通文件、未变化
	st, err := os.Lstat(it.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return fail(errors.New("文件已不存在"))
		}
		return fail(err)
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return fail(ErrSymlink)
	}
	if !st.Mode().IsRegular() {
		return fail(errors.New("不是普通文件"))
	}
	if it.MtimeNS > 0 && st.ModTime().UnixNano() != it.MtimeNS {
		return fail(ErrChanged)
	}
	if it.Size > 0 && st.Size() != it.Size {
		return fail(ErrChanged)
	}

	// 2. 只读卷兜底：计划可能是在卷可写时生成的，执行前再确认一次。
	//    查不到归属（文件已从索引移除）时按只读处理，宁可跳过不可误删。
	var writable int
	err = x.st.Get(ctx, &writable,
		`SELECT COALESCE(r.writable,0) FROM files f JOIN roots r ON r.id=f.root_id WHERE f.id=?`,
		it.FileID)
	if err != nil || writable == 0 {
		if _, e2 := x.st.Exec(context.Background(),
			`UPDATE clean_items SET action='skipped', error=?, executed_at=? WHERE id=?`,
			ErrReadOnly.Error(), store.NowMS(), it.ID); e2 != nil {
			return e2
		}
		return ErrReadOnly
	}

	if task.ExecuteMode == "permanent" {
		if err := os.Remove(it.Path); err != nil {
			return fail(err)
		}
		_, _ = x.st.Exec(context.Background(),
			`UPDATE clean_items SET action='deleted', executed_at=? WHERE id=?`, store.NowMS(), it.ID)
		_, _ = x.st.Exec(context.Background(), `UPDATE files SET state='deleted', updated_at=? WHERE id=?`,
			store.NowMS(), it.FileID)
		return nil
	}

	qid, err := x.q.Store(ctx, it.Path, it.Size, it.MtimeNS, 0, it.RuleID, it.TaskID, it.Kind)
	if err != nil {
		return fail(err)
	}
	if _, err := x.st.Exec(context.Background(),
		`UPDATE clean_items SET action='quarantined', quarantine_id=?, executed_at=? WHERE id=?`,
		qid, store.NowMS(), it.ID); err != nil {
		return err
	}
	_, _ = x.st.Exec(context.Background(), `UPDATE files SET state='quarantined', updated_at=? WHERE id=?`,
		store.NowMS(), it.FileID)
	return nil
}

// Cancel 中断执行中的任务。
func (x *Executor) Cancel(taskID int64) error {
	x.mu.Lock()
	cancel, ok := x.running[taskID]
	x.mu.Unlock()
	if !ok {
		return errors.New("任务未在执行")
	}
	cancel()
	return nil
}

// ---------- 查询 ----------

func (x *Executor) GetTask(ctx context.Context, id int64) (*Task, error) {
	var (
		t        Task
		ruleID   sql.NullInt64
		stats    string
		started  sql.NullInt64
		finished sql.NullInt64
		msg      sql.NullString
	)
	err := x.st.Get(ctx, []any{&t.ID, &t.Name, &ruleID, &t.Status, &t.ExecuteMode, &stats,
		&t.CreatedAt, &started, &finished, &msg},
		`SELECT id, name, rule_id, status, execute_mode, stats_json, created_at,
		        started_at, finished_at, message FROM clean_tasks WHERE id=?`, id)
	if err != nil {
		return nil, errors.New("清理任务不存在")
	}
	if ruleID.Valid {
		t.RuleID = ruleID.Int64
	}
	t.Stats = map[string]any{}
	_ = json.Unmarshal([]byte(stats), &t.Stats)
	if started.Valid {
		v := started.Int64
		t.StartedAt = &v
	}
	if finished.Valid {
		v := finished.Int64
		t.FinishedAt = &v
	}
	t.Message = msg.String
	return &t, nil
}

// ListTasks 分页列出清理任务。
func (x *Executor) ListTasks(ctx context.Context, limit, offset int) ([]Task, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var total int
	if err := x.st.Get(ctx, &total, `SELECT COUNT(*) FROM clean_tasks`); err != nil {
		return nil, 0, err
	}
	rows, err := x.st.Select(ctx, `
		SELECT id, name, COALESCE(rule_id,0), status, execute_mode, stats_json, created_at,
		       started_at, finished_at, COALESCE(message,'')
		FROM clean_tasks ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var (
			t        Task
			stats    string
			started  sql.NullInt64
			finished sql.NullInt64
			msg      string
			ruleID   int64
		)
		if err := rows.Scan(&t.ID, &t.Name, &ruleID, &t.Status, &t.ExecuteMode, &stats,
			&t.CreatedAt, &started, &finished, &msg); err != nil {
			return nil, 0, err
		}
		t.RuleID = ruleID
		t.Message = msg
		t.Stats = map[string]any{}
		_ = json.Unmarshal([]byte(stats), &t.Stats)
		if started.Valid {
			v := started.Int64
			t.StartedAt = &v
		}
		if finished.Valid {
			v := finished.Int64
			t.FinishedAt = &v
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// Items 列出任务明细；onlySelected 为 true 时只返回已勾选项。
func (x *Executor) Items(ctx context.Context, taskID int64, onlySelected bool) ([]Item, error) {
	q := `SELECT id, task_id, COALESCE(group_id,0), COALESCE(file_id,0), path, name, ext, size,
	             media_kind, COALESCE(reason,''), COALESCE(rule_id,0), COALESCE(hit_rules,''),
	             COALESCE(keeper_path,''), COALESCE(assoc_of,0), action, COALESCE(error,''),
	             COALESCE(quarantine_id,0), executed_at, selected, expected_mtime_ns
	      FROM clean_items WHERE task_id=?`
	if onlySelected {
		q += ` AND selected=1 AND action='pending'`
	}
	q += ` ORDER BY size DESC, id`
	rows, err := x.st.Select(ctx, q, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var (
			it       Item
			executed sql.NullInt64
			selected int
		)
		if err := rows.Scan(&it.ID, &it.TaskID, &it.GroupID, &it.FileID, &it.Path, &it.Name, &it.Ext,
			&it.Size, &it.Kind, &it.Reason, &it.RuleID, &it.HitRules, &it.KeeperPath, &it.AssocOf,
			&it.Action, &it.Error, &it.QuarantineID, &executed, &selected, &it.MtimeNS); err != nil {
			return nil, err
		}
		it.Selected = selected == 1
		if executed.Valid {
			v := executed.Int64
			it.ExecutedAt = &v
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// SetSelected 在确认页勾选/取消某个文件。
func (x *Executor) SetSelected(ctx context.Context, itemID int64, selected bool) error {
	res, err := x.st.Exec(ctx, `UPDATE clean_items SET selected=? WHERE id=? AND action='pending'`,
		boolBit(selected), itemID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("条目不存在或已执行")
	}
	return nil
}

// SetGroupSelected 整组勾选。
func (x *Executor) SetGroupSelected(ctx context.Context, taskID, groupID int64, selected bool) error {
	_, err := x.st.Exec(ctx,
		`UPDATE clean_items SET selected=? WHERE task_id=? AND group_id=? AND action='pending'`,
		boolBit(selected), taskID, groupID)
	return err
}

// DeleteTask 删除草稿任务（仅未执行的任务允许）。
func (x *Executor) DeleteTask(ctx context.Context, id int64) error {
	t, err := x.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if t.Status == TaskRunning {
		return errors.New("任务执行中，请先取消")
	}
	return x.st.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM clean_items WHERE task_id=?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM clean_tasks WHERE id=?`, id)
		return err
	})
}

// ---------- 小工具 ----------

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func boolBit(b bool) int {
	if b {
		return 1
	}
	return 0
}

func joinRules(list []string) string {
	if len(list) == 0 {
		return ""
	}
	s := ""
	for i, v := range list {
		if i > 0 {
			s += ", "
		}
		s += v
	}
	return s
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), units[exp])
}

// 让 Executor 满足一个可被 API 层依赖的最小接口（便于将来替换实现）。
type Service interface {
	Execute(ctx context.Context, taskID int64, actor, ip string) error
	Cancel(taskID int64) error
}

var _ Service = (*Executor)(nil)

// 供定时任务统计使用
func (x *Executor) ReclaimedTotal(ctx context.Context) (int64, error) {
	var sum sql.NullInt64
	if err := x.st.Get(ctx, &sum,
		`SELECT SUM(size) FROM quarantine_items WHERE status IN ('quarantined','purged')`); err != nil {
		return 0, err
	}
	return sum.Int64, nil
}
