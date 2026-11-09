package media

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	xdraw "golang.org/x/image/draw"

	"mediacleaner/internal/config"
)

// ThumbUnavailable 表示无法生成缩略图（附明确原因），调用方据此返回 available=false。
type ThumbUnavailable struct {
	Code   string
	Reason string
}

func (e *ThumbUnavailable) Error() string { return e.Reason }

func thumbPath(cfg *config.Config, id, mtimeNS int64, width int) string {
	key := fmt.Sprintf("%d_%d_%d", id, mtimeNS, width)
	dir := filepath.Join(cfg.ThumbDir(), key[:2], key[2:4])
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, key+".jpg")
}

// Thumb 生成并缓存缩略图。缓存键含 mtime，文件变化自动失效。
func Thumb(cfg *config.Config, kind, src string, id, mtimeNS int64, width int, durationMS int64) (string, error) {
	if width <= 0 {
		width = cfg.ThumbSize
	}
	dest := thumbPath(cfg, id, mtimeNS, width)
	if st, err := os.Stat(dest); err == nil && st.Size() > 0 {
		return dest, nil
	}
	switch kind {
	case "image":
		return dest, imageThumb(src, dest, width, cfg.ThumbQuality)
	case "video":
		if !FFmpegAvailable() {
			return "", &ThumbUnavailable{Code: "no_ffmpeg", Reason: "未安装 FFmpeg，无法生成视频缩略图。生产镜像已内置 ffmpeg。"}
		}
		ts := 1
		if durationMS > 0 {
			ts = int(durationMS / 1000 / 10)
		}
		tmp := dest + ".tmp.jpg"
		cmd := exec.Command(FFmpegPath(), "-hide_banner", "-loglevel", "error", "-ss", strconv.Itoa(ts),
			"-i", src, "-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:-1", width), "-y", tmp)
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = os.Remove(tmp)
			return "", &ThumbUnavailable{Code: "ffmpeg_failed", Reason: "FFmpeg 抽帧失败：" + strings.TrimSpace(string(out))}
		}
		if err := os.Rename(tmp, dest); err != nil {
			return "", &ThumbUnavailable{Code: "ffmpeg_failed", Reason: "缩略图写入失败：" + err.Error()}
		}
		return dest, nil
	case "audio":
		return dest, placeholderThumb(dest, width)
	default:
		return "", &ThumbUnavailable{Code: "unsupported_kind", Reason: "该文件类型没有缩略图"}
	}
}

func imageThumb(src, dest string, width, quality int) error {
	f, err := os.Open(src)
	if err != nil {
		return &ThumbUnavailable{Code: "unreadable", Reason: "无法打开图片：" + err.Error()}
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return &ThumbUnavailable{Code: "decode_failed", Reason: "图片解码失败：" + err.Error()}
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := float64(width) / float64(w)
	if scale > 1 {
		scale = 1
	}
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 || nh < 1 {
		return errors.New("图片尺寸异常")
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)

	tmp := dest + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(out, dst, &jpeg.Options{Quality: quality}); err != nil {
		out.Close()
		return err
	}
	out.Close()
	return os.Rename(tmp, dest)
}

func placeholderThumb(dest string, width int) error {
	img := image.NewRGBA(image.Rect(0, 0, width, width))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{32, 34, 40, 255}}, image.Point{}, draw.Src)
	drawCircle(img, width/2, width/2, width/3, color.RGBA{120, 124, 140, 255})
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	return jpeg.Encode(out, img, &jpeg.Options{Quality: 80})
}

func drawCircle(img *image.RGBA, cx, cy, r int, c color.Color) {
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			dx, dy := x-cx, y-cy
			d := dx*dx + dy*dy
			if d <= r*r && d >= (r-4)*(r-4) && x >= 0 && y >= 0 && x < img.Bounds().Dx() && y < img.Bounds().Dy() {
				img.Set(x, y, c)
			}
		}
	}
}
