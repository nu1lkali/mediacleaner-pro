// Package api 提供 REST 接口、SSE 进度推送与前端静态资源托管。
package api

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mediacleaner/internal/auth"
	"mediacleaner/internal/cleaner"
	"mediacleaner/internal/config"
	"mediacleaner/internal/media"
	"mediacleaner/internal/rules"
	"mediacleaner/internal/scan"
	"mediacleaner/internal/store"
)

// Server 汇总所有依赖，构造 HTTP 路由。
type Server struct {
	cfg        *config.Config
	st         *store.Store
	auth       *auth.Service
	roots      *scan.RootService
	eng        *scan.Engine
	ruleStore  *rules.Store
	ruleEngine *rules.Engine
	exec       *cleaner.Executor
	quar       *cleaner.Quarantine
	limiter    *loginLimiter
	mux        *http.ServeMux
}

func New(cfg *config.Config, st *store.Store, au *auth.Service, roots *scan.RootService,
	eng *scan.Engine, rs *rules.Store, re *rules.Engine, ex *cleaner.Executor, q *cleaner.Quarantine) *Server {
	s := &Server{cfg: cfg, st: st, auth: au, roots: roots, eng: eng,
		ruleStore: rs, ruleEngine: re, exec: ex, quar: q, limiter: newLoginLimiter()}
	s.mux = http.NewServeMux()
	s.routes()
	return s
}

// Handler 返回带中间件的根 handler。
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.mux
	h = s.withSecurityHeaders(h)
	h = s.withLogging(h)
	h = s.withRecover(h)
	return h
}

func (s *Server) routes() {
	m := s.mux

	// ---------- 公开接口 ----------
	m.HandleFunc("GET /api/health", s.handleHealth)
	m.HandleFunc("GET /api/system/bootstrap", s.handleBootstrap) // 登录页需要知道是否已初始化
	m.HandleFunc("POST /api/auth/login", s.handleLogin)
	m.HandleFunc("POST /api/auth/logout", s.handleLogout)

	// ---------- 需要登录 ----------
	m.HandleFunc("GET /api/auth/me", s.requireAuth(s.handleMe))
	m.HandleFunc("POST /api/auth/password", s.requireAuth(s.handleChangePassword))

	// 用户管理（管理员）
	m.HandleFunc("GET /api/users", s.requireAdmin(s.handleListUsers))
	m.HandleFunc("POST /api/users", s.requireAdmin(s.handleCreateUser))
	m.HandleFunc("DELETE /api/users/{id}", s.requireAdmin(s.handleDeleteUser))

	// 系统与存储卷
	m.HandleFunc("GET /api/system/info", s.requireAuth(s.handleSystemInfo))
	m.HandleFunc("GET /api/roots", s.requireAuth(s.handleListRoots))
	m.HandleFunc("POST /api/roots", s.requireAdmin(s.handleAddRoot))
	m.HandleFunc("PATCH /api/roots/{id}", s.requireAdmin(s.handleUpdateRoot))
	m.HandleFunc("DELETE /api/roots/{id}", s.requireAdmin(s.handleDeleteRoot))
	m.HandleFunc("GET /api/roots/browse", s.requireAuth(s.handleBrowse))

	// 扫描
	m.HandleFunc("GET /api/scans", s.requireAuth(s.handleListScans))
	m.HandleFunc("GET /api/scans/profiles", s.requireAuth(s.handleScanProfiles))
	m.HandleFunc("GET /api/scans/current", s.requireAuth(s.handleCurrentScan))
	m.HandleFunc("POST /api/scans", s.requireAuth(s.handleCreateScan))
	m.HandleFunc("GET /api/scans/{id}", s.requireAuth(s.handleGetScan))
	m.HandleFunc("POST /api/scans/{id}/control", s.requireAuth(s.handleScanControl))
	m.HandleFunc("GET /api/scans/{id}/events", s.requireAuth(s.handleScanEvents))
	m.HandleFunc("GET /api/scans/{id}/errors", s.requireAuth(s.handleScanErrors))
	m.HandleFunc("DELETE /api/scans/{id}", s.requireAuth(s.handleDeleteScan))

	// 重复组
	m.HandleFunc("GET /api/groups", s.requireAuth(s.handleListGroups))
	m.HandleFunc("GET /api/groups/summary", s.requireAuth(s.handleGroupSummary))
	m.HandleFunc("GET /api/groups/{id}", s.requireAuth(s.handleGetGroup))
	m.HandleFunc("POST /api/groups/{id}/keeper", s.requireAuth(s.handleSetKeeper))

	// 文件与媒体
	m.HandleFunc("GET /api/files/{id}", s.requireAuth(s.handleFileDetail))
	m.HandleFunc("GET /api/files/{id}/thumb", s.requireAuth(s.handleThumb))
	m.HandleFunc("GET /api/files/{id}/raw", s.requireAuth(s.handleRaw))
	m.HandleFunc("GET /api/files/{id}/proxy", s.requireAuth(s.handleProxy))

	// 规则
	m.HandleFunc("GET /api/rules", s.requireAuth(s.handleListRules))
	m.HandleFunc("POST /api/rules", s.requireAuth(s.handleCreateRule))
	m.HandleFunc("GET /api/rules/{id}", s.requireAuth(s.handleGetRule))
	m.HandleFunc("PUT /api/rules/{id}", s.requireAuth(s.handleUpdateRule))
	m.HandleFunc("DELETE /api/rules/{id}", s.requireAuth(s.handleDeleteRule))
	m.HandleFunc("POST /api/rules/{id}/toggle", s.requireAuth(s.handleToggleRule))
	m.HandleFunc("POST /api/rules/dry-run", s.requireAuth(s.handleDryRun))

	// 清理任务
	m.HandleFunc("GET /api/clean/tasks", s.requireAuth(s.handleListCleanTasks))
	m.HandleFunc("POST /api/clean/tasks", s.requireAuth(s.handleCreateCleanTask))
	m.HandleFunc("GET /api/clean/tasks/{id}", s.requireAuth(s.handleGetCleanTask))
	m.HandleFunc("GET /api/clean/tasks/{id}/items", s.requireAuth(s.handleCleanItems))
	m.HandleFunc("POST /api/clean/tasks/{id}/execute", s.requireAuth(s.handleExecuteClean))
	m.HandleFunc("POST /api/clean/tasks/{id}/cancel", s.requireAuth(s.handleCancelClean))
	m.HandleFunc("DELETE /api/clean/tasks/{id}", s.requireAuth(s.handleDeleteCleanTask))
	m.HandleFunc("PATCH /api/clean/items/{id}", s.requireAuth(s.handleSelectItem))

	// 隔离区
	m.HandleFunc("GET /api/quarantine", s.requireAuth(s.handleListQuarantine))
	m.HandleFunc("GET /api/quarantine/{id}", s.requireAuth(s.handleGetQuarantine))
	m.HandleFunc("POST /api/quarantine/{id}/restore", s.requireAuth(s.handleRestore))
	m.HandleFunc("DELETE /api/quarantine/{id}", s.requireAuth(s.handlePurgeQuarantine))
	m.HandleFunc("POST /api/quarantine/purge-expired", s.requireAdmin(s.handlePurgeExpired))

	// 统计
	m.HandleFunc("GET /api/stats/dashboard", s.requireAuth(s.handleDashboard))
	m.HandleFunc("GET /api/stats/library", s.requireAuth(s.handleLibraryStats))
	m.HandleFunc("GET /api/stats/suggestions", s.requireAuth(s.handleSuggestions))

	// 导出
	m.HandleFunc("POST /api/export", s.requireAuth(s.handleExport))
	m.HandleFunc("GET /api/export/download/{name}", s.requireAuth(s.handleDownload))

	// 设置
	m.HandleFunc("GET /api/settings", s.requireAuth(s.handleGetSettings))
	m.HandleFunc("PUT /api/settings", s.requireAdmin(s.handlePutSettings))

	// 前端静态资源 + SPA 回退
	m.HandleFunc("/", s.handleStatic)
}

// ---------- 基础接口 ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]any{
		"status":  "ok",
		"version": s.cfg.Version,
		"time":    time.Now().UnixMilli(),
	})
}

// handleBootstrap 供登录页展示少量非敏感信息（不泄露是否已初始化之外的任何数据）。
func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	n, _ := s.auth.CountUsers(r.Context())
	ok(w, map[string]any{
		"app":          s.cfg.AppName,
		"version":      s.cfg.Version,
		"initialized":  n > 0,
		"capabilities": media.Capabilities(),
	})
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	var files, groups int64
	var bytes int64
	_ = s.st.Get(r.Context(), []any{&files, &bytes},
		`SELECT COUNT(*), COALESCE(SUM(size),0) FROM files WHERE state='active'`)
	_ = s.st.Get(r.Context(), &groups, `SELECT COUNT(*) FROM dup_groups`)

	qCount, qBytes, _ := s.quar.Stats(r.Context())
	ok(w, map[string]any{
		"app":     s.cfg.AppName,
		"version": s.cfg.Version,
		"home":    s.cfg.Home,
		"data_root": s.cfg.DataRoot,
		"capabilities": media.Capabilities(),
		"probe":        media.ProbeStatus(),
		"indexed_files": files,
		"indexed_bytes": bytes,
		"groups":        groups,
		"quarantine":    map[string]any{"count": qCount, "bytes": qBytes},
		"threads":       s.cfg.Threads,
		"limits": map[string]any{
			"max_threads": s.cfg.MaxThreads,
			"batch_size":  s.cfg.BatchSize,
		},
	})
}

// ---------- 静态资源 ----------

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	dist := s.cfg.DistDir()
	indexPath := filepath.Join(dist, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		// 未构建前端时给出明确提示，而不是 404 空白页
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<title>MediaCleaner Pro</title>
<style>body{font-family:-apple-system,"Segoe UI","PingFang SC",sans-serif;background:#0f1115;color:#e6e8eb;
display:flex;align-items:center;justify-content:center;height:100vh;margin:0}div{max-width:560px;line-height:1.7}
code{background:#1b1f27;padding:2px 6px;border-radius:5px;color:#8ab4f8}</style></head><body><div>
<h2>前端尚未构建</h2><p>后端已启动，接口位于 <code>/api/*</code>。</p>
<p>请在 <code>frontend</code> 目录执行 <code>npm install &amp;&amp; npm run build</code>，
或将镜像构建产物放到 <code>` + dist + `</code>。</p></div></body></html>`))
		return
	}

	// API / 静态资源之外的路径一律回退到 index.html（前端路由）
	p := strings.TrimPrefix(filepath.Clean(r.URL.Path), "/")
	if p == "" || p == "." {
		s.serveIndex(w, r, indexPath)
		return
	}
	full := filepath.Join(dist, p)
	if info, err := os.Stat(full); err == nil && !info.IsDir() {
		// 带 hash 的资源可长缓存，其余短缓存
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeFile(w, r, full)
		return
	}
	s.serveIndex(w, r, indexPath)
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request, indexPath string) {
	f, err := os.Open(indexPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, f)
}

// startPurgeLoop 定期清理过期隔离文件（由 main 启动）。
func (s *Server) StartBackground(ctx context.Context) {
	go func() {
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := s.quar.PurgeExpired(ctx); err != nil {
					log.Printf("purge expired quarantine: %v", err)
				} else if n > 0 {
					log.Printf("purged %d expired quarantine items", n)
				}
				s.auth.PurgeExpiredSessions(ctx)
			}
		}
	}()
}
