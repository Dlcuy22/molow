//go:build unix

package artcache

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDirFollowsXDGCacheOnLinux pins the platform rule: the cache lives under
// the XDG cache root, not the temp directory. Windows resolves Dir to the
// temporary directory by contract, so the pin is UNIX-only.
func TestDirFollowsXDGCacheOnLinux(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)

	dir, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	want := filepath.Join(root, "molo", "art")
	if dir != want {
		t.Fatalf("Dir = %q, want %q", dir, want)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("Dir did not create the directory: %v", err)
	}
}
