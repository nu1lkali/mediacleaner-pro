//go:build !windows && !linux

package scan

import (
	"os"
	"syscall"
)

// fileTimes 返回 (真实创建时间 btime, inode 变更时间 ctime, 设备号, inode)。
//
// 非 Linux/Windows 平台没有统一的出生时间接口，且各 BSD 的 Stat_t 时间字段
// 命名不一致；这里一律把 btime/ctime 记为 0（未知），避免把任何近似值误当作
// 创建时间。dev/inode 仍保留用于「改名后继承指纹」。
func fileTimes(info os.FileInfo, path string) (int64, int64, int64, int64) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0, 0
	}
	return 0, 0, int64(st.Dev), int64(st.Ino)
}
