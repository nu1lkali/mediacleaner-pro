// Package store 封装 SQLite 访问。
//
// 并发模型：WAL 模式下读写互不阻塞；写连接串行化（MaxOpenConns=1）避免 SQLITE_BUSY，
// 读连接池独立，保证扫描批量写入时 Web 界面仍可流畅查询。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	rw   *sql.DB
	ro   *sql.DB
	Path string
}

func NowMS() int64 { return time.Now().UnixMilli() }

func Open(path string) (*Store, error) {
	if err := ensureDir(path); err != nil {
		return nil, err
	}
	w := dsn(path, false)
	r := dsn(path, true)

	rw, err := sql.Open("sqlite", w)
	if err != nil {
		return nil, fmt.Errorf("open rw: %w", err)
	}
	rw.SetMaxOpenConns(1)
	rw.SetMaxIdleConns(1)
	rw.SetConnMaxLifetime(0)

	ro, err := sql.Open("sqlite", r)
	if err != nil {
		return nil, fmt.Errorf("open ro: %w", err)
	}
	ro.SetMaxOpenConns(4)
	ro.SetMaxIdleConns(2)

	s := &Store{rw: rw, ro: ro, Path: path}
	if err := s.init(); err != nil {
		return nil, err
	}
	return s, nil
}

func ensureDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" {
		return nil
	}
	return mkdirAll(dir)
}

func dsn(path string, readonly bool) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "busy_timeout(30000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "temp_store(MEMORY)")
	if readonly {
		q.Add("_pragma", "query_only(1)")
		q.Add("mode", "ro")
	}
	return "file:" + filepath.ToSlash(path) + "?" + q.Encode()
}

func (s *Store) init() error {
	// 顺序约束：schemaSQL 必须只引用「建表时就存在」的列。
	// 旧库的表不会被 CREATE TABLE IF NOT EXISTS 重建，迁移列要到第 2 步才出现，
	// 因此任何依赖迁移列的索引都必须放进 ensureIndexes()，不能写进 schemaSQL。
	if _, err := s.rw.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if err := s.ensureColumns(); err != nil {
		return err
	}
	if err := s.ensureIndexes(); err != nil {
		return err
	}
	if _, err := s.rw.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, ?)`, NowMS()); err != nil {
		return err
	}
	// 首次建表后落一次 WAL checkpoint，确保容器重启后 -wal 不无限增长
	_, _ = s.rw.Exec(`PRAGMA wal_autocheckpoint=1000`)
	return nil
}

// ensureColumns 给已存在的旧库补列。SQLite 的 ADD COLUMN 没有 IF NOT EXISTS，
// 只能执行后忽略 "duplicate column name"，这样新库和老库都能走到同一个结构。
func (s *Store) ensureColumns() error {
	alters := []string{
		// 分组证据等级：exact=已逐字节校验；sampled=仅采样一致，清理前再校验
		`ALTER TABLE dup_groups ADD COLUMN evidence TEXT NOT NULL DEFAULT 'exact'`,
		// 真实创建时间（btime）；旧行保持 0=未知，重新扫描后才会填充
		`ALTER TABLE files ADD COLUMN btime_ns INTEGER NOT NULL DEFAULT 0`,
		// 同组保留条件；旧库默认空串=未配置，行为与升级前完全一致
		`ALTER TABLE dup_groups ADD COLUMN keeper_rule TEXT NOT NULL DEFAULT ''`,
		// 批次隔离标记；旧库的现有分组全部视为「当前激活批次」，升级后界面照常显示
		`ALTER TABLE dup_groups ADD COLUMN batch_active INTEGER NOT NULL DEFAULT 1`,
		// 产出该批分组的扫描任务；更老的库没有这一列，补上才能让批次唯一索引成立。
		// 0 = 升级前的遗留分组（未知任务）。ADD COLUMN 有默认值，旧数据自动填 0。
		`ALTER TABLE dup_groups ADD COLUMN scan_task_id INTEGER NOT NULL DEFAULT 0`,
		// 隔离区记录的哈希算法（sha256/blake3/none）；历史记录默认按 sha256 校验
		`ALTER TABLE quarantine_items ADD COLUMN hash_algo TEXT NOT NULL DEFAULT 'sha256'`,
	}
	for _, q := range alters {
		if _, err := s.rw.Exec(q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// ensureIndexes 处理索引定义变更，且在 ensureColumns() 之后运行。
// SQLite 没有 CREATE OR REPLACE INDEX，旧库的 ux_groups_sig(kind, signature)
// 会阻止「不同扫描任务产出相同签名」，必须移除后改建成带 scan_task_id 的批次唯一索引。
// 批次相关的两个索引都只在这里创建：它们引用的列在旧库上是迁移补出来的，
// 不能放进先执行的 schemaSQL。旧数据里每个 (kind,signature) 本来就唯一，
// 加上默认值 scan_task_id=0 后仍唯一，因此建唯一索引不会与历史数据冲突。
func (s *Store) ensureIndexes() error {
	stmts := []string{
		`DROP INDEX IF EXISTS ux_groups_sig`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ux_groups_batch ON dup_groups(scan_task_id, kind, signature)`,
		`CREATE INDEX IF NOT EXISTS ix_groups_batch ON dup_groups(batch_active, kind)`,
	}
	for _, q := range stmts {
		if _, err := s.rw.Exec(q); err != nil {
			return fmt.Errorf("migrate index: %w", err)
		}
	}
	return nil
}

func (s *Store) RW() *sql.DB { return s.rw }
func (s *Store) RO() *sql.DB { return s.ro }

func (s *Store) Close() error {
	if s.ro != nil {
		_ = s.ro.Close()
	}
	if s.rw != nil {
		return s.rw.Close()
	}
	return nil
}

// Tx 在一个写事务中执行 fn，出错自动回滚。
func (s *Store) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.rw.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return s.rw.ExecContext(ctx, q, args...)
}

// Get 查询单行。dst 可以是单个指针，也可以是 []any{&a, &b} 形式的多列目标。
func (s *Store) Get(ctx context.Context, dst any, q string, args ...any) error {
	row := s.ro.QueryRowContext(ctx, q, args...)
	if cols, ok := dst.([]any); ok {
		return row.Scan(cols...)
	}
	return row.Scan(dst)
}

func (s *Store) Select(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return s.ro.QueryContext(ctx, q, args...)
}
