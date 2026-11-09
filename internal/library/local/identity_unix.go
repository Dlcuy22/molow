//go:build unix

package local

import (
	"io/fs"
	"syscall"
)

// statIdentity reads the filesystem identity where the platform exposes one.
func statIdentity(info fs.FileInfo) (dev, ino uint64) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, 0
	}

	return uint64(st.Dev), uint64(st.Ino)
}
