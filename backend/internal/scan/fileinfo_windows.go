//go:build windows

package scan

import (
	"os"
	"syscall"
)

// fileTimes 返回 (创建时间纳秒, 设备号, inode)。Windows 无稳定 inode，返回 0。
func fileTimes(info os.FileInfo) (int64, int64, int64) {
	if d, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return d.CreationTime.Nanoseconds(), 0, 0
	}
	return 0, 0, 0
}
