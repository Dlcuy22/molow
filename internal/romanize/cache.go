package romanize

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ikawaha/kagome-dict/ipa"
)

// CacheVersion identifies the serialized dictionary layout; mismatches trigger
// a rebuild.
const CacheVersion = "ipa-2.7.0-kagome1"

// WriteCache atomically writes [ipa.Dict] to path so later runs can [Open] an
// owned copy instead of pinning the embedded dictionary.
func WriteCache(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("romanize: cache dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("romanize: cache file: %w", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()

	zw := zip.NewWriter(tmp)
	if err := ipa.Dict().Save(zw); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("romanize: write cache: %w", err)
	}
	if err := zw.Close(); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("romanize: write cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("romanize: cache file: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("romanize: commit cache: %w", err)
	}

	return nil
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
