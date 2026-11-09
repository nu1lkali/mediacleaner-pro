package scan

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"mediacleaner/internal/config"
	"mediacleaner/internal/store"
)

var (
	ErrRootNotFound = errors.New("存储卷不存在")
	ErrRootExists   = errors.New("该路径已注册")
)

// Root 是一个受管的存储卷根目录。
type Root struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	HostPath  string `json:"host_path"`
	RealPath  string `json:"real_path"`
	Volume    string `json:"volume"`
	Writable  bool   `json:"writable"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`

	// 运行时统计（列表接口填充，不落库）
	FileCount int64 `json:"file_count"`
	TotalSize int64 `json:"total_size"`
}

type RootService struct {
	st  *store.Store
	cfg *config.Config
}

func NewRootService(st *store.Store, cfg *config.Config) *RootService {
	return &RootService{st: st, cfg: cfg}
}

func (s *RootService) List(ctx context.Context) ([]Root, error) {
	rows, err := s.st.Select(ctx, `
		SELECT r.id, r.name, r.host_path, r.real_path, r.volume, r.writable, r.enabled, r.created_at,
		       (SELECT COUNT(*) FROM files f WHERE f.root_id=r.id AND f.state='active'),
		       (SELECT COALESCE(SUM(f.size),0) FROM files f WHERE f.root_id=r.id AND f.state='active')
		FROM roots r ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Root{}
	for rows.Next() {
		var (
			r             Root
			writable, en  int
		)
		if err := rows.Scan(&r.ID, &r.Name, &r.HostPath, &r.RealPath, &r.Volume,
			&writable, &en, &r.CreatedAt, &r.FileCount, &r.TotalSize); err != nil {
			return nil, err
		}
		r.Writable = writable == 1
		r.Enabled = en == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *RootService) Get(ctx context.Context, id int64) (*Root, error) {
	var (
		r            Root
		writable, en int
	)
	err := s.st.Get(ctx, []any{&r.ID, &r.Name, &r.HostPath, &r.RealPath, &r.Volume,
		&writable, &en, &r.CreatedAt},
		`SELECT id, name, host_path, real_path, volume, writable, enabled, created_at FROM roots WHERE id=?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRootNotFound
	}
	if err != nil {
		return nil, err
	}
	r.Writable = writable == 1
	r.Enabled = en == 1
	return &r, nil
}

func (s *RootService) Enabled(ctx context.Context) ([]Root, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Root, 0, len(all))
	for _, r := range all {
		if r.Enabled {
			out = append(out, r)
		}
	}
	return out, nil
}

// FindByPath 按真实路径或用户输入路径查找已注册的卷（用于"重复添加"时复用）。
func (s *RootService) FindByPath(ctx context.Context, path string) (*Root, error) {
	clean := filepath.Clean(path)
	var id int64
	err := s.st.Get(ctx, &id, `SELECT id FROM roots WHERE real_path=? OR host_path=?`, clean, clean)
	if err != nil {
		return nil, ErrRootNotFound
	}
	return s.Get(ctx, id)
}

type AddRootInput struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Writable bool   `json:"writable"`
}

func (s *RootService) Add(ctx context.Context, in AddRootInput) (*Root, error) {
	hostPath, realPath, err := NormalizeRoot(in.Path)
	if err != nil {
		return nil, err
	}
	// 安全检查：注册的根目录必须位于 DataRoot 之内，或在显式允许的额外目录内
	if !config.IsUnder(realPath, s.cfg.DataRoot) && !s.allowedExtra(realPath) {
		return nil, errors.New("路径不在允许的数据根目录 " + s.cfg.DataRoot + " 之内")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = filepath.Base(realPath)
		if name == "." || name == string(filepath.Separator) {
			name = "根目录"
		}
	}
	vol := VolumeOf(realPath)
	if _, err := s.st.Exec(ctx,
		`INSERT INTO roots(name, host_path, real_path, volume, writable, enabled, created_at) VALUES(?,?,?,?,?,1,?)`,
		name, hostPath, realPath, vol, boolInt(in.Writable), store.NowMS()); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrRootExists
		}
		return nil, err
	}
	var id int64
	if err := s.st.Get(ctx, &id, `SELECT id FROM roots WHERE real_path=?`, realPath); err != nil {
		return nil, err
	}
	r := &Root{ID: id, Name: name, HostPath: hostPath, RealPath: realPath, Volume: vol,
		Writable: in.Writable, Enabled: true, CreatedAt: store.NowMS()}
	return r, nil
}

func (s *RootService) Update(ctx context.Context, id int64, name *string, enabled, writable *bool) error {
	if _, err := s.Get(ctx, id); err != nil {
		return err
	}
	if name != nil {
		if _, err := s.st.Exec(ctx, `UPDATE roots SET name=? WHERE id=?`, strings.TrimSpace(*name), id); err != nil {
			return err
		}
	}
	if enabled != nil {
		if _, err := s.st.Exec(ctx, `UPDATE roots SET enabled=? WHERE id=?`, boolInt(*enabled), id); err != nil {
			return err
		}
	}
	if writable != nil {
		if _, err := s.st.Exec(ctx, `UPDATE roots SET writable=? WHERE id=?`, boolInt(*writable), id); err != nil {
			return err
		}
	}
	return nil
}

// Delete 仅移除索引与卷注册，绝不删除磁盘文件。
func (s *RootService) Delete(ctx context.Context, id int64) error {
	return s.st.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE root_id=?`, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM roots WHERE id=?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrRootNotFound
		}
		return nil
	})
}

// Resolve 校验一个数据库内的绝对路径仍在某个受管根目录内，返回对应的根。
// 这是所有"按路径操作文件"接口的唯一入口，防止路径穿越。
func (s *RootService) Resolve(ctx context.Context, path string) (*Root, error) {
	clean := filepath.Clean(path)
	roots, err := s.Enabled(ctx)
	if err != nil {
		return nil, err
	}
	for i := range roots {
		if config.IsUnder(clean, roots[i].RealPath) || config.IsUnder(clean, roots[i].HostPath) {
			return &roots[i], nil
		}
	}
	return nil, errors.New("路径不在任何已注册的存储卷内")
}

// EnsureDefaults 根据 MC_ROOTS 环境变量（分号分隔）注册初始卷，已存在则跳过。
//
// 注意：自动注册的卷默认「不允许清理」（writable=false），必须由用户在界面上
// 显式打开开关后，规则引擎才会产出删除项。这是第二道安全闸门 ——
// 即使脚本挂载时给了写权限，程序也不会自行删除任何文件。
// 需要开箱即写时设置 MC_ROOTS_WRITABLE=1。
func (s *RootService) EnsureDefaults(ctx context.Context) []string {
	var notes []string
	spec := strings.TrimSpace(os.Getenv("MC_ROOTS"))
	if spec == "" {
		return nil
	}
	writable := config.EnvBool("MC_ROOTS_WRITABLE", false)
	for _, item := range strings.FieldsFunc(spec, func(r rune) bool { return r == ';' || r == '\n' }) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, "=", 2)
		name, path := "", parts[0]
		if len(parts) == 2 {
			name, path = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		}
		if _, err := os.Stat(path); err != nil {
			notes = append(notes, "跳过不存在的挂载点: "+path)
			continue
		}
		if _, err := s.Add(ctx, AddRootInput{Name: name, Path: path, Writable: writable}); err != nil {
			if errors.Is(err, ErrRootExists) {
				continue
			}
			notes = append(notes, "注册 "+path+" 失败: "+err.Error())
		}
	}
	return notes
}

func (s *RootService) allowedExtra(p string) bool {
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

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
