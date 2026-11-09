package api

import (
	"context"
	"log"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"mediacleaner/internal/auth"
)

type ctxKey string

const userKey ctxKey = "mc.user"

// userFrom 取出当前登录用户。
func userFrom(r *http.Request) *auth.User {
	if v, ok := r.Context().Value(userKey).(*auth.User); ok {
		return v
	}
	return nil
}

func actorOf(r *http.Request) string {
	if u := userFrom(r); u != nil {
		return u.Username
	}
	return "anonymous"
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.IndexByte(v, ','); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return strings.TrimSpace(v)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// requireAuth 校验会话 Cookie，并把用户放入请求上下文。
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(s.cfg.CookieName)
		if err != nil || cookie.Value == "" {
			fail(w, unauthorized("请先登录"))
			return
		}
		user, err := s.auth.LookupSession(r.Context(), cookie.Value)
		if err != nil {
			http.SetCookie(w, &http.Cookie{
				Name: s.cfg.CookieName, Value: "", Path: "/", MaxAge: -1,
				HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
			fail(w, unauthorized("会话已过期，请重新登录"))
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
	}
}

// requireAdmin 在登录基础上要求管理员角色。
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		u := userFrom(r)
		if u == nil || u.Role != "admin" {
			fail(w, forbidden("需要管理员权限"))
			return
		}
		next(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = 200
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

// Flush 让 SSE 能在包装后继续工作。
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 静态资源与 SSE 不记录，避免日志噪音
		if strings.HasPrefix(r.URL.Path, "/assets/") || strings.HasSuffix(r.URL.Path, "/events") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = 200
		}
		log.Printf("%s %s %d %dB %s %s", r.Method, r.URL.Path, rec.status,
			rec.bytes, time.Since(start).Round(time.Millisecond), clientIP(r))
	})
}

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("panic %s %s: %v\n%s", r.Method, r.URL.Path, v, debug.Stack())
				writeJSON(w, http.StatusInternalServerError, &apiError{
					Status: 500, Code: "panic", Msg: "服务内部错误，请查看日志",
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withSecurityHeaders 加一组保守的安全响应头。
func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		// 预览需要 blob/data 与内联样式，因此 CSP 只做最小限制
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; "+
				"style-src 'self' 'unsafe-inline'; script-src 'self'; "+
				"connect-src 'self'; font-src 'self' data:; frame-ancestors 'self'")
		next.ServeHTTP(w, r)
	})
}
