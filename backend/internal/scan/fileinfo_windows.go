//go:build windows

package scan

import (
	"os"
	"syscall"
)

// fileTimes 返回 (真实创建时间 btime, inode 变更时间 ctime, 设备号, inode)。
// Windows 的 CreationTime 就是真实的出生时间；为兼容既有行为，ctime_ns 仍写入
// 同一值（Windows 没有 inode 变更时间概念）。Windows 无稳定 inode，dev/ino 记 0。
func fileTimes(info os.FileInfo, path string) (int64, int64, int64, int64) {
	if d, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		t := d.CreationTime.Nanoseconds()
		return t, t, 0, 0
	}
	return 0, 0, 0, 0
}
