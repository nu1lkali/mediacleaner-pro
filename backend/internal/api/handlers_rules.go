package api

import (
	"errors"
	"net/http"

	"mediacleaner/internal/rules"
)

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	list, err := s.ruleStore.List(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, map[string]any{"items": list})
}

func (s *Server) handleGetRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	rule, err := s.ruleStore.Get(r.Context(), id)
	if err != nil {
		fail(w, notFound("规则不存在"))
		return
	}
	ok(w, rule)
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	var rule rules.Rule
	if err := decodeBody(r, &rule); err != nil {
		fail(w, err)
		return
	}
	rule.ID = 0
	created, err := s.ruleStore.Create(r.Context(), &rule)
	if err != nil {
		fail(w, badRequest(err.Error()))
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "rule.create", "rule", created.Name, created, clientIP(r))
	ok(w, created)
}

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	var rule rules.Rule
	if err := decodeBody(r, &rule); err != nil {
		fail(w, err)
		return
	}
	rule.ID = id
	updated, err := s.ruleStore.Update(r.Context(), &rule)
	if err != nil {
		if errors.Is(err, rules.ErrNotFound) {
			fail(w, notFound("规则不存在"))
			return
		}
		fail(w, badRequest(err.Error()))
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "rule.update", "rule", updated.Name, updated, clientIP(r))
	ok(w, updated)
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.ruleStore.Delete(r.Context(), id); err != nil {
		if errors.Is(err, rules.ErrNotFound) {
			fail(w, notFound("规则不存在"))
			return
		}
		fail(w, err)
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "rule.delete", "rule", "", map[string]any{"id": id}, clientIP(r))
	ok(w, map[string]any{"ok": true})
}

type toggleReq struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) handleToggleRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	var req toggleReq
	if err := decodeBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	if err := s.ruleStore.SetEnabled(r.Context(), id, req.Enabled); err != nil {
		fail(w, badRequest(err.Error()))
		return
	}
	rule, err := s.ruleStore.Get(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, rule)
}

type dryRunReq struct {
	RuleIDs []int64 `json:"rule_ids"`
	// MaxItems 仅影响返回给前端的明细条数，不影响总量统计
	MaxItems int `json:"max_items"`
}

// handleDryRun 是"清理前预览"的核心：只计算，不改动任何文件。
func (s *Server) handleDryRun(w http.ResponseWriter, r *http.Request) {
	var req dryRunReq
	if err := decodeBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	plan, err := s.ruleEngine.Evaluate(r.Context(), req.RuleIDs)
	if err != nil {
		fail(w, badRequest(err.Error()))
		return
	}
	total := len(plan.Items)
	items := plan.Items
	truncated := false
	max := req.MaxItems
	if max <= 0 {
		max = 2000
	}
	if len(items) > max {
		items = items[:max]
		truncated = true
	}
	ok(w, map[string]any{
		"items":       items,
		"total_items": total,
		"truncated":   truncated,
		"group_count": plan.GroupCount,
		"total_bytes": plan.TotalBytes,
		"by_kind":     plan.ByKind,
		"by_kind_bytes": plan.BytKindBytes,
		"rule_stats":  plan.RuleStats,
		"notes":       plan.Notes,
	})
}
