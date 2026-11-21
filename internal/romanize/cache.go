package romanize

import (
	"fmt"
	"os"

	"github.com/ikawaha/kagome-dict/ipa"
)

// CacheVersion identifies the serialized dictionary layout; mismatches trigger
// a rebuild.
const CacheVersion = "ipa-2.7.0-mmap1"

// WriteCache atomically compiles [ipa.Dict] into a flat mmap-ready file at path
// so later runs can [Open] it without loading IPADIC tables onto the Go heap.
func WriteCache(path string) error {
	return writeMmap(ipa.Dict(), path)
}

// CacheStampPath returns the sidecar version file path beside the cache.
func CacheStampPath(path string) string {
	return path + ".version"
}

// CacheValid reports whether path is non-empty and its stamp matches [CacheVersion].
func CacheValid(path string) bool {
	stamp, err := os.ReadFile(CacheStampPath(path))
	if err != nil || string(stamp) != CacheVersion {
		return false
	}
	info, err := os.Stat(path)

	return err == nil && info.Size() > 0
}

// RecordCacheStamp writes [CacheVersion] to [CacheStampPath].
func RecordCacheStamp(path string) error {
	if err := os.WriteFile(CacheStampPath(path), []byte(CacheVersion), 0o644); err != nil {
		return fmt.Errorf("romanize: cache stamp: %w", err)
	}

	return nil
}
