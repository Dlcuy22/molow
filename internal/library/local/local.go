// Package local is the filesystem library: one root, scanned into the shared
// SQLite catalogue, browsed through the same contract a remote library uses.
package local

import (
	"context"
	"fmt"
	"github.com/dlcuy22/molo"
	"github.com/dlcuy22/molo/core"
	"github.com/dlcuy22/molo/decode"
	"github.com/dlcuy22/molo/meta"
	"github.com/dlcuy22/molow/internal/artcache"
	"github.com/dlcuy22/molow/internal/cover"
	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/sqlite"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ScanProgress is the payload a UI can render as a progress line.
type ScanProgress struct {
	LibraryID string
	Done      int
	Total     int
}

// Local is one watched root.
type Local struct {
	store *sqlite.Store
	lib   library.Library
	exts  map[string]bool

	mu       sync.Mutex
	status   library.Status
	message  string
	onChange func(ScanProgress)
}

// New builds a local source over an existing store row.
func New(store *sqlite.Store, lib library.Library, onChange func(ScanProgress)) *Local {
	exts := make(map[string]bool)
	for _, ext := range molo.SupportedExtensions() {
		exts[strings.ToLower(ext)] = true
	}

	return &Local{store: store, lib: lib, exts: exts, status: lib.Status, onChange: onChange}
}

// Describe reports the library with a live track count.
func (l *Local) Describe(context.Context) (library.Library, error) {
	lib, err := l.store.Library(l.lib.ID)
	if err != nil {
		return l.lib, err
	}
	lib.Kind = library.KindLocal
	lib.Status = l.status
	lib.Message = l.message
	lib.Capabilities = library.CapabilitiesOf(l)

	return lib, nil
}

// Feed answers one tab. Cursor is an offset; a page is small enough that a
// numeric cursor is simpler than an opaque one.
func (l *Local) Feed(_ context.Context, tab, cursor string, limit int) (library.Page, error) {
	offset, _ := strconv.Atoi(cursor)
	if limit <= 0 {
		limit = 60
	}

	switch {
	case tab == "all":
		albums, total, err := l.store.Albums(l.lib.ID, limit, offset)
		if err != nil {
			return library.Page{}, err
		}

		return library.Page{Shelves: []library.Shelf{{
			ID: "albums", Title: "Albums", Layout: "grid", Kind: "albums",
			Albums: albums, Total: total, Cursor: nextCursor(offset+len(albums), total),
		}}}, nil
	case tab == "" || tab == "home":
		return l.home(limit)
	case tab == "tracks":
		items, total, err := l.store.Tracks(l.lib.ID, limit, offset)
		if err != nil {
			return library.Page{}, err
		}

		return library.Page{Shelves: []library.Shelf{{
			ID: "tracks", Title: "Songs", Layout: "list", Kind: "tracks",
			Items: items, Total: total, Cursor: nextCursor(offset+len(items), total),
		}}}, nil
	case tab == "albums":
		albums, total, err := l.store.Albums(l.lib.ID, limit, offset)
		if err != nil {
			return library.Page{}, err
		}

		return library.Page{Shelves: []library.Shelf{{
			ID: "albums", Title: "Albums", Layout: "grid", Kind: "albums",
			Albums: albums, Total: total, Cursor: nextCursor(offset+len(albums), total),
		}}}, nil
	case tab == "artists":
		artists, total, err := l.store.Artists(l.lib.ID, limit, offset)
		if err != nil {
			return library.Page{}, err
		}

		return library.Page{Shelves: []library.Shelf{{
			ID: "artists", Title: "Artists", Layout: "grid", Kind: "artists",
			Artists: artists, Total: total, Cursor: nextCursor(offset+len(artists), total),
		}}}, nil
	case tab == "playlists":
		list, err := l.store.Playlists(l.lib.ID)
		if err != nil {
			return library.Page{}, err
		}

		return library.Page{Shelves: []library.Shelf{{
			ID: "playlists", Title: "Playlists", Layout: "grid", Kind: "playlists",
			Playlists: list, Total: len(list),
		}}}, nil
	case strings.HasPrefix(tab, "playlist:"):
		id := strings.TrimPrefix(tab, "playlist:")
		items, err := l.store.PlaylistTracks(id, limit, offset)
		if err != nil {
			return library.Page{}, err
		}

		return library.Page{Shelves: []library.Shelf{{
			ID: id, Title: "Playlist", Layout: "list", Kind: "tracks", Items: items,
		}}}, nil
	}

	return library.Page{}, fmt.Errorf("library: unknown tab %q", tab)
}

func (l *Local) home(limit int) (library.Page, error) {
	if limit > 9 {
		limit = 9
	}
	tracks, err := l.store.RecentTracks(l.lib.ID, limit)
	if err != nil {
		return library.Page{}, err
	}
	albums, _, err := l.store.Albums(l.lib.ID, 5, 0)
	if err != nil {
		return library.Page{}, err
	}
	artists, _, err := l.store.Artists(l.lib.ID, 5, 0)
	if err != nil {
		return library.Page{}, err
	}

	return library.Page{Shelves: []library.Shelf{
		{ID: "recent", Title: "Recently added", Layout: "grid", Kind: "tracks", Items: tracks},
		{ID: "albums", Title: "Albums", Layout: "grid", Kind: "albums", Albums: albums},
		{ID: "artists", Title: "Artists", Layout: "grid", Kind: "artists", Artists: artists},
	}}, nil
}

// Tracks resolves refs to catalogue rows.
func (l *Local) Tracks(_ context.Context, refs []string) ([]library.Track, error) {
	out := make([]library.Track, 0, len(refs))
	for _, ref := range refs {
		t, ok, err := l.store.TrackByRef(ref)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, t)
		}
	}

	return out, nil
}

// AlbumTracks reads one album in track order.
func (l *Local) AlbumTracks(_ context.Context, album, artist string) ([]library.Track, error) {
	return l.store.AlbumTracks(l.lib.ID, album, artist)
}

// Search runs the catalogue's FTS index.
func (l *Local) Search(_ context.Context, query string, limit int) (library.Page, error) {
	if limit <= 0 {
		limit = 40
	}
	items, err := l.store.Search(l.lib.ID, query, limit)
	if err != nil {
		return library.Page{}, err
	}

	return library.Page{Shelves: []library.Shelf{{
		ID: "search", Title: "Songs", Layout: "list", Kind: "tracks", Items: items,
	}}}, nil
}

// SetLiked writes one like.
func (l *Local) SetLiked(_ context.Context, ref string, liked bool) error {
	return l.store.SetLiked(ref, liked)
}

func (l *Local) Playlists(context.Context) ([]library.Playlist, error) {
	return l.store.Playlists(l.lib.ID)
}

func (l *Local) PlaylistTracks(_ context.Context, id, cursor string, limit int) (library.Page, error) {
	offset, _ := strconv.Atoi(cursor)

	return l.Feed(context.Background(), "playlist:"+id, strconv.Itoa(offset), limit)
}

func (l *Local) CreatePlaylist(_ context.Context, name string) (library.Playlist, error) {
	return l.store.CreatePlaylist(l.lib.ID, name)
}

func (l *Local) AddToPlaylist(_ context.Context, id string, refs []string) error {
	return l.store.AddToPlaylist(id, refs)
}

func (l *Local) RemoveFromPlaylist(_ context.Context, id string, refs []string) error {
	return l.store.RemoveFromPlaylist(id, refs)
}

// Scan walks the root and writes the catalogue. The walk is synchronous; the
// service runs it on a worker so a large folder does not block the UI.
func (l *Local) Scan(ctx context.Context) (int, error) {
	l.setStatus(library.StatusScanning, "")
	start := time.Now().Unix()

	var (
		seen  int
		errs  int
		last  error
		paths []string
	)
	err := filepath.WalkDir(l.lib.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			errs++

			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != l.lib.Root && (strings.HasPrefix(name, ".") || name == "@eaDir") {
				return fs.SkipDir
			}

			return nil
		}
		if l.exts[strings.ToLower(filepath.Ext(path))] {
			paths = append(paths, path)
		}

		return nil
	})
	if err != nil {
		l.setStatus(library.StatusError, err.Error())

		return 0, err
	}

	for i, path := range paths {
		if ctx.Err() != nil {
			break
		}
		if err := l.index(ctx, path); err != nil {
			errs++
			last = err
		} else {
			seen++
		}
		if l.onChange != nil && (i%25 == 0 || i == len(paths)-1) {
			l.onChange(ScanProgress{LibraryID: l.lib.ID, Done: i + 1, Total: len(paths)})
		}
	}

	if err := l.store.MarkMissing(l.lib.ID, start); err != nil {
		l.setStatus(library.StatusError, err.Error())

		return seen, err
	}
	if err := l.store.TouchScan(l.lib.ID); err != nil {
		l.setStatus(library.StatusError, err.Error())

		return seen, err
	}
	if errs > 0 && last != nil {
		l.setStatus(library.StatusReady, fmt.Sprintf("%d files could not be read", errs))

		return seen, nil
	}
	l.setStatus(library.StatusReady, "")

	return seen, nil
}

func (l *Local) index(ctx context.Context, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	f := file(path, info)

	m, err := meta.Resolve(ctx, path)
	if err != nil || m == nil {
		m = &meta.Meta{Tags: meta.Tags{Title: titleFromPath(path)}}
	}
	tags := m.Tags

	title := strings.TrimSpace(tags.Title)
	if title == "" {
		title = titleFromPath(path)
	}
	album := strings.TrimSpace(tags.Album)
	if album == "" {
		album = albumFromPath(path)
	}

	duration := int64(0)
	if prober, ok := decode.Default.Probe(path); ok {
		if stream, err := prober.Probe(path, decode.ProbeOptions{Duration: core.DurationProbe}); err == nil {
			duration = stream.Duration().Milliseconds()
		}
	}

	coverID := ""
	if len(tags.Cover) > 0 {
		coverID = cover.ID(tags.Cover)
		// Keep the bytes so a library card and the album player can ask for
		// this cover when the track is not the one playing.
		_ = artcache.Store(coverID, tags.Cover)
	}

	_, err = l.store.UpsertTrack(l.lib.ID, f, sqlite.TrackRow{
		NativeID:    nativeID(f),
		Title:       title,
		Artist:      strings.TrimSpace(tags.Artist),
		Album:       album,
		AlbumArtist: strings.TrimSpace(tags.AlbumArtist),
		Year:        tags.Year,
		TrackNo:     tags.Track,
		DiscNo:      tags.Disc,
		DurationMs:  duration,
		CoverID:     coverID,
	})

	return err
}

func (l *Local) setStatus(s library.Status, message string) {
	l.mu.Lock()
	l.status = s
	l.message = message
	l.mu.Unlock()
}

func nextCursor(next, total int) string {
	if next >= total {
		return ""
	}

	return strconv.Itoa(next)
}

// titleFromPath is the last-resort name the meta chain would also produce.
func titleFromPath(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = strings.TrimSpace(base)
	// A leading "01. " or "01 - " is a track index, not the name.
	if i := strings.IndexAny(base, " .-"); i > 0 && i <= 4 && isDigits(base[:i]) {
		base = strings.TrimSpace(strings.TrimLeft(base[i:], " .-"))
	}

	return base
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}

// albumFromPath uses the containing folder when the tags have no album. It is
// the one guess the scanner makes, and it is what an untagged album folder
// means.
func albumFromPath(path string) string {
	dir := filepath.Base(filepath.Dir(path))
	if i := strings.Index(dir, " ["); i > 0 {
		dir = dir[:i]
	}

	return strings.TrimSpace(dir)
}
