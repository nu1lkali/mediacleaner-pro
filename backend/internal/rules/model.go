// Package rules 实现可配置的清理规则引擎。
//
// 一条规则由四段声明构成：作用范围（Scope）、匹配条件（Match）、
// 保留策略（Retention）、删除条件（Delete），外加关联文件策略（Associated）。
// 规则只产出"清理计划"（PlanItem），不直接动文件 —— 执行权在 cleaner 包，
// 且必须经过人工确认（除非规则显式 auto_execute）。
package rules

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Rule 对应 rules 表的一行。
type Rule struct {
	ID                 int64       `json:"id"`
	Name               string      `json:"name"`
	Description        string      `json:"description"`
	Enabled            bool        `json:"enabled"`
	Priority           int         `json:"priority"`
	AutoExecute        bool        `json:"auto_execute"`
	RequireConfirmation bool       `json:"require_confirmation"`
	Scope              Scope       `json:"scope"`
	Match              Match       `json:"match"`
	Retention          Retention   `json:"retention"`
	Delete             Delete      `json:"delete"`
	Associated         Associated  `json:"associated"`
	CreatedAt          int64       `json:"created_at"`
	UpdatedAt          int64       `json:"updated_at"`
}

// Scope 决定哪些文件"可以被这条规则处理"。
// 注意：作用范围只影响候选删除文件，不影响保留文件的选取。
type Scope struct {
	RootIDs     []int64  `json:"root_ids"`
	Volumes     []string `json:"volumes"`
	Dirs        []string `json:"dirs"`         // 路径前缀，任一命中即可
	ExcludeDirs []string `json:"exclude_dirs"` // 路径前缀排除
	Kinds       []string `json:"kinds"`        // image / video / audio / document
	ExtInclude  []string `json:"ext_include"`
	ExtExclude  []string `json:"ext_exclude"`
	MinSize     int64    `json:"min_size"`
	MaxSize     int64    `json:"max_size"`
	NameInclude []string `json:"name_include"` // 子串匹配（不区分大小写）
	NameExclude []string `json:"name_exclude"`
	NameRegex   string   `json:"name_regex"`
}

// 分组类型（与 scan 包保持一致，这里用常量避免循环依赖）
const (
	GroupExact = "exact"
	GroupImage = "image"
	GroupVideo = "video"
	GroupName  = "name"
)

// Match 决定"哪些重复组进入这条规则"。
type Match struct {
	// GroupKinds 为空时默认 ["exact"]；可选 exact / image / video / name。
	GroupKinds []string `json:"group_kinds"`
	// MinSimilarity 用于 image / video 组：组内相似度下限，0 表示用系统默认。
	MinSimilarity float64 `json:"min_similarity"`
	// MinGroupMembers 组内至少几个文件才算重复（默认 2）。
	MinGroupMembers int `json:"min_group_members"`
	// RequireExactContent 要求组内必须通过全文校验（仅 exact 有意义）。
	RequireExactContent bool `json:"require_exact_content"`
	// StaleDays > 0 时，只处理修改时间早于 N 天的文件。
	StaleDays int `json:"stale_days"`
	// Operator 为 and 时所有条件同时满足，or 时任一条满足即可。
	Operator string `json:"operator"`
	// SameName 用于"同名不同后缀"场景；GroupKinds 含 name 时必须提供。
	SameName *SameNameMatch `json:"same_name"`
}

// SameNameMatch 描述同名不同后缀的匹配方式。
type SameNameMatch struct {
	// TriggerExtensions 触发主体（如 .mkv/.mp4）。
	TriggerExtensions []string `json:"trigger_extensions"`
	// AssociatedExtensions 允许被判定为"关联文件"的扩展名。
	AssociatedExtensions []string `json:"associated_extensions"`
	// SameDirectoryOnly 默认 true：只处理同目录。
	SameDirectoryOnly bool `json:"same_directory_only"`
	// SameBasenameOnly 默认 true：只处理同名主体。
	SameBasenameOnly bool `json:"same_basename_only"`
	// ProtectReferenced 默认 true：若同名主体另有保留文件，则保护该文件。
	ProtectReferenced bool `json:"protect_referenced"`
}

// Retention 决定组内保留哪些文件。
type Retention struct {
	// Strategy: keep_best（按权重评分，默认）/ keep_largest / keep_newest / keep_oldest
	Strategy string `json:"strategy"`
	// Keys 是自定义优先级组合，按数组顺序、权重累加。
	Keys []RetentionKey `json:"keys"`
	// KeepCount 每组至少保留几个（默认 1）。
	KeepCount int `json:"keep_count"`
	// PinnedPaths 是用户手动指定必须保留的路径（全局）。
	PinnedPaths []string `json:"pinned_paths"`
}

// RetentionKey 是一个排序维度。
//
// Field 取值：
//
//	resolution  分辨率像素总数
//	bitrate     码率
//	size        文件体积
//	mtime       修改时间
//	ctime       创建时间
//	depth       目录深度（越浅越好）
//	ext         扩展名是否命中 Prefer
//	dir_prefix  路径是否以 Value 开头
//	volume      是否位于指定卷
//	name_regex  文件名是否匹配 Value
type RetentionKey struct {
	Field  string   `json:"field"`
	Order  string   `json:"order"` // desc（默认，值大者优先保留）/ asc
	Weight float64  `json:"weight"`
	Value  string   `json:"value"`
	Prefer []string `json:"prefer"`
}

// Delete 决定在保留文件之外，哪些候选文件允许被删除。
type Delete struct {
	// Kinds 限制只删这些媒体类型；为空表示全部。
	Kinds []string `json:"kinds"`
	// MaxResolution > 0 时，只删除分辨率低于该值的文件（"清理低清版本"）。
	MaxResolution int `json:"max_resolution"`
	// MinResolution > 0 时，只删除分辨率高于该值的文件（少见，谨慎）。
	MinResolution int `json:"min_resolution"`
	// OlderThanDays > 0 时，只删除旧于 N 天的文件。
	OlderThanDays int `json:"older_than_days"`
	// PathRegex 命中任一正则才允许删除。
	PathRegex []string `json:"path_regex"`
	// MaxDeletes 单次计划最多删除的文件数（0 不限），防止误伤。
	MaxDeletes int `json:"max_deletes_per_run"`
	// MinReclaimBytes 本次计划必须至少可释放的字节数。
	MinReclaimBytes int64 `json:"min_reclaim_bytes"`
	// KeepAtLeastOne 保证每组至少留一个，恒为 true，保留字段用于兼容。
	KeepAtLeastOne bool `json:"keep_at_least_one"`
}

// Associated 是"删除主文件时同步清理附属文件"的策略。
type Associated struct {
	Enabled              bool     `json:"enabled"`
	TriggerExtensions    []string `json:"trigger_extensions"`
	AssociatedExtensions []string `json:"associated_extensions"`
	SameDirectoryOnly    bool     `json:"same_directory_only"`
	SameBasenameOnly     bool     `json:"same_basename_only"`
	RequireConfirmation  bool     `json:"require_confirmation"`
	ProtectReferenced    bool     `json:"protect_referenced"`
}

// ---------- 序列化 ----------

func (r *Rule) ScopeJSON() string     { return mustJSON(r.Scope) }
func (r *Rule) MatchJSON() string     { return mustJSON(r.Match) }
func (r *Rule) RetentionJSON() string { return mustJSON(r.Retention) }
func (r *Rule) DeleteJSON() string    { return mustJSON(r.Delete) }
func (r *Rule) AssocJSON() string     { return mustJSON(r.Associated) }

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Normalize 补齐默认值并校验必填项。
func (r *Rule) Normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Name == "" {
		return fmt.Errorf("规则名称不能为空")
	}
	if len(r.Match.GroupKinds) == 0 {
		r.Match.GroupKinds = []string{GroupExact}
	}
	for _, k := range r.Match.GroupKinds {
		switch k {
		case GroupExact, GroupImage, GroupVideo, GroupName:
		default:
			return fmt.Errorf("未知的重复判断方式: %s", k)
		}
	}
	if r.Match.Operator == "" {
		r.Match.Operator = "and"
	}
	r.Match.Operator = strings.ToLower(r.Match.Operator)
	if r.Match.Operator != "and" && r.Match.Operator != "or" {
		return fmt.Errorf("operator 只能是 and / or")
	}
	if r.Match.MinGroupMembers < 2 {
		r.Match.MinGroupMembers = 2
	}
	if r.Retention.Strategy == "" {
		r.Retention.Strategy = "keep_best"
	}
	if r.Retention.KeepCount < 1 {
		r.Retention.KeepCount = 1
	}
	for i := range r.Retention.Keys {
		k := &r.Retention.Keys[i]
		if k.Order == "" {
			k.Order = "desc"
		}
		k.Order = strings.ToLower(k.Order)
		if k.Weight == 0 {
			k.Weight = 1
		}
		switch k.Field {
		case "resolution", "bitrate", "size", "mtime", "ctime", "depth",
			"ext", "dir_prefix", "volume", "name_regex", "duration":
		default:
			return fmt.Errorf("未知的保留维度: %s", k.Field)
		}
	}
	if r.Priority == 0 {
		r.Priority = 100
	}
	if contains(r.Match.GroupKinds, GroupName) {
		if r.Match.SameName == nil {
			return fmt.Errorf("选择\"同名不同后缀\"时必须提供 same_name 配置")
		}
		if len(r.Match.SameName.AssociatedExtensions) == 0 {
			return fmt.Errorf("same_name.associated_extensions 不能为空")
		}
	}
	if r.Associated.Enabled && len(r.Associated.AssociatedExtensions) == 0 {
		return fmt.Errorf("启用关联文件清理时必须指定关联扩展名")
	}
	// 校验正则
	if r.Scope.NameRegex != "" {
		if _, err := regexp.Compile(r.Scope.NameRegex); err != nil {
			return fmt.Errorf("文件名正则无效: %v", err)
		}
	}
	for _, p := range r.Delete.PathRegex {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("路径正则无效 %s: %v", p, err)
		}
	}
	return nil
}

// DefaultRules 是首次启动时内置的几套规则，覆盖常见场景。
// 全部默认 require_confirmation=true 且 auto_execute=false —— 安全优先。
func DefaultRules() []Rule {
	return []Rule{
		{
			Name:                "清理完全重复文件",
			Description:         "保留路径层级最浅的一份，其余进入隔离区。同时清理同名附属文件（NFO/字幕/封面）。",
			Enabled:             true,
			Priority:            10,
			RequireConfirmation: true,
			Scope:               Scope{Kinds: []string{"image", "video", "audio", "document"}},
			Match:               Match{GroupKinds: []string{GroupExact}, RequireExactContent: true, MinGroupMembers: 2, Operator: "and"},
			Retention: Retention{
				Strategy:  "keep_best",
				KeepCount: 1,
				Keys: []RetentionKey{
					{Field: "depth", Order: "asc", Weight: 3},
					{Field: "mtime", Order: "asc", Weight: 1},
				},
			},
			Delete: Delete{Kinds: []string{"image", "video", "audio", "document"}},
			Associated: Associated{
				Enabled:              true,
				TriggerExtensions:    []string{".mkv", ".mp4", ".avi", ".mov", ".ts"},
				AssociatedExtensions: []string{".nfo", ".jpg", ".png", ".srt", ".ass", ".xml"},
				SameDirectoryOnly:    true,
				SameBasenameOnly:     true,
				RequireConfirmation:  true,
				ProtectReferenced:    true,
			},
		},
		{
			Name:                "保留高分辨率版本",
			Description:         "同一视频的多个分辨率版本中保留最高的，低分辨率版本作为候选删除。",
			Enabled:             true,
			Priority:            20,
			RequireConfirmation: true,
			Scope:               Scope{Kinds: []string{"video", "image"}},
			Match:               Match{GroupKinds: []string{GroupVideo, GroupImage}, MinSimilarity: 0.88, MinGroupMembers: 2},
			Retention: Retention{
				Strategy:  "keep_best",
				KeepCount: 1,
				Keys: []RetentionKey{
					{Field: "resolution", Order: "desc", Weight: 5},
					{Field: "bitrate", Order: "desc", Weight: 3},
					{Field: "size", Order: "desc", Weight: 1},
				},
			},
			Delete: Delete{Kinds: []string{"video", "image"}},
		},
		{
			Name:                "清理低清重复图片",
			Description:         "相似图片组中删除分辨率不足 720p 的副本，保护原图。",
			Enabled:             false,
			Priority:            30,
			RequireConfirmation: true,
			Scope:               Scope{Kinds: []string{"image"}},
			Match:               Match{GroupKinds: []string{GroupImage}, MinSimilarity: 0.9, MinGroupMembers: 2},
			Retention: Retention{
				Strategy:  "keep_best",
				KeepCount: 1,
				Keys: []RetentionKey{
					{Field: "resolution", Order: "desc", Weight: 4},
					{Field: "size", Order: "desc", Weight: 2},
				},
			},
			Delete: Delete{Kinds: []string{"image"}, MaxResolution: 1280 * 720},
		},
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
