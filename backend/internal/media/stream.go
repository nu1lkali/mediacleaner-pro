package media

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"mediacleaner/internal/config"
)

var browserSafeExt = map[string]bool{".mp4": true, ".m4v": true, ".webm": true, ".mov": true}
var browserSafeCodec = map[string]bool{
	"h264": true, "avc1": true, "vp8": true, "vp9": true, "av1": true, "theora": true,
	"aac": true, "mp3": true, "opus": true, "vorbis": true, "flac": true,
}

// NeedsProxy 判断浏览器能否直接播放；不能则需要转码代理。
func NeedsProxy(ext, vcodec, acodec string) bool {
	if !browserSafeExt[strings.ToLower(ext)] {
		return true
	}
	if v := strings.ToLower(vcodec); v != "" && !browserSafeCodec[v] {
		return true
	}
	if a := strings.ToLower(acodec); a != "" && !browserSafeCodec[a] {
		return true
	}
	return false
}

// ContentType 返回媒体 MIME。
func ContentType(ext string) string {
	switch strings.ToLower(ext) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".webm":
		return "video/webm"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".ts":
		return "video/mp2t"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".bmp":
		return "image/bmp"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".mp3":
		return "audio/mpeg"
	case ".flac":
		return "audio/flac"
	case ".m4a":
		return "audio/mp4"
	case ".wav":
		return "audio/wav"
	}
	return "application/octet-stream"
}

// ServeFile 直接流式输出，http.ServeContent 自带 Range / If-Modified-Since 支持。
func ServeFile(w http.ResponseWriter, r *http.Request, path, displayName, contentType string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if displayName != "" {
		w.Header().Set("Content-Disposition", "inline; filename=\""+displayName+"\"")
	}
	http.ServeContent(w, r, displayName, st.ModTime(), f)
	return nil
}

// ---------- 转码代理 ----------

type ProxyState struct {
	Status  string `json:"status"` // idle|running|ready|failed|unavailable
	Percent int    `json:"percent,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Path    string `json:"path,omitempty"`
}

var (
	proxyMu    sync.Mutex
	proxyState = map[int64]ProxyState{}
)

func ProxyPathFor(cfg *config.Config, id, mtimeNS int64) string {
	_ = os.MkdirAll(cfg.ProxyDir(), 0o755)
	return filepath.Join(cfg.ProxyDir(), strconv.FormatInt(id, 10)+"_"+strconv.FormatInt(mtimeNS, 10)+".mp4")
}

func ProxyStatus(id int64) ProxyState {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	if st, ok := proxyState[id]; ok {
		return st
	}
	return ProxyState{Status: "idle"}
}

// BuildProxy 同步生成 MP4(H.264/AAC) 代理并缓存；已有缓存直接复用。
func BuildProxy(cfg *config.Config, id int64, src string, mtimeNS, durationMS int64) (string, error) {
	dest := ProxyPathFor(cfg, id, mtimeNS)
	if st, err := os.Stat(dest); err == nil && st.Size() > 0 {
		setProxy(id, ProxyState{Status: "ready", Percent: 100, Path: dest})
		return dest, nil
	}
	if !FFmpegAvailable() {
		setProxy(id, ProxyState{Status: "unavailable", Reason: "未安装 FFmpeg，无法转码预览"})
		return "", &ThumbUnavailable{Code: "no_ffmpeg", Reason: "未安装 FFmpeg，无法转码预览"}
	}
	proxyMu.Lock()
	if st, ok := proxyState[id]; ok && st.Status == "running" {
		proxyMu.Unlock()
		for i := 0; i < 600; i++ {
			time.Sleep(500 * time.Millisecond)
			if cur := ProxyStatus(id); cur.Status != "running" {
				if cur.Status == "ready" {
					return dest, nil
				}
				break
			}
		}
	} else {
		proxyState[id] = ProxyState{Status: "running"}
		proxyMu.Unlock()
	}

	tmp := dest + ".part.mp4"
	cmd := exec.Command(FFmpegPath(), "-hide_banner", "-loglevel", "error", "-y", "-i", src,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart", "-f", "mp4", tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(tmp)
		setProxy(id, ProxyState{Status: "failed", Reason: strings.TrimSpace(string(out))})
		return "", &ThumbUnavailable{Code: "ffmpeg_failed", Reason: "转码失败：" + strings.TrimSpace(string(out))}
	}
	if err := os.Rename(tmp, dest); err != nil {
		setProxy(id, ProxyState{Status: "failed", Reason: err.Error()})
		return "", err
	}
	setProxy(id, ProxyState{Status: "ready", Percent: 100, Path: dest})
	return dest, nil
}

func setProxy(id int64, st ProxyState) {
	proxyMu.Lock()
	proxyState[id] = st
	proxyMu.Unlock()
}
