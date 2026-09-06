//go:build linux

package helperipc

import (
	"io/fs"
	"syscall"
)

func dirOwnerUID(info fs.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}
