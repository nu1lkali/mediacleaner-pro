package rules

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"mediacleaner/internal/config"
	"mediacleaner/internal/store"
)

// MemberInfo 是组内一个文件的完整视图。
type MemberInfo struct {
	FileID     int64
	RootID     int64
	Path       string
	DirPath    string
	Name       string
	Stem       string
	Ext        string
	Kind       string
	Size       int64
	MtimeNS    int64
	Width      int
	Height     int
	Bitrate    int64
	DurationMS int64
	Volume     string
	Depth      int
	Similarity float64
}

func (m MemberInfo) resolution() int { return m.Width * m.Height }

// GroupInfo 是一个重复组。
type GroupInfo struct {
	ID         int64
	Kind       string
	Signature  string
	Size       int64
	TotalBytes int64
	AvgSim     float64
	Members    []MemberInfo
}

// PlanItem 是清理计划中的一条待处理文件。
type PlanItem struct {
	GroupID    int64   `json:"group_id"`
	FileID     int64   `json:"file_id"`
	Path       string  `json:"path"`
	Name       string  `json:"name"`
	Ext        string  `json:"ext"`
	Size       int64   `json:"size"`
	Kind       string  `json:"media_kind"`
	Reason     string  `json:"reason"`
	RuleID     int64   `json:"rule_id"`
	RuleName   string  `json:"rule_name"`
	KeeperPath string  `json:"keeper_path"`
	AssocOf    string  `json:"assoc_of"`
	IsAssoc    bool    `json:"is_assoc"`
	MtimeNS    int64   `json:"expected_mtime_ns"`
	RootID     int64   `json:"root_id"`
	Similarity float64 `json:"similarity"`
	Volume     string  `json:"volume"`
	HitRules   []string `json:"hit_rules,omitempty"`
}

// Plan 是一次 Dry Run 的完整结果。
type Plan struct {
	Items        []PlanItem     `json:"items"`
	GroupCount   int            `json:"group_count"`
	TotalBytes   int64          `json:"total_bytes"`
	ByKind       map[string]int `json:"by_kind"`
	BytKindBytes map[string]int64 `json:"by_kind_bytes"`
	Truncated    bool           `json:"truncated"`
	Notes        []string       `json:"notes"`
	RuleStats    []RuleStat     `json:"rule_stats"`
}

// RuleStat 统计单条规则命中情况，便于用户理解"是谁删的"。
type RuleStat struct {
	RuleID    int64  `json:"rule_id"`
	RuleName  string `json:"rule_name"`
	Items     int    `json:"items"`
	Bytes     int64  `json:"bytes"`
	Groups    int    `json:"groups"`
}

// Engine 执行规则求值。
type Engine struct {
	st  *store.Store
	cfg *config.Config
}

func NewEngine(st *store.Store, cfg *config.Config) *Engine {
	return &Engine{st: st, cfg: cfg}
}

// Evaluate 对指定规则（空则全部启用规则）求值，产出清理计划。
//
// 这是纯计算过程：不移动、不删除任何文件，可反复执行。
func (e *Engine) Evaluate(ctx context.Context, ruleIDs []int64) (*Plan, error) {
	var (
		ruleList []Rule
		err      error
	)
	if len(ruleIDs) == 0 {
		ruleList, err = NewStore(e.st).Enabled(ctx)
	} else {
		for _, id := range ruleIDs {
			r, err := NewStore(e.st).Get(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("规则 %d 不可用: %w", id, err)
			}
			ruleList = append(ruleList, *r)
		}
	}
	if err != nil {
		return nil, err
	}
	if len(ruleList) == 0 {
		return &Plan{Items: []PlanItem{}, ByKind: map[string]int{}, BytKindBytes: map[string]int64{}}, nil
	}
	sort.Slice(ruleList, func(i, j int) bool { return ruleList[i].Priority < ruleList[j].Priority })

	// 全局保护集：所有组的保留文件与用户手动钉住的路径，任何规则都不能删
	protected, err := e.protectedIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range ruleList {
		for _, p := range r.Retention.PinnedPaths {
			if id, ok := e.lookupID(ctx, p); ok {
				protected[id] = true
			}
		}
	}

	plan := &Plan{
		Items:        []PlanItem{},
		ByKind:       map[string]int{},
		BytKindBytes: map[string]int64{},
	}
	seen := map[int64]int{} // fileID → Items 下标，用于合并命中规则
	groupSeen := map[int64]bool{}

	// 只读卷闸门：未开启「允许清理」的存储卷，任何规则都不得从中产出删除项。
	// 这条约束与界面开关一一对应，缺了它"允许清理"就只是装饰。
	writable, err := e.writableRoots(ctx)
	if err != nil {
		return nil, err
	}
	readOnlySkipped := 0

	for i := range ruleList {
		rule := &ruleList[i]
		if !rule.Enabled {
			continue
		}
		res, err := e.evalRule(ctx, rule, protected)
		if err != nil {
			return nil, err
		}
		if res == nil {
			continue
		}
		stat := RuleStat{RuleID: rule.ID, RuleName: rule.Name}
		for _, it := range res.items {
			if it.RootID > 0 && !writable[it.RootID] {
				readOnlySkipped++
				continue
			}
			if idx, ok := seen[it.FileID]; ok {
				// 同一文件被多条规则命中：保留首条（高优先级）理由，追加规则名
				plan.Items[idx].HitRules = append(plan.Items[idx].HitRules, rule.Name)
				continue
			}
			it.HitRules = []string{rule.Name}
			seen[it.FileID] = len(plan.Items)
			plan.Items = append(plan.Items, it)
			stat.Items++
			stat.Bytes += it.Size
			plan.TotalBytes += it.Size
			plan.ByKind[it.Kind]++
			plan.BytKindBytes[it.Kind] += it.Size
		}
		for _, g := range res.groups {
			if !groupSeen[g] {
				groupSeen[g] = true
			}
		}
		if stat.Items > 0 {
			plan.RuleStats = append(plan.RuleStats, stat)
		}
		// 单规则上限
		if rule.Delete.MaxDeletes > 0 && stat.Items > rule.Delete.MaxDeletes {
			plan.Notes = append(plan.Notes,
				fmt.Sprintf("规则「%s」命中 %d 项，超过上限 %d，建议收紧条件", rule.Name, stat.Items, rule.Delete.MaxDeletes))
		}
	}

	plan.GroupCount = len(groupSeen)

	if readOnlySkipped > 0 {
		plan.Notes = append(plan.Notes, fmt.Sprintf(
			"%d 个文件因所在存储卷未开启「允许清理」被跳过；确认无误后可在系统设置中开启", readOnlySkipped))
	}

	// 关联文件（第二轮）：主文件确认删除后，处理孤立的附属文件
	if err := e.attachAssociated(ctx, ruleList, plan, seen); err != nil {
		return nil, err
	}

	sort.SliceStable(plan.Items, func(i, j int) bool {
		if plan.Items[i].IsAssoc != plan.Items[j].IsAssoc {
			return !plan.Items[i].IsAssoc
		}
		return plan.Items[i].Size > plan.Items[j].Size
	})
	return plan, nil
}

type ruleResult struct {
	items  []PlanItem
	groups []int64
}

func (e *Engine) evalRule(ctx context.Context, rule *Rule, protected map[int64]bool) (*ruleResult, error) {
	res := &ruleResult{}

	wantKinds := rule.Match.GroupKinds
	hasName := false
	var groupKinds []string
	for _, k := range wantKinds {
		if k == GroupName {
			hasName = true
			continue
		}
		groupKinds = append(groupKinds, k)
	}

	if len(groupKinds) > 0 {
		groups, err := e.loadGroups(ctx, groupKinds, rule.Match.MinGroupMembers)
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			if len(g.Members) < rule.Match.MinGroupMembers {
				continue
			}
			items, ok := e.evalGroup(rule, g, protected)
			if !ok || len(items) == 0 {
				continue
			}
			// 单组最多删除 = 成员数 - KeepCount，天然保证保留一份
			res.items = append(res.items, items...)
			res.groups = append(res.groups, g.ID)
		}
	}

	if hasName && rule.Match.SameName != nil {
		items, groups, err := e.evalSameName(ctx, rule, protected)
		if err != nil {
			return nil, err
		}
		res.items = append(res.items, items...)
		res.groups = append(res.groups, groups...)
	}

	// 规则级总量约束
	if rule.Delete.MaxDeletes > 0 && len(res.items) > rule.Delete.MaxDeletes {
		sort.SliceStable(res.items, func(i, j int) bool { return res.items[i].Size > res.items[j].Size })
		res.items = res.items[:rule.Delete.MaxDeletes]
	}
	if rule.Delete.MinReclaimBytes > 0 {
		var total int64
		for _, it := range res.items {
			total += it.Size
		}
		if total < rule.Delete.MinReclaimBytes {
			res.items = nil
			res.groups = nil
		}
	}
	return res, nil
}

// evalGroup 处理一个内容层面的重复组。
func (e *Engine) evalGroup(rule *Rule, g GroupInfo, protected map[int64]bool) ([]PlanItem, bool) {
	// 相似度门槛只对相似类组生效
	members := make([]MemberInfo, 0, len(g.Members))
	for _, m := range g.Members {
		if (g.Kind == GroupImage || g.Kind == GroupVideo) && rule.Match.MinSimilarity > 0 &&
			m.Similarity+1e-9 < rule.Match.MinSimilarity {
			continue
		}
		if rule.Match.StaleDays > 0 && !olderThanDays(m.MtimeNS, rule.Match.StaleDays) {
			continue
		}
		members = append(members, m)
	}
	if len(members) < 2 {
		return nil, false
	}

	keepers := chooseKeepers(members, rule.Retention, protected)
	if len(keepers) == 0 {
		return nil, false
	}
	keepSet := map[int64]bool{}
	keeperPath := ""
	for _, k := range keepers {
		keepSet[k.FileID] = true
		if keeperPath == "" {
			keeperPath = k.Path
		}
	}

	// 空切片而非 nil：nil 会被 JSON 编码为 null，前端 items.slice 会抛错
	items := []PlanItem{}
	for _, m := range members {
		if keepSet[m.FileID] || protected[m.FileID] {
			continue
		}
		if !rule.Scope.allows(m) || !rule.Delete.allows(m, m.FileID) {
			continue
		}
		items = append(items, PlanItem{
			GroupID:    g.ID,
			FileID:     m.FileID,
			Path:       m.Path,
			Name:       m.Name,
			Ext:        m.Ext,
			Size:       m.Size,
			Kind:       m.Kind,
			Reason:     reasonFor(g.Kind, m, keeperPath),
			RuleID:     rule.ID,
			RuleName:   rule.Name,
			KeeperPath: keeperPath,
			MtimeNS:    m.MtimeNS,
			RootID:     m.RootID,
			Similarity: m.Similarity,
			Volume:     m.Volume,
		})
	}
	return items, len(items) > 0
}

func reasonFor(kind string, m MemberInfo, keeper string) string {
	switch kind {
	case GroupImage:
		return fmt.Sprintf("与保留图片视觉相似（%.1f%%），保留 %s", m.Similarity*100, filepath.Base(keeper))
	case GroupVideo:
		return fmt.Sprintf("与保留视频内容相似（%.1f%%），保留 %s", m.Similarity*100, filepath.Base(keeper))
	case GroupName:
		return fmt.Sprintf("同名不同后缀的冗余文件，保留 %s", filepath.Base(keeper))
	default:
		return fmt.Sprintf("内容与 %s 完全一致", filepath.Base(keeper))
	}
}

// evalSameName 处理"同名不同后缀"规则：同目录同名主体存在多个触发扩展名时，
// 视作同一媒体的冗余副本，保留最佳一份。
func (e *Engine) evalSameName(ctx context.Context, rule *Rule, protected map[int64]bool) ([]PlanItem, []int64, error) {
	spec := rule.Match.SameName
	trigger := normalizeExts(spec.TriggerExtensions)
	if len(trigger) == 0 {
		return nil, nil, fmt.Errorf("规则「%s」缺少触发扩展名", rule.Name)
	}
	rows, err := e.st.Select(ctx, `
		SELECT f.id, f.root_id, f.path, f.dir_path, f.name, f.stem, f.ext, f.media_kind,
		       f.size, f.mtime_ns, COALESCE(m.width,0), COALESCE(m.height,0),
		       COALESCE(m.bitrate,0), COALESCE(m.duration_ms,0)
		FROM files f LEFT JOIN file_media m ON m.file_id=f.id
		WHERE f.state='active'
		ORDER BY f.dir_path, f.stem`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	byStem := map[string][]MemberInfo{}
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, nil, err
		}
		key := m.DirPath + "\x00" + m.Stem
		byStem[key] = append(byStem[key], m)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var (
		items  []PlanItem
		groups []int64
	)
	for _, members := range byStem {
		// 只保留触发扩展名的成员作为"主文件"
		var mains []MemberInfo
		for _, m := range members {
			if trigger[m.Ext] {
				mains = append(mains, m)
			}
		}
		if len(mains) < 2 {
			continue
		}
		keepers := chooseKeepers(mains, rule.Retention, protected)
		if len(keepers) == 0 {
			continue
		}
		keepSet := map[int64]bool{}
		keeperPath := ""
		for _, k := range keepers {
			keepSet[k.FileID] = true
			if keeperPath == "" {
				keeperPath = k.Path
			}
		}
		for _, m := range mains {
			if keepSet[m.FileID] || protected[m.FileID] {
				continue
			}
			if !rule.Scope.allows(m) || !rule.Delete.allows(m, m.FileID) {
				continue
			}
			items = append(items, PlanItem{
				GroupID:    0,
				FileID:     m.FileID,
				Path:       m.Path,
				Name:       m.Name,
				Ext:        m.Ext,
				Size:       m.Size,
				Kind:       m.Kind,
				Reason:     reasonFor(GroupName, m, keeperPath),
				RuleID:     rule.ID,
				RuleName:   rule.Name,
				KeeperPath: keeperPath,
				MtimeNS:    m.MtimeNS,
				RootID:     m.RootID,
				Volume:     m.Volume,
			})
		}
	}
	return items, groups, nil
}

// attachAssociated 为每个待删主文件找出"仅被它引用"的附属文件。
//
// 保护逻辑：若同目录下存在任何一个"幸存"的同名文件（不在删除计划中），
// 则附属文件仍然被引用，一律保护，不加入计划。
func (e *Engine) attachAssociated(ctx context.Context, ruleList []Rule, plan *Plan, seen map[int64]int) error {
	// 规则选择：优先使用命中该主文件的那条规则的关联策略
	ruleByID := map[int64]*Rule{}
	for i := range ruleList {
		ruleByID[ruleList[i].ID] = &ruleList[i]
	}
	elems := make(map[int64]bool, len(plan.Items))
	for _, it := range plan.Items {
		elems[it.FileID] = true
	}

	type assocCandidate struct {
		main    *PlanItem
		exts    map[string]bool
		sameDir bool
		sameBase bool
		protect bool
	}
	var todo []assocCandidate
	for i := range plan.Items {
		it := &plan.Items[i]
		rule := ruleByID[it.RuleID]
		if rule == nil {
			continue
		}
		specs := []Associated{}
		if rule.Associated.Enabled {
			specs = append(specs, rule.Associated)
		}
		if rule.Match.SameName != nil {
			// 同名规则本身也允许声明关联扩展名
			specs = append(specs, Associated{
				Enabled:              true,
				AssociatedExtensions: rule.Match.SameName.AssociatedExtensions,
				SameDirectoryOnly:    rule.Match.SameName.SameDirectoryOnly,
				SameBasenameOnly:     rule.Match.SameName.SameBasenameOnly,
				ProtectReferenced:    rule.Match.SameName.ProtectReferenced,
			})
		}
		for _, sp := range specs {
			if len(sp.TriggerExtensions) > 0 && !normalizeExts(sp.TriggerExtensions)[it.Ext] {
				continue
			}
			todo = append(todo, assocCandidate{
				main:     it,
				exts:     normalizeExts(sp.AssociatedExtensions),
				sameDir:  sp.SameDirectoryOnly,
				sameBase: sp.SameBasenameOnly,
				protect:  sp.ProtectReferenced,
			})
			break
		}
	}
	if len(todo) == 0 {
		return nil
	}

	for _, c := range todo {
		if len(c.exts) == 0 {
			continue
		}
		q := `SELECT f.id, f.root_id, f.path, f.dir_path, f.name, f.stem, f.ext, f.media_kind,
		             f.size, f.mtime_ns, 0, 0, 0, 0
		      FROM files f WHERE f.state='active' AND f.id<>?`
		args := []any{c.main.FileID}
		if c.sameBase {
			q += ` AND f.stem=?`
			args = append(args, filepath.Base(strings.TrimSuffix(c.main.Name, c.main.Ext)))
		}
		if c.sameDir {
			q += ` AND f.dir_path=?`
			args = append(args, filepath.Dir(c.main.Path))
		}
		rows, err := e.st.Select(ctx, q, args...)
		if err != nil {
			return err
		}
		var cands []MemberInfo
		for rows.Next() {
			m, err := scanMember(rows)
			if err != nil {
				rows.Close()
				return err
			}
			if c.exts[m.Ext] {
				cands = append(cands, m)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		for _, a := range cands {
			if elems[a.FileID] {
				continue
			}
			if c.protect {
				survivor, err := e.hasStemSurvivor(ctx, a.DirPath, a.Stem, elems)
				if err != nil {
					return err
				}
				if survivor {
					continue
				}
			}
			it := PlanItem{
				GroupID:    c.main.GroupID,
				FileID:     a.FileID,
				Path:       a.Path,
				Name:       a.Name,
				Ext:        a.Ext,
				Size:       a.Size,
				Kind:       a.Kind,
				Reason:     "随主文件 " + c.main.Name + " 清理的关联文件",
				RuleID:     c.main.RuleID,
				RuleName:   c.main.RuleName,
				KeeperPath: c.main.KeeperPath,
				AssocOf:    c.main.Path,
				IsAssoc:    true,
				MtimeNS:    a.MtimeNS,
				RootID:     a.RootID,
				Volume:     a.Volume,
			}
			elems[a.FileID] = true
			seen[a.FileID] = len(plan.Items)
			plan.Items = append(plan.Items, it)
			plan.TotalBytes += it.Size
			plan.ByKind[it.Kind]++
			plan.BytKindBytes[it.Kind] += it.Size
		}
	}
	return nil
}

// hasStemSurvivor 判断同目录同名主体是否还有文件不会被删除。
func (e *Engine) hasStemSurvivor(ctx context.Context, dir, stem string, deleting map[int64]bool) (bool, error) {
	rows, err := e.st.Select(ctx,
		`SELECT id FROM files WHERE state='active' AND dir_path=? AND stem=?`, dir, stem)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return false, err
		}
		if !deleting[id] {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ---------- 组加载 ----------

func (e *Engine) loadGroups(ctx context.Context, kinds []string, minMembers int) ([]GroupInfo, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")
	args := make([]any, 0, len(kinds))
	for _, k := range kinds {
		args = append(args, k)
	}
	rows, err := e.st.Select(ctx, `
		SELECT id, kind, signature, size, total_bytes, avg_similarity
		FROM dup_groups WHERE kind IN (`+placeholders+`) AND member_count>=?`, append(args, minMembers)...)
	if err != nil {
		return nil, err
	}
	var groups []GroupInfo
	index := map[int64]*GroupInfo{}
	for rows.Next() {
		var g GroupInfo
		if err := rows.Scan(&g.ID, &g.Kind, &g.Signature, &g.Size, &g.TotalBytes, &g.AvgSim); err != nil {
			rows.Close()
			return nil, err
		}
		groups = append(groups, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range groups {
		index[groups[i].ID] = &groups[i]
	}
	if len(groups) == 0 {
		return nil, nil
	}

	rows, err = e.st.Select(ctx, `
		SELECT m.group_id, f.id, f.root_id, f.path, f.dir_path, f.name, f.stem, f.ext,
		       f.media_kind, f.size, f.mtime_ns, COALESCE(md.width,0), COALESCE(md.height,0),
		       COALESCE(md.bitrate,0), COALESCE(md.duration_ms,0), m.similarity
		FROM dup_group_members m
		JOIN files f ON f.id=m.file_id
		LEFT JOIN file_media md ON md.file_id=f.id
		WHERE f.state='active' AND m.group_id IN (
		  SELECT id FROM dup_groups WHERE kind IN (`+placeholders+`) AND member_count>=?
		)`, append(args, minMembers)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			gid int64
			m   MemberInfo
		)
		if err := rows.Scan(&gid, &m.FileID, &m.RootID, &m.Path, &m.DirPath, &m.Name, &m.Stem,
			&m.Ext, &m.Kind, &m.Size, &m.MtimeNS, &m.Width, &m.Height, &m.Bitrate,
			&m.DurationMS, &m.Similarity); err != nil {
			return nil, err
		}
		m.Depth = strings.Count(filepath.ToSlash(m.Path), "/")
		m.Volume = volumeOf(m.Path)
		if g, ok := index[gid]; ok {
			g.Members = append(g.Members, m)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]GroupInfo, 0, len(groups))
	for _, g := range groups {
		if len(g.Members) >= 2 {
			out = append(out, g)
		}
	}
	return out, nil
}

// writableRoots 返回"允许清理"的存储卷集合。
//
// 未开启 writable 的卷只读：扫描照常索引、分组照常展示，但任何规则都不能
// 从里面产出删除项。这是安全底线，也是界面开关的实际含义。
func (e *Engine) writableRoots(ctx context.Context) (map[int64]bool, error) {
	rows, err := e.st.Select(ctx, `SELECT id FROM roots WHERE writable=1 AND enabled=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (e *Engine) protectedIDs(ctx context.Context) (map[int64]bool, error) {
	rows, err := e.st.Select(ctx, `SELECT file_id FROM dup_group_members WHERE is_keeper=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (e *Engine) lookupID(ctx context.Context, path string) (int64, bool) {
	var id int64
	if err := e.st.Get(ctx, &id, `SELECT id FROM files WHERE path=? AND state='active' LIMIT 1`, path); err != nil {
		return 0, false
	}
	return id, true
}

func scanMember(rows interface {
	Scan(dest ...any) error
}) (MemberInfo, error) {
	var m MemberInfo
	err := rows.Scan(&m.FileID, &m.RootID, &m.Path, &m.DirPath, &m.Name, &m.Stem, &m.Ext,
		&m.Kind, &m.Size, &m.MtimeNS, &m.Width, &m.Height, &m.Bitrate, &m.DurationMS)
	m.Depth = strings.Count(filepath.ToSlash(m.Path), "/")
	m.Volume = volumeOf(m.Path)
	return m, err
}

// ---------- 保留策略 ----------

// chooseKeepers 按规则权重对成员排名，返回必须保留的成员（数量 = KeepCount）。
//
// 采用"排名归一化"而不是直接加权求和原始值：分辨率与体积的量纲相差 3~4 个数量级，
// 直接相加会让分辨率维度彻底压过其他维度。
func chooseKeepers(members []MemberInfo, ret Retention, protected map[int64]bool) []MemberInfo {
	n := len(members)
	score := make([]float64, n)
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}

	keys := ret.Keys
	if len(keys) == 0 {
		// 默认策略：层级浅 + 体积大
		keys = []RetentionKey{{Field: "depth", Order: "asc", Weight: 3}, {Field: "size", Order: "desc", Weight: 1}}
	}
	if ret.Strategy == "keep_largest" {
		keys = []RetentionKey{{Field: "size", Order: "desc", Weight: 1}}
	}
	if ret.Strategy == "keep_newest" {
		keys = []RetentionKey{{Field: "mtime", Order: "desc", Weight: 1}}
	}
	if ret.Strategy == "keep_oldest" {
		keys = []RetentionKey{{Field: "mtime", Order: "asc", Weight: 1}}
	}

	for _, k := range keys {
		vals := make([]float64, n)
		for i, m := range members {
			vals[i] = keyValue(k, m)
		}
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool {
			if k.Order == "asc" {
				return vals[idx[a]] < vals[idx[b]]
			}
			return vals[idx[a]] > vals[idx[b]]
		})
		for rank, i := range idx {
			// 排名越靠前（越该保留）得分越高
			score[i] += k.Weight * float64(n-rank)
		}
	}

	// 钉住（用户指定保留 / 其他规则的 keeper）优先
	for i, m := range members {
		if protected[m.FileID] {
			score[i] += 1e6
		}
	}

	sorted := make([]int, n)
	for i := range sorted {
		sorted[i] = i
	}
	sort.SliceStable(sorted, func(a, b int) bool {
		if score[sorted[a]] != score[sorted[b]] {
			return score[sorted[a]] > score[sorted[b]]
		}
		return members[sorted[a]].Path < members[sorted[b]].Path
	})

	keep := ret.KeepCount
	if keep < 1 {
		keep = 1
	}
	if keep > n {
		keep = n
	}
	// 至少保留一个非保护文件（避免把整组都判死）
	out := make([]MemberInfo, 0, keep)
	for i := 0; i < keep; i++ {
		out = append(out, members[sorted[i]])
	}
	if len(out) == 0 {
		out = append(out, members[sorted[0]])
	}
	return out
}

func keyValue(k RetentionKey, m MemberInfo) float64 {
	switch k.Field {
	case "resolution":
		return float64(m.resolution())
	case "bitrate":
		return float64(m.Bitrate)
	case "size":
		return float64(m.Size)
	case "mtime":
		return float64(m.MtimeNS)
	case "ctime":
		return float64(m.MtimeNS)
	case "duration":
		return float64(m.DurationMS)
	case "depth":
		// 层级浅更好：默认 desc 时值大者优先，所以取负
		return float64(-m.Depth)
	case "ext":
		for _, e := range k.Prefer {
			if strings.EqualFold(m.Ext, ensureDot(e)) {
				return 1
			}
		}
		return 0
	case "dir_prefix":
		if k.Value != "" && strings.HasPrefix(m.Path, k.Value) {
			return 1
		}
		return 0
	case "volume":
		if strings.EqualFold(m.Volume, k.Value) {
			return 1
		}
		return 0
	case "name_regex":
		if k.Value == "" {
			return 0
		}
		re, err := regexp.Compile(k.Value)
		if err != nil {
			return 0
		}
		if re.MatchString(m.Name) {
			return 1
		}
		return 0
	}
	return 0
}

// ---------- 条件判断 ----------

func (s Scope) allows(m MemberInfo) bool {
	if len(s.RootIDs) > 0 && !inInt64(s.RootIDs, m.RootID) {
		return false
	}
	if len(s.Volumes) > 0 && !inStrFold(s.Volumes, m.Volume) {
		return false
	}
	if len(s.Dirs) > 0 {
		ok := false
		for _, d := range s.Dirs {
			if strings.HasPrefix(m.Path, d) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for _, d := range s.ExcludeDirs {
		if d != "" && strings.HasPrefix(m.Path, d) {
			return false
		}
	}
	if len(s.Kinds) > 0 && !inStr(s.Kinds, m.Kind) {
		return false
	}
	if len(s.ExtInclude) > 0 && !inExts(s.ExtInclude, m.Ext) {
		return false
	}
	if len(s.ExtExclude) > 0 && inExts(s.ExtExclude, m.Ext) {
		return false
	}
	if s.MinSize > 0 && m.Size < s.MinSize {
		return false
	}
	if s.MaxSize > 0 && m.Size > s.MaxSize {
		return false
	}
	if len(s.NameInclude) > 0 {
		ok := false
		for _, n := range s.NameInclude {
			if n != "" && strings.Contains(strings.ToLower(m.Name), strings.ToLower(n)) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for _, n := range s.NameExclude {
		if n != "" && strings.Contains(strings.ToLower(m.Name), strings.ToLower(n)) {
			return false
		}
	}
	if s.NameRegex != "" {
		re, err := regexp.Compile(s.NameRegex)
		if err == nil && !re.MatchString(m.Name) {
			return false
		}
	}
	return true
}

func (d Delete) allows(m MemberInfo, _ int64) bool {
	if len(d.Kinds) > 0 && !inStr(d.Kinds, m.Kind) {
		return false
	}
	if d.MaxResolution > 0 && m.resolution() >= d.MaxResolution {
		return false
	}
	if d.MinResolution > 0 && m.resolution() < d.MinResolution {
		return false
	}
	if d.OlderThanDays > 0 && !olderThanDays(m.MtimeNS, d.OlderThanDays) {
		return false
	}
	if len(d.PathRegex) > 0 {
		ok := false
		for _, p := range d.PathRegex {
			re, err := regexp.Compile(p)
			if err == nil && re.MatchString(m.Path) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// ---------- 小工具 ----------

func normalizeExts(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, e := range list {
		if e = strings.TrimSpace(strings.ToLower(e)); e != "" {
			out[ensureDot(e)] = true
		}
	}
	return out
}

func ensureDot(e string) string {
	if strings.HasPrefix(e, ".") {
		return e
	}
	return "." + e
}

func inStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func inStrFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

func inInt64(list []int64, v int64) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func inExts(list []string, ext string) bool {
	for _, e := range list {
		if strings.EqualFold(ensureDot(e), ext) {
			return true
		}
	}
	return false
}

func olderThanDays(mtimeNS int64, days int) bool {
	if days <= 0 {
		return true
	}
	return store.NowMS()-mtimeNS/1e6 > int64(days)*86400000
}

// volumeOf 与 scan.VolumeOf 逻辑一致，这里独立实现以避免包循环依赖。
func volumeOf(p string) string {
	clean := filepath.Clean(p)
	vol := filepath.VolumeName(clean)
	if vol != "" {
		return strings.ToUpper(vol)
	}
	parts := strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "" {
		return "/"
	}
	if parts[0] == "mnt" || parts[0] == "media" {
		if len(parts) > 1 {
			return "/" + parts[0] + "/" + parts[1]
		}
	}
	return "/" + parts[0]
}
