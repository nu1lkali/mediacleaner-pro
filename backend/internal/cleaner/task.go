package cleaner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"mediacleaner/internal/config"
	"mediacleaner/internal/fingerprint"
	"mediacleaner/internal/rules"
	"mediacleaner/internal/store"
)

var (
	// ErrContentMismatch 表示待删文件与保留副本的全文哈希不同，已阻止执行。
	ErrContentMismatch = errors.New("与保留副本内容不一致，已阻止执行")
	// ErrNoKeeper 表示找不到可用的保留副本，无法证明"删掉还有一份"，已阻止执行。
	ErrNoKeeper = errors.New("找不到可用的保留副本")
)

// 清理任务状态
const (
	TaskDraft     = "draft"
	TaskRunning   = "running"
	TaskPaused    = "paused"
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

// ExecOptions 是执行清理任务时的额外选择。
type ExecOptions struct {
	// SkipVerify 跳过"与保留副本逐字节比对"。
	//
	// 默认 false：采样证据（evidence=sampled）的分组只比过 size + 采样指纹，
	// 不能排除同大小不同内容的误判，因此删除前必须与保留副本做一次全文比对。
	// 设为 true 时不读内容直接执行，风险由调用方承担（会写入审计日志）。
	SkipVerify bool
}

// verifyState 在一次执行内复用保留副本的全文哈希，避免同一个保留副本被反复读取。
type verifyState struct {
	skip   bool
	mu     sync.Mutex
	hashes map[string]string
}

func newVerifyState(skip bool) *verifyState {
	return &verifyState{skip: skip, hashes: map[string]string{}}
}

func (v *verifyState) get(path string) (string, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	h, ok := v.hashes[path]
	return h, ok
}

func (v *verifyState) put(path, hash string) {
	v.mu.Lock()
	v.hashes[path] = hash
	v.mu.Unlock()
}

// Execute 执行清理任务（默认带删除前校验）。
func (x *Executor) Execute(ctx context.Context, taskID int64, actor, ip string) error {
	return x.ExecuteWith(ctx, taskID, ExecOptions{}, actor, ip)
}

// ExecuteWith 执行清理任务，可指定是否跳过删除前校验。
func (x *Executor) ExecuteWith(ctx context.Context, taskID int64, opts ExecOptions, actor, ip string) error {
	return x.execute(ctx, taskID, opts, actor, ip, false)
}

// execute 是执行循环本体。resume=true 表示「继续被暂停的任务」：允许从 paused
// 状态起步，即使进程里已经没有对应的协程也能重新拉起（容器重启后就靠这条恢复）。
func (x *Executor) execute(ctx context.Context, taskID int64, opts ExecOptions, actor, ip string, resume bool) error {
	task, err := x.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	x.mu.Lock()
	_, alive := x.running[taskID]
	x.mu.Unlock()
	switch task.Status {
	case TaskRunning:
		if alive {
			return errors.New("任务已在执行中")
		}
		// 进程重启后残留的 running：没有协程在干活，允许重新拉起
	case TaskPaused:
		if !resume {
			return errors.New("任务已暂停，请先点「继续」再执行")
		}
	case TaskSucceeded, TaskCanceled:
		return errors.New("任务已结束，无法重复执行")
	}
	items, err := x.Items(ctx, taskID, true)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return errors.New("没有勾选任何待清理文件")
	}

	// 「同组保留条件」：删除前先按条件重算组内保留文件，再只删未被保留的重复项。
	// 没有配置条件时 enforceKeeperRules 是空操作，行为与改造前完全一致。
	items, keeperStats, keeperSkipped, err := x.enforceKeeperRules(ctx, items)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		// 全部待删项都被判为保留：一个文件都不能删，任务正常收尾而不是报错
		msg := fmt.Sprintf("全部 %d 个待删文件均被同组保留条件判定为保留文件，未执行删除", keeperSkipped)
		stats := map[string]any{
			"items": 0, "done": 0, "failed": 0, "skipped": keeperSkipped,
			"bytes": 0, "quarantined": 0, "skip_verify": opts.SkipVerify,
			"keeper_rule": keeperStats,
		}
		sj, _ := json.Marshal(stats)
		now := store.NowMS()
		if _, err := x.st.Exec(context.Background(),
			`UPDATE clean_tasks SET status=?, started_at=?, finished_at=?, message=?, stats_json=? WHERE id=?`,
			TaskSucceeded, now, now, msg, string(sj), taskID); err != nil {
			return err
		}
		x.st.Audit(context.Background(), actor, "clean.execute", "clean_task", fmt.Sprint(taskID), stats, ip)
		return nil
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
		mu       sync.Mutex
		firstErr error
	)
	// 校验状态在一次执行内共享：同一个保留副本的全文哈希只算一次
	vs := newVerifyState(opts.SkipVerify)
	// 串行执行：批量删除本身是 IO 密集操作，并发反而加重 NAS 负载
	for _, it := range items {
		if runCtx.Err() != nil {
			break
		}
		// 暂停是「挂起」而不是退出：卡在这里既不推进也不丢进度，继续后从下一个文件接着做
		if err := x.waitIfPaused(runCtx, taskID); err != nil {
			break
		}
		err := x.processOne(runCtx, task, it, vs)
		// 只读卷跳过不算失败，不必进错误统计；逐项结果已写进 clean_items.action
		if err != nil && !errors.Is(err, ErrReadOnly) {
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
		}
	}

	// 汇总以「整张任务」为准，而不是只统计本次循环：暂停后「继续」只处理剩余项，
	// 只算本次会把暂停前已完成的几百个文件从结果里"忘掉"。
	tDone, tFailed, tSkipped, tQuar, tFreed := x.summarize(ctx, taskID)

	final := TaskSucceeded
	msg := fmt.Sprintf("成功 %d 个，失败 %d 个，释放 %s", tDone, tFailed, humanSize(tFreed))
	if tSkipped > 0 {
		msg = fmt.Sprintf("成功 %d 个，失败 %d 个，跳过 %d 个（只读卷），释放 %s",
			tDone, tFailed, tSkipped, humanSize(tFreed))
	}
	if keeperSkipped > 0 {
		msg = fmt.Sprintf("保留条件保护 %d 个文件；", keeperSkipped) + msg
	}
	if runCtx.Err() != nil {
		final = TaskCanceled
		msg = "已取消：" + msg
	} else if tFailed > 0 && tDone == 0 {
		final = TaskFailed
		msg = "全部失败：" + msg
		if firstErr != nil {
			msg += "；首个错误：" + firstErr.Error()
		}
	}
	stats := map[string]any{
		"items":          len(items),
		"done":           tDone,
		"failed":         tFailed,
		"skipped":        tSkipped,
		"bytes":          tFreed,
		"quarantined":    tQuar,
		"skip_verify":    opts.SkipVerify,
		"keeper_skipped": keeperSkipped,
		"keeper_rule":    keeperStats,
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

// summarize 统计整张任务清单（只看已勾选项）的最终结果。
//
// 逐项结果在执行时已写进 clean_items.action，这里一次性聚合，既包含本次循环
// 处理的，也包含暂停前/上次进程处理过的。SUM 在没有行时返回 NULL，用 NullInt64 兜住。
func (x *Executor) summarize(ctx context.Context, taskID int64) (done, failed, skipped, quarantined, freed int64) {
	var d, f, s, q, b sql.NullInt64
	err := x.st.Get(ctx, []any{&d, &f, &s, &q, &b}, `
		SELECT SUM(CASE WHEN action IN ('quarantined','deleted') THEN 1 ELSE 0 END),
		       SUM(CASE WHEN action='failed' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN action='skipped' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN action='quarantined' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN action IN ('quarantined','deleted') THEN size ELSE 0 END)
		FROM clean_items WHERE task_id=? AND selected=1`, taskID)
	if err != nil {
		return 0, 0, 0, 0, 0
	}
	return d.Int64, f.Int64, s.Int64, q.Int64, b.Int64
}

// processOne 处理单个文件：复核 → 内容校验 → 隔离/删除 → 回写结果。
func (x *Executor) processOne(ctx context.Context, task *Task, it Item, vs *verifyState) error {
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

	// 3. 内容校验：采样证据的分组（evidence=sampled）只比过 size + 采样指纹，
	//    删除前必须与保留副本逐字节确认，否则可能把「同大小不同内容」当成重复删掉。
	//    已全文校验的分组两边都有 content_hash，这里只做一次字符串比较，不读盘。
	//    trusted（用户确认免校验）不比对内容，但仍确认保留副本存在，
	//    避免删掉组内最后一份文件。
	if !vs.skip {
		need, trusted, err := x.needsVerify(ctx, it)
		if err != nil {
			return fail(err)
		}
		switch {
		case need:
			if err := x.verifyAgainstKeeper(ctx, it, vs); err != nil {
				return fail(err)
			}
		case trusted:
			if err := x.ensureKeeperExists(ctx, it); err != nil {
				return fail(err)
			}
		}
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

	// 隔离时优先复用已有哈希，避免为了「给文件起名」把整个文件再读一遍：
	//   1. 本次执行刚复验过（sampled 组）→ verifyState 缓存里就有；
	//   2. exact 组扫描时已全文校验 → files.content_hash 可用；
	//   3. 都没有才现场算 SHA-256。
	qhash := ""
	if !vs.skip {
		if h, ok := vs.get(it.Path); ok && h != "" {
			qhash = h
		} else if h, err := x.knownHash(ctx, it.FileID); err == nil {
			qhash = h
		}
	}
	qid, err := x.q.Store(ctx, it.Path, it.Size, it.MtimeNS, 0, it.RuleID, it.TaskID, it.Kind, qhash, vs.skip)
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

// verifyAgainstKeeper 确认「待删文件」与「保留副本」内容一致。
//
// 快速路径：两边都有 content_hash（扫描时已全文校验）→ 直接比字符串，不读盘。
// 慢速路径：至少一侧没有哈希（延迟校验 / 采样证据模式）→ 对缺的一侧算 BLAKE3，
// 保留副本的哈希在本次执行内缓存，同一个保留副本被多个待删文件引用时只读一次。
func (x *Executor) verifyAgainstKeeper(ctx context.Context, it Item, vs *verifyState) error {
	keeperPath, keeperHash, err := x.keeperOf(ctx, it)
	if err != nil {
		return err
	}
	if keeperPath == "" || keeperPath == it.Path {
		return nil // 手动选择、没有保留副本可参照
	}

	// 保留副本必须仍存在且是普通文件：否则删掉这份就真的没有"另一份"了
	st, err := os.Lstat(keeperPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w：%s 已不存在，无法确认还有一份拷贝", ErrNoKeeper, keeperPath)
		}
		return fmt.Errorf("%w：%s 无法访问: %v", ErrNoKeeper, keeperPath, err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%w：%s 不是普通文件", ErrNoKeeper, keeperPath)
	}

	candHash, err := x.knownHash(ctx, it.FileID)
	if err != nil {
		return err
	}
	if candHash == "" {
		if candHash, err = x.hashOfPath(ctx, it.Path, vs); err != nil {
			return fmt.Errorf("无法读取待删除文件: %w", err)
		}
	}
	if keeperHash == "" {
		if keeperHash, err = x.hashOfPath(ctx, keeperPath, vs); err != nil {
			return fmt.Errorf("无法读取保留副本: %w", err)
		}
	}
	if candHash != keeperHash {
		return fmt.Errorf("%w：%s 与保留副本 %s 的全文哈希不同", ErrContentMismatch, it.Path, keeperPath)
	}
	return nil
}

// needsVerify 判断某条清理项是否需要「与保留副本逐字节比对」，以及是否属于
// 用户已确认免校验的分组。
//
//   - exact / sampled：需要比对（exact 两侧已有全文哈希，只做字符串比较，不读盘）；
//   - trusted：不比对内容，但仍确认保留副本存在（第二个返回值为 true）；
//   - similar：相似图片/视频本就允许内容不同，完全跳过。
//
// 分组若已被新一轮扫描重建（查不到 evidence），保守起见按需要校验处理。
func (x *Executor) needsVerify(ctx context.Context, it Item) (bool, bool, error) {
	if it.KeeperPath == "" {
		return false, false, nil
	}
	if it.GroupID > 0 {
		var evidence sql.NullString
		err := x.st.Get(ctx, &evidence, `SELECT evidence FROM dup_groups WHERE id=?`, it.GroupID)
		switch {
		case err == nil:
			switch evidence.String {
			case store.EvidenceSimilar:
				return false, false, nil
			case store.EvidenceTrusted:
				return false, true, nil
			default:
				return true, false, nil
			}
		case errors.Is(err, sql.ErrNoRows):
			return true, false, nil // 分组已被重建，宁可多校验一次
		default:
			return false, false, err
		}
	}
	return true, false, nil
}

// ensureKeeperExists 只确认「保留副本还在且是普通文件」，不读取任何内容。
// 免校验分组跳过了逐字节比对，但这条底线不能省：否则当保留副本已被外部
// 删除时，可能把组内最后一份文件也移进隔离区。
func (x *Executor) ensureKeeperExists(ctx context.Context, it Item) error {
	if it.KeeperPath == "" {
		return nil
	}
	path, _, err := x.keeperOf(ctx, it)
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w：%s 已不存在，无法确认还有一份拷贝", ErrNoKeeper, path)
		}
		return fmt.Errorf("%w：%s 无法访问: %v", ErrNoKeeper, path, err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%w：%s 不是普通文件", ErrNoKeeper, path)
	}
	return nil
}

// keeperOf 返回保留副本的路径与其已知的全文哈希（可能为空）。
func (x *Executor) keeperOf(ctx context.Context, it Item) (string, string, error) {
	if it.KeeperPath == "" {
		return "", "", nil
	}
	// 分组可能已被新一轮扫描重建，优先按 group_id 找当前登记为保留的文件
	if it.GroupID > 0 {
		var path, hash string
		err := x.st.Get(ctx, []any{&path, &hash}, `
			SELECT f.path, COALESCE(f.content_hash,'')
			FROM dup_group_members m JOIN files f ON f.id=m.file_id
			WHERE m.group_id=? AND m.is_keeper=1 LIMIT 1`, it.GroupID)
		switch {
		case err == nil && path != "":
			return path, hash, nil
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return "", "", err
		}
	}
	return it.KeeperPath, "", nil
}

// knownHash 取库里已记录的全文哈希，没有则返回空串。
func (x *Executor) knownHash(ctx context.Context, fileID int64) (string, error) {
	if fileID <= 0 {
		return "", nil
	}
	var h sql.NullString
	if err := x.st.Get(ctx, &h, `SELECT content_hash FROM files WHERE id=?`, fileID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return h.String, nil
}

// hashOfPath 计算并缓存单个文件的全文哈希。
func (x *Executor) hashOfPath(ctx context.Context, path string, vs *verifyState) (string, error) {
	if h, ok := vs.get(path); ok && h != "" {
		return h, nil
	}
	chunk := x.cfg.ExactChunk
	if chunk <= 0 {
		chunk = 1 << 20
	}
	sum, err := fingerprint.Full(path, chunk, func() bool { return ctx.Err() == nil }, nil)
	if err != nil {
		return "", err
	}
	vs.put(path, sum)
	return sum, nil
}

// Pause 暂停执行中的任务：只改数据库状态，执行循环会在下一个文件前挂起。
//
// 之所以不用 context 取消实现暂停：取消会让任务直接收尾成 canceled，剩下的文件
// 再也没法"接着做"。暂停只是把循环挡在 waitIfPaused 里，进度、已完成项、
// 隔离区记录都保持原样，继续后从下一个文件开始。
func (x *Executor) Pause(ctx context.Context, taskID int64) error {
	x.mu.Lock()
	_, running := x.running[taskID]
	x.mu.Unlock()
	if !running {
		return errors.New("任务未在执行")
	}
	res, err := x.st.Exec(ctx,
		`UPDATE clean_tasks SET status=? WHERE id=? AND status=?`, TaskPaused, taskID, TaskRunning)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("任务当前不在运行状态")
	}
	return nil
}

// Resume 继续被暂停的任务。
//
// 两种情况：
//  1. 进程内还有执行协程卡在 waitIfPaused → 只把状态改回 running，循环自己醒过来；
//  2. 协程已经没了（容器重启、或上次执行异常退出）→ 重新拉起执行循环，从剩余
//     的 pending 项接着做。只改状态不拉起协程会让任务永远停在 running 却没人干活。
func (x *Executor) Resume(ctx context.Context, taskID int64) error {
	var status string
	if err := x.st.Get(ctx, &status, `SELECT status FROM clean_tasks WHERE id=?`, taskID); err != nil {
		return errors.New("清理任务不存在")
	}
	if status != TaskPaused {
		return errors.New("任务当前不在暂停状态")
	}
	x.mu.Lock()
	_, alive := x.running[taskID]
	x.mu.Unlock()
	if alive {
		if _, err := x.st.Exec(ctx,
			`UPDATE clean_tasks SET status=? WHERE id=? AND status=?`,
			TaskRunning, taskID, TaskPaused); err != nil {
			return err
		}
		return nil
	}
	go func() {
		_ = x.execute(context.Background(), taskID, ExecOptions{}, "system", "", true)
	}()
	return nil
}

// waitIfPaused 在每处理一个文件前检查任务是否被暂停，暂停则阻塞轮询。
// 取消（ctx.Done）会立刻返回，由外层收尾为 canceled。
func (x *Executor) waitIfPaused(ctx context.Context, taskID int64) error {
	for {
		var status string
		if err := x.st.Get(ctx, &status, `SELECT status FROM clean_tasks WHERE id=?`, taskID); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if status != TaskPaused {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(400 * time.Millisecond):
		}
	}
}

// Cancel 中断执行中的任务。
//
// 三种情况都必须能收尾，否则按钮就是"点了没反应"：
//  1. 进程内还有执行协程 → 取消 context，循环自己收尾成 canceled；
//  2. 协程已经不在了（容器重启过、或执行协程异常退出）但库里还是 running/paused
//     → 直接把状态改成 canceled，任务不再"卡在执行中"；
//  3. 已经是终态 → 明确报错，让界面能给出提示。
func (x *Executor) Cancel(ctx context.Context, taskID int64) error {
	x.mu.Lock()
	cancel, alive := x.running[taskID]
	x.mu.Unlock()
	if alive {
		cancel()
		return nil
	}
	res, err := x.st.Exec(ctx,
		`UPDATE clean_tasks SET status=?, finished_at=?, message=?
		 WHERE id=? AND status IN (?,?)`,
		TaskCanceled, store.NowMS(), "已取消（执行进程已退出，未处理的文件保持原样）",
		taskID, TaskRunning, TaskPaused)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("任务已结束，无需取消")
	}
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

// 明细列表的过滤视图。界面只需要「正在执行 + 即将执行」的那一段，
// 全量把几千行塞给浏览器会直接把页面卡死。
const (
	ItemsPending = "pending" // 已勾选、尚未处理（含正在处理的那一条之后的排队项）
	ItemsDone    = "done"    // 已成功处理（隔离 / 永久删除）
	ItemsFailed  = "failed"  // 处理失败
	ItemsSkipped = "skipped" // 因只读卷等原因跳过
	ItemsAll     = "all"
)

// ItemCounts 是整张任务的计数汇总（与分页无关，永远反映全量）。
type ItemCounts struct {
	Total         int64 `json:"total"`
	Selected      int64 `json:"selected"`
	Pending       int64 `json:"pending"`
	Done          int64 `json:"done"`
	Failed        int64 `json:"failed"`
	Skipped       int64 `json:"skipped"`
	TotalBytes    int64 `json:"total_bytes"`
	SelectedBytes int64 `json:"selected_bytes"`
}

// itemsSelect 是明细查询的列清单，Items / ItemsPage 共用，避免两处 Scan 对不上。
const itemsSelect = `SELECT id, task_id, COALESCE(group_id,0), COALESCE(file_id,0), path, name, ext, size,
	             media_kind, COALESCE(reason,''), COALESCE(rule_id,0), COALESCE(hit_rules,''),
	             COALESCE(keeper_path,''), COALESCE(assoc_of,0), action, COALESCE(error,''),
	             COALESCE(quarantine_id,0), executed_at, selected, expected_mtime_ns
	      FROM clean_items WHERE task_id=?`

func scanItems(rows *sql.Rows) ([]Item, error) {
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

// Items 列出任务明细；onlySelected 为 true 时只返回已勾选项。
func (x *Executor) Items(ctx context.Context, taskID int64, onlySelected bool) ([]Item, error) {
	q := itemsSelect
	if onlySelected {
		q += ` AND selected=1 AND action='pending'`
	}
	q += ` ORDER BY size DESC, id`
	rows, err := x.st.Select(ctx, q, taskID)
	if err != nil {
		return nil, err
	}
	return scanItems(rows)
}

// ItemsPage 分页取明细，并附带全量计数。
//
// 排序即执行顺序：
//   - pending：`size DESC, id`，与执行循环取数顺序一致，所以第一条就是"正在执行"；
//   - done / failed / skipped：按处理时间倒序，最近处理的在最上面；
//   - all：按 id。
func (x *Executor) ItemsPage(ctx context.Context, taskID int64, filter string, limit, offset int) ([]Item, ItemCounts, error) {
	counts, err := x.ItemCounts(ctx, taskID)
	if err != nil {
		return nil, counts, err
	}
	q := itemsSelect
	switch filter {
	case ItemsPending:
		q += ` AND selected=1 AND action='pending' ORDER BY size DESC, id`
	case ItemsDone:
		q += ` AND action IN ('quarantined','deleted') ORDER BY COALESCE(executed_at,0) DESC, id DESC`
	case ItemsFailed:
		q += ` AND action='failed' ORDER BY COALESCE(executed_at,0) DESC, id DESC`
	case ItemsSkipped:
		q += ` AND action='skipped' ORDER BY COALESCE(executed_at,0) DESC, id DESC`
	default:
		filter = ItemsAll
		q += ` ORDER BY id`
	}
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		rows, err := x.st.Select(ctx, q, taskID, limit, offset)
		if err != nil {
			return nil, counts, err
		}
		items, err := scanItems(rows)
		return items, counts, err
	}
	rows, err := x.st.Select(ctx, q, taskID)
	if err != nil {
		return nil, counts, err
	}
	items, err := scanItems(rows)
	return items, counts, err
}

// ItemCounts 汇总整张任务的明细计数（一次聚合查询，不读明细行）。
func (x *Executor) ItemCounts(ctx context.Context, taskID int64) (ItemCounts, error) {
	var (
		c                            ItemCounts
		pending, done, failed, skipd sql.NullInt64
	)
	err := x.st.Get(ctx, []any{
		&c.Total, &c.Selected, &pending, &done, &failed, &skipd, &c.TotalBytes, &c.SelectedBytes,
	}, `
		SELECT COUNT(*),
		       COALESCE(SUM(selected),0),
		       COALESCE(SUM(CASE WHEN selected=1 AND action='pending' THEN 1 ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN action IN ('quarantined','deleted') THEN 1 ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN action='failed' THEN 1 ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN action='skipped' THEN 1 ELSE 0 END),0),
		       COALESCE(SUM(size),0),
		       COALESCE(SUM(CASE WHEN selected=1 THEN size ELSE 0 END),0)
		FROM clean_items WHERE task_id=?`, taskID)
	if err != nil {
		return c, err
	}
	c.Pending, c.Done, c.Failed, c.Skipped = pending.Int64, done.Int64, failed.Int64, skipd.Int64
	return c, nil
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
	Cancel(ctx context.Context, taskID int64) error
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
