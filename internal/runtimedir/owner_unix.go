//go:build unix

package runtimedir

import (
	"os"
	"syscall"
)

func ownerUID(info os.FileInfo) (int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(stat.Uid), true
}
