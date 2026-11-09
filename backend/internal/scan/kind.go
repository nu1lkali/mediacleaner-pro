// Package scan 实现文件索引与五阶段重复检测流水线。
package scan

import (
	"path/filepath"
	"strings"
)

const (
	KindImage = "image"
	KindVideo = "video"
	KindAudio = "audio"
	KindDoc   = "document"
	KindOther = "other"
)

// extKind 是扩展名 → 媒体类别映射。未命中的扩展名归为 other，默认不入库（可配置）。
var extKind = map[string]string{
	// 图片
	".jpg": KindImage, ".jpeg": KindImage, ".png": KindImage, ".gif": KindImage,
	".bmp": KindImage, ".webp": KindImage, ".tif": KindImage, ".tiff": KindImage,
	".heic": KindImage, ".heif": KindImage, ".avif": KindImage, ".jxl": KindImage,
	".cr2": KindImage, ".cr3": KindImage, ".nef": KindImage, ".arw": KindImage,
	".dng": KindImage, ".raf": KindImage, ".orf": KindImage, ".rw2": KindImage,
	".psd": KindImage, ".svg": KindImage, ".ico": KindImage,
	// 视频
	".mp4": KindVideo, ".m4v": KindVideo, ".mkv": KindVideo, ".avi": KindVideo,
	".mov": KindVideo, ".wmv": KindVideo, ".flv": KindVideo, ".f4v": KindVideo,
	".ts": KindVideo, ".m2ts": KindVideo, ".mts": KindVideo, ".webm": KindVideo,
	".mpg": KindVideo, ".mpeg": KindVideo, ".rmvb": KindVideo, ".rm": KindVideo,
	".3gp": KindVideo, ".3g2": KindVideo, ".vob": KindVideo, ".asf": KindVideo,
	// 音频
	".mp3": KindAudio, ".flac": KindAudio, ".wav": KindAudio, ".aac": KindAudio,
	".ogg": KindAudio, ".oga": KindAudio, ".m4a": KindAudio, ".wma": KindAudio,
	".ape": KindAudio, ".dsf": KindAudio, ".dff": KindAudio, ".aiff": KindAudio,
	".opus": KindAudio, ".amr": KindAudio,
	// 文档
	".pdf": KindDoc, ".doc": KindDoc, ".docx": KindDoc, ".xls": KindDoc,
	".xlsx": KindDoc, ".ppt": KindDoc, ".pptx": KindDoc, ".txt": KindDoc,
	".md": KindDoc, ".rtf": KindDoc, ".epub": KindDoc, ".mobi": KindDoc,
	".csv": KindDoc, ".json": KindDoc, ".xml": KindDoc, ".html": KindDoc,
	".htm": KindDoc, ".odt": KindDoc, ".ods": KindDoc, ".odp": KindDoc,
}

// ExtOf 返回小写扩展名（含点）。
func ExtOf(name string) string {
	return strings.ToLower(filepath.Ext(name))
}

// KindOf 根据文件名判定媒体类别。
func KindOf(name string) string {
	if k, ok := extKind[ExtOf(name)]; ok {
		return k
	}
	return KindOther
}

// IsMedia 判断是否属于需要检测的媒体类别。
func IsMedia(name string) bool { return KindOf(name) != KindOther }

// StemOf 返回去扩展名的文件名，用于"同名不同后缀"规则匹配。
func StemOf(name string) string {
	ext := filepath.Ext(name)
	if ext == "" {
		return name
	}
	return strings.TrimSuffix(name, ext)
}

// skipDirs 是默认跳过的目录名集合：群晖缩略图/回收站、系统目录、开发产物。
//
// 挂载宿主机根目录（/ → /host）后，扫描范围可能覆盖到系统目录，这里把
// proc/sys/dev 等伪文件系统与程序自身的数据目录一并挡掉，避免无意义的遍历。
var skipDirs = map[string]bool{
	"@eadir": true, "#recycle": true, "#snapshot": true, "@tmp": true,
	"@appstore": true, "@database": true, "@synologydrive": true,
	"$recycle.bin": true, "system volume information": true,
	".trash": true, ".trashes": true, ".spotlight-v100": true, ".fseventsd": true,
	"lost+found": true, "node_modules": true, ".git": true, ".svn": true,
	"__pycache__": true, ".venv": true, "venv": true,
	// 伪文件系统与系统目录
	"proc": true, "sys": true, "dev": true, "run": true, "boot": true,
	// 程序自身的数据目录
	"mediacleaner-data": true,
}

// ShouldSkipDir 判断目录是否应跳过（群晖系统目录一律不进）。
func ShouldSkipDir(name string) bool {
	return skipDirs[strings.ToLower(name)]
}

// Categories 返回所有类别名，用于前端筛选。
func Categories() []string {
	return []string{KindImage, KindVideo, KindAudio, KindDoc, KindOther}
}
