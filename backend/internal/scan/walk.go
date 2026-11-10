package scan

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Entry 是遍历产出的单个文件条目，字段与 files 表对齐。
type Entry struct {
	RootID    int64
	Path      string
	DirPath   string
	Name      string
	Stem      string
	Ext       string
	Size      int64
	MtimeNS   int64
	CtimeNS   int64
	BtimeNS   int64
	Dev       int64
	Inode     int64
	IsSymlink bool
	Kind      string
}

// WalkOptions 控制遍历行为。
type WalkOptions struct {
	MaxDepth       int      // 0 表示不限
	MinSize        int64    // 小于该字节数的媒体文件跳过（文档除外）
	OnlyKinds      []string // 为空表示不限
	IncludeOther   bool     // 是否收录非媒体文件
	FollowSymlinks bool
	ExcludeDirs    []string // 目录绝对路径前缀，命中即整棵子树跳过
	ExcludeNames   []string // 文件名通配（* ? 支持的简单 glob），命中即跳过
	OnDir          func(dir string)
	OnError        func(path string, err error)
}

// globMatch 是简化的通配匹配：支持 * 与 ?，大小写不敏感。
// 不引入 path/filepath.Match 是为了避免 Windows 下分隔符干扰文件名匹配。
func globMatch(pattern, name string) bool {
	pattern = strings.ToLower(pattern)
	name = strings.ToLower(name)
	// 经典双指针回溯匹配
	var (
		p, n         int
		starIdx      = -1
		matchIdx     = 0
	)
	for n < len(name) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == name[n]):
			p++
			n++
		case p < len(pattern) && pattern[p] == '*':
			starIdx = p
			matchIdx = n
			p++
		case starIdx >= 0:
			p = starIdx + 1
			matchIdx++
			n = matchIdx
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

type kindSet map[string]bool

func (o *WalkOptions) kinds() kindSet {
	if len(o.OnlyKinds) == 0 {
		return nil
	}
	s := make(kindSet, len(o.OnlyKinds))
	for _, k := range o.OnlyKinds {
		s[k] = true
	}
	return s
}

// Walk 深度遍历 root，对每个符合条件的文件回调 emit。
//
// 安全性：不跟随符号链接（默认），跳过群晖系统目录，路径始终由 filepath.Join 拼装，
// 越界（如 ".."）不会出现在 ReadDir 返回项中，因此无需额外路径校验。
// 上下文取消时立即返回 ctx.Err()。
func Walk(ctx context.Context, rootID int64, root string, opt WalkOptions, emit func(Entry) error) error {
	want := opt.kinds()
	err := walkDir(ctx, rootID, root, root, 0, opt, want, emit)
	if err != nil && errors.Is(err, context.Canceled) {
		return err
	}
	return err
}

func walkDir(ctx context.Context, rootID int64, root, dir string, depth int, opt WalkOptions, want kindSet, emit func(Entry) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if opt.MaxDepth > 0 && depth > opt.MaxDepth {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if opt.OnError != nil {
			opt.OnError(dir, err)
		}
		return nil // 单个目录不可读不应中断整次扫描
	}
	if opt.OnDir != nil {
		opt.OnDir(dir)
	}
	for _, de := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := de.Name()
		full := filepath.Join(dir, name)

		mode := de.Type()
		isLink := mode&fs.ModeSymlink != 0

		if de.IsDir() {
			if ShouldSkipDir(name) {
				continue
			}
			if strings.HasPrefix(name, ".") && name != "." {
				continue // 隐藏目录默认跳过
			}
			if excludedDir(full, opt.ExcludeDirs) {
				continue
			}
			if err := walkDir(ctx, rootID, root, full, depth+1, opt, want, emit); err != nil {
				return err
			}
			continue
		}

		if isLink {
			if !opt.FollowSymlinks {
				continue // 不跟随符号链接，避免环路与越权
			}
			if st, err := os.Stat(full); err != nil || !st.Mode().IsRegular() {
				continue
			}
		} else if !mode.IsRegular() {
			continue // FIFO / 设备文件等一律忽略
		}

		info, err := de.Info()
		if err != nil {
			if opt.OnError != nil {
				opt.OnError(full, err)
			}
			continue
		}
		if !info.Mode().IsRegular() && !opt.FollowSymlinks {
			continue
		}

		kind := KindOf(name)
		if kind == KindOther && !opt.IncludeOther {
			continue
		}
		if want != nil && !want[kind] {
			continue
		}
		if excludedName(name, opt.ExcludeNames) {
			continue
		}
		size := info.Size()
		if kind != KindDoc && kind != KindOther && size < opt.MinSize {
			continue
		}
		if size == 0 {
			continue
		}

		btime, ctime, dev, inode := fileTimes(info, full)
		e := Entry{
			RootID:    rootID,
			Path:      full,
			DirPath:   dir,
			Name:      name,
			Stem:      StemOf(name),
			Ext:       ExtOf(name),
			Size:      size,
			MtimeNS:   info.ModTime().UnixNano(),
			CtimeNS:   ctime,
			BtimeNS:   btime,
			Dev:       dev,
			Inode:     inode,
			IsSymlink: isLink,
			Kind:      kind,
		}
		if err := emit(e); err != nil {
			return err
		}
	}
	return nil
}

func excludedDir(path string, prefixes []string) bool {
	for _, p := range prefixes {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(path, filepath.Clean(p)) {
			return true
		}
	}
	return false
}

func excludedName(name string, patterns []string) bool {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == name || globMatch(p, name) {
			return true
		}
	}
	return false
}

// NormalizeRoot 把用户输入的路径规范化为绝对、无尾斜杠的形式，并解析符号链接。
func NormalizeRoot(p string) (string, string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", "", errors.New("路径不能为空")
	}
	if !filepath.IsAbs(p) {
		return "", "", errors.New("必须是绝对路径")
	}
	clean := filepath.Clean(p)
	st, err := os.Stat(clean)
	if err != nil {
		return "", "", err
	}
	if !st.IsDir() {
		return "", "", errors.New("路径不是目录")
	}
	real := clean
	if r, err := filepath.EvalSymlinks(clean); err == nil {
		real = r
	}
	return clean, real, nil
}

// VolumeOf 返回挂载卷标识，用于跨卷去重提示。
//
// Linux 下取路径前缀：/volume1、/mnt/usb 等；Windows 下取盘符。
func VolumeOf(p string) string {
	clean := filepath.Clean(p)
	if v := filepath.VolumeName(clean); v != "" {
		return strings.ToUpper(v) // C:
	}
	parts := strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "" {
		return "/"
	}
	if strings.HasPrefix(parts[0], "volume") || parts[0] == "mnt" || parts[0] == "media" {
		if parts[0] == "mnt" || parts[0] == "media" {
			if len(parts) > 1 {
				return "/" + parts[0] + "/" + parts[1]
			}
		}
		return "/" + parts[0]
	}
	return "/" + parts[0]
}

// FormatSize 供日志与消息使用。
func FormatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return itoa(n) + " B"
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	whole := n / div
	frac := (n % div) * 10 / div
	return itoa(whole) + "." + itoa(frac) + " " + units[exp]
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
