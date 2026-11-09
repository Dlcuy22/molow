//go:build !unix

package local

import "io/fs"

// statIdentity has no portable answer on this platform, so the caller falls
// back to the size and mtime identity.
func statIdentity(fs.FileInfo) (dev, ino uint64) { return 0, 0 }
