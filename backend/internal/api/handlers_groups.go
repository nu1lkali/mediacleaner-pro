package api

import (
	"fmt"
	"net/http"
	"strings"
)

// GroupMember 是组内成员的展示视图。
type GroupMember struct {
	FileID     int64   `json:"file_id"`
	Path       string  `json:"path"`
	Name       string  `json:"name"`
	Ext        string  `json:"ext"`
	Size       int64   `json:"size"`
	Kind       string  `json:"media_kind"`
	MtimeNS    int64   `json:"mtime_ns"`
	Similarity float64 `json:"similarity"`
	IsKeeper   bool    `json:"is_keeper"`
	KeepReason string  `json:"keep_reason"`
	Volume     string  `json:"volume"`
	RootID     int64   `json:"root_id"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	DurationMS int64   `json:"duration_ms"`
	Bitrate    int64   `json:"bitrate"`
	VideoCodec string  `json:"video_codec"`
	AudioCodec string  `json:"audio_codec"`
	HashStatus string  `json:"hash_status"`
	State      string  `json:"state"`
}

// Group 是一个重复组的展示视图。
type Group struct {
	ID               int64         `json:"id"`
	Kind             string        `json:"kind"`
	Signature        string        `json:"signature"`
	Size             int64         `json:"size"`
	MemberCount      int           `json:"member_count"`
	TotalBytes       int64         `json:"total_bytes"`
	ReclaimableBytes int64         `json:"reclaimable_bytes"`
	AvgSimilarity    float64       `json:"avg_similarity"`
	CreatedAt        int64         `json:"created_at"`
	Members          []GroupMember `json:"members"`
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = "exact"
	}
	page := queryInt(r, "page", 1)
	if page < 1 {
		page = 1
	}
	size := queryInt(r, "size", 20)
	if size <= 0 || size > 200 {
		size = 20
	}
	minReclaim := queryInt64(r, "min_reclaim", 0)
	volume := strings.TrimSpace(r.URL.Query().Get("volume"))
	sortBy := strings.TrimSpace(r.URL.Query().Get("sort"))
	if sortBy == "" {
		sortBy = "reclaim"
	}

	where := []string{"g.kind=?", "g.member_count>1"}
	args := []any{kind}
	if minReclaim > 0 {
		where = append(where, "g.reclaimable_bytes>=?")
		args = append(args, minReclaim)
	}
	if volume != "" {
		where = append(where, `EXISTS (SELECT 1 FROM dup_group_members m
			JOIN files f ON f.id=m.file_id JOIN roots rt ON rt.id=f.root_id
			WHERE m.group_id=g.id AND rt.volume=?)`)
		args = append(args, volume)
	}
	order := "g.reclaimable_bytes DESC, g.id"
	switch sortBy {
	case "size":
		order = "g.total_bytes DESC, g.id"
	case "members":
		order = "g.member_count DESC, g.id"
	case "similarity":
		order = "g.avg_similarity DESC, g.id"
	case "newest":
		order = "g.created_at DESC, g.id"
	}
	whereSQL := strings.Join(where, " AND ")

	var total int
	if err := s.st.Get(r.Context(), &total,
		`SELECT COUNT(*) FROM dup_groups g WHERE `+whereSQL, args...); err != nil {
		fail(w, err)
		return
	}

	rows, err := s.st.Select(r.Context(), fmt.Sprintf(`
		SELECT g.id, g.kind, g.signature, g.size, g.member_count, g.total_bytes,
		       g.reclaimable_bytes, g.avg_similarity, g.created_at
		FROM dup_groups g WHERE %s ORDER BY %s LIMIT ? OFFSET ?`, whereSQL, order),
		append(args, size, (page-1)*size)...)
	if err != nil {
		fail(w, err)
		return
	}
	// 必须初始化为空切片：nil 会被编码成 null，前端 items.map 会直接抛错
	groups := []*Group{}
	index := map[int64]*Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Kind, &g.Signature, &g.Size, &g.MemberCount,
			&g.TotalBytes, &g.ReclaimableBytes, &g.AvgSimilarity, &g.CreatedAt); err != nil {
			rows.Close()
			fail(w, err)
			return
		}
		g.Members = []GroupMember{}
		groups = append(groups, &g)
		index[g.ID] = &g
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		fail(w, err)
		return
	}

	if err := s.fillMembers(r, index, 0); err != nil {
		fail(w, err)
		return
	}
	ok(w, map[string]any{
		"items": groups,
		"total": total,
		"page":  page,
		"size":  size,
	})
}

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	var g Group
	err = s.st.Get(r.Context(), []any{&g.ID, &g.Kind, &g.Signature, &g.Size, &g.MemberCount,
		&g.TotalBytes, &g.ReclaimableBytes, &g.AvgSimilarity, &g.CreatedAt},
		`SELECT id, kind, signature, size, member_count, total_bytes, reclaimable_bytes,
		        avg_similarity, created_at FROM dup_groups WHERE id=?`, id)
	if err != nil {
		fail(w, notFound("分组不存在"))
		return
	}
	index := map[int64]*Group{g.ID: &g}
	if err := s.fillMembers(r, index, 0); err != nil {
		fail(w, err)
		return
	}
	ok(w, g)
}

func (s *Server) handleGroupSummary(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.Select(r.Context(), `
		SELECT kind, COUNT(*) AS groups, COALESCE(SUM(member_count - 1),0) AS removable,
		       COALESCE(SUM(reclaimable_bytes),0) AS bytes
		FROM dup_groups WHERE member_count>1 GROUP BY kind`)
	if err != nil {
		fail(w, err)
		return
	}
	defer rows.Close()
	type sum struct {
		Kind      string `json:"kind"`
		Groups    int64  `json:"groups"`
		Removable int64  `json:"removable"`
		Bytes     int64  `json:"bytes"`
	}
	items := []sum{}
	var totalGroups, totalRemovable, totalBytes int64
	for rows.Next() {
		var x sum
		if err := rows.Scan(&x.Kind, &x.Groups, &x.Removable, &x.Bytes); err != nil {
			fail(w, err)
			return
		}
		items = append(items, x)
		totalGroups += x.Groups
		totalRemovable += x.Removable
		totalBytes += x.Bytes
	}
	ok(w, map[string]any{
		"items":           items,
		"total_groups":    totalGroups,
		"removable_files": totalRemovable,
		"reclaimable":     totalBytes,
	})
}

// fillMembers 批量加载组成员，避免 N+1 查询。
// limit<=0 表示不限制。
func (s *Server) fillMembers(r *http.Request, index map[int64]*Group, limit int) error {
	if len(index) == 0 {
		return nil
	}
	ids := make([]any, 0, len(index))
	ph := make([]string, 0, len(index))
	for id := range index {
		ids = append(ids, id)
		ph = append(ph, "?")
	}
	rows, err := s.st.Select(r.Context(), `
		SELECT m.group_id, f.id, f.path, f.name, f.ext, f.size, f.media_kind, f.mtime_ns,
		       m.similarity, m.is_keeper, COALESCE(m.keep_reason,''), COALESCE(rt.volume,''),
		       f.root_id, COALESCE(md.width,0), COALESCE(md.height,0), COALESCE(md.duration_ms,0),
		       COALESCE(md.bitrate,0), COALESCE(md.video_codec,''), COALESCE(md.audio_codec,''),
		       f.hash_status, f.state
		FROM dup_group_members m
		JOIN files f ON f.id=m.file_id
		LEFT JOIN roots rt ON rt.id=f.root_id
		LEFT JOIN file_media md ON md.file_id=f.id
		WHERE m.group_id IN (`+strings.Join(ph, ",")+`)
		ORDER BY m.group_id, m.is_keeper DESC, f.size DESC, f.path`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			gid   int64
			m     GroupMember
			keep  int
		)
		if err := rows.Scan(&gid, &m.FileID, &m.Path, &m.Name, &m.Ext, &m.Size, &m.Kind,
			&m.MtimeNS, &m.Similarity, &keep, &m.KeepReason, &m.Volume, &m.RootID,
			&m.Width, &m.Height, &m.DurationMS, &m.Bitrate, &m.VideoCodec, &m.AudioCodec,
			&m.HashStatus, &m.State); err != nil {
			return err
		}
		m.IsKeeper = keep == 1
		if g, ok := index[gid]; ok {
			if limit > 0 && len(g.Members) >= limit {
				continue
			}
			g.Members = append(g.Members, m)
		}
	}
	return rows.Err()
}

type setKeeperReq struct {
	FileID int64 `json:"file_id"`
}

// handleSetKeeper 允许用户手动指定保留文件 —— 手动优先于任何自动策略。
func (s *Server) handleSetKeeper(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	var req setKeeperReq
	if err := decodeBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	var exists int
	if err := s.st.Get(r.Context(), &exists,
		`SELECT COUNT(*) FROM dup_group_members WHERE group_id=? AND file_id=?`, id, req.FileID); err != nil {
		fail(w, err)
		return
	}
	if exists == 0 {
		fail(w, badRequest("该文件不属于此分组"))
		return
	}

	// 先把组内 keeper 全部清零，再设置新的
	if _, err := s.st.Exec(r.Context(),
		`UPDATE dup_group_members SET is_keeper=0, keep_reason=NULL WHERE group_id=?`, id); err != nil {
		fail(w, err)
		return
	}
	if _, err := s.st.Exec(r.Context(),
		`UPDATE dup_group_members SET is_keeper=1, keep_reason=? WHERE group_id=? AND file_id=?`,
		"用户手动指定", id, req.FileID); err != nil {
		fail(w, err)
		return
	}
	s.st.Audit(r.Context(), actorOf(r), "group.set_keeper", "dup_group", fmt.Sprint(id),
		map[string]any{"file_id": req.FileID}, clientIP(r))

	var g Group
	if err := s.st.Get(r.Context(), []any{&g.ID, &g.Kind, &g.Signature, &g.Size, &g.MemberCount,
		&g.TotalBytes, &g.ReclaimableBytes, &g.AvgSimilarity, &g.CreatedAt},
		`SELECT id, kind, signature, size, member_count, total_bytes, reclaimable_bytes,
		        avg_similarity, created_at FROM dup_groups WHERE id=?`, id); err != nil {
		fail(w, err)
		return
	}
	index := map[int64]*Group{g.ID: &g}
	if err := s.fillMembers(r, index, 0); err != nil {
		fail(w, err)
		return
	}
	ok(w, g)
}
