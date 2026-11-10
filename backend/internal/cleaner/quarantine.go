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
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

	// quarantineDir 的探测结果只算一次（要 stat 两三个路径，没必要每个文件都做）
	dirOnce sync.Once
	dirPath string
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
	HashAlgo     string `json:"hash_algo"`
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
// 返回隔离记录 ID。内容相同的文件只存一份，多条记录共享 stored_path。
//
// knownHash：调用方已经算过的全文哈希（BLAKE3，来自 files.content_hash 或本次执行的
// 复验缓存）。非空时直接拿它当存储名，隔离时就不必再读一遍整个文件 —— 这是
// 「同卷移动却像复制一样慢」的主要原因之一。
// skipVerify：完全免校验模式。不算任何哈希，用 size/mtime/纳秒时间戳派生唯一键，
// 并且不做内容去重（宁可多占空间，也不能把两个同大小、同 mtime 但内容不同的文件
// 合并成一份，导致恢复时少一份）。
func (q *Quarantine) Store(ctx context.Context, src string, size, mtimeNS, rootID, ruleID, taskID int64,
	kind, knownHash string, skipVerify bool) (int64, error) {
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

	var sum, algo string
	switch {
	case skipVerify:
		// 带纳秒时间戳的派生键，保证每次唯一：免校验模式不做内容去重
		sum = fmt.Sprintf("n%d-%d-%d", st.Size(), st.ModTime().UnixNano(), time.Now().UnixNano())
		algo = fingerprint.AlgoNone
	case knownHash != "":
		sum, algo = strings.ToLower(knownHash), fingerprint.AlgoBLAKE3
	default:
		h, err := fingerprint.SHA256File(src)
		if err != nil {
			return 0, fmt.Errorf("计算校验和失败: %w", err)
		}
		sum, algo = h, fingerprint.AlgoSHA256
	}

	ext := filepath.Ext(src)
	// 搬文件走「与媒体卷同一 mount」的路径；入库仍存 /cache 视角的规范路径，
	// 这样以后即使容器没挂 /host，隔离区记录依然可访问。
	destFast := q.storedPath(q.quarantineDir(), sum, ext)
	dest := q.storedPath(q.cfg.QuarantineDir(), sum, ext)

	if fi, err := os.Stat(dest); err == nil {
		// 同内容已在隔离区，直接移除源文件（去重）；源恰是同一条路径时不能删
		si, err2 := os.Stat(src)
		if err2 == nil && os.SameFile(fi, si) {
			return 0, errors.New("源文件已在隔离区内，拒绝操作")
		}
		if err := os.Remove(src); err != nil {
			return 0, err
		}
	} else if _, err := q.moveOrCopy(src, destFast, sum, algo); err != nil {
		return 0, err
	}

	now := store.NowMS()
	var expires any
	if q.cfg.QuarantineRetentionDays > 0 {
		expires = now + int64(q.cfg.QuarantineRetentionDays)*86400000
	}
	res, err := q.st.Exec(ctx, `
		INSERT INTO quarantine_items(orig_path, root_id, stored_path, size, sha256, hash_algo, media_kind,
		                             rule_id, clean_task_id, status, quarantined_at, expires_at, message)
		VALUES(?,?,?,?,?,?,?,?,?, 'quarantined', ?,?, '')`,
		src, rootID, dest, st.Size(), sum, algo, kind, ruleID, taskID, now, expires)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// quarantineDir 返回隔离区在容器内「与媒体卷处于同一 mount」的路径。
//
// 为什么需要它：默认部署把宿主机 / 挂到 /host，媒体卷因此是 /host/volume1/xxx，
// 而隔离区在 /cache —— 容器的 mount namespace 里这是两个 mount。即使宿主机上
// 它们同属 volume1，rename(2) 跨 mount 边界一律返回 EXDEV，"同卷移动"于是
// 退化成整文件复制（这正是用户在 Windows 上"移动瞬间完成"、在这里却像复制的原因）。
//
// 做法：用 /host + 缓存的宿主路径拼出同一份目录的另一条路径，再用 os.SameFile
// 确认两者确实是同一个 inode（同一个目录），成立就改用它搬文件；不成立就退回
// /cache，行为与以前完全一致。入库记录的 stored_path 始终是 /cache 视角的规范路径，
// 所以即使以后没挂 /host 也能照常访问。
func (q *Quarantine) quarantineDir() string {
	q.dirOnce.Do(func() {
		canon := q.cfg.QuarantineDir()
		q.dirPath = canon
		hostRoot := strings.TrimRight(strings.TrimSpace(q.cfg.HostRoot), "/")
		if hostRoot == "" {
			return
		}
		if _, err := os.Stat(hostRoot); err != nil {
			return // 没挂 /host（--no-host-root），保持 /cache 原路径
		}
		if err := os.MkdirAll(canon, 0o755); err != nil {
			return
		}
		base, err := os.Stat(canon)
		if err != nil {
			return
		}
		for _, alt := range q.hostCandidates() {
			// 父目录不存在说明这条拼接不成立；绝不能 MkdirAll，会在宿主机根上乱建目录
			if _, err := os.Stat(filepath.Dir(alt)); err != nil {
				continue
			}
			if err := os.MkdirAll(alt, 0o755); err != nil {
				continue
			}
			fa, err := os.Stat(alt)
			if err != nil || !os.SameFile(fa, base) {
				continue
			}
			// 只读挂载 / 属主不对时 MkdirAll 也会"成功"（目录已存在即视为 ok），
			// 必须真写一次才知道能不能用；不能用的候选直接跳过，退回 /cache。
			if !writableDir(alt) {
				continue
			}
			q.dirPath = alt
			return
		}
	})
	return q.dirPath
}

// hostCandidates 列出「/host 视角」下隔离区可能的路径，按可能性排序：
//
//	① /host + MC_HOST_CACHE_DIR + quarantine      —— 默认布局（/cache 独立挂载）
//	② /host + MC_HOST_DATA_DIR + cache/quarantine —— 同上，由数据目录推导
//	③ /host + MC_HOST_DATA_DIR + data/cache/quarantine —— cache 落在 /data 里的老布局
//
// 全部用 SameFile 校验，猜错也没有副作用。
func (q *Quarantine) hostCandidates() []string {
	root := strings.TrimRight(strings.TrimSpace(q.cfg.HostRoot), "/")
	join := func(parts ...string) string {
		return filepath.Join(append([]string{root}, parts...)...)
	}
	out := []string{}
	if d := strings.TrimRight(strings.TrimSpace(q.cfg.HostCacheDir), "/"); d != "" {
		out = append(out, join(d, "quarantine"))
	}
	if d := strings.TrimRight(strings.TrimSpace(q.cfg.HostDataRoot), "/"); d != "" {
		out = append(out, join(d, "cache", "quarantine"))
		out = append(out, join(d, "data", "cache", "quarantine"))
	}
	return out
}

// writableDir 用"建一个临时文件再删掉"的方式判断目录可写。
// 只看权限位不够（ACL、只读挂载、root_squash 都会骗过 Stat），只能真写一次。
func writableDir(dir string) bool {
	f, err := os.CreateTemp(dir, ".mc-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// moveOrCopy 把 src 移到 dest，返回 moved=true 表示没搬数据。
//
// 三级策略，全部优先保证「不读一遍整个文件再写一遍」：
//  1. rename：同一 mount 内的纯元数据操作（O(1)，不搬数据）；
//  2. link + 删源：跨 mount 但同一文件系统时同样只是元数据操作 —— Linux 的 rename
//     只认「同一个 mount」，但 link(2) 只要求「同一个文件系统」，所以
//     --no-host-root 下 /media 与 /cache 这两个 bind mount 之间也能秒移；
//  3. 复制：真的跨盘时才读一遍，边写边算哈希，落地即完成校验，不再回读。
func (q *Quarantine) moveOrCopy(src, dest, sum, algo string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return false, err
	}
	// 目标已存在（同内容去重）：不搬运，只删源。link 撞 EEXIST 也走不到复制覆盖。
	if fi, err := os.Stat(dest); err == nil {
		if si, e2 := os.Stat(src); e2 == nil && os.SameFile(fi, si) {
			return true, nil
		}
		return true, os.Remove(src)
	}
	if err := os.Rename(src, dest); err == nil {
		return true, nil
	}
	if err := os.Link(src, dest); err == nil {
		if err := os.Remove(src); err != nil {
			// 链接建好了但源没删掉：撤掉刚建的链接，保持"要么移成功要么没动"的语义
			_ = os.Remove(dest)
			return false, err
		}
		return true, nil
	}
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()
	tmp := dest + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return false, err
	}
	h := fingerprint.NewHasher(algo)
	var w io.Writer = out
	if h != nil {
		w = io.MultiWriter(out, h)
	}
	if _, err := io.Copy(w, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return false, err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return false, err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	if h != nil && hex.EncodeToString(h.Sum(nil)) != sum {
		_ = os.Remove(tmp)
		return false, errors.New("隔离区写入校验失败")
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return false, os.Remove(src)
}

func (q *Quarantine) storedPath(dir, sum, ext string) string {
	prefix := sum
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	return filepath.Join(dir, prefix, sum+strings.ToLower(ext))
}

// List 分页列出隔离区记录。
//
// kind 可选：video/image/audio/document/other，传空或 "all" 表示不按类型过滤。
func (q *Quarantine) List(ctx context.Context, status, kind string, limit, offset int) ([]QuarantineItem, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	where := "1=1"
	args := []any{}
	if status != "" && status != "all" {
		where = "status=?"
		args = append(args, status)
	}
	if kind != "" && kind != "all" {
		where += " AND media_kind=?"
		args = append(args, kind)
	}
	var total int
	if err := q.st.Get(ctx, &total, `SELECT COUNT(*) FROM quarantine_items WHERE `+where, args...); err != nil {
		return nil, 0, err
	}
	rows, err := q.st.Select(ctx, `
		SELECT id, orig_path, COALESCE(root_id,0), stored_path, size, sha256, COALESCE(hash_algo,'sha256'),
		       COALESCE(media_kind,''), COALESCE(rule_id,0), COALESCE(clean_task_id,0), status, quarantined_at,
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
			&it.HashAlgo, &it.MediaKind, &it.RuleID, &it.CleanTaskID, &it.Status, &it.QuarantinedAt,
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
		&it.SHA256, &it.HashAlgo, &it.MediaKind, &it.RuleID, &it.CleanTaskID, &it.Status,
		&it.QuarantinedAt, &it.ExpiresAt, &it.RestoredAt, &it.Message},
		`SELECT id, orig_path, COALESCE(root_id,0), stored_path, size, sha256, COALESCE(hash_algo,'sha256'),
		        COALESCE(media_kind,''), COALESCE(rule_id,0), COALESCE(clean_task_id,0), status,
		        quarantined_at, COALESCE(expires_at,0), restored_at, COALESCE(message,'')
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

// OriginMatch 是一条「仍在库里的同内容文件」，即隔离文件的主本候选。
type OriginMatch struct {
	FileID    int64  `json:"file_id"`
	Path      string `json:"path"`
	Name      string `json:"name"`
	Dir       string `json:"dir"`
	MediaKind string `json:"media_kind"`
	Size      int64  `json:"size"`
	MtimeNS   int64  `json:"mtime_ns"`
	State     string `json:"state"`
	IsKeeper  bool   `json:"is_keeper"`
}

// OriginInfo 是隔离文件的反查结果：它是谁的副本、主本现在在哪。
type OriginInfo struct {
	QuarantineID int64         `json:"quarantine_id"`
	OrigPath     string        `json:"orig_path"`
	Hash         string        `json:"hash"`
	HashAlgo     string        `json:"hash_algo"`
	MediaKind    string        `json:"media_kind"` // 隔离文件自身的类型（预览用）
	Method       string        `json:"method"`    // group=分组关系 / hash=全文哈希 / none=找不到
	Note         string        `json:"note"`
	Matches      []OriginMatch `json:"matches"`
}

// Origin 反查隔离文件的主本（当时判定保留的那一份），不需要重新扫描。
//
// 两条路，按可信度排序，互为兜底：
//  1. 分组关系：隔离文件的行还留在 files 表（只是 state 变了），它所在激活批次
//     分组的 is_keeper 成员就是主本 —— 这最准确，因为它就是隔离时"留下的那份"；
//  2. 全文哈希：隔离时用 BLAKE3 命名的记录，sum 与 files.content_hash 同源，
//     即使分组被新一轮扫描重建，仍能按内容找出所有还在索引里的同内容文件。
//
// 找不到的组合只有一种：免校验模式隔离（hash_algo=none，没有内容哈希）
// 且分组关系又被重建过 —— 这时只能重新扫描。
func (q *Quarantine) Origin(ctx context.Context, id int64, hash string) (*OriginInfo, error) {
	if id == 0 {
		if hash == "" {
			return nil, errors.New("需要提供隔离记录 id 或内容哈希")
		}
		if err := q.st.Get(ctx, &id,
			`SELECT id FROM quarantine_items WHERE sha256=? ORDER BY quarantined_at DESC LIMIT 1`, hash); err != nil {
			return nil, ErrNotFound
		}
	}
	it, err := q.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	info := &OriginInfo{
		QuarantineID: it.ID,
		OrigPath:     it.OrigPath,
		Hash:         it.SHA256,
		HashAlgo:     it.HashAlgo,
		MediaKind:    it.MediaKind,
		Matches:      []OriginMatch{},
	}
	seen := map[int64]bool{}
	groupHits := 0

	// ① 分组关系：被隔离文件（按 orig_path 定位行）所在激活批次的 keeper
	rows, err := q.st.Select(ctx, `
		SELECT f.id, f.path, f.name, COALESCE(f.dir_path,''), f.media_kind, f.size, f.mtime_ns, f.state
		FROM dup_group_members m
		JOIN dup_groups g ON g.id = m.group_id AND g.batch_active = 1
		JOIN files f ON f.id = m.file_id
		WHERE m.is_keeper = 1
		  AND m.group_id IN (
		    SELECT gm.group_id FROM dup_group_members gm
		    JOIN dup_groups g2 ON g2.id = gm.group_id AND g2.batch_active = 1
		    JOIN files qf ON qf.id = gm.file_id
		    WHERE qf.path = ?
		  )
		ORDER BY f.path`, it.OrigPath)
	if err == nil {
		for rows.Next() {
			var m OriginMatch
			if err := rows.Scan(&m.FileID, &m.Path, &m.Name, &m.Dir, &m.MediaKind, &m.Size, &m.MtimeNS, &m.State); err != nil {
				break
			}
			m.IsKeeper = true
			if !seen[m.FileID] {
				seen[m.FileID] = true
				groupHits++
				info.Matches = append(info.Matches, m)
			}
		}
		rows.Close()
	}

	// ② 内容哈希：仅 BLAKE3 命名的记录可比对（files.content_hash 是 BLAKE3）
	if it.HashAlgo == fingerprint.AlgoBLAKE3 && it.SHA256 != "" {
		keepers := map[int64]bool{}
		if krows, err := q.st.Select(ctx, `
			SELECT DISTINCT m.file_id
			FROM dup_group_members m
			JOIN dup_groups g ON g.id = m.group_id AND g.batch_active = 1
			JOIN files f ON f.id = m.file_id
			WHERE m.is_keeper = 1 AND f.content_hash = ?`, it.SHA256); err == nil {
			for krows.Next() {
				var fid int64
				if err := krows.Scan(&fid); err == nil {
					keepers[fid] = true
				}
			}
			krows.Close()
		}
		hrows, err := q.st.Select(ctx, `
			SELECT id, path, name, COALESCE(dir_path,''), media_kind, size, mtime_ns, state
			FROM files
			WHERE content_hash = ? AND state = 'active'
			ORDER BY path`, it.SHA256)
		if err == nil {
			for hrows.Next() {
				var m OriginMatch
				if err := hrows.Scan(&m.FileID, &m.Path, &m.Name, &m.Dir, &m.MediaKind, &m.Size, &m.MtimeNS, &m.State); err != nil {
					break
				}
				m.IsKeeper = keepers[m.FileID]
				if !seen[m.FileID] {
					seen[m.FileID] = true
					info.Matches = append(info.Matches, m)
				}
			}
			hrows.Close()
		}
	}

	switch {
	case groupHits > 0:
		info.Method = "group"
		info.Note = "按隔离时的分组关系定位：下面标「保留」的就是当时判定留下的主本。"
	case len(info.Matches) > 0:
		info.Method = "hash"
		info.Note = "分组关系已被新一轮扫描重建，改为按全文 BLAKE3 匹配仍在索引中的同内容文件。"
	case it.HashAlgo == fingerprint.AlgoBLAKE3:
		info.Method = "none"
		info.Note = "索引里已经没有同内容、且状态为 active 的文件 —— 主本可能被移动、改名或也被清理了；重新扫描可确认。"
	default:
		info.Method = "none"
		info.Note = "该记录是免校验模式隔离的（没有内容哈希），且分组关系已重建，无法定位主本；重新扫描后可在分组页找到同内容文件。"
	}
	return info, nil
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
	// 按隔离时实际使用的算法重算：exact 组存的是 BLAKE3，兜底路径才是 SHA-256
	h := fingerprint.NewHasher(it.HashAlgo)
	if h == nil {
		return false, errors.New("该记录隔离时未计算内容哈希（完全免校验模式），无法做完整性校验")
	}
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == it.SHA256, nil
}

