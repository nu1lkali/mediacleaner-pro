// Package cleaner 负责安全删除：隔离区入库、恢复、过期清理与批量执行。
//
// 安全约束（全部为硬约束，不提供绕过开关）：
//  1. 只能操作已注册存储卷内的文件；
//  2. 不跟随符号链接，遇到链接一律拒绝；
//  3. 执行前重新比对 size 与 mtime，文件已变化则跳过并记录；
//  4. 目标位置已存在同名文件时绝不覆盖；
//  5. 默认"移入隔离区"，永久删除需显式指定且二次确认。
package cleaner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mediacleaner/internal/config"
	"mediacleaner/internal/fingerprint"
	"mediacleaner/internal/store"
)

var (
	ErrOutsideRoots = errors.New("路径不在已注册的存储卷内")
	ErrSymlink      = errors.New("拒绝操作符号链接")
	ErrChanged      = errors.New("文件在计划生成后被修改，已跳过")
	ErrNotFound     = errors.New("隔离记录不存在")
	ErrRestoreBusy  = errors.New("原路径已存在文件，无法恢复")
	// ErrReadOnly 表示文件所在存储卷未开启「允许清理」。执行阶段的兜底检查，
	// 防止计划生成后管理员又把卷改回只读。
	ErrReadOnly = errors.New("存储卷未开启「允许清理」，已跳过")
)

// Quarantine 管理隔离区。
type Quarantine struct {
	st  *store.Store
	cfg *config.Config

	rootOf func(ctx context.Context, path string) (bool, error)
}

func NewQuarantine(st *store.Store, cfg *config.Config, rootCheck func(ctx context.Context, path string) (bool, error)) *Quarantine {
	return &Quarantine{st: st, cfg: cfg, rootOf: rootCheck}
}

// QuarantineItem 是隔离区中的一条记录。
type QuarantineItem struct {
	ID           int64  `json:"id"`
	OrigPath     string `json:"orig_path"`
	RootID       int64  `json:"root_id"`
	StoredPath   string `json:"stored_path"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	MediaKind    string `json:"media_kind"`
	RuleID       int64  `json:"rule_id"`
	CleanTaskID  int64  `json:"clean_task_id"`
	Status       string `json:"status"`
	QuarantinedAt int64 `json:"quarantined_at"`
	ExpiresAt    int64  `json:"expires_at"`
	RestoredAt   *int64 `json:"restored_at"`
	Message      string `json:"message"`
}

// Store 把文件移入隔离区。
//
// 返回隔离记录 ID。同名内容（sha256 相同）只存一份，多条记录共享 stored_path。
func (q *Quarantine) Store(ctx context.Context, src string, size, mtimeNS, rootID, ruleID, taskID int64, kind string) (int64, error) {
	if ok, err := q.rootOf(ctx, src); err != nil {
		return 0, err
	} else if !ok {
		return 0, ErrOutsideRoots
	}
	st, err := os.Lstat(src)
	if err != nil {
		return 0, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return 0, ErrSymlink
	}
	if !st.Mode().IsRegular() {
		return 0, errors.New("不是普通文件")
	}
	if mtimeNS > 0 && st.ModTime().UnixNano() != mtimeNS {
		return 0, ErrChanged
	}
	if size > 0 && st.Size() != size {
		return 0, ErrChanged
	}

	sum, err := fingerprint.SHA256File(src)
	if err != nil {
		return 0, fmt.Errorf("计算校验和失败: %w", err)
	}
	dest := q.storedPath(sum, filepath.Ext(src))

	if _, err := os.Stat(dest); err != nil {
		if err := q.moveOrCopy(src, dest); err != nil {
			return 0, err
		}
		// 校验落地内容，防止跨盘复制中途损坏
		got, err := fingerprint.SHA256File(dest)
		if err != nil || got != sum {
			_ = os.Remove(dest)
			return 0, errors.New("隔离区写入校验失败")
		}
	} else {
		// 同内容已在隔离区，直接移除源文件
		if err := os.Remove(src); err != nil {
			return 0, err
		}
	}

	now := store.NowMS()
	var expires any
	if q.cfg.QuarantineRetentionDays > 0 {
		expires = now + int64(q.cfg.QuarantineRetentionDays)*86400000
	}
	res, err := q.st.Exec(ctx, `
		INSERT INTO quarantine_items(orig_path, root_id, stored_path, size, sha256, media_kind,
		                             rule_id, clean_task_id, status, quarantined_at, expires_at, message)
		VALUES(?,?,?,?,?,?,?,?, 'quarantined', ?,?, '')`,
		src, rootID, dest, st.Size(), sum, kind, ruleID, taskID, now, expires)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// moveOrCopy 优先同盘 rename；跨文件系统时退化为复制 + 删除。
func (q *Quarantine) moveOrCopy(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	err := os.Rename(src, dest)
	if err == nil {
		return nil
	}
	// rename 失败通常是跨设备；用复制兜底
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dest + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Remove(src)
}

func (q *Quarantine) storedPath(sum, ext string) string {
	dir := filepath.Join(q.cfg.QuarantineDir(), sum[:2])
	return filepath.Join(dir, sum+strings.ToLower(ext))
}

// List 分页列出隔离区记录。
func (q *Quarantine) List(ctx context.Context, status string, limit, offset int) ([]QuarantineItem, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	where := "1=1"
	args := []any{}
	if status != "" && status != "all" {
		where = "status=?"
		args = append(args, status)
	}
	var total int
	if err := q.st.Get(ctx, &total, `SELECT COUNT(*) FROM quarantine_items WHERE `+where, args...); err != nil {
		return nil, 0, err
	}
	rows, err := q.st.Select(ctx, `
		SELECT id, orig_path, COALESCE(root_id,0), stored_path, size, sha256, COALESCE(media_kind,''),
		       COALESCE(rule_id,0), COALESCE(clean_task_id,0), status, quarantined_at,
		       COALESCE(expires_at,0), restored_at, COALESCE(message,'')
		FROM quarantine_items WHERE `+where+`
		ORDER BY quarantined_at DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []QuarantineItem{}
	for rows.Next() {
		var it QuarantineItem
		if err := rows.Scan(&it.ID, &it.OrigPath, &it.RootID, &it.StoredPath, &it.Size, &it.SHA256,
			&it.MediaKind, &it.RuleID, &it.CleanTaskID, &it.Status, &it.QuarantinedAt,
			&it.ExpiresAt, &it.RestoredAt, &it.Message); err != nil {
			return nil, 0, err
		}
		out = append(out, it)
	}
	return out, total, rows.Err()
}

// Get 读取单条隔离记录。
func (q *Quarantine) Get(ctx context.Context, id int64) (*QuarantineItem, error) {
	var it QuarantineItem
	err := q.st.Get(ctx, []any{&it.ID, &it.OrigPath, &it.RootID, &it.StoredPath, &it.Size,
		&it.SHA256, &it.MediaKind, &it.RuleID, &it.CleanTaskID, &it.Status,
		&it.QuarantinedAt, &it.ExpiresAt, &it.RestoredAt, &it.Message},
		`SELECT id, orig_path, COALESCE(root_id,0), stored_path, size, sha256, COALESCE(media_kind,''),
		        COALESCE(rule_id,0), COALESCE(clean_task_id,0), status, quarantined_at,
		        COALESCE(expires_at,0), restored_at, COALESCE(message,'')
		 FROM quarantine_items WHERE id=?`, id)
	if err != nil {
		return nil, ErrNotFound
	}
	return &it, nil
}

// Restore 把文件恢复到原始路径。存在冲突时不覆盖，返回 ErrRestoreBusy。
func (q *Quarantine) Restore(ctx context.Context, id int64) error {
	it, err := q.Get(ctx, id)
	if err != nil {
		return err
	}
	if it.Status != "quarantined" {
		return fmt.Errorf("当前状态 %s 无法恢复", it.Status)
	}
	if ok, err := q.rootOf(ctx, it.OrigPath); err != nil || !ok {
		return ErrOutsideRoots
	}
	if _, err := os.Lstat(it.OrigPath); err == nil {
		return ErrRestoreBusy
	}
	if err := os.MkdirAll(filepath.Dir(it.OrigPath), 0o755); err != nil {
		return err
	}
	// 该 sha256 可能被多条记录共享，先复制出来再标记，最后清理孤立副本
	if err := q.copyBack(it.StoredPath, it.OrigPath); err != nil {
		return err
	}
	now := store.NowMS()
	if _, err := q.st.Exec(ctx,
		`UPDATE quarantine_items SET status='restored', restored_at=? WHERE id=?`, now, id); err != nil {
		return err
	}
	q.releaseBlob(ctx, it.StoredPath)
	return nil
}

func (q *Quarantine) copyBack(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dest + ".mcrestore"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// releaseBlob 在没有任何记录引用该存储文件时删除它。
func (q *Quarantine) releaseBlob(ctx context.Context, stored string) {
	var n int
	if err := q.st.Get(ctx, &n,
		`SELECT COUNT(*) FROM quarantine_items WHERE stored_path=? AND status='quarantined'`, stored); err != nil {
		return
	}
	if n == 0 {
		_ = os.Remove(stored)
	}
}

// Purge 永久删除单条隔离文件。
func (q *Quarantine) Purge(ctx context.Context, id int64) error {
	it, err := q.Get(ctx, id)
	if err != nil {
		return err
	}
	if _, err := q.st.Exec(ctx,
		`UPDATE quarantine_items SET status='purged', message='用户永久删除' WHERE id=?`, id); err != nil {
		return err
	}
	q.releaseBlob(ctx, it.StoredPath)
	return nil
}

// PurgeExpired 清理超过保留期的隔离文件（由定时任务调用）。
func (q *Quarantine) PurgeExpired(ctx context.Context) (int, error) {
	rows, err := q.st.Select(ctx,
		`SELECT id FROM quarantine_items WHERE status='quarantined' AND expires_at IS NOT NULL AND expires_at < ?`,
		store.NowMS())
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	n := 0
	for _, id := range ids {
		if err := q.Purge(ctx, id); err == nil {
			n++
		}
	}
	return n, nil
}

// Stats 返回隔离区汇总。
func (q *Quarantine) Stats(ctx context.Context) (count int, bytes int64, err error) {
	type row struct {
		n int
		b int64
	}
	var r row
	if err := q.st.Get(ctx, []any{&r.n, &r.b},
		`SELECT COUNT(*), COALESCE(SUM(size),0) FROM quarantine_items WHERE status='quarantined'`); err != nil {
		return 0, 0, err
	}
	return r.n, r.b, nil
}

// Verify 校验隔离文件完整性（sha256 比对），用于"隔离区体检"。
func (q *Quarantine) Verify(ctx context.Context, id int64) (bool, error) {
	it, err := q.Get(ctx, id)
	if err != nil {
		return false, err
	}
	f, err := os.Open(it.StoredPath)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == it.SHA256, nil
}

