package local

import (
	"io/fs"
	"strconv"

	"github.com/dlcuy22/molow/internal/library/sqlite"
)

// file turns a directory entry into the identity the store compares.
func file(path string, info fs.FileInfo) sqlite.File {
	dev, ino := statIdentity(info)

	return sqlite.File{
		Path:  path,
		Dev:   dev,
		Ino:   ino,
		Size:  info.Size(),
		Mtime: info.ModTime().UnixNano(),
	}
}

// nativeID is stable while the file does not change. A filesystem with real
// inode numbers gives a move-proof key; the fallback is still stable for a
// file that is only copied.
func nativeID(f sqlite.File) string {
	if f.Dev != 0 || f.Ino != 0 {
		return "id:" + strconv.FormatUint(f.Dev, 10) + ":" + strconv.FormatUint(f.Ino, 10)
	}

	return "sm:" + strconv.FormatInt(f.Size, 10) + ":" + strconv.FormatInt(f.Mtime, 10)
}
