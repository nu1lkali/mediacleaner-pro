//go:build !windows

package scan

import (
	"os"
	"syscall"
)

// fileTimes 返回 (创建时间纳秒, 设备号, inode)。非 Linux/BSD 平台 Stat_t 无 Ctim 时回落为 0。
func fileTimes(info os.FileInfo) (int64, int64, int64) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0
	}
	return int64(st.Ctim.Sec)*1e9 + int64(st.Ctim.Nsec), int64(st.Dev), int64(st.Ino)
}
