package scan

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"mediacleaner/internal/sim"
	"mediacleaner/internal/store"
)

// 分组类型
const (
	GroupExact = "exact" // 内容完全一致
	GroupImage = "image" // 图片视觉相似
	GroupVideo = "video" // 视频内容相似
	GroupName  = "name"  // 同名不同后缀（由规则引擎产生，不在此处构建）
)

// member 是分组内的一个候选文件。
type member struct {
	ID         int64
	Path       string
	Size       int64
	Depth      int
	Similarity float64
	Reason     string
	Keeper     bool
}

// groupBuilder 负责把 files 表里的指纹汇总成 dup_groups。
type groupBuilder struct {
	st     *store.Store
	taskID int64
}

func (e *Engine) buildGroups(ctx context.Context, rs *runState, kinds []string) error {
	gb := &groupBuilder{st: e.st, taskID: rs.taskID}
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}

	// 分组是派生数据，整体重建；未启用的类型直接清空，避免旧结果误导
	if err := gb.reset(ctx); err != nil {
		return err
	}
	done := int64(0)
	if want[GroupExact] {
		rs.update(func(p *Progress) { p.Message = "重建完全重复分组" })
		if err := gb.exact(ctx); err != nil {
			return err
		}
		done++
		rs.update(func(p *Progress) { p.PhaseDone = done })
	}
	if want[GroupImage] {
		rs.update(func(p *Progress) { p.Message = "重建图片相似分组" })
		if err := gb.images(ctx, e.cfg.SimilarThreshold, 4000); err != nil {
			return err
		}
		done++
		rs.update(func(p *Progress) { p.PhaseDone = done })
	}
	if want[GroupVideo] {
		rs.update(func(p *Progress) { p.Message = "重建视频相似分组" })
		if err := gb.videos(ctx, 0.86, e.cfg.SimilarMinHits); err != nil {
			return err
		}
		done++
		rs.update(func(p *Progress) { p.PhaseDone = done })
	}

	rs.update(func(p *Progress) { p.Message = "校正分组统计" })
	if err := gb.refreshCounters(ctx); err != nil {
		return err
	}
	rs.update(func(p *Progress) { p.PhaseDone = int64(len(kinds) + 1) })
	return nil
}

func (gb *groupBuilder) reset(ctx context.Context) error {
	// 分组是派生数据，每次扫描整体重建，避免陈旧分组残留
	_, err := gb.st.Exec(ctx, `DELETE FROM dup_groups`)
	return err
}

// ---------- 完全重复 ----------

func (gb *groupBuilder) exact(ctx context.Context) error {
	rows, err := gb.st.Select(ctx, `
		SELECT content_hash, COUNT(*) AS c, SUM(size) AS total, MIN(size) AS one
		FROM files
		WHERE state='active' AND content_hash IS NOT NULL AND content_hash<>''
		GROUP BY content_hash HAVING c>1`)
	if err != nil {
		return err
	}
	type bucket struct {
		hash  string
		count int64
		total int64
		one   int64
	}
	var buckets []bucket
	for rows.Next() {
		var b bucket
		if err := rows.Scan(&b.hash, &b.count, &b.total, &b.one); err != nil {
			rows.Close()
			return err
		}
		buckets = append(buckets, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(buckets) == 0 {
		return nil
	}

	return gb.st.Tx(ctx, func(tx *sql.Tx) error {
		insGroup, err := tx.PrepareContext(ctx, insertGroupSQL)
		if err != nil {
			return err
		}
		defer insGroup.Close()
		insMember, err := tx.PrepareContext(ctx, insertMemberSQL)
		if err != nil {
			return err
		}
		defer insMember.Close()
		now := store.NowMS()

		for _, b := range buckets {
			members, err := loadMembers(ctx, tx, `content_hash=?`, b.hash)
			if err != nil {
				return err
			}
			if len(members) < 2 {
				continue
			}
			keeper := pickKeeper(members)
			gid, err := insertGroup(ctx, insGroup, GroupExact, b.hash, keeper.Size, b.total, 1.0, gb.taskID, now)
			if err != nil {
				return err
			}
			if err := insertMembers(ctx, insMember, gid, members, keeper); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------- 图片相似 ----------

func (gb *groupBuilder) images(ctx context.Context, threshold, exactLimit int) error {
	rows, err := gb.st.Select(ctx, `
		SELECT id, phash, COALESCE(content_hash,'')
		FROM files
		WHERE state='active' AND media_kind='image' AND phash IS NOT NULL AND phash<>''`)
	if err != nil {
		return err
	}
	// 完全重复的文件已经由 exact 组负责；这里按内容哈希只保留一份代表，
	// 避免同一个文件同时出现在 exact 与 image 组里、让可回收空间被重复计算。
	seenContent := map[string]bool{}
	var items []sim.Item
	for rows.Next() {
		var it sim.Item
		if err := rows.Scan(&it.FileID, &it.Hash, &it.ContentHash); err != nil {
			rows.Close()
			return err
		}
		if it.ContentHash != "" {
			if seenContent[it.ContentHash] {
				continue
			}
			seenContent[it.ContentHash] = true
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(items) < 2 {
		return nil
	}
	clusters := sim.ClusterByPHash(items, threshold, exactLimit)
	if len(clusters) == 0 {
		return nil
	}
	hashes := make(map[int64]string, len(items))
	for _, it := range items {
		hashes[it.FileID] = it.Hash
	}
	for _, cl := range clusters {
		sig := minHash(cl, hashes)
		if err := gb.persistCluster(ctx, GroupImage, sig, cl); err != nil {
			return err
		}
	}
	return nil
}

// ---------- 视频相似 ----------

func (gb *groupBuilder) videos(ctx context.Context, threshold float64, minHits int) error {
	rows, err := gb.st.Select(ctx, `
		SELECT f.id, f.path, COALESCE(m.duration_ms,0), COALESCE(m.width,0), COALESCE(m.height,0),
		       COALESCE(f.phash_frames,''), COALESCE(f.content_hash,'')
		FROM files f LEFT JOIN file_media m ON m.file_id=f.id
		WHERE f.state='active' AND f.media_kind='video'
		  AND f.phash_frames IS NOT NULL AND f.phash_frames<>''`)
	if err != nil {
		return err
	}
	// 同 images()：完全重复的视频由 exact 组负责，这里按内容哈希只留一份代表
	seenContent := map[string]bool{}
	var (
		metas []sim.VideoMeta
		fps   = map[int64]sim.VideoFingerprint{}
	)
	for rows.Next() {
		var (
			id, dur    int64
			w, h       int
			path, raw  string
			contentHash string
		)
		if err := rows.Scan(&id, &path, &dur, &w, &h, &raw, &contentHash); err != nil {
			rows.Close()
			return err
		}
		fp, ok := sim.ParseFrames(raw)
		if !ok {
			continue
		}
		if contentHash != "" {
			if seenContent[contentHash] {
				continue
			}
			seenContent[contentHash] = true
		}
		metas = append(metas, sim.VideoMeta{FileID: id, Path: path, DurationMS: dur, Width: w, Height: h})
		fps[id] = fp
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(metas) < 2 {
		return nil
	}
	clusters := sim.ClusterVideos(metas, fps, threshold, minHits, 200000)
	if len(clusters) == 0 {
		return nil
	}
	// 视频签名取首帧 pHash 最小者，跨重建稳定
	first := make(map[int64]string, len(metas))
	for id, fp := range fps {
		if len(fp.Frames) > 0 {
			first[id] = fp.Frames[0].Hash
		}
	}
	for _, cl := range clusters {
		sig := minHash(cl, first)
		if err := gb.persistCluster(ctx, GroupVideo, sig, cl); err != nil {
			return err
		}
	}
	return nil
}

// persistCluster 把一组聚类结果写入 dup_groups / dup_group_members。
func (gb *groupBuilder) persistCluster(ctx context.Context, kind, sig string, cl sim.Cluster) error {
	if len(cl) < 2 {
		return nil
	}
	ids := make([]int64, 0, len(cl))
	simOf := make(map[int64]float64, len(cl))
	for _, m := range cl {
		ids = append(ids, m.FileID)
		simOf[m.FileID] = m.Similarity
	}
	if sig == "" {
		sig = fmt.Sprintf("%s-%d", kind, ids[0])
	}

	return gb.st.Tx(ctx, func(tx *sql.Tx) error {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		args := make([]any, 0, len(ids))
		for _, id := range ids {
			args = append(args, id)
		}
		rows, err := tx.QueryContext(ctx,
			`SELECT id, path, size FROM files WHERE state='active' AND id IN (`+placeholders+`)`, args...)
		if err != nil {
			return err
		}
		var members []member
		var total int64
		for rows.Next() {
			m := member{}
			if err := rows.Scan(&m.ID, &m.Path, &m.Size); err != nil {
				rows.Close()
				return err
			}
			m.Depth = strings.Count(filepath.ToSlash(m.Path), "/")
			m.Similarity = simOf[m.ID]
			total += m.Size
			members = append(members, m)
		}
		rows.Close()
		if len(members) < 2 {
			return nil // 期间有文件被删，跳过该组
		}
		sort.Slice(members, func(i, j int) bool { return members[i].Path < members[j].Path })

		keeper := pickKeeper(members)
		avg := 0.0
		for _, m := range members {
			avg += m.Similarity
		}
		avg /= float64(len(members))

		now := store.NowMS()
		insGroup, err := tx.PrepareContext(ctx, insertGroupSQL)
		if err != nil {
			return err
		}
		defer insGroup.Close()
		insMember, err := tx.PrepareContext(ctx, insertMemberSQL)
		if err != nil {
			return err
		}
		defer insMember.Close()

		gid, err := insertGroup(ctx, insGroup, kind, sig, keeper.Size, total, avg, gb.taskID, now)
		if err != nil {
			return err
		}
		return insertMembers(ctx, insMember, gid, members, keeper)
	})
}

// ---------- 通用 SQL 与辅助 ----------

const insertGroupSQL = `
INSERT INTO dup_groups(kind, signature, size, member_count, total_bytes,
                       reclaimable_bytes, avg_similarity, scan_task_id, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?)`

const insertMemberSQL = `
INSERT OR IGNORE INTO dup_group_members(group_id, file_id, similarity, is_keeper, keep_reason)
VALUES(?,?,?,?,?)`

// insertGroup 写入分组。baseSize 是保留文件的体积 —— 可回收空间必须按
// "总量 - 保留文件" 计算，否则相似组里保留大文件、按最小文件算，
// 会把可回收空间算多。
func insertGroup(ctx context.Context, stmt *sql.Stmt, kind, sig string, baseSize, total int64,
	avg float64, taskID, now int64) (int64, error) {
	reclaim := total - baseSize
	if reclaim < 0 {
		reclaim = 0
	}
	res, err := stmt.ExecContext(ctx, kind, sig, baseSize, 0, total, reclaim, avg, taskID, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func insertMembers(ctx context.Context, stmt *sql.Stmt, gid int64, members []member, keeper member) error {
	for _, m := range members {
		isKeeper, reason := 0, ""
		if m.ID == keeper.ID {
			isKeeper, reason = 1, keeper.Reason
		}
		if _, err := stmt.ExecContext(ctx, gid, m.ID, m.Similarity, isKeeper, reason); err != nil {
			return err
		}
	}
	return nil
}

func loadMembers(ctx context.Context, tx *sql.Tx, where string, arg any) ([]member, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, path, size FROM files
		WHERE state='active' AND `+where+`
		ORDER BY LENGTH(path), mtime_ns`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []member
	for rows.Next() {
		m := member{Similarity: 1}
		if err := rows.Scan(&m.ID, &m.Path, &m.Size); err != nil {
			return nil, err
		}
		m.Depth = strings.Count(filepath.ToSlash(m.Path), "/")
		out = append(out, m)
	}
	return out, rows.Err()
}

// pickKeeper 默认保留策略：路径层级最浅优先，同层级取字典序最小。
// 这是"确定性"策略，不依赖文件系统返回顺序，便于自动化与复现。
func pickKeeper(members []member) member {
	best := members[0]
	for _, m := range members[1:] {
		if m.Depth < best.Depth || (m.Depth == best.Depth && m.Path < best.Path) {
			best = m
		}
	}
	best.Reason = "默认保留：路径层级最浅"
	return best
}

func minHash(cl sim.Cluster, hashes map[int64]string) string {
	best := ""
	for _, m := range cl {
		h, ok := hashes[m.FileID]
		if !ok || h == "" {
			continue
		}
		if best == "" || h < best {
			best = h
		}
	}
	return best
}

// refreshCounters 校正 member_count 与 reclaimable_bytes，防止插入失败导致的偏差。
func (gb *groupBuilder) refreshCounters(ctx context.Context) error {
	_, err := gb.st.Exec(ctx, `
		UPDATE dup_groups SET
		  member_count = (SELECT COUNT(*) FROM dup_group_members m WHERE m.group_id=dup_groups.id),
		  reclaimable_bytes = CASE
		    WHEN (SELECT COUNT(*) FROM dup_group_members m WHERE m.group_id=dup_groups.id) > 1
		    THEN MAX(total_bytes - size, 0) ELSE 0 END
		WHERE 1`)
	return err
}
