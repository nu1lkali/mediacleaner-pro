package api

import (
	"net/http"
	"strings"

	"mediacleaner/internal/keeper"
)

// handleGetKeeperRule 返回某类分组当前生效的保留条件、全部条件口径定义，
// 以及该类型下创建时间（btime）的可用比例，供前端准确展示适用范围。
func (s *Server) handleGetKeeperRule(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = "exact"
	}
	known, total, err := keeper.BtimeCoverage(r.Context(), s.st, kind)
	if err != nil {
		fail(w, err)
		return
	}
	var groups, configured int
	if err := s.st.Get(r.Context(), []any{&groups, &configured}, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN keeper_rule<>'' THEN 1 ELSE 0 END),0)
		FROM dup_groups WHERE kind=? AND batch_active=1`, kind); err != nil {
		fail(w, err)
		return
	}
	ok(w, map[string]any{
		"kind":         kind,
		"rule":          keeper.EffectiveRule(r.Context(), s.st, kind),
		"defaults":     keeper.Defaults(r.Context(), s.st),
		"definitions":  keeper.Definitions,
		"groups":       groups,
		"configured":   configured,
		"btime_known":  known,
		"btime_total":  total,
		"settings_key": keeper.SettingsKey,
	})
}

type applyKeeperRuleReq struct {
	Kind    string `json:"kind"`
	Rule    string `json:"rule"`
	GroupID int64  `json:"group_id"`
}

// handleApplyKeeperRule 一键按所选条件智能设定保留项。
// group_id>0 时只作用于单个分组（不改动该类型的默认条件）；
// group_id=0 时作用于该类型全部分组，并把条件持久化为默认值，扫描重建后自动恢复。
// rule="" 表示清除条件：不改动任何 is_keeper，完全回到原有去重行为。
func (s *Server) handleApplyKeeperRule(w http.ResponseWriter, r *http.Request) {
	var req applyKeeperRuleReq
	if err := decodeBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	req.Kind = strings.TrimSpace(req.Kind)
	req.Rule = strings.TrimSpace(req.Rule)
	if req.GroupID == 0 && req.Kind == "" {
		req.Kind = "exact"
	}
	if !keeper.IsValid(req.Rule) {
		fail(w, badRequest("不支持的保留条件: "+req.Rule))
		return
	}

	// 单组应用时把该组真实的 kind 带出来，便于返回统计
	if req.GroupID > 0 && req.Kind == "" {
		_ = s.st.Get(r.Context(), &req.Kind, `SELECT kind FROM dup_groups WHERE id=? AND batch_active=1`, req.GroupID)
	}

	stats, err := keeper.Apply(r.Context(), s.st, req.Kind, req.Rule, req.GroupID)
	if err != nil {
		fail(w, badRequest(err.Error()))
		return
	}
	if req.GroupID == 0 {
		if err := keeper.SetDefault(r.Context(), s.st, req.Kind, req.Rule); err != nil {
			fail(w, serverError("保存默认保留条件失败: "+err.Error()))
			return
		}
	}
	def, _ := keeper.Definition(req.Rule)
	s.st.Audit(r.Context(), actorOf(r), "group.apply_keeper_rule", "dup_group", "",
		map[string]any{
			"kind": req.Kind, "rule": req.Rule, "label": def.Label,
			"group_id": req.GroupID, "groups": stats.Groups, "changed": stats.Changed,
			"unavailable": stats.Unavailable,
		}, clientIP(r))
	ok(w, map[string]any{
		"ok": true, "stats": stats, "defaults": keeper.Defaults(r.Context(), s.st),
	})
}
