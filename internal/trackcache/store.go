// Package trackcache stores streamed online audio tracks on disk under a
// size-bounded LRU. Each track is kept as a raw <key>.webm file; filesystem
// mtime is the source of truth across restarts and an in-memory refcount keeps
// actively playing files out of eviction on Windows.
package trackcache

import (
	"container/list"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMaxBytes is the default disk budget for cached tracks (512 MiB).
	DefaultMaxBytes int64 = 512 << 20

	fallbackSubdir = "molow-tracks"
	trackExt       = ".webm"
	mtimeThrottle  = time.Minute
)

// Stats reports the current size and capacity of a [Store].
type Stats struct {
	Count      int
	TotalBytes int64
	MaxBytes   int64
	Dir        string
}

type entry struct {
	key       string
	size      int64
	mtime     time.Time
	lastTouch time.Time
}

// Store owns the on-disk track cache directory and its in-memory LRU index.
// All methods are safe for concurrent use; a nil *Store behaves as a disabled
// cache that always misses and ignores writes.
type Store struct {
	dir string

	mu         sync.Mutex
	maxBytes   int64
	totalBytes int64
	order      *list.List // front = MRU, back = LRU
	items      map[string]*list.Element
	inUse      map[string]int

	now func() time.Time
}

// DefaultDir returns the platform track cache directory, creating it on first
// use. Every platform uses <UserCacheDir>/molow/tracks and falls back to the
// temp directory only when the cache root is unavailable.
func DefaultDir() (string, error) {
	dir := fallbackDir()
	if base, err := os.UserCacheDir(); err == nil && base != "" {
		dir = filepath.Join(base, "molow", "tracks")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	return dir, nil
}

func fallbackDir() string {
	base := os.TempDir()
	if base == "" {
		base = "."
	}

	return filepath.Join(base, fallbackSubdir)
}

// Open returns a [Store] rooted at dir with budget maxBytes, creating dir when
// needed. When dir is empty, [DefaultDir] is used. Stale temp files are swept
// and any excess entries are evicted oldest-first before returning.
func Open(dir string, maxBytes int64) (*Store, error) {
	if dir == "" {
		var err error
		dir, err = DefaultDir()
		if err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if maxBytes < 0 {
		maxBytes = 0
	}

	s := &Store{
		dir:      dir,
		maxBytes: maxBytes,
		order:    list.New(),
		items:    make(map[string]*list.Element),
		inUse:    make(map[string]int),
		now:      time.Now,
	}

	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("trackcache: read dir: %w", err)
	}

	var found []entry
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		full := filepath.Join(dir, name)
		if strings.Contains(name, ".tmp") {
			_ = os.Remove(full)
			continue
		}
		if !strings.HasSuffix(name, trackExt) {
			continue
		}
		key := strings.TrimSuffix(name, trackExt)
		if !safeKey(key) {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		if info.Size() <= 0 {
			_ = os.Remove(full)
			continue
		}
		found = append(found, entry{
			key:   key,
			size:  info.Size(),
			mtime: info.ModTime(),
		})
	}

	// Sort oldest-first so PushFront puts the oldest entry at Back (LRU tail)
	// and the newest entry at Front (MRU head).
	sort.Slice(found, func(i, j int) bool {
		if found[i].mtime.Equal(found[j].mtime) {
			return found[i].key < found[j].key
		}

		return found[i].mtime.Before(found[j].mtime)
	})

	for i := range found {
		ent := found[i]
		elem := s.order.PushFront(&ent)
		s.items[ent.key] = elem
		s.totalBytes += ent.size
	}
	s.evictLocked()

	return s, nil
}

func safeKey(key string) bool {
	return key != "" &&
		!strings.HasPrefix(key, ".") &&
		!strings.ContainsAny(key, `/\.:*?"<>|`)
}

func (s *Store) path(key string) string {
	return filepath.Join(s.dir, key+trackExt)
}

// Has reports whether key is currently tracked in the cache index. It does not
// update LRU order or open the file.
func (s *Store) Has(key string) bool {
	if s == nil || !safeKey(key) {
		return false
	}
	s.mu.Lock()
	_, ok := s.items[key]
	s.mu.Unlock()

	return ok
}

// Get opens the cached track for key and returns a seekable reader, its byte
// size, and true. Calling Close on the returned reader releases the in-use pin
// so the entry can be evicted again when space is needed.
func (s *Store) Get(key string) (io.ReadSeekCloser, int64, bool) {
	if s == nil || !safeKey(key) {
		return nil, 0, false
	}

	s.mu.Lock()
	elem, ok := s.items[key]
	if !ok {
		s.mu.Unlock()

		return nil, 0, false
	}
	ent := elem.Value.(*entry)
	now := s.now()
	touch := ent.lastTouch.IsZero() || now.Sub(ent.lastTouch) >= mtimeThrottle
	if touch {
		ent.lastTouch = now
		ent.mtime = now
	}
	s.order.MoveToFront(elem)
	// Pin before dropping the lock so a concurrent eviction cannot unlink the
	// file before os.Open runs.
	s.inUse[key]++
	size := ent.size
	p := s.path(key)
	s.mu.Unlock()

	f, err := os.Open(p)
	if err != nil {
		s.mu.Lock()
		s.releaseLocked(key)
		if errors.Is(err, os.ErrNotExist) {
			if cur, exists := s.items[key]; exists {
				s.removeElemLocked(cur)
			}
		}
		s.mu.Unlock()

		return nil, 0, false
	}

	if touch {
		_ = os.Chtimes(p, now, now)
	}
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		size = info.Size()
	}

	return &trackedFile{File: f, store: s, key: key}, size, true
}

// Put writes data atomically under key, updates the LRU index, and evicts the
// oldest unused entries until total bytes fit within the budget. Empty data,
// disabled caches (maxBytes <= 0), oversized tracks (> maxBytes), and keys
// currently held open by a reader are skipped without error.
func (s *Store) Put(key string, data []byte) error {
	if s == nil || !safeKey(key) || len(data) == 0 {
		return nil
	}

	size := int64(len(data))
	s.mu.Lock()
	if s.maxBytes <= 0 || size > s.maxBytes || s.inUse[key] > 0 {
		s.mu.Unlock()

		return nil
	}
	s.mu.Unlock()

	tmp, err := os.CreateTemp(s.dir, key+".tmp*")
	if err != nil {
		return fmt.Errorf("trackcache: temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("trackcache: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("trackcache: close: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Re-check budget and pin under the lock in case SetMaxBytes or Get raced
	// with the temp write; on Windows os.Rename fails over an open file.
	if s.maxBytes <= 0 || size > s.maxBytes || s.inUse[key] > 0 {
		return nil
	}
	if err := os.Rename(tmpName, s.path(key)); err != nil {
		return fmt.Errorf("trackcache: commit: %w", err)
	}
	cleanup = false

	now := s.now()
	if elem, ok := s.items[key]; ok {
		ent := elem.Value.(*entry)
		s.totalBytes -= ent.size
		ent.size = size
		ent.mtime = now
		ent.lastTouch = now
		s.totalBytes += size
		s.order.MoveToFront(elem)
	} else {
		elem := s.order.PushFront(&entry{
			key:       key,
			size:      size,
			mtime:     now,
			lastTouch: now,
		})
		s.items[key] = elem
		s.totalBytes += size
	}
	s.evictLocked()

	return nil
}

// SetMaxBytes updates the cache byte budget and immediately evicts unused
// entries until the new limit is met. A value <= 0 disables caching and evicts
// every unused file.
func (s *Store) SetMaxBytes(maxBytes int64) error {
	if s == nil {
		return nil
	}
	if maxBytes < 0 {
		maxBytes = 0
	}
	s.mu.Lock()
	s.maxBytes = maxBytes
	s.evictLocked()
	s.mu.Unlock()

	return nil
}

// Clear deletes every cached track file that is not currently open.
func (s *Store) Clear() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var firstErr error
	for elem := s.order.Back(); elem != nil; {
		prev := elem.Prev()
		ent := elem.Value.(*entry)
		if s.inUse[ent.key] > 0 {
			elem = prev
			continue
		}
		err := os.Remove(s.path(ent.key))
		if err == nil || errors.Is(err, os.ErrNotExist) {
			s.removeElemLocked(elem)
		} else if firstErr == nil {
			firstErr = err
		}
		elem = prev
	}

	return firstErr
}

// Stats returns a snapshot of the store's count, size, budget, and directory.
func (s *Store) Stats() Stats {
	if s == nil {
		return Stats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return Stats{
		Count:      len(s.items),
		TotalBytes: s.totalBytes,
		MaxBytes:   s.maxBytes,
		Dir:        s.dir,
	}
}

func (s *Store) evictLocked() {
	for elem := s.order.Back(); elem != nil && s.totalBytes > s.maxBytes; {
		prev := elem.Prev()
		ent := elem.Value.(*entry)
		if s.inUse[ent.key] > 0 {
			elem = prev
			continue
		}
		err := os.Remove(s.path(ent.key))
		if err == nil || errors.Is(err, os.ErrNotExist) {
			s.removeElemLocked(elem)
		}
		elem = prev
	}
}

func (s *Store) removeElemLocked(elem *list.Element) {
	ent := elem.Value.(*entry)
	s.order.Remove(elem)
	delete(s.items, ent.key)
	s.totalBytes -= ent.size
	if s.totalBytes < 0 {
		s.totalBytes = 0
	}
}

func (s *Store) releaseLocked(key string) {
	if n := s.inUse[key]; n <= 1 {
		delete(s.inUse, key)
	} else {
		s.inUse[key] = n - 1
	}
}

type trackedFile struct {
	*os.File
	store *Store
	key   string
	once  sync.Once
}

func (f *trackedFile) Close() error {
	var err error
	f.once.Do(func() {
		// Release the OS handle before unpinning so Windows permits eviction to
		// remove the file immediately if the cache is over budget.
		err = f.File.Close()
		f.store.mu.Lock()
		f.store.releaseLocked(f.key)
		if f.store.totalBytes > f.store.maxBytes {
			f.store.evictLocked()
		}
		f.store.mu.Unlock()
	})

	return err
}
