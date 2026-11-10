package keeper

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"mediacleaner/internal/store"
)

// SettingsKey 保存「按分组类型的默认保留条件」，值为 JSON 对象 {kind: rule}。
// dup_groups 每轮扫描都会整体重建，条件必须在这里留一份，重建后再自动套回去。
const SettingsKey = "keeper_rules"

// 一次性取出某类（或某个）分组的全部成员，含已隔离/已删除的成员——
// 判定时要主动把这些成员从 keeper 位上撤下来。
const membersOfKindSQL = `
SELECT g.id, g.keeper_rule, m.file_id, f.path, f.mtime_ns,
       COALESCE(f.btime_ns,0), m.is_keeper, f.state
FROM dup_groups g
JOIN dup_group_members m ON m.group_id=g.id
JOIN files f ON f.id=m.file_id
WHERE g.kind=? AND g.batch_active=1
ORDER BY g.id, m.file_id`

const membersOfGroupSQL = `
SELECT g.id, g.keeper_rule, m.file_id, f.path, f.mtime_ns,
       COALESCE(f.btime_ns,0), m.is_keeper, f.state
FROM dup_groups g
JOIN dup_group_members m ON m.group_id=g.id
JOIN files f ON f.id=m.file_id
WHERE g.id=? AND g.batch_active=1
ORDER BY g.id, m.file_id`

// 执行清理前的复核只处理「已配置条件」的分组，未配置的分组一律不动。
const configuredMembersSQL = `
SELECT g.id, g.keeper_rule, m.file_id, f.path, f.mtime_ns,
       COALESCE(f.btime_ns,0), m.is_keeper, f.state
FROM dup_groups g
JOIN dup_group_members m ON m.group_id=g.id
JOIN files f ON f.id=m.file_id
WHERE g.keeper_rule<>'' AND g.batch_active=1
ORDER BY g.id, m.file_id`

// 保留文件换过之后，可回收空间必须按「新的保留文件体积」重算。
// 完全重复组各成员一样大，影响为零；相似图片/视频组成员体积不同，不重算会显示错。
const refreshGroupSizeSQL = `
UPDATE dup_groups SET
  size = COALESCE((
    SELECT f.size FROM dup_group_members m JOIN files f ON f.id=m.file_id
    WHERE m.group_id=dup_groups.id AND m.is_keeper=1 LIMIT 1), size),
  reclaimable_bytes = CASE WHEN member_count > 1 THEN MAX(total_bytes - COALESCE((
    SELECT f.size FROM dup_group_members m JOIN files f ON f.id=m.file_id
    WHERE m.group_id=dup_groups.id AND m.is_keeper=1 LIMIT 1), size), 0) ELSE 0 END
WHERE id=?`

type groupView struct {
	ID      int64
	Rule    string
	Members []Member
}

// decision 是一个分组最终要落库的 keeper。
type decision struct {
	GroupID    int64
	Rule       string
	KeeperID   int64
	KeeperPath string
	Reason     string
	Changed    bool
	Fallback   bool
}

// Stats 汇总一次判定/应用的统计，供 API 响应与审计日志使用。
type Stats struct {
	Kind        string `json:"kind"`
	Rule        string `json:"rule"`
	Groups      int    `json:"groups"`
	Changed    int    `json:"changed"`
	Unavailable int    `json:"unavailable"`
	Protected  int    `json:"protected"`
	GroupID    int64  `json:"group_id,omitempty"`
}

func loadGroups(ctx context.Context, st *store.Store, q string, args ...any) ([]groupView, error) {
	rows, err := st.Select(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []groupView
	// 存下标而不是指针：out 追加时可能整体搬迁，指针会失效
	byID := map[int64]int{}
	for rows.Next() {
		var (
			gid                  int64
			rule, path, state    string
			fileID, mtime, btime int64
			isKeeper             int
		)
		if err := rows.Scan(&gid, &rule, &fileID, &path, &mtime, &btime, &isKeeper, &state); err != nil {
			return nil, err
		}
		idx, ok := byID[gid]
		if !ok {
			out = append(out, groupView{ID: gid, Rule: rule})
			idx = len(out) - 1
			byID[gid] = idx
		}
		out[idx].Members = append(out[idx].Members, Member{
			FileID: fileID, Path: path, MtimeNS: mtime, BtimeNS: btime,
			Depth: DepthOf(path), Active: state == "active", IsKeeper: isKeeper == 1,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func decide(groups []groupView, ruleOf func(groupView) string) ([]decision, int) {
	out := make([]decision, 0, len(groups))
	unavailable := 0
	for _, g := range groups {
		rule := ruleOf(g)
		res := Resolve(g.Members, rule)
		// 原 keeper：优先取 active 的，否则取任意一个（便于判断「是否变化」）
		var before int64
		for _, m := range g.Members {
			if m.IsKeeper {
				before = m.FileID
				if m.Active {
					break
				}
			}
		}
		keeperPath := ""
		for _, m := range g.Members {
			if m.FileID == res.KeeperID {
				keeperPath = m.Path
				break
			}
		}
		d := decision{GroupID: g.ID, Rule: rule, KeeperID: res.KeeperID,
			KeeperPath: keeperPath, Reason: res.Reason, Fallback: res.Fallback}
		if res.Applicable && rule != RuleNone {
			d.Changed = res.KeeperID != before
		}
		// 只统计「条件本身不适用」的组；「仅剩一份」不算条件失效
		if res.Applicable && res.Note == "全组 btime 未知" && rule != RuleNone {
			unavailable++
		}
		out = append(out, d)
	}
	return out, unavailable
}

func writeDecisions(ctx context.Context, st *store.Store,
	decisions []decision, setRule bool) error {
	return st.Tx(ctx, func(tx *sql.Tx) error {
		if setRule {
			stmt, err := tx.PrepareContext(ctx, `UPDATE dup_groups SET keeper_rule=?, updated_at=? WHERE id=?`)
			if err != nil {
				return err
			}
			for _, d := range decisions {
				if _, err := stmt.ExecContext(ctx, d.Rule, store.NowMS(), d.GroupID); err != nil {
					stmt.Close()
					return err
				}
			}
			if err := stmt.Close(); err != nil {
				return err
			}
		}

		clearStmt, err := tx.PrepareContext(ctx,
			`UPDATE dup_group_members SET is_keeper=0, keep_reason=NULL WHERE group_id=?`)
		if err != nil {
			return err
		}
		defer clearStmt.Close()
		setStmt, err := tx.PrepareContext(ctx,
			`UPDATE dup_group_members SET is_keeper=1, keep_reason=? WHERE group_id=? AND file_id=?`)
		if err != nil {
			return err
		}
		defer setStmt.Close()

		ids := make([]int64, 0, len(decisions))
		for _, d := range decisions {
			if d.Rule == RuleNone || d.KeeperID == 0 {
				continue // 未配置 / 不适用：保持原有 keeper 不变
			}
			if _, err := clearStmt.ExecContext(ctx, d.GroupID); err != nil {
				return err
			}
			if _, err := setStmt.ExecContext(ctx, d.Reason, d.GroupID, d.KeeperID); err != nil {
				return err
			}
			ids = append(ids, d.GroupID)
		}
		return refreshInTx(ctx, tx, ids)
	})
}

func refreshInTx(ctx context.Context, tx *sql.Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, refreshGroupSizeSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.ExecContext(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// RefreshGroupSizes 供「手动指定保留文件」等外部入口在改动 keeper 后重算统计。
func RefreshGroupSizes(ctx context.Context, st *store.Store, ids ...int64) error {
	if len(ids) == 0 {
		return nil
	}
	return st.Tx(ctx, func(tx *sql.Tx) error { return refreshInTx(ctx, tx, ids) })
}

// Apply 把条件写入分组并立即完成组内保留判定。
// groupID>0 只作用于该组（不改动 settings 默认值）；否则作用于 kind 下全部分组。
// rule 为空串表示清除条件，此时不改动任何 is_keeper，原有行为完全保留。
func Apply(ctx context.Context, st *store.Store, kind, rule string, groupID int64) (Stats, error) {
	if !IsValid(rule) {
		return Stats{}, fmt.Errorf("不支持的保留条件: %q", rule)
	}
	q, arg := membersOfKindSQL, any(kind)
	if groupID > 0 {
		q, arg = membersOfGroupSQL, any(groupID)
	}
	groups, err := loadGroups(ctx, st, q, arg)
	if err != nil {
		return Stats{}, err
	}
	if groupID > 0 && len(groups) == 0 {
		return Stats{}, fmt.Errorf("分组不存在: %d", groupID)
	}
	if kind == "" && groupID <= 0 {
		return Stats{}, fmt.Errorf("缺少分组类型")
	}

	decisions, unavailable := decide(groups, func(groupView) string { return rule })
	if err := writeDecisions(ctx, st, decisions, true); err != nil {
		return Stats{}, err
	}

	protected := map[int64]bool{}
	changed := 0
	for _, d := range decisions {
		if d.Changed {
			changed++
		}
		if d.Rule != RuleNone && d.KeeperID > 0 {
			protected[d.KeeperID] = true
		}
	}
	return Stats{
		Kind: kind, Rule: rule, Groups: len(groups), Changed: changed,
		Unavailable: unavailable, Protected: len(protected), GroupID: groupID,
	}, nil
}

// Enforce 在执行清理前复核所有已配置条件的分组：按当前成员状态重新判定 keeper，
// 写回 is_keeper/keep_reason，并返回：
//   - protected：本轮被保护的 file_id 集合（待删清单必须剔除）
//   - keeperPaths：group_id → 保留文件路径（供采样证据的逐字节比对取参照）
//
// 未配置条件的分组完全不参与，保证「未配置时行为不变」。
func Enforce(ctx context.Context, st *store.Store) (map[int64]bool, map[int64]string, Stats, error) {
	groups, err := loadGroups(ctx, st, configuredMembersSQL)
	if err != nil {
		return nil, nil, Stats{}, err
	}
	decisions, unavailable := decide(groups, func(g groupView) string { return g.Rule })
	// 复核阶段不改写 keeper_rule（它已经是当前生效的条件）
	if err := writeDecisions(ctx, st, decisions, false); err != nil {
		return nil, nil, Stats{}, err
	}
	protected := map[int64]bool{}
	paths := map[int64]string{}
	changed := 0
	for _, d := range decisions {
		if d.Changed {
			changed++
		}
		if d.KeeperID > 0 {
			protected[d.KeeperID] = true
			paths[d.GroupID] = d.KeeperPath
		}
	}
	return protected, paths, Stats{
		Rule: "*", Groups: len(groups), Changed: changed,
		Unavailable: unavailable, Protected: len(protected),
	}, nil
}

// Defaults 读取按类型保存的默认条件。
func Defaults(ctx context.Context, st *store.Store) map[string]string {
	raw := st.Setting(ctx, SettingsKey, "{}")
	m := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]string{}
	}
	for k, v := range m {
		if !IsValid(v) || v == "" {
			delete(m, k)
		}
	}
	return m
}

// SetDefault 更新某一类型的默认条件；空串表示删除该键。
func SetDefault(ctx context.Context, st *store.Store, kind, rule string) error {
	if !IsValid(rule) {
		return fmt.Errorf("不支持的保留条件: %q", rule)
	}
	m := Defaults(ctx, st)
	if rule == "" {
		delete(m, kind)
	} else {
		m[kind] = rule
	}
	return st.SetSetting(ctx, SettingsKey, m)
}

// EffectiveRule 返回某类型当前保存的默认条件（未配置时为空串）。
func EffectiveRule(ctx context.Context, st *store.Store, kind string) string {
	return Defaults(ctx, st)[kind]
}

// ApplyDefaults 在扫描重建分组后，把已保存的默认条件重新套到新分组上。
func ApplyDefaults(ctx context.Context, st *store.Store) ([]Stats, error) {
	m := Defaults(ctx, st)
	out := []Stats{}
	for kind, rule := range m {
		s, err := Apply(ctx, st, kind, rule, 0)
		if err != nil {
			return out, fmt.Errorf("恢复 %s 的保留条件失败: %w", kind, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// BtimeCoverage 统计某类分组中「真实创建时间」的可用比例，供前端提示口径是否可用。
func BtimeCoverage(ctx context.Context, st *store.Store, kind string) (known, total int, err error) {
	err = st.Get(ctx, []any{&known, &total}, `
		SELECT COALESCE(SUM(CASE WHEN f.btime_ns>0 THEN 1 ELSE 0 END),0), COUNT(*)
		FROM dup_group_members m
		JOIN dup_groups g ON g.id=m.group_id
		JOIN files f ON f.id=m.file_id
		WHERE g.kind=? AND g.batch_active=1 AND f.state='active'`, kind)
	return
}
