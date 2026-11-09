package api

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediacleaner/internal/config"
	"mediacleaner/internal/scan"
)

func (s *Server) handleListRoots(w http.ResponseWriter, r *http.Request) {
	list, err := s.roots.List(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, map[string]any{"items": list, "data_root": s.cfg.DataRoot})
}

type addRootReq struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Writable bool   `json:"writable"`
}

func (s *Server) handleAddRoot(w http.ResponseWriter, r *http.Request) {
	var req addRootReq
	if err := decodeBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	root, err := s.roots.Add(r.Context(), scan.AddRootInput{Name: req.Name, Path: req.Path, Writable: req.Writable})
	if err != nil {
		fail(w, badRequest(err.Error()))
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "root.add", "root", req.Path, nil, clientIP(r))
	ok(w, root)
}

type updateRootReq struct {
	Name     *string `json:"name"`
	Enabled  *bool   `json:"enabled"`
	Writable *bool   `json:"writable"`
}

func (s *Server) handleUpdateRoot(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	var req updateRootReq
	if err := decodeBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	if err := s.roots.Update(r.Context(), id, req.Name, req.Enabled, req.Writable); err != nil {
		fail(w, badRequest(err.Error()))
		return
	}
	root, err := s.roots.Get(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, root)
}

func (s *Server) handleDeleteRoot(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	root, _ := s.roots.Get(r.Context(), id)
	if err := s.roots.Delete(r.Context(), id); err != nil {
		fail(w, badRequest(err.Error()))
		return
	}
	if root != nil {
		// 只删索引，明确告知用户磁盘文件未动
		s.st.Audit(r.Context(), actorOf(r), "root.remove", "root", root.RealPath,
			map[string]any{"note": "仅移除索引，磁盘文件未删除"}, clientIP(r))
	}
	ok(w, map[string]any{"ok": true, "note": "索引已移除，磁盘文件未被删除"})
}

type browseEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// browseBase 是目录选择器允许进入的一个起点。
type browseBase struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// browseBases 汇总目录选择器允许浏览的根：已注册存储卷 > MC_ALLOW_PATHS > 数据根。
//
// 注意：注册的媒体目录挂载在 /media 下，而程序数据在 /data。早期实现只允许浏览
// 数据根，导致界面上根本选不到媒体目录，这里按"注册卷优先"重建白名单。
func (s *Server) browseBases(r *http.Request) []browseBase {
	var out []browseBase

	seen := map[string]bool{}
	add := func(name, path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if st, err := os.Stat(path); err != nil || !st.IsDir() {
			return
		}
		clean := filepath.Clean(path)
		if seen[clean] {
			return
		}
		seen[clean] = true
		out = append(out, browseBase{Name: name, Path: clean})
	}

	if s.roots != nil {
		if list, err := s.roots.Enabled(r.Context()); err == nil {
			for _, rt := range list {
				add(rt.Name, rt.RealPath)
			}
		}
	}
	for _, item := range strings.Split(os.Getenv("MC_ALLOW_PATHS"), ";") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		add(filepath.Base(item), item)
	}
	add("数据根目录", s.cfg.DataRoot)
	return out
}

// underAnyBase 判断路径是否落在任一允许的浏览根之内。
func underAnyBase(p string, bases []browseBase) bool {
	for _, b := range bases {
		if config.IsUnder(p, b.Path) {
			return true
		}
	}
	return false
}

// handleBrowse 是目录选择器：只允许浏览已注册存储卷 / 白名单目录之内的路径，
// 防止越权探测；未传 path 时落在第一个可浏览的根上，而不是程序数据目录。
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	bases := s.browseBases(r)
	if len(bases) == 0 {
		fail(w, badRequest("还没有可浏览的存储卷。请先在「系统设置 → 存储卷」添加目录，或检查媒体目录是否已挂载到容器内"))
		return
	}

	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		raw = bases[0].Path
	}
	abs, err := filepath.Abs(filepath.Clean(raw))
	if err != nil {
		fail(w, badRequest("路径无效"))
		return
	}
	if !underAnyBase(abs, bases) {
		fail(w, forbidden("只能浏览已注册存储卷之内的路径"))
		return
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		// 群晖上最常见的是容器用户对真实目录没有读权限，这里直接说清楚怎么修，
		// 否则用户只会看到一句 permission denied 无从下手。
		if errors.Is(err, fs.ErrPermission) {
			uid := os.Getenv("PUID")
			gid := os.Getenv("PGID")
			if uid == "" {
				uid = "?"
			}
			if gid == "" {
				gid = "?"
			}
			fail(w, forbidden(fmt.Sprintf(
				"容器内运行身份（uid=%s gid=%s）没有权限读取 %s。"+
					"处理方式二选一：① 把 .env 里的 PUID/PGID 改成该目录的属主；"+
					"② 在 NAS 上执行 sudo bash deploy/synology-deploy.sh --run-as-root 让容器以 root 运行。",
				uid, gid, abs)))
			return
		}
		fail(w, badRequest("无法读取目录: "+err.Error()))
		return
	}
	dirs := make([]browseEntry, 0, len(entries))
	var files int
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || scan.ShouldSkipDir(name) {
			continue
		}
		full := filepath.Join(abs, name)
		if e.IsDir() {
			dirs = append(dirs, browseEntry{Name: name, Path: full, IsDir: true})
			continue
		}
		files++
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })

	parent := ""
	if up := filepath.Dir(abs); up != abs && underAnyBase(up, bases) {
		parent = up
	}

	ok(w, map[string]any{
		"path":       abs,
		"parent":     parent,
		"root":       bases[0].Path,
		"roots":      bases,
		"dirs":       dirs,
		"file_count": files,
	})
}

func (s *Server) isExtraAllowed(p string) bool {
	extra := strings.TrimSpace(os.Getenv("MC_ALLOW_PATHS"))
	if extra == "" {
		return false
	}
	for _, item := range strings.Split(extra, ";") {
		item = strings.TrimSpace(item)
		if item != "" && config.IsUnder(p, item) {
			return true
		}
	}
	return false
}
