package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

var (
	capOnce     sync.Once
	ffmpegPath  string
	ffprobePath string
)

func initCapabilities(cfgFFmpeg, cfgFFprobe string) {
	capOnce.Do(func() {
		if p, err := exec.LookPath(cfgFFmpeg); err == nil {
			ffmpegPath = p
		}
		if p, err := exec.LookPath(cfgFFprobe); err == nil {
			ffprobePath = p
		}
	})
}

// SetBinaries 在启动时注入配置的 ffmpeg/ffprobe 名称，并解析真实路径。
func SetBinaries(ffmpeg, ffprobe string) { initCapabilities(ffmpeg, ffprobe) }

func FFmpegAvailable() bool  { return ffmpegPath != "" }
func FFprobeAvailable() bool { return ffprobePath != "" }
func FFmpegPath() string     { return ffmpegPath }

func Capabilities() map[string]bool {
	return map[string]bool{
		"ffmpeg":            FFmpegAvailable(),
		"ffprobe":           FFprobeAvailable(),
		"video_thumbnail":   FFmpegAvailable(),
		"video_transcode":   FFmpegAvailable(),
		"video_similarity":  FFmpegAvailable(),
		"native_probe":      true,
		"image_probe":       true,
		"image_similarity":  true,
	}
}

// ProbeStatus 供前端展示当前降级状态，避免"功能存在但不可用"的误导。
func ProbeStatus() map[string]any {
	mode := "native"
	note := "未检测到 ffprobe，视频元数据使用内置解析器（MP4/MKV/AVI），其余容器仅记录文件级信息。"
	if FFprobeAvailable() {
		mode = "ffprobe+native"
		note = ""
	}
	return map[string]any{
		"ffprobe": FFprobeAvailable(),
		"ffmpeg":  FFmpegAvailable(),
		"mode":    mode,
		"note":    note,
	}
}

type ffprobeOut struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
	Streams []struct {
		CodecType    string `json:"codec_type"`
		CodecName    string `json:"codec_name"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		Duration     string `json:"duration"`
		AvgFrameRate string `json:"avg_frame_rate"`
		Channels     int    `json:"channels"`
		SampleRate   string `json:"sample_rate"`
		Profile      string `json:"profile"`
		PixFmt       string `json:"pix_fmt"`
	} `json:"streams"`
}

// ProbeFile 探测单个文件；失败返回零值 Info（Source="none"），不伪造数值。
func ProbeFile(path, kind string, size int64, ext string) Info {
	switch kind {
	case "image":
		if info, ok := probeImage(path); ok {
			return info
		}
		return Info{Source: "none"}
	case "video", "audio":
		if FFprobeAvailable() {
			if info, ok := probeFFprobe(path, size); ok {
				return info
			}
		}
		if n := ProbeNative(path, ext, size); n != nil {
			return *n
		}
		return Info{Source: "none"}
	}
	return Info{Source: "none"}
}

func probeFFprobe(path string, size int64) (Info, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffprobePath, "-v", "quiet", "-print_format", "json",
		"-show_format", "-show_streams", "-of", "json", path)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return Info{}, false
	}
	var data ffprobeOut
	if err := json.Unmarshal(out.Bytes(), &data); err != nil {
		return Info{}, false
	}
	info := Info{Source: "ffprobe", Container: strings.Split(data.Format.FormatName, ",")[0]}
	dur := parseFloat(data.Format.Duration)
	for _, s := range data.Streams {
		if s.CodecType == "video" && info.VideoCodec == "" {
			info.Width, info.Height = s.Width, s.Height
			info.VideoCodec = s.CodecName
			if dur == 0 {
				dur = parseFloat(s.Duration)
			}
			if n, d, ok := strings.Cut(s.AvgFrameRate, "/"); ok {
				num, den := parseFloat(n), parseFloat(d)
				if den > 0 {
					info.FPS = num / den
				}
			}
			extra, _ := json.Marshal(map[string]any{"profile": s.Profile, "pix_fmt": s.PixFmt})
			info.ExtraJSON = string(extra)
		}
		if s.CodecType == "audio" && info.AudioCodec == "" {
			info.AudioCodec = s.CodecName
		}
	}
	info.DurationMS = int64(dur * 1000)
	info.Bitrate = int64(parseFloat(data.Format.BitRate))
	if info.Bitrate == 0 && dur > 0 && size > 0 {
		info.Bitrate = int64(float64(size) * 8 / dur)
	}
	if info.DurationMS == 0 && info.Width == 0 {
		return Info{}, false
	}
	return info, true
}

func parseFloat(s string) float64 {
	var f float64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%f", &f)
	if err != nil {
		return 0
	}
	return f
}

func probeImage(path string) (Info, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, false
	}
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return Info{}, false
	}
	info := Info{Width: cfg.Width, Height: cfg.Height, Container: strings.ToLower(format), Source: "native"}
	extra, _ := json.Marshal(map[string]any{"format": format})
	info.ExtraJSON = string(extra)
	info.ExifJSON = extractExif(path)
	return info, true
}

// extractExif 只读取 JPEG APP1 (Exif) 中的少量常用标签；解析失败返回空串。
func extractExif(path string) string {
	if !strings.EqualFold(filepath.Ext(path), ".jpg") && !strings.EqualFold(filepath.Ext(path), ".jpeg") {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return ""
	}
	var app1 []byte
	pos := 2
	for pos+4 <= len(data) {
		if data[pos] != 0xFF {
			break
		}
		marker := data[pos+1]
		if marker == 0xDA {
			break
		}
		segLen := int(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
		if segLen < 2 || pos+2+segLen > len(data) {
			break
		}
		if marker == 0xE1 && segLen > 8 && string(data[pos+4:pos+10]) == "Exif\x00\x00" {
			app1 = data[pos+10 : pos+2+segLen]
			break
		}
		pos += 2 + segLen
	}
	if len(app1) < 8 {
		return ""
	}
	var order binary.ByteOrder = binary.LittleEndian
	switch string(app1[0:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return ""
	}
	ifdOffset := int(order.Uint32(app1[4:8]))
	if ifdOffset+2 > len(app1) {
		return ""
	}
	names := map[uint16]string{
		271: "Make", 272: "Model", 305: "Software", 306: "DateTime",
		36867: "DateTimeOriginal", 33437: "FNumber", 34855: "ISOSpeedRatings",
	}
	out := map[string]any{}
	count := int(order.Uint16(app1[ifdOffset : ifdOffset+2]))
	for i := 0; i < count; i++ {
		e := ifdOffset + 2 + i*12
		if e+12 > len(app1) {
			break
		}
		tag := order.Uint16(app1[e : e+2])
		name, ok := names[tag]
		if !ok {
			continue
		}
		typ := order.Uint16(app1[e+2 : e+4])
		n := int(order.Uint32(app1[e+4 : e+8]))
		if typ == 2 && n > 0 && n <= 200 {
			start := e + 8
			if n > 4 {
				off := int(order.Uint32(app1[e+8 : e+12]))
				if off+n > len(app1) {
					continue
				}
				start = off
			}
			out[name] = strings.TrimRight(string(app1[start:start+n]), "\x00 ")
		}
	}
	if len(out) == 0 {
		return ""
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}
