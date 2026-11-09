package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// FramePositions 是抽帧的相对位置（避开片头片尾的 logo/黑场）。
var FramePositions = []float64{0.10, 0.30, 0.50, 0.70, 0.90}

// VideoFrames 用 ffmpeg 在若干时间点抽单帧，再对帧做 pHash。
//
// 这是"相似但不相同"视频检测的核心：完全相同的视频走 content_hash，
// 转码/裁边/换音轨的同一内容才会落到这里。
// 任何一帧抽取失败都跳过该帧而非整体失败；全部失败则返回错误。
func VideoFrames(ffmpeg, path string, durationMS int64, tmpDir string) (VideoFingerprint, error) {
	if ffmpeg == "" {
		return VideoFingerprint{}, fmt.Errorf("ffmpeg 不可用")
	}
	if durationMS <= 0 {
		return VideoFingerprint{}, fmt.Errorf("时长未知")
	}
	dir, err := os.MkdirTemp(tmpDir, "frames-")
	if err != nil {
		return VideoFingerprint{}, err
	}
	defer os.RemoveAll(dir)

	out := VideoFingerprint{Frames: make([]Frame, 0, len(FramePositions))}
	var lastErr error
	for i, pos := range FramePositions {
		ts := int64(float64(durationMS) * pos)
		if ts < 0 {
			ts = 0
		}
		dest := filepath.Join(dir, fmt.Sprintf("f%d.png", i))
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		args := []string{
			"-hide_banner", "-loglevel", "error", "-nostdin",
			"-ss", fmt.Sprintf("%.3f", float64(ts)/1000.0),
			"-i", path,
			"-frames:v", "1",
			"-vf", "scale=160:-2",
			"-y", dest,
		}
		cmd := exec.CommandContext(ctx, ffmpeg, args...)
		cmd.Stdout = nil
		cmd.Stderr = nil
		err := cmd.Run()
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		data, err := os.ReadFile(dest)
		if err != nil || len(data) == 0 {
			lastErr = err
			continue
		}
		h, ok := HashBytes(data)
		if !ok {
			lastErr = fmt.Errorf("帧解码失败")
			continue
		}
		out.Frames = append(out.Frames, Frame{TS: ts, Hash: h})
	}
	if len(out.Frames) == 0 {
		if lastErr == nil {
			lastErr = fmt.Errorf("未抽到任何帧")
		}
		return VideoFingerprint{}, lastErr
	}
	return out, nil
}

// ParseFrames 反序列化 phash_frames 字段。
func ParseFrames(raw string) (VideoFingerprint, bool) {
	if strings.TrimSpace(raw) == "" {
		return VideoFingerprint{}, false
	}
	var fp VideoFingerprint
	if err := json.Unmarshal([]byte(raw), &fp); err != nil || len(fp.Frames) == 0 {
		return VideoFingerprint{}, false
	}
	return fp, true
}
