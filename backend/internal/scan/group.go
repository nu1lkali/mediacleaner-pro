package scan

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"mediacleaner/internal/keeper"
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

// 分组证据等级，写入 dup_groups.evidence。取值统一定义在 store 包，
// 清理器（cleaner）消费时不会与 scan 包形成循环依赖。
const (
	// EvidenceExact 表示同组文件的 content_hash 相同，即已逐字节校验过。
	EvidenceExact = store.EvidenceExact
	// EvidenceSampled 表示只比过 size + 采样指纹（首/1/3/2/3/尾 各 64KB），
	// 没有读全文。这类组在清理执行前必须再与保留副本比对一次。
	EvidenceSampled = store.EvidenceSampled
	// EvidenceSimilar 用于相似图片/相似视频分组：相似度来自感知哈希，
	// 本就允许不同内容，不涉及逐字节相等。
	EvidenceSimilar = store.EvidenceSimilar
	// EvidenceTrusted 表示用户显式确认「信任采样结果，清理时也不逐字节比对」。
	// 来源是扫描时的「完全免校验」开关，或分组页的「一键信任」按钮。
	// 注意：这**不是**已全文校验，只是把误判风险显式交给用户承担；
	// UI 必须显示成「免校验」，不得冒充「已校验」。
	EvidenceTrusted = store.EvidenceTrusted
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

func (e *Engine) buildGroups(ctx context.Context, rs *runState, kinds []string, opts Options) error {
	gb := &groupBuilder{st: e.st, taskID: rs.taskID}
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}

	// 本批分组的证据等级。SkipVerify 隐含 DeferVerify：都不读全文，
	// 区别只在清理时是否还要与保留副本比对。
	evidence := EvidenceExact
	if opts.DeferVerify {
		evidence = EvidenceSampled
	}
	if opts.SkipVerify {
		evidence = EvidenceTrusted
	}

	// 只清理「本任务上一次」的分组；其它任务的批次归档保留、可切回查看。
	if err := gb.reset(ctx); err != nil {
		return err
	}
	done := int64(0)
	if want[GroupExact] {
		switch evidence {
		case EvidenceExact:
			rs.update(func(p *Progress) { p.Message = "重建完全重复分组" })
		case EvidenceTrusted:
			rs.update(func(p *Progress) { p.Message = "重建完全重复分组（免校验：清理时也不比对）" })
		default:
			rs.update(func(p *Progress) { p.Message = "重建完全重复分组（采样证据，未全文校验）" })
		}
		if err := gb.exact(ctx, evidence); err != nil {
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
	// 新分组以 batch_active=0 插入，全部构建成功后才一次性激活：
	// 扫描中途失败时，界面仍停留在上一个完整批次，不会看到半截清单。
	rs.update(func(p *Progress) { p.Message = "切换到本次扫描结果" })
	if err := gb.activate(ctx); err != nil {
		return err
	}
	// 分组是派生数据、每轮扫描重建（见 reset）。把 settings 里保存的
	// 「同组保留条件」重新套到当前激活批次上，否则用户配置的条件一扫描就丢。
	rs.update(func(p *Progress) { p.Message = "恢复同组保留条件" })
	if _, err := keeper.ApplyDefaults(ctx, gb.st); err != nil {
		return err
	}
	rs.update(func(p *Progress) { p.PhaseDone = int64(len(kinds) + 1) })
	return nil
}

func (gb *groupBuilder) reset(ctx context.Context) error {
	// 只删本任务上一次产出（同一个任务重跑时），其它任务的批次完整保留。
	// 旧实现是 DELETE FROM dup_groups 全表清空，导致多次扫描结果互相覆盖。
	_, err := gb.st.Exec(ctx, `DELETE FROM dup_groups WHERE scan_task_id=?`, gb.taskID)
	return err
}

// maxArchivedBatches 限制归档批次数量，避免长期运行时 dup_groups 无限增长。
// 当前激活批次始终保留，不计入这个上限。
const maxArchivedBatches = 5

func (gb *groupBuilder) activate(ctx context.Context) error {
	return gb.st.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE dup_groups SET batch_active=0 WHERE batch_active=1`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE dup_groups SET batch_active=1 WHERE scan_task_id=?`, gb.taskID); err != nil {
			return err
		}
		// 即使本次扫描 0 个重复组，也要记住「现在看的是哪一批」。
		// 注意：必须复用当前事务的连接，写连接池 MaxOpenConns=1，
		// 事务期间另起一次 st.Exec 会自己等自己（死锁）。
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings(key, value_json, updated_at) VALUES(?,?,?)
			 ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json, updated_at=excluded.updated_at`,
			store.ActiveBatchKey, fmt.Sprint(gb.taskID), store.NowMS()); err != nil {
			return err
		}
		// 归档只留最近 N 批，按批次内最新分组时间排序
		_, err := tx.ExecContext(ctx, `
			DELETE FROM dup_groups WHERE batch_active=0 AND COALESCE(scan_task_id,0) NOT IN (
			  SELECT COALESCE(scan_task_id,0) FROM dup_groups WHERE batch_active=0
			  GROUP BY COALESCE(scan_task_id,0) ORDER BY MAX(created_at) DESC LIMIT ?
			)`, maxArchivedBatches)
		return err
	})
}

// ---------- 完全重复 ----------

// exact 构建完全重复分组。evidence=exact 时按 content_hash 建组（已全文校验），
// 否则按 (size, quick_hash) 建组，证据等级沿用传入值（sampled 或 trusted）。
func (gb *groupBuilder) exact(ctx context.Context, evidence string) error {
	if evidence == EvidenceExact {
		return gb.exactVerified(ctx)
	}
	return gb.exactSampled(ctx, evidence)
}

// exactVerified 用全文校验得到的 content_hash 分组：组内文件逐字节相同。
func (gb *groupBuilder) exactVerified(ctx context.Context) error {
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
			gid, err := insertGroup(ctx, insGroup, GroupExact, b.hash, keeper.Size, b.total, 1.0,
				EvidenceExact, gb.taskID, now)
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

// exactSampled 只用 (size, quick_hash) 建组，不读全文。
//
// 采样指纹覆盖首 64KB、1/3、2/3、末 64KB，同大小又同时撞上这 4 段的两份文件，
// 实际上就是同一份内容的副本；但「实际上」不等于「已证明」。
// evidence=sampled 时清理执行前还会与保留副本逐字节比对（见 cleaner.processOne）；
// evidence=trusted 时用户已确认跳过该比对，风险由用户承担。
func (gb *groupBuilder) exactSampled(ctx context.Context, evidence string) error {
	rows, err := gb.st.Select(ctx, `
		SELECT size, quick_hash, SUM(size) AS total
		FROM files
		WHERE state='active' AND quick_hash IS NOT NULL AND quick_hash<>''
		  AND media_kind<>'other'
		GROUP BY size, quick_hash HAVING COUNT(*)>1`)
	if err != nil {
		return err
	}
	type bucket struct {
		size  int64
		quick string
		total int64
	}
	var buckets []bucket
	for rows.Next() {
		var b bucket
		if err := rows.Scan(&b.size, &b.quick, &b.total); err != nil {
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
			members, err := loadMembers(ctx, tx, `size=? AND quick_hash=?`, b.size, b.quick)
			if err != nil {
				return err
			}
			if len(members) < 2 {
				continue
			}
			keeper := pickKeeper(members)
			sig := fmt.Sprintf("%d|%s", b.size, b.quick)
			gid, err := insertGroup(ctx, insGroup, GroupExact, sig, keeper.Size, b.total, 1.0,
				evidence, gb.taskID, now)
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
		SELECT id, phash, COALESCE(content_hash,''), size, COALESCE(quick_hash,'')
		FROM files
		WHERE state='active' AND media_kind='image' AND phash IS NOT NULL AND phash<>''`)
	if err != nil {
		return err
	}
	// 完全重复的文件已经由 exact 组负责；这里按内容哈希只保留一份代表，
	// 避免同一个文件同时出现在 exact 与 image 组里、让可回收空间被重复计算。
	// 采样证据模式下 content_hash 为空，退化为与 exact 组相同的 size|quick 签名。
	seenContent := map[string]bool{}
	var items []sim.Item
	for rows.Next() {
		var (
			it    sim.Item
			size  int64
			quick string
		)
		if err := rows.Scan(&it.FileID, &it.Hash, &it.ContentHash, &size, &quick); err != nil {
			rows.Close()
			return err
		}
		key := it.ContentHash
		if key == "" && quick != "" {
			key = fmt.Sprintf("%d|%s", size, quick)
		}
		if key != "" {
			if seenContent[key] {
				continue
			}
			seenContent[key] = true
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
		       COALESCE(f.phash_frames,''), COALESCE(f.content_hash,''), f.size, COALESCE(f.quick_hash,'')
		FROM files f LEFT JOIN file_media m ON m.file_id=f.id
		WHERE f.state='active' AND f.media_kind='video'
		  AND f.phash_frames IS NOT NULL AND f.phash_frames<>''`)
	if err != nil {
		return err
	}
	// 同 images()：完全重复的视频由 exact 组负责，这里按内容哈希只留一份代表；
	// 采样证据模式下用与 exact 组相同的 size|quick 签名。
	seenContent := map[string]bool{}
	var (
		metas []sim.VideoMeta
		fps   = map[int64]sim.VideoFingerprint{}
	)
	for rows.Next() {
		var (
			id, dur     int64
			size        int64
			w, h        int
			path, raw   string
			contentHash string
			quick       string
		)
		if err := rows.Scan(&id, &path, &dur, &w, &h, &raw, &contentHash, &size, &quick); err != nil {
			rows.Close()
			return err
		}
		fp, ok := sim.ParseFrames(raw)
		if !ok {
			continue
		}
		key := contentHash
		if key == "" && quick != "" {
			key = fmt.Sprintf("%d|%s", size, quick)
		}
		if key != "" {
			if seenContent[key] {
				continue
			}
			seenContent[key] = true
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

		gid, err := insertGroup(ctx, insGroup, kind, sig, keeper.Size, total, avg,
			EvidenceSimilar, gb.taskID, now)
		if err != nil {
			return err
		}
		return insertMembers(ctx, insMember, gid, members, keeper)
	})
}

// ---------- 通用 SQL 与辅助 ----------

// batch_active 固定先写 0（待激活），全部建组成功后由 activate 原子切换，
// 保证扫描失败时界面不会停在半截清单上。
const insertGroupSQL = `
INSERT INTO dup_groups(kind, signature, size, member_count, total_bytes,
                       reclaimable_bytes, avg_similarity, evidence, scan_task_id,
                       batch_active, created_at, updated_at)
VALUES(?,?,?,?,?,?,?,?,?,0,?,?)`

const insertMemberSQL = `
INSERT OR IGNORE INTO dup_group_members(group_id, file_id, similarity, is_keeper, keep_reason)
VALUES(?,?,?,?,?)`

// insertGroup 写入分组。baseSize 是保留文件的体积 —— 可回收空间必须按
// "总量 - 保留文件" 计算，否则相似组里保留大文件、按最小文件算，
// 会把可回收空间算多。
func insertGroup(ctx context.Context, stmt *sql.Stmt, kind, sig string, baseSize, total int64,
	avg float64, evidence string, taskID, now int64) (int64, error) {
	reclaim := total - baseSize
	if reclaim < 0 {
		reclaim = 0
	}
	res, err := stmt.ExecContext(ctx, kind, sig, baseSize, 0, total, reclaim, avg, evidence, taskID, now, now)
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

func loadMembers(ctx context.Context, tx *sql.Tx, where string, args ...any) ([]member, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, path, size FROM files
		WHERE state='active' AND `+where+`
		ORDER BY LENGTH(path), mtime_ns`, args...)
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

// refreshCounters 校正本批次分组的 member_count 与 reclaimable_bytes。
func (gb *groupBuilder) refreshCounters(ctx context.Context) error {
	_, err := gb.st.Exec(ctx, `
		UPDATE dup_groups SET
		  member_count = (SELECT COUNT(*) FROM dup_group_members m WHERE m.group_id=dup_groups.id),
		  reclaimable_bytes = CASE
		    WHEN (SELECT COUNT(*) FROM dup_group_members m WHERE m.group_id=dup_groups.id) > 1
		    THEN MAX(total_bytes - size, 0) ELSE 0 END
		WHERE scan_task_id=?`, gb.taskID)
	return err
}
