//go:build linux

package scan

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// fileTimes 返回 (真实创建时间 btime, inode 变更时间 ctime, 设备号, inode)。
//
// Linux 的 syscall.Stat_t.Ctim 是「inode 变更时间」——chmod / chown / 改名 /
// 硬链接都会刷新它，并不是文件出生时间。真实创建时间只能通过 statx(2) 的
// STATX_BTIME 读取（内核 >= 4.11 且文件系统支持，ext4 / btrfs / xfs 通常支持）。
// 取不到时 btime 记 0（未知），绝不拿 ctime 冒充创建时间。
func fileTimes(info os.FileInfo, path string) (int64, int64, int64, int64) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0, 0
	}
	ctime := int64(st.Ctim.Sec)*1e9 + int64(st.Ctim.Nsec)
	dev, ino := int64(st.Dev), int64(st.Ino)

	var sx unix.Statx_t
	// AT_STATX_SYNC_AS_STAT 与 stat(2) 的同步语义一致，不强制回写元数据。
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_STATX_SYNC_AS_STAT,
		unix.STATX_BTIME, &sx); err != nil {
		return 0, ctime, dev, ino
	}
	if sx.Mask&unix.STATX_BTIME == 0 || sx.Btime.Sec <= 0 {
		return 0, ctime, dev, ino
	}
	btime := sx.Btime.Sec*1e9 + int64(sx.Btime.Nsec)
	if btime <= 0 {
		return 0, ctime, dev, ino
	}
	return btime, ctime, dev, ino
}
