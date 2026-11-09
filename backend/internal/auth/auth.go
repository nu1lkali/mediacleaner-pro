// Package auth 提供账号、口令与会话管理。
//
// 设计：
//   - 口令使用 PBKDF2-HMAC-SHA256（21 万次迭代）+ 16 字节随机盐，纯标准库实现，无外部依赖；
//   - 会话令牌 32 字节随机数，仅把 SHA-256 摘要入库，泄库也无法直接复用令牌；
//   - Cookie：HttpOnly + SameSite=Lax（同源 SPA 足够，跨站请求不带 Cookie，天然防 CSRF 主体）；
//   - 首次启动若无账号，自动创建 admin，口令来自 MC_ADMIN_PASSWORD，或随机生成并写入 config/initial-admin.txt。
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mediacleaner/internal/config"
	"mediacleaner/internal/store"
)

var (
	ErrInvalidCredentials = errors.New("用户名或密码错误")
	ErrDisabled           = errors.New("账号已被禁用")
	ErrWeakPassword       = errors.New("密码至少 8 位，且不能与用户名相同")
	ErrUserExists         = errors.New("用户名已存在")
	ErrUnauthorized       = errors.New("未登录或会话已过期")
)

const iterations = 210000

type User struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	Disabled    bool   `json:"disabled"`
	CreatedAt   int64  `json:"created_at"`
	LastLoginAt *int64 `json:"last_login_at"`
}

type Service struct {
	st  *store.Store
	cfg *config.Config
}

func New(st *store.Store, cfg *config.Config) *Service { return &Service{st: st, cfg: cfg} }

// ---------- 口令哈希 ----------

func pbkdf2SHA256(password string, salt []byte, iter, keyLen int) []byte {
	hLen := 32
	blocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, blocks*hLen)
	for block := 1; block <= blocks; block++ {
		mac := hmac.New(sha256.New, []byte(password))
		mac.Write(salt)
		mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := mac.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for i := 1; i < iter; i++ {
			mac = hmac.New(sha256.New, []byte(password))
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := pbkdf2SHA256(password, salt, iterations, 32)
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got := pbkdf2SHA256(password, salt, iter, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ---------- 账号 ----------

func (s *Service) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.st.Get(ctx, &n, `SELECT COUNT(*) FROM users`)
	return n, err
}

// EnsureAdmin 首次启动创建管理员。返回生成的初始口令（若由系统生成）。
func (s *Service) EnsureAdmin(ctx context.Context) (string, error) {
	n, err := s.CountUsers(ctx)
	if err != nil {
		return "", err
	}
	if n > 0 {
		return "", nil
	}
	password := strings.TrimSpace(os.Getenv("MC_ADMIN_PASSWORD"))
	generated := false
	if len(password) < 8 {
		password = randomToken(9)
		generated = true
	}
	hash, err := HashPassword(password)
	if err != nil {
		return "", err
	}
	if _, err := s.st.Exec(ctx,
		`INSERT INTO users(username, password_hash, role, disabled, created_at) VALUES(?,?, 'admin', 0, ?)`,
		"admin", hash, store.NowMS()); err != nil {
		return "", err
	}
	if generated {
		_ = os.MkdirAll(s.cfg.ConfigDir, 0o755)
		path := filepath.Join(s.cfg.ConfigDir, "initial-admin.txt")
		content := fmt.Sprintf("用户名: admin\n初始密码: %s\n\n请登录后立即修改密码，并删除本文件。\n", password)
		_ = os.WriteFile(path, []byte(content), 0o600)
	}
	return password, nil
}

func (s *Service) Authenticate(ctx context.Context, username, password string) (*User, error) {
	var (
		id       int64
		hash     string
		role     string
		disabled int
		created  int64
		last     sql.NullInt64
	)
	err := s.st.Get(ctx, []any{&id, &hash, &role, &disabled, &created, &last},
		`SELECT id, password_hash, role, disabled, created_at, last_login_at FROM users WHERE username=?`, username)
	if errors.Is(err, sql.ErrNoRows) {
		// 统一错误信息，避免用户名探测
		_ = VerifyPassword("pbkdf2_sha256$1$AAAA$AAAA", password)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if !VerifyPassword(hash, password) {
		return nil, ErrInvalidCredentials
	}
	if disabled == 1 {
		return nil, ErrDisabled
	}
	now := store.NowMS()
	_, _ = s.st.Exec(ctx, `UPDATE users SET last_login_at=? WHERE id=?`, now, id)
	u := &User{ID: id, Username: username, Role: role, Disabled: false, CreatedAt: created}
	u.LastLoginAt = &now
	return u, nil
}

func (s *Service) GetUser(ctx context.Context, id int64) (*User, error) {
	var (
		u        User
		disabled int
		last     sql.NullInt64
	)
	err := s.st.Get(ctx, []any{&u.ID, &u.Username, &u.Role, &disabled, &u.CreatedAt, &last},
		`SELECT id, username, role, disabled, created_at, last_login_at FROM users WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	u.Disabled = disabled == 1
	if last.Valid {
		v := last.Int64
		u.LastLoginAt = &v
	}
	return &u, nil
}

func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.st.Select(ctx, `SELECT id, username, role, disabled, created_at, last_login_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var (
			u        User
			disabled int
			last     sql.NullInt64
		)
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &disabled, &u.CreatedAt, &last); err != nil {
			return nil, err
		}
		u.Disabled = disabled == 1
		if last.Valid {
			v := last.Int64
			u.LastLoginAt = &v
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Service) CreateUser(ctx context.Context, username, password, role string) (*User, error) {
	username = strings.TrimSpace(username)
	if len(username) < 3 {
		return nil, errors.New("用户名至少 3 位")
	}
	if len(password) < 8 || strings.EqualFold(password, username) {
		return nil, ErrWeakPassword
	}
	if role != "admin" && role != "viewer" {
		role = "viewer"
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	now := store.NowMS()
	if _, err := s.st.Exec(ctx,
		`INSERT INTO users(username, password_hash, role, disabled, created_at) VALUES(?,?,?,0,?)`,
		username, hash, role, now); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrUserExists
		}
		return nil, err
	}
	var id int64
	if err := s.st.Get(ctx, &id, `SELECT id FROM users WHERE username=?`, username); err != nil {
		return nil, err
	}
	return &User{ID: id, Username: username, Role: role, CreatedAt: now}, nil
}

func (s *Service) ChangePassword(ctx context.Context, userID int64, oldPassword, newPassword string) error {
	var hash string
	if err := s.st.Get(ctx, &hash, `SELECT password_hash FROM users WHERE id=?`, userID); err != nil {
		return errors.New("账号不存在")
	}
	if !VerifyPassword(hash, oldPassword) {
		return errors.New("原密码不正确")
	}
	if len(newPassword) < 8 {
		return ErrWeakPassword
	}
	nh, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	if _, err := s.st.Exec(ctx, `UPDATE users SET password_hash=? WHERE id=?`, nh, userID); err != nil {
		return err
	}
	_, _ = s.st.Exec(ctx, `DELETE FROM sessions WHERE user_id=?`, userID)
	return nil
}

func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	var n int
	if err := s.st.Get(ctx, &n, `SELECT COUNT(*) FROM users`); err != nil {
		return err
	}
	if n <= 1 {
		return errors.New("至少保留一个账号")
	}
	_, err := s.st.Exec(ctx, `DELETE FROM users WHERE id=?`, id)
	return err
}

// ---------- 会话 ----------

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().String()))[:2*n]
	}
	return hex.EncodeToString(b)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Service) CreateSession(ctx context.Context, userID int64, ip, ua string) (string, time.Time, error) {
	token := randomToken(32)
	expires := time.Now().Add(s.cfg.SessionTTL)
	_, err := s.st.Exec(ctx,
		`INSERT INTO sessions(token_hash, user_id, created_at, expires_at, ip, user_agent) VALUES(?,?,?,?,?,?)`,
		hashToken(token), userID, store.NowMS(), expires.UnixMilli(), ip, truncate(ua, 200))
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

func (s *Service) LookupSession(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, ErrUnauthorized
	}
	var (
		userID  int64
		expires int64
	)
	err := s.st.Get(ctx, []any{&userID, &expires},
		`SELECT user_id, expires_at FROM sessions WHERE token_hash=?`, hashToken(token))
	if err != nil {
		return nil, ErrUnauthorized
	}
	if expires < store.NowMS() {
		_, _ = s.st.Exec(ctx, `DELETE FROM sessions WHERE token_hash=?`, hashToken(token))
		return nil, ErrUnauthorized
	}
	u, err := s.GetUser(ctx, userID)
	if err != nil || u.Disabled {
		return nil, ErrUnauthorized
	}
	return u, nil
}

func (s *Service) DeleteSession(ctx context.Context, token string) {
	if token == "" {
		return
	}
	_, _ = s.st.Exec(ctx, `DELETE FROM sessions WHERE token_hash=?`, hashToken(token))
}

func (s *Service) PurgeExpiredSessions(ctx context.Context) {
	_, _ = s.st.Exec(ctx, `DELETE FROM sessions WHERE expires_at < ?`, store.NowMS())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
