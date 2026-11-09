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
	if _, err := s.rw.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := s.rw.Exec(`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, ?)`, NowMS()); err != nil {
		return err
	}
	// 首次建表后落一次 WAL checkpoint，确保容器重启后 -wal 不无限增长
	_, _ = s.rw.Exec(`PRAGMA wal_autocheckpoint=1000`)
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
