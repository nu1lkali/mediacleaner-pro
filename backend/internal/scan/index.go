package scan

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"mediacleaner/internal/store"
)

// invalidateCond 是"指纹失效"判定条件。
//
// 身份键是 (root_id, path)，但"文件是否变过"只看 size 与 mtime_ns：
// 两者都没变就认为内容未变，直接沿用上次的 quick_hash / content_hash / phash。
//
// 打开 MC_CTIME_CHECK=1 后额外比较 ctime（inode 变更时间）：
// 能挡住"改完内容又把 mtime 改回原值"这类伪造，代价是 chmod / chown / touch
// 也会触发重算，所以默认关闭。
func invalidateCond() string {
	cond := "files.size<>excluded.size OR files.mtime_ns<>excluded.mtime_ns"
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MC_CTIME_CHECK"))) {
	case "1", "true", "yes", "on":
		cond += " OR (files.ctime_ns>0 AND excluded.ctime_ns>0 AND files.ctime_ns<>excluded.ctime_ns)"
	}
	return cond
}

// upsertSQL 是单条文件入库语句。
//
// 增量关键点：只有当 (size, mtime_ns) 变化时才清空已有指纹，否则保留原值，
// 这样二次扫描一百万文件只需一次 UPSERT 遍历，无需重算任何哈希。
var upsertSQL = buildUpsertSQL()

func buildUpsertSQL() string {
	inv := invalidateCond()
	return `
INSERT INTO files(root_id, path, dir_path, name, stem, ext, size, mtime_ns, ctime_ns,
                  dev, inode, is_symlink, media_kind, state, scan_task_id, last_seen_at,
                  created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'active',?,?,?,?)
ON CONFLICT(root_id, path) DO UPDATE SET
  dir_path     = excluded.dir_path,
  name         = excluded.name,
  stem         = excluded.stem,
  ext          = excluded.ext,
  ctime_ns     = excluded.ctime_ns,
  dev          = excluded.dev,
  inode        = excluded.inode,
  is_symlink   = excluded.is_symlink,
  media_kind   = excluded.media_kind,
  state        = 'active',
  scan_task_id = excluded.scan_task_id,
  last_seen_at = excluded.last_seen_at,
  updated_at   = excluded.updated_at,
  quick_hash   = CASE WHEN ` + inv + ` THEN NULL ELSE files.quick_hash END,
  content_hash = CASE WHEN ` + inv + ` THEN NULL ELSE files.content_hash END,
  hash_status  = CASE WHEN ` + inv + ` THEN 'none' ELSE files.hash_status END,
  phash        = CASE WHEN ` + inv + ` THEN NULL ELSE files.phash END,
  phash_frames = CASE WHEN ` + inv + ` THEN NULL ELSE files.phash_frames END,
  size         = excluded.size,
  mtime_ns     = excluded.mtime_ns`
}

// Indexer 负责把遍历结果分批写入 files 表。
type Indexer struct {
	st     *store.Store
	taskID int64
	batch  int
	now    int64

	tx    *sql.Tx
	stmt  *sql.Stmt
	dirty int
	seen  int64
	bytes int64
}

func NewIndexer(st *store.Store, taskID int64, batch int) *Indexer {
	if batch <= 0 {
		batch = 1000
	}
	return &Indexer{st: st, taskID: taskID, batch: batch, now: store.NowMS()}
}

// Add 把一个条目写入当前事务，达到批量阈值自动提交一批。
func (ix *Indexer) Add(ctx context.Context, e Entry) error {
	if ix.tx == nil {
		if err := ix.begin(ctx); err != nil {
			return err
		}
	}
	if _, err := ix.stmt.ExecContext(ctx,
		e.RootID, e.Path, e.DirPath, e.Name, e.Stem, e.Ext, e.Size, e.MtimeNS, e.CtimeNS,
		e.Dev, e.Inode, boolInt(e.IsSymlink), e.Kind, ix.taskID, ix.now,
		ix.now, ix.now); err != nil {
		return fmt.Errorf("insert %s: %w", e.Path, err)
	}
	ix.seen++
	ix.bytes += e.Size
	ix.dirty++
	if ix.dirty >= ix.batch {
		return ix.Flush(ctx)
	}
	return nil
}

// Flush 提交当前批次。
func (ix *Indexer) Flush(ctx context.Context) error {
	if ix.tx == nil || ix.dirty == 0 {
		return nil
	}
	if err := ix.stmt.Close(); err != nil {
		return err
	}
	if err := ix.tx.Commit(); err != nil {
		return err
	}
	ix.tx, ix.stmt, ix.dirty = nil, nil, 0
	return nil
}

func (ix *Indexer) begin(ctx context.Context) error {
	tx, err := ix.st.RW().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, upsertSQL)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	ix.tx, ix.stmt = tx, stmt
	return nil
}

// Close 收尾：提交剩余数据；全量扫描时把本轮未见的文件标记为 missing。
func (ix *Indexer) Close(ctx context.Context, rootID int64, markMissing bool) error {
	if err := ix.Flush(ctx); err != nil {
		return err
	}
	if markMissing && rootID > 0 {
		// scan_task_id 不是本轮 → 本轮没有再次遇到 → 磁盘上已不存在
		if _, err := ix.st.Exec(ctx,
			`UPDATE files SET state='missing', updated_at=?
			 WHERE root_id=? AND state='active' AND (scan_task_id IS NULL OR scan_task_id<>?)`,
			store.NowMS(), rootID, ix.taskID); err != nil {
			return err
		}
	}
	return nil
}

func (ix *Indexer) Stats() (int64, int64) { return ix.seen, ix.bytes }

// ---------- inode 层面的指纹继承 ----------

// reconcileSQL 把「同一个物理文件」的指纹从旧行搬到新行。
//
// UPSERT 的键是 (root_id, path)，文件被改名或移动后路径变了，会被当成新文件、
// 重新算一遍哈希。但同一卷内改名不改变 inode：只要 (dev, inode, size, mtime_ns)
// 全部一致，就是同一个字节内容，指纹可以直接继承。
//
// dev/inode 为 0 的情况（Windows、部分网络文件系统）自动跳过——条件里已排除。
const reconcileSQL = `
UPDATE files SET
  quick_hash   = d.quick_hash,
  content_hash = d.content_hash,
  phash        = d.phash,
  phash_frames = d.phash_frames,
  hash_status  = d.hash_status,
  updated_at   = ?
FROM files AS d
WHERE files.scan_task_id = ?
  AND files.hash_status = 'none'
  AND files.dev <> 0 AND files.inode <> 0
  AND d.id <> files.id
  AND d.hash_status <> 'none'
  AND d.dev = files.dev AND d.inode = files.inode
  AND d.size = files.size AND d.mtime_ns = files.mtime_ns`

// ReconcileByInode 为本轮新出现（无指纹）的行补上同 inode 旧行的指纹，
// 返回被继承的行数。调用点在本轮遍历结束后、任何哈希阶段之前。
//
// 只是性能优化：失败不影响正确性（最多退化成重新计算哈希），因此调用方
// 可以忽略错误，仅记录到错误表里便于排查。
func ReconcileByInode(ctx context.Context, st *store.Store, taskID int64) (int64, error) {
	res, err := st.Exec(ctx, reconcileSQL, store.NowMS(), taskID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ---------- 候选集查询 ----------

// SizeCandidate 是按大小聚合出的候选桶。
type SizeCandidate struct {
	Size  int64
	Count int
}

// SizeDuplicates 返回同一存储卷内大小重复的桶（≥2 个文件）。
func SizeDuplicates(ctx context.Context, st *store.Store, rootID int64, minSize int64) ([]SizeCandidate, error) {
	q := `SELECT size, COUNT(*) c FROM files
	      WHERE state='active' AND size>=? AND media_kind<>'other'`
	args := []any{minSize}
	if rootID > 0 {
		q += ` AND root_id=?`
		args = append(args, rootID)
	}
	q += ` GROUP BY size HAVING c>1 ORDER BY size DESC`
	rows, err := st.Select(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SizeCandidate{}
	for rows.Next() {
		var c SizeCandidate
		if err := rows.Scan(&c.Size, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PendingQuick 返回需要计算快速指纹的候选文件（仅大小重复桶内的文件）。
type FileRef struct {
	ID        int64
	RootID    int64
	Path      string
	Size      int64
	MtimeNS   int64
	Kind      string
	Ext       string
	QuickHash string
	Content   string
	PHash     string
	State     string
}

// QueryCandidates 返回所有处于"大小重复桶"内的文件，用于后续快速/精确哈希。
func QueryCandidates(ctx context.Context, st *store.Store, minSize int64, rootID int64) ([]FileRef, error) {
	q := `
	WITH dup AS (
	  SELECT size FROM files
	  WHERE state='active' AND media_kind<>'other' AND size>=? ` + rootFilter(rootID) + `
	  GROUP BY size HAVING COUNT(*)>1
	)
	SELECT f.id, f.root_id, f.path, f.size, f.mtime_ns, f.media_kind, f.ext,
	       COALESCE(f.quick_hash,''), COALESCE(f.content_hash,''), COALESCE(f.phash,''), f.state
	FROM files f JOIN dup d ON f.size=d.size
	WHERE f.state='active' AND f.media_kind<>'other' ` + rootFilter(rootID) + `
	ORDER BY f.size DESC, f.id`
	args := []any{minSize}
	if rootID > 0 {
		args = append(args, rootID, rootID)
	}
	rows, err := st.Select(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FileRef{}
	for rows.Next() {
		var f FileRef
		if err := rows.Scan(&f.ID, &f.RootID, &f.Path, &f.Size, &f.MtimeNS, &f.Kind, &f.Ext,
			&f.QuickHash, &f.Content, &f.PHash, &f.State); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func rootFilter(rootID int64) string {
	if rootID > 0 {
		return "AND root_id=?"
	}
	return ""
}
