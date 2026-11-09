package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mediacleaner/internal/media"
)

// FileDetail 是单个文件的完整信息。
type FileDetail struct {
	ID         int64  `json:"id"`
	RootID     int64  `json:"root_id"`
	Path       string `json:"path"`
	DirPath    string `json:"dir_path"`
	Name       string `json:"name"`
	Stem       string `json:"stem"`
	Ext        string `json:"ext"`
	Size       int64  `json:"size"`
	MtimeNS    int64  `json:"mtime_ns"`
	Kind       string `json:"media_kind"`
	State      string `json:"state"`
	HashStatus string `json:"hash_status"`
	QuickHash  string `json:"quick_hash"`
	ContentHash string `json:"content_hash"`
	PHash      string `json:"phash"`
	Volume     string `json:"volume"`
	RootName   string `json:"root_name"`

	Media     *media.Info `json:"media"`
	ExtraJSON string      `json:"extra_json"`
	ExifJSON  string      `json:"exif_json"`

	// 播放能力提示
	NeedsProxy bool `json:"needs_proxy"`
	Playable   bool `json:"playable"`

	Groups []GroupRef `json:"groups"`
}

// GroupRef 指向该文件所属的重复组。
type GroupRef struct {
	GroupID   int64   `json:"group_id"`
	Kind      string  `json:"kind"`
	Members   int     `json:"members"`
	Similarity float64 `json:"similarity"`
	IsKeeper  bool    `json:"is_keeper"`
}

func (s *Server) loadFile(r *http.Request, id int64) (*FileDetail, error) {
	var (
		f        FileDetail
		quick    sql.NullString
		content  sql.NullString
		phash    sql.NullString
		volume   sql.NullString
		rootName sql.NullString
	)
	err := s.st.Get(r.Context(), []any{&f.ID, &f.RootID, &f.Path, &f.DirPath, &f.Name, &f.Stem,
		&f.Ext, &f.Size, &f.MtimeNS, &f.Kind, &f.State, &f.HashStatus, &quick, &content,
		&phash, &volume, &rootName},
		`SELECT f.id, f.root_id, f.path, f.dir_path, f.name, f.stem, f.ext, f.size, f.mtime_ns,
		        f.media_kind, f.state, f.hash_status, f.quick_hash, f.content_hash, f.phash,
		        rt.volume, rt.name
		 FROM files f LEFT JOIN roots rt ON rt.id=f.root_id WHERE f.id=?`, id)
	if err != nil {
		return nil, err
	}
	f.QuickHash = quick.String
	f.ContentHash = content.String
	f.PHash = phash.String
	f.Volume = volume.String
	f.RootName = rootName.String

	// 媒体信息（可能不存在）
	var (
		dur, bitrate int64
		w, h         int
		vcodec, acodec, container, source, extra, exif sql.NullString
		fps sql.NullFloat64
	)
	err = s.st.Get(r.Context(), []any{&dur, &w, &h, &vcodec, &acodec, &bitrate, &fps,
		&container, &source, &extra, &exif},
		`SELECT duration_ms, width, height, video_codec, audio_codec, bitrate, fps,
		        container, source, extra_json, exif_json FROM file_media WHERE file_id=?`, id)
	if err == nil {
		f.Media = &media.Info{
			DurationMS: dur, Width: w, Height: h,
			VideoCodec: vcodec.String, AudioCodec: acodec.String,
			Bitrate: bitrate, FPS: fps.Float64,
			Container: container.String, Source: source.String,
			ExtraJSON: extra.String, ExifJSON: exif.String,
		}
		f.ExtraJSON = extra.String
		f.ExifJSON = exif.String
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	if f.Kind == "video" || f.Kind == "audio" {
		vc, ac := "", ""
		if f.Media != nil {
			vc, ac = f.Media.VideoCodec, f.Media.AudioCodec
		}
		f.NeedsProxy = media.NeedsProxy(f.Ext, vc, ac)
		f.Playable = !f.NeedsProxy
	} else if f.Kind == "image" {
		f.Playable = true
	}

	// 所属分组
	rows, err := s.st.Select(r.Context(), `
		SELECT m.group_id, g.kind, g.member_count, m.similarity, m.is_keeper
		FROM dup_group_members m JOIN dup_groups g ON g.id=m.group_id
		WHERE m.file_id=? ORDER BY g.kind`, id)
	if err == nil {
		for rows.Next() {
			var g GroupRef
			var keep int
			if err := rows.Scan(&g.GroupID, &g.Kind, &g.Members, &g.Similarity, &keep); err == nil {
				g.IsKeeper = keep == 1
				f.Groups = append(f.Groups, g)
			}
		}
		rows.Close()
	}
	return &f, nil
}

func (s *Server) handleFileDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	f, err := s.loadFile(r, id)
	if err != nil {
		fail(w, notFound("文件不存在"))
		return
	}
	ok(w, f)
}

// resolveFile 是媒体访问的统一入口：先查库，再校验路径仍在受管卷内，最后确认磁盘存在。
func (s *Server) resolveFile(r *http.Request, id int64) (*FileDetail, string, error) {
	f, err := s.loadFile(r, id)
	if err != nil {
		return nil, "", notFound("文件不存在")
	}
	if f.State != "active" {
		return nil, "", conflict("该文件已被清理，状态：" + f.State)
	}
	if _, err := s.roots.Resolve(r.Context(), f.Path); err != nil {
		return nil, "", forbidden("文件不在受管存储卷内")
	}
	st, err := os.Lstat(f.Path)
	if err != nil {
		return nil, "", notFound("磁盘上已找不到该文件")
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return nil, "", forbidden("拒绝访问符号链接")
	}
	return f, f.Path, nil
}

// handleRaw 原文件流式输出，支持 Range（视频拖动进度必需）。
func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	f, path, err := s.resolveFile(r, id)
	if err != nil {
		fail(w, err)
		return
	}
	ct := media.ContentType(f.Ext)
	if err := media.ServeFile(w, r, path, f.Name, ct); err != nil {
		// ServeFile 内部已写响应头，这里只记录
		return
	}
}

// handleThumb 输出缩略图（带磁盘缓存）。
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	f, path, err := s.resolveFile(r, id)
	if err != nil {
		fail(w, err)
		return
	}
	width := queryInt(r, "w", s.cfg.ThumbSize)
	if width <= 0 || width > 1600 {
		width = s.cfg.ThumbSize
	}
	var dur int64
	if f.Media != nil {
		dur = f.Media.DurationMS
	}
	thumb, err := media.Thumb(s.cfg, f.Kind, path, f.ID, f.MtimeNS, width, dur)
	if err != nil {
		var un *media.ThumbUnavailable
		if errors.As(err, &un) {
			writeJSON(w, http.StatusNotImplemented, &apiError{
				Status: 501, Code: "thumb_unavailable", Msg: un.Reason,
			})
			return
		}
		fail(w, serverError("生成缩略图失败: "+err.Error()))
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, thumb)
}

// handleProxy 为浏览器不兼容的格式生成 H.264/AAC 代理文件再播放。
func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		fail(w, err)
		return
	}
	f, path, err := s.resolveFile(r, id)
	if err != nil {
		fail(w, err)
		return
	}
	if f.Kind != "video" {
		fail(w, badRequest("仅视频支持代理播放"))
		return
	}
	if !media.NeedsProxy(f.Ext, codecOf(f, "video"), codecOf(f, "audio")) {
		// 本来就兼容，直接给原文件
		if err := media.ServeFile(w, r, path, f.Name, media.ContentType(f.Ext)); err != nil {
			return
		}
		return
	}
	if !media.FFmpegAvailable() {
		fail(w, &apiError{Status: http.StatusNotImplemented, Code: "ffmpeg_missing",
			Msg: "该格式需要 ffmpeg 转码，但当前环境未检测到 ffmpeg"})
		return
	}
	var dur int64
	if f.Media != nil {
		dur = f.Media.DurationMS
	}
	st := media.ProxyStatus(f.ID)
	if st.Status == "running" {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status": "converting", "progress": st.Percent,
			"message": "正在转码，请稍候",
		})
		return
	}
	proxy, err := media.BuildProxy(s.cfg, f.ID, path, f.MtimeNS, dur)
	if err != nil {
		fail(w, serverError("转码失败: "+err.Error()))
		return
	}
	if err := media.ServeFile(w, r, proxy, f.Stem+".mp4", "video/mp4"); err != nil {
		return
	}
}

func codecOf(f *FileDetail, kind string) string {
	if f.Media == nil {
		return ""
	}
	if kind == "video" {
		return f.Media.VideoCodec
	}
	return f.Media.AudioCodec
}

var _ = json.Marshal
var _ = filepath.Ext
var _ = strconv.Itoa
var _ = strings.TrimSpace
