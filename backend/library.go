package backend

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dlcuy22/molow/internal/artcache"
	"github.com/dlcuy22/molow/internal/color"
	"github.com/dlcuy22/molow/internal/cover"
	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/local"
	"github.com/dlcuy22/molow/internal/library/sqlite"
	"github.com/dlcuy22/molow/internal/lyrics"
	ytm "github.com/dlcuy22/ytm-go"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// eventLibrary carries scan progress and completion. The frontend re-reads a
// feed when it names the library it is showing, rather than polling.
const eventLibrary = "molo:library"

// LibraryProgress is the scan event payload.
type LibraryProgress struct {
	LibraryID string `json:"libraryId"`
	Done      int    `json:"done"`
	Total     int    `json:"total"`
	State     string `json:"state"`
}

// libraryDBPath is the catalogue database, beside settings.json.
func libraryDBPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(base, "molow", "library.db"), nil
}

// initLibraries opens the catalogue and registers the stored roots. A failure
// is logged and browsing stays empty; playback does not depend on it.
func (s *PlayerService) initLibraries() {
	p, err := libraryDBPath()
	if err != nil {
		s.log.Warn("library: no config dir", "error", err)

		return
	}
	store, err := sqlite.Open(p)
	if err != nil {
		s.log.Warn("library: open database", "error", err)

		return
	}

	s.libMu.Lock()
	s.libStore = store
	s.libs = map[string]library.Source{}
	s.scanning = map[string]bool{}
	s.libMu.Unlock()

	rows, err := store.Libraries()
	if err != nil {
		s.log.Warn("library: list", "error", err)

		return
	}
	for _, row := range rows {
		s.registerLocal(row)
		// Scan on startup, on a worker: the window must open before a large
		// folder has been walked.
		_ = s.RescanLibrary(row.ID)
	}
}

func (s *PlayerService) registerLocal(row library.Library) {
	src := local.New(s.libStore, row, func(p local.ScanProgress) {
		s.emit(eventLibrary, LibraryProgress{
			LibraryID: p.LibraryID, Done: p.Done, Total: p.Total, State: "scanning",
		})
	})
	s.libMu.Lock()
	s.libs[row.ID] = src
	s.libMu.Unlock()
}

func (s *PlayerService) librarySource(id string) (library.Source, bool) {
	s.libMu.Lock()
	defer s.libMu.Unlock()
	src, ok := s.libs[id]

	return src, ok
}

// Libraries lists every registered library.
func (s *PlayerService) Libraries() ([]library.Library, error) {
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return nil, nil
	}

	rows, err := store.Libraries()
	if err != nil {
		return nil, err
	}
	out := make([]library.Library, 0, len(rows))
	for _, row := range rows {
		if src, ok := s.librarySource(row.ID); ok {
			if described, err := src.Describe(s.ctx); err == nil {
				out = append(out, described)

				continue
			}
		}
		out = append(out, row)
	}

	return out, nil
}

// AddLocalLibrary registers a folder as a library and starts its first scan.
// The folder is the identity: adding the same path twice returns the row that
// already exists rather than a second library.
func (s *PlayerService) AddLocalLibrary(path string) (library.Library, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil || abs == "" {
		return library.Library{}, fmt.Errorf("library: bad path")
	}
	info, err := os.Stat(abs)
	if err != nil {
		return library.Library{}, err
	}
	if !info.IsDir() {
		return library.Library{}, fmt.Errorf("library: not a folder: %s", abs)
	}

	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return library.Library{}, fmt.Errorf("library: store is not available")
	}

	sum := sha1.Sum([]byte(abs))
	row := library.Library{
		ID:     "local:" + hex.EncodeToString(sum[:8]),
		Kind:   library.KindLocal,
		Name:   filepath.Base(abs),
		Root:   abs,
		Status: library.StatusScanning,
	}
	saved, err := store.AddLibrary(row)
	if err != nil {
		return library.Library{}, err
	}
	s.registerLocal(saved)
	_ = s.RescanLibrary(saved.ID)

	if src, ok := s.librarySource(saved.ID); ok {
		return src.Describe(s.ctx)
	}

	return saved, nil
}

func (s *PlayerService) RemoveLibrary(id string) error {
	s.libMu.Lock()
	store := s.libStore
	delete(s.libs, id)
	s.libMu.Unlock()
	if store == nil {
		return fmt.Errorf("library: store is not available")
	}

	return store.RemoveLibrary(id)
}

// RescanLibrary rescans one root on a worker. A scan already running is not
// duplicated.
func (s *PlayerService) RescanLibrary(id string) error {
	src, ok := s.librarySource(id)
	if !ok {
		return fmt.Errorf("library: unknown id %q", id)
	}
	scanner, ok := src.(*local.Local)
	if !ok {
		return fmt.Errorf("library: %q is not a local library", id)
	}

	s.libMu.Lock()
	if s.scanning[id] {
		s.libMu.Unlock()

		return nil
	}
	s.scanning[id] = true
	s.libMu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.libMu.Lock()
			delete(s.scanning, id)
			s.libMu.Unlock()
		}()

		n, err := scanner.Scan(s.ctx)
		state := "ready"
		if err != nil {
			state = "error"
			s.log.Warn("library: scan failed", "id", id, "error", err)
		}
		s.emit(eventLibrary, LibraryProgress{LibraryID: id, Done: n, Total: n, State: state})
	}()

	return nil
}

// Liked reports whether a catalogued ref carries a like. An uncatalogued path
// (a file dropped on the window) is simply not liked.
func (s *PlayerService) Liked(path string) (bool, error) {
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil || path == "" {
		return false, nil
	}
	liked, err := store.Liked(path)
	if err != nil {
		return false, nil
	}

	return liked, nil
}

// ToggleLike flips a like for a catalogued ref and returns the new state.
func (s *PlayerService) ToggleLike(path string) (bool, error) {
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil || path == "" {
		return false, fmt.Errorf("library: no catalogue")
	}
	liked, err := store.ToggleLiked(path)
	if err != nil {
		return false, err
	}

	return liked, nil
}

// LibraryFeed answers one tab of a library.
func (s *PlayerService) LibraryFeed(id, tab, cursor string, limit int) (library.Page, error) {
	src, ok := s.librarySource(id)
	if !ok {
		return library.Page{}, fmt.Errorf("library: unknown id %q", id)
	}

	return src.Feed(s.ctx, tab, cursor, limit)
}

// LibraryAlbumTracks reads one album's tracks in order.
func (s *PlayerService) LibraryAlbumTracks(id, album, artist string) ([]library.Track, error) {
	src, ok := s.librarySource(id)
	if !ok {
		return nil, fmt.Errorf("library: unknown id %q", id)
	}
	if localSrc, ok := src.(*local.Local); ok {
		return localSrc.AlbumTracks(s.ctx, album, artist)
	}

	return nil, fmt.Errorf("library: %q cannot list an album", id)
}

// PlayRefs replaces the queue with refs and starts the first. It is the library
// "play this album" action; playing one row passes a single ref.
func (s *PlayerService) PlayRefs(refs []string) error {
	return s.command(func() error { return s.player.PlayQueue(refs) })
}

// LibrarySearch runs a library's own search capability.
func (s *PlayerService) LibrarySearch(id, query string, limit int) (library.Page, error) {
	src, ok := s.librarySource(id)
	if !ok {
		return library.Page{}, fmt.Errorf("library: unknown id %q", id)
	}
	searchable, ok := src.(library.Searchable)
	if !ok {
		return library.Page{}, fmt.Errorf("library: %q cannot search", id)
	}

	return searchable.Search(s.ctx, query, limit)
}

// SetLiked writes a like through the library that owns the ref.
func (s *PlayerService) SetLiked(libraryID, ref string, liked bool) error {
	src, ok := s.librarySource(libraryID)
	if !ok {
		return fmt.Errorf("library: unknown id %q", libraryID)
	}
	likable, ok := src.(library.Likable)
	if !ok {
		return fmt.Errorf("library: %q cannot like", libraryID)
	}

	return likable.SetLiked(s.ctx, ref, liked)
}

func (s *PlayerService) Playlists(libraryID string) ([]library.Playlist, error) {
	src, ok := s.librarySource(libraryID)
	if !ok {
		return nil, fmt.Errorf("library: unknown id %q", libraryID)
	}
	reader, ok := src.(library.PlaylistReader)
	if !ok {
		return nil, fmt.Errorf("library: %q has no playlists", libraryID)
	}

	return reader.Playlists(s.ctx)
}

func (s *PlayerService) CreatePlaylist(libraryID, name string) (library.Playlist, error) {
	src, ok := s.librarySource(libraryID)
	if !ok {
		return library.Playlist{}, fmt.Errorf("library: unknown id %q", libraryID)
	}
	writer, ok := src.(library.PlaylistWriter)
	if !ok {
		return library.Playlist{}, fmt.Errorf("library: %q cannot edit playlists", libraryID)
	}

	return writer.CreatePlaylist(s.ctx, name)
}

func (s *PlayerService) AddToPlaylist(libraryID, playlistID string, refs []string) error {
	src, ok := s.librarySource(libraryID)
	if !ok {
		return fmt.Errorf("library: unknown id %q", libraryID)
	}
	writer, ok := src.(library.PlaylistWriter)
	if !ok {
		return fmt.Errorf("library: %q cannot edit playlists", libraryID)
	}

	return writer.AddToPlaylist(s.ctx, playlistID, refs)
}

func (s *PlayerService) RemoveFromPlaylist(libraryID, playlistID string, refs []string) error {
	src, ok := s.librarySource(libraryID)
	if !ok {
		return fmt.Errorf("library: unknown id %q", libraryID)
	}
	writer, ok := src.(library.PlaylistWriter)
	if !ok {
		return fmt.Errorf("library: %q cannot edit playlists", libraryID)
	}

	return writer.RemoveFromPlaylist(s.ctx, playlistID, refs)
}

// PickLibraryFolder shows the native folder picker and returns the path without
// queueing anything. It is separate from OpenFolder, which means "play this".
func (s *PlayerService) PickLibraryFolder() (string, error) {
	app := application.Get()
	if app == nil {
		return "", fmt.Errorf("no application")
	}

	dir, err := app.Dialog.OpenFile().
		SetTitle("Add a library folder").
		CanChooseDirectories(true).
		CanChooseFiles(false).
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}

	return dir, nil
}

// Lyrics returns the lyric sheet of a track. The sidecar loader is the only
// read path; when its winner has not landed yet, the prefetch is joined. No
// sheet renders ahead of the chain, so the embedded sheet arrives with the
// same event as any fetched one. No lyrics is a nil document, not an error.
func (s *PlayerService) Lyrics(path string) (*lyrics.Document, error) {
	doc, err := s.readLyricsOrJoin(path)
	if err != nil || doc == nil {
		return doc, err
	}

	return s.instrumentalMarks(path, doc), nil
}

// readLyricsOrJoin is the one resolver: the sidecar loader first, then the
// prefetch that is still running or just concluded.
func (s *PlayerService) readLyricsOrJoin(path string) (*lyrics.Document, error) {
	if doc, err := s.readLyrics(path); doc != nil || err != nil {
		return doc, err
	}
	if doc, ok := s.joinLyrics(path); ok {
		return doc, nil
	}

	return nil, nil
}

// ytmLyricsDocument maps a catalogue sheet onto what the now-playing column
// draws. The line end has no counterpart in the document, which follows a line
// by its start time, and the instrumental markers the endpoint emits are
// dropped so an instrumental intro does not render as a line of its own.
func ytmLyricsDocument(sheet *ytm.Lyrics) *lyrics.Document {
	doc := &lyrics.Document{Synced: sheet.Synced, Source: "ytmusic"}
	for _, line := range sheet.Lines {
		if line.Text == "" || line.Text == "♪" {
			continue
		}
		doc.Lines = append(doc.Lines, lyrics.Line{TimeMs: line.StartMs, Text: line.Text})
	}

	return doc
}

// CoverAccents returns the ranked accent palette of one cover. The album
// player uses the first entries; the rest are there for a caller that wants a
// different tie-break. The bytes come from the artwork cache, which the scanner
// fills, so this works for a library cover that was never the current track.
func (s *PlayerService) CoverAccents(coverID string) ([]color.Colour, error) {
	if coverID == "" {
		return nil, nil
	}
	data := artcache.Load(coverID)
	if len(data) == 0 {
		// A cover the engine has open but the scanner has not stored yet.
		if cover.ID(s.player.Snapshot().Meta.Tags.Cover) == coverID {
			data = s.player.Snapshot().Meta.Tags.Cover
		}
	}
	if len(data) == 0 {
		return nil, nil
	}

	return color.Palette(data, 5, true)
}
