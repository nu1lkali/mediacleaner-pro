package rules

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"mediacleaner/internal/store"
)

var ErrNotFound = errors.New("规则不存在")

type Store struct {
	st *store.Store
}

func NewStore(st *store.Store) *Store { return &Store{st: st} }

const ruleColumns = `id, name, description, enabled, priority, auto_execute, require_confirmation,
	scope_json, match_json, retention_json, delete_json, associated_json, created_at, updated_at`

func (s *Store) List(ctx context.Context) ([]Rule, error) {
	rows, err := s.st.Select(ctx, `SELECT `+ruleColumns+` FROM rules ORDER BY priority, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id int64) (*Rule, error) {
	rows, err := s.st.Select(ctx, `SELECT `+ruleColumns+` FROM rules WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrNotFound
	}
	r, err := scanRule(rows)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Store) Enabled(ctx context.Context) ([]Rule, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(all))
	for _, r := range all {
		if r.Enabled {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *Store) Create(ctx context.Context, r *Rule) (*Rule, error) {
	if err := r.Normalize(); err != nil {
		return nil, err
	}
	now := store.NowMS()
	res, err := s.st.Exec(ctx, `
		INSERT INTO rules(name, description, enabled, priority, auto_execute, require_confirmation,
		                  scope_json, match_json, retention_json, delete_json, associated_json,
		                  created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.Name, r.Description, bit(r.Enabled), r.Priority, bit(r.AutoExecute), bit(r.RequireConfirmation),
		r.ScopeJSON(), r.MatchJSON(), r.RetentionJSON(), r.DeleteJSON(), r.AssocJSON(), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Get(ctx, id)
}

func (s *Store) Update(ctx context.Context, r *Rule) (*Rule, error) {
	if r.ID == 0 {
		return nil, errors.New("缺少规则 ID")
	}
	if err := r.Normalize(); err != nil {
		return nil, err
	}
	res, err := s.st.Exec(ctx, `
		UPDATE rules SET name=?, description=?, enabled=?, priority=?, auto_execute=?,
		  require_confirmation=?, scope_json=?, match_json=?, retention_json=?,
		  delete_json=?, associated_json=?, updated_at=?
		WHERE id=?`,
		r.Name, r.Description, bit(r.Enabled), r.Priority, bit(r.AutoExecute), bit(r.RequireConfirmation),
		r.ScopeJSON(), r.MatchJSON(), r.RetentionJSON(), r.DeleteJSON(), r.AssocJSON(),
		store.NowMS(), r.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.Get(ctx, r.ID)
}

func (s *Store) SetEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := s.st.Exec(ctx, `UPDATE rules SET enabled=?, updated_at=? WHERE id=?`, bit(enabled), store.NowMS(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.st.Exec(ctx, `DELETE FROM rules WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SeedDefaults 仅在规则表为空时写入内置规则。
func (s *Store) SeedDefaults(ctx context.Context) (int, error) {
	var n int
	if err := s.st.Get(ctx, &n, `SELECT COUNT(*) FROM rules`); err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, nil
	}
	created := 0
	for _, r := range DefaultRules() {
		rule := r
		if _, err := s.Create(ctx, &rule); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRule(rows *sql.Rows) (*Rule, error) {
	var (
		r                          Rule
		enabled, auto, needConfirm int
		scope, match, retention    string
		del, assoc                 string
	)
	if err := rows.Scan(&r.ID, &r.Name, &r.Description, &enabled, &r.Priority, &auto, &needConfirm,
		&scope, &match, &retention, &del, &assoc, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.Enabled = enabled == 1
	r.AutoExecute = auto == 1
	r.RequireConfirmation = needConfirm == 1
	_ = json.Unmarshal([]byte(scope), &r.Scope)
	_ = json.Unmarshal([]byte(match), &r.Match)
	_ = json.Unmarshal([]byte(retention), &r.Retention)
	_ = json.Unmarshal([]byte(del), &r.Delete)
	_ = json.Unmarshal([]byte(assoc), &r.Associated)
	// 老数据可能缺字段，补齐默认
	_ = r.Normalize()
	return &r, nil
}

func bit(b bool) int {
	if b {
		return 1
	}
	return 0
}
