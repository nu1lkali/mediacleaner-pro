// Package keeper 实现「同组保留条件」：在同一个重复组内，按配置的条件选出
// 唯一一个保留文件。判定逻辑集中在这里，一键应用与执行清理前的复核共用同一套口径。
package keeper

import (
	"strconv"
	"strings"
)

// 条件 ID。空串表示「未配置」——此时完全沿用原有去重行为，不做任何额外干预。
const (
	RuleNone           = ""
	RuleOldestMtime    = "oldest_mtime"
	RuleNewestMtime    = "newest_mtime"
	RuleOldestCreated  = "oldest_created"
	RuleNewestCreated  = "newest_created"
	RuleDeepestPath    = "deepest_path"
	RuleShallowestPath = "shallowest_path"
)

// Def 是一个条件的完整口径定义，由 API 下发给前端展示，避免前后端文案/含义漂移。
type Def struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Field      string `json:"field"`
	Criteria   string `json:"criteria"`
	Scope      string `json:"scope"`
	NeedsBtime bool   `json:"needs_btime"`
}

// Definitions 是全部可选条件（不含「未配置」）。顺序即前端下拉顺序。
var Definitions = []Def{
	{
		ID:       RuleOldestMtime,
		Label:    "最早修改",
		Field:    "files.mtime_ns",
		Criteria: "组内 mtime_ns 最小者保留；并列时取路径字典序最小者。mtime 是文件内容最后被写入的时间，复制文件会生成新的 mtime（除非带 -p 保留）。",
		Scope:    "全部重复类型（完全重复 / 相似图片 / 相似视频）；仅从 state=active 的成员中挑选。",
	},
	{
		ID:       RuleNewestMtime,
		Label:    "最晚修改",
		Field:    "files.mtime_ns",
		Criteria: "组内 mtime_ns 最大者保留；并列时取路径字典序最小者。适合把最近编辑过的版本留下。",
		Scope:    "全部重复类型；仅从 state=active 的成员中挑选。",
	},
	{
		ID:         RuleOldestCreated,
		Label:      "最早创建",
		Field:      "files.btime_ns",
		Criteria:   "组内 btime_ns（真实出生时间）最小者保留；并列时取路径字典序最小者。btime_ns=0 视为「未知」，不参与比较，也不会被选为保留文件。Linux 由 statx(STATX_BTIME) 采集，Windows 取 CreationTime；文件系统不支持时全组都未知。",
		Scope:      "仅当组内至少 1 个成员有已知 btime 时生效；全组未知则条件不适用，回退原有保留项。仅 state=active 成员参与。",
		NeedsBtime: true,
	},
	{
		ID:         RuleNewestCreated,
		Label:      "最晚创建",
		Field:      "files.btime_ns",
		Criteria:   "组内 btime_ns（真实出生时间）最大者保留；并列时取路径字典序最小者。btime_ns=0 视为「未知」，不参与比较。适合保留最近拷贝进来的那份。",
		Scope:      "仅当组内至少 1 个成员有已知 btime 时生效；全组未知则条件不适用，回退原有保留项。仅 state=active 成员参与。",
		NeedsBtime: true,
	},
	{
		ID:       RuleDeepestPath,
		Label:    "路径最深",
		Field:    "files.path",
		Criteria: "把路径统一成 / 后按分隔符个数计层数，层数最大者保留；并列时取路径字典序最小者。注意比较的是绝对路径，跨存储卷时根前缀长度会影响层数。",
		Scope:    "全部重复类型；仅从 state=active 的成员中挑选。",
	},
	{
		ID:       RuleShallowestPath,
		Label:    "路径最浅",
		Field:    "files.path",
		Criteria: "路径层级数最小者保留；并列时取路径字典序最小者。等价于扫描时的默认保留策略（pickKeeper）。",
		Scope:    "全部重复类型；仅从 state=active 的成员中挑选。",
	},
}

// IsValid 判断条件 ID 是否合法（空串=未配置，同样合法）。
func IsValid(rule string) bool {
	if rule == RuleNone {
		return true
	}
	for _, d := range Definitions {
		if d.ID == rule {
			return true
		}
	}
	return false
}

// Definition 返回条件定义。
func Definition(rule string) (Def, bool) {
	for _, d := range Definitions {
		if d.ID == rule {
			return d, true
		}
	}
	return Def{}, false
}

// NeedsBtime 判断条件是否依赖真实创建时间。
func NeedsBtime(rule string) bool {
	d, ok := Definition(rule)
	return ok && d.NeedsBtime
}

// DepthOf 统一按 / 计算路径层级数，与 scan.pickKeeper 的口径保持一致。
func DepthOf(path string) int {
	return strings.Count(strings.ReplaceAll(path, "\\", "/"), "/")
}

// Member 是组内一个成员参与判定所需的最小信息。
type Member struct {
	FileID   int64
	Path     string
	MtimeNS  int64
	BtimeNS  int64 // 0 = 文件系统未提供出生时间
	Depth    int
	Active   bool
	IsKeeper bool
}

// Result 是一次「组内保留判定」的结果。
type Result struct {
	KeeperID int64  // 被判定为保留的 file_id；0 表示本组不干预
	Reason   string // 写入 dup_group_members.keep_reason
	// Applicable=false 表示条件不适用（未配置 / 组内有效成员为 0 / 创建时间全未知）。
	// 调用方据此保持原有 keeper 不变。
	Applicable bool
	// Fallback=true 表示虽然给出了 keeper，但用的是回退口径而不是所选条件。
	Fallback bool
	Note      string
}

func (m Member) depth() int {
	if m.Depth > 0 {
		return m.Depth
	}
	return DepthOf(m.Path)
}

// Resolve 在单个组内按条件选出唯一保留文件。这是一个纯函数，便于单测与复现。
//
// 判定优先级：
//  1. 只考虑 Active 成员（已隔离/已删除/丢失的文件不能作为保留副本）；
//  2. 组内有效成员 = 0 → 不干预；= 1 → 强制保留这最后一份，防止整组被删空；
//  3. 未配置条件 → 不干预（原有行为不变）；
//  4. 依赖 btime 的条件先筛出已知 btime 的成员；全组未知 → 回退原有保留项；
//  5. 按主键排序，主键相同 → 路径字典序升序 → file_id 升序（完全确定性）。
func Resolve(members []Member, rule string) Result {
	active := make([]Member, 0, len(members))
	var existing int64
	for _, m := range members {
		if !m.Active {
			continue
		}
		active = append(active, m)
		if m.IsKeeper && existing == 0 {
			existing = m.FileID
		}
	}
	if len(active) == 0 {
		return Result{Note: "组内无有效成员"}
	}
	if len(active) == 1 {
		return Result{
			KeeperID: active[0].FileID, Reason: "保护最后一份：组内仅剩 1 个有效副本",
			Applicable: true, Fallback: true, Note: "仅剩一份",
		}
	}
	if rule == RuleNone {
		return Result{Applicable: false, Note: "未配置保留条件"}
	}

	def, ok := Definition(rule)
	if !ok {
		return Result{Applicable: false, Note: "未知条件"}
	}

	// 创建时间不可用时回退：优先沿用组内原有的 active keeper，
	// 若没有则退回默认策略「路径最浅 → 路径字典序最小」。
	fallback := func(note string) Result {
		id := existing
		reason := "创建时间不可用，沿用原有保留文件"
		if id == 0 {
			best := shallowest(active)
			id = best.FileID
			reason = "创建时间不可用，回退默认保留（路径最浅）"
		}
		return Result{KeeperID: id, Reason: reason, Applicable: true, Fallback: true, Note: note}
	}

	candidates := active
	if def.NeedsBtime {
		known := make([]Member, 0, len(active))
		for _, m := range active {
			if m.BtimeNS > 0 {
				known = append(known, m)
			}
		}
		if len(known) == 0 {
			return fallback("全组 btime 未知")
		}
		candidates = known
	}

	best := candidates[0]
	for _, m := range candidates[1:] {
		if better(m, best, rule) {
			best = m
		}
	}
	reason := "智能保留 · " + def.Label
	if def.NeedsBtime && len(candidates) < len(active) {
		reason += "（" + strconv.Itoa(len(active)-len(candidates)) + " 份创建时间未知，已排除）"
	}
	return Result{KeeperID: best.FileID, Reason: reason, Applicable: true}
}

// better 判断 a 是否比 b 更应该被保留。
func better(a, b Member, rule string) bool {
	cmp := 0
	switch rule {
	case RuleOldestMtime:
		cmp = compareInt(a.MtimeNS, b.MtimeNS) // 小的优先
	case RuleNewestMtime:
		cmp = -compareInt(a.MtimeNS, b.MtimeNS) // 大的优先
	case RuleOldestCreated:
		cmp = compareInt(a.BtimeNS, b.BtimeNS)
	case RuleNewestCreated:
		cmp = -compareInt(a.BtimeNS, b.BtimeNS)
	case RuleDeepestPath:
		cmp = -compareInt(int64(a.depth()), int64(b.depth())) // 深的优先
	case RuleShallowestPath:
		cmp = compareInt(int64(a.depth()), int64(b.depth())) // 浅的优先
	}
	if cmp != 0 {
		return cmp < 0
	}
	// 主键并列 → 路径字典序升序 → file_id 升序，保证结果不依赖数据库返回顺序
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	return a.FileID < b.FileID
}

func compareInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func shallowest(members []Member) Member {
	best := members[0]
	for _, m := range members[1:] {
		if better(m, best, RuleShallowestPath) {
			best = m
		}
	}
	return best
}
