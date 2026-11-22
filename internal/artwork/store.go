package artwork

import (
	"container/list"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/dlcuy22/molow/internal/color"
)

const (
	subdir   = "molow-art"
	cacheMax = 64
)

// DefaultDir returns the platform artwork cache directory, creating it on first
// use. Every platform uses <UserCacheDir>/molow/art and falls back to the temp
// directory only when the cache root is unavailable.
func DefaultDir() (string, error) {
	dir := fallbackDir()
	if base, err := os.UserCacheDir(); err == nil && base != "" {
		dir = filepath.Join(base, "molow", "art")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		legacy := fallbackDir()
		if !strings.EqualFold(filepath.Clean(legacy), filepath.Clean(dir)) {
			migrateDir(legacy, dir)
		}
	}

	return dir, nil
}

func fallbackDir() string {
	base := os.TempDir()
	if base == "" {
		base = "."
	}

	return filepath.Join(base, subdir)
}

// migrateDir moves cached .jpg and .png files from a legacy temp directory into
// to when to is still empty, so previously imported covers stay intact after
// switching to UserCacheDir. Failures on individual files are skipped so a
// locked temp file never prevents startup.
func migrateDir(from, to string) {
	existing, err := os.ReadDir(to)
	if err != nil || len(existing) > 0 {
		return
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".jpg" && ext != ".png" {
			continue
		}
		src := filepath.Join(from, name)
		dst := filepath.Join(to, name)
		if err := os.Rename(src, dst); err != nil {
			if err := copyAndRemove(src, dst); err != nil {
				continue
			}
		}
	}
}

func copyAndRemove(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".migrate-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return err
	}
	_ = in.Close()
	_ = os.Remove(src)

	return nil
}

// Store owns the canonical on-disk artwork cache, singleflight ingest, and
// bounded in-memory LRUs for data URLs and accent palettes.
type Store struct {
	dir string

	putMu       sync.Mutex
	putInflight map[string]*putCall

	urlMu  sync.Mutex
	urlLRU *lruCache[string, string]

	accentMu       sync.Mutex
	accentLRU      *lruCache[accentKey, []color.Colour]
	accentInflight map[accentKey]*accentCall
}

type putCall struct {
	done chan struct{}
	err  error
}

type accentKey struct {
	id string
	n  int
}

type accentCall struct {
	done   chan struct{}
	colors []color.Colour
	err    error
}

// Open returns a [Store] rooted at dir, creating the directory if needed. When
// dir is empty, [DefaultDir] is used.
func Open(dir string) (*Store, error) {
	if dir == "" {
		var err error
		dir, err = DefaultDir()
		if err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	return &Store{
		dir:            dir,
		putInflight:    make(map[string]*putCall),
		urlLRU:         newLRU[string, string](cacheMax),
		accentLRU:      newLRU[accentKey, []color.Colour](cacheMax),
		accentInflight: make(map[accentKey]*accentCall),
	}, nil
}

// Dir returns the cache directory path.
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}

	return s.dir
}

func safeID(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\.`)
}

func (s *Store) fileFor(id string) (path, mime string, ok bool) {
	if s == nil || !safeID(id) {
		return "", "", false
	}
	for _, cand := range [2]struct {
		ext  string
		mime string
	}{
		{".jpg", "image/jpeg"},
		{".png", "image/png"},
	} {
		p := filepath.Join(s.dir, id+cand.ext)
		if info, err := os.Stat(p); err == nil && info.Size() > 0 {
			return p, cand.mime, true
		}
	}

	return "", "", false
}

// Has reports whether a normalized artwork file exists on disk for id.
func (s *Store) Has(id string) bool {
	_, _, ok := s.fileFor(id)

	return ok
}

// Put hashes raw and ensures a canonical <= [MaxEdge] file exists on disk under
// its ID. Repeated calls for an already-stored cover skip decoding entirely,
// and concurrent calls for the same ID share one decode and write.
func (s *Store) Put(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	id := ID(raw)
	if s == nil {
		return id, nil
	}
	if s.Has(id) {
		return id, nil
	}

	s.putMu.Lock()
	if call, ok := s.putInflight[id]; ok {
		s.putMu.Unlock()
		<-call.done
		if call.err != nil {
			return "", call.err
		}

		return id, nil
	}
	call := &putCall{done: make(chan struct{})}
	s.putInflight[id] = call
	s.putMu.Unlock()

	err := s.putOnce(id, raw)

	s.putMu.Lock()
	call.err = err
	delete(s.putInflight, id)
	s.putMu.Unlock()
	close(call.done)

	if err != nil {
		return "", err
	}

	return id, nil
}

func (s *Store) putOnce(id string, raw []byte) error {
	if s.Has(id) {
		return nil
	}
	img, err := Decode(raw)
	if err != nil {
		return err
	}
	encoded, mime, err := Encode(Resize(img, MaxEdge))
	if err != nil {
		return err
	}

	ext := ".jpg"
	if mime == "image/png" {
		ext = ".png"
	}
	dst := filepath.Join(s.dir, id+ext)
	tmp, err := os.CreateTemp(s.dir, id+".tmp*")
	if err != nil {
		return fmt.Errorf("artwork: temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("artwork: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("artwork: close: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("artwork: commit: %w", err)
	}

	return nil
}

// Bytes reads the normalized cover bytes and MIME type for id from disk.
func (s *Store) Bytes(id string) ([]byte, string, bool) {
	p, mime, ok := s.fileFor(id)
	if !ok {
		return nil, "", false
	}
	data, err := os.ReadFile(p)
	if err != nil || len(data) == 0 {
		return nil, "", false
	}

	return data, mime, true
}

// FilePath returns the on-disk path of the normalized .jpg or .png cover for id.
func (s *Store) FilePath(id string) (string, bool) {
	p, _, ok := s.fileFor(id)

	return p, ok
}

// DataURL returns id as an inline base64 data URL, backed by a bounded LRU.
func (s *Store) DataURL(id string) (string, bool) {
	if s == nil || !safeID(id) {
		return "", false
	}

	s.urlMu.Lock()
	if url, ok := s.urlLRU.get(id); ok {
		s.urlMu.Unlock()

		return url, true
	}
	s.urlMu.Unlock()

	data, mime, ok := s.Bytes(id)
	if !ok {
		return "", false
	}
	url := FormatDataURL(data, mime)

	s.urlMu.Lock()
	s.urlLRU.put(id, url)
	s.urlMu.Unlock()

	return url, true
}

// Accents returns the top n accent colours for id, backed by singleflight and a
// bounded LRU. Missing artwork returns (nil, nil).
func (s *Store) Accents(id string, n int) ([]color.Colour, error) {
	if s == nil || !safeID(id) || n <= 0 {
		return nil, nil
	}
	key := accentKey{id: id, n: n}

	s.accentMu.Lock()
	if cached, ok := s.accentLRU.get(key); ok {
		out := append([]color.Colour(nil), cached...)
		s.accentMu.Unlock()

		return out, nil
	}
	if call, ok := s.accentInflight[key]; ok {
		s.accentMu.Unlock()
		<-call.done

		return append([]color.Colour(nil), call.colors...), call.err
	}
	call := &accentCall{done: make(chan struct{})}
	s.accentInflight[key] = call
	s.accentMu.Unlock()

	colors, err := s.computeAccents(id, n)

	s.accentMu.Lock()
	delete(s.accentInflight, key)
	if err == nil && len(colors) > 0 {
		s.accentLRU.put(key, append([]color.Colour(nil), colors...))
	}
	call.colors = colors
	call.err = err
	s.accentMu.Unlock()
	close(call.done)

	return append([]color.Colour(nil), colors...), err
}

func (s *Store) computeAccents(id string, n int) ([]color.Colour, error) {
	data, _, ok := s.Bytes(id)
	if !ok {
		return nil, nil
	}
	img, err := Decode(data)
	if err != nil {
		return nil, err
	}

	return color.PaletteImage(Resize(img, accentEdge), n, true), nil
}

type lruEntry[K comparable, V any] struct {
	key K
	val V
}

type lruCache[K comparable, V any] struct {
	cap   int
	order *list.List
	items map[K]*list.Element
}

func newLRU[K comparable, V any](capacity int) *lruCache[K, V] {
	return &lruCache[K, V]{
		cap:   capacity,
		order: list.New(),
		items: make(map[K]*list.Element, capacity),
	}
}

func (c *lruCache[K, V]) get(k K) (V, bool) {
	if elem, ok := c.items[k]; ok {
		c.order.MoveToFront(elem)

		return elem.Value.(*lruEntry[K, V]).val, true
	}
	var zero V

	return zero, false
}

func (c *lruCache[K, V]) put(k K, v V) {
	if elem, ok := c.items[k]; ok {
		elem.Value.(*lruEntry[K, V]).val = v
		c.order.MoveToFront(elem)

		return
	}
	if c.order.Len() >= c.cap {
		if back := c.order.Back(); back != nil {
			c.order.Remove(back)
			delete(c.items, back.Value.(*lruEntry[K, V]).key)
		}
	}
	elem := c.order.PushFront(&lruEntry[K, V]{key: k, val: v})
	c.items[k] = elem
}
