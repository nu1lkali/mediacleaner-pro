package api

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"mediacleaner/internal/auth"
	"mediacleaner/internal/media"
)

// loginLimiter 是极简的登录失败限流（按 IP），防止在线爆破。
// 只在内存中保存，重启即清空 —— 对单实例 NAS 部署足够。
type loginLimiter struct {
	mu       sync.Mutex
	fails    map[string][]int64
	maxFails int
	window   time.Duration
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{fails: map[string][]int64{}, maxFails: 8, window: 5 * time.Minute}
}

func (l *loginLimiter) blocked(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().UnixMilli()
	cut := now - l.window.Milliseconds()
	kept := l.fails[ip][:0]
	var oldest int64
	for _, t := range l.fails[ip] {
		if t > cut {
			kept = append(kept, t)
			if oldest == 0 {
				oldest = t
			}
		}
	}
	l.fails[ip] = kept
	if len(kept) < l.maxFails {
		return false, 0
	}
	return true, time.Duration(oldest + l.window.Milliseconds() - now) * time.Millisecond
}

func (l *loginLimiter) record(ip string) {
	l.mu.Lock()
	l.fails[ip] = append(l.fails[ip], time.Now().UnixMilli())
	l.mu.Unlock()
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.fails, ip)
	l.mu.Unlock()
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Remember bool   `json:"remember"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if blocked, wait := s.limiter.blocked(ip); blocked {
		writeJSON(w, http.StatusTooManyRequests, &apiError{
			Status: 429, Code: "too_many_attempts",
			Msg: "登录失败次数过多，请 " + wait.Round(time.Second).String() + " 后重试",
		})
		return
	}
	var req loginReq
	if err := decodeBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		fail(w, badRequest("请输入用户名和密码"))
		return
	}

	user, err := s.auth.Authenticate(r.Context(), req.Username, req.Password)
	if err != nil {
		s.limiter.record(ip)
		switch {
		case errors.Is(err, auth.ErrDisabled):
			fail(w, forbidden("账号已被禁用"))
		case errors.Is(err, auth.ErrInvalidCredentials):
			fail(w, unauthorized("用户名或密码错误"))
		default:
			fail(w, serverError(err.Error()))
		}
		return
	}
	s.limiter.reset(ip)

	token, expires, err := s.auth.CreateSession(r.Context(), user.ID, ip, r.UserAgent())
	if err != nil {
		fail(w, serverError("创建会话失败: "+err.Error()))
		return
	}
	cookie := &http.Cookie{
		Name:     s.cfg.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	}
	if req.Remember {
		cookie.Expires = expires
		cookie.MaxAge = int(time.Until(expires).Seconds())
	}
	http.SetCookie(w, cookie)

	s.st.Audit(r.Context(), user.Username, "auth.login", "user", user.Username,
		map[string]any{"ip": ip}, ip)
	ok(w, map[string]any{
		"user":       user,
		"expires_at": expires.UnixMilli(),
		"capabilities": media.Capabilities(),
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.cfg.CookieName); err == nil && c.Value != "" {
		s.auth.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.cfg.CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	ok(w, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if u == nil {
		fail(w, unauthorized("未登录"))
		return
	}
	count, _ := s.auth.CountUsers(r.Context())
	ok(w, map[string]any{
		"user":         u,
		"user_count":   count,
		"capabilities": media.Capabilities(),
		"app":          s.cfg.AppName,
		"version":      s.cfg.Version,
	})
}

type changePasswordReq struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var req changePasswordReq
	if err := decodeBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	if err := s.auth.ChangePassword(r.Context(), u.ID, req.OldPassword, req.NewPassword); err != nil {
		fail(w, badRequest(err.Error()))
		return
	}
	s.st.Audit(r.Context(), u.Username, "auth.change_password", "user", u.Username, nil, clientIP(r))
	http.SetCookie(w, &http.Cookie{
		Name: s.cfg.CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	ok(w, map[string]any{"ok": true, "message": "密码已修改，请重新登录"})
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.auth.ListUsers(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, map[string]any{"items": list})
}

type createUserReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserReq
	if err := decodeBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	u, err := s.auth.CreateUser(r.Context(), req.Username, req.Password, req.Role)
	if err != nil {
		if errors.Is(err, auth.ErrUserExists) {
			fail(w, conflict(err.Error()))
			return
		}
		fail(w, badRequest(err.Error()))
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "user.create", "user", u.Username, nil, clientIP(r))
	ok(w, u)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	if u := userFrom(r); u != nil && u.ID == id {
		fail(w, badRequest("不能删除当前登录账号"))
		return
	}
	if err := s.auth.DeleteUser(r.Context(), id); err != nil {
		fail(w, badRequest(err.Error()))
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "user.delete", "user", "", nil, clientIP(r))
	ok(w, map[string]any{"ok": true})
}
