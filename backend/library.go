package backend

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

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

// defaultLibraryID is the library a fresh install imports into.
const defaultLibraryID = "local:default"

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
	// A fresh install gets one empty library to import into: the Add button
	// merges into the selected library, so something selected must exist.
	if len(rows) == 0 {
		row, err := store.AddLibrary(library.Library{
			ID:   defaultLibraryID,
			Kind: library.KindLocal,
			Name: "Library",
		})
		if err != nil {
			s.log.Warn("library: create default", "error", err)

			return
		}
		s.log.Info("library: created the default library", "id", row.ID)
		rows = []library.Library{row}
	}
	for _, row := range rows {
		s.registerLocal(row)
		// Scan on startup, on a worker: the window must open before a large
		// folder has been walked. A library with no roots has nothing to walk.
		roots, err := store.Roots(row.ID)
		if err != nil {
			s.log.Warn("library: roots", "id", row.ID, "error", err)

			continue
		}
		if len(roots) > 0 {
			_ = s.RescanLibrary(row.ID)
		}
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

// AddLocalLibrary registers a folder as a new library and starts its first
// scan. The folder is the identity: adding the same path twice returns the row
// that already exists rather than a second library. To merge a folder into an
// existing library, ImportFolder is the call.
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
	if owner, root, found, err := s.watchedElsewhere(store, row.ID, abs); err != nil {
		return library.Library{}, err
	} else if found {
		return library.Library{}, foreignFolderError(abs, owner, root)
	}
	saved, err := store.AddLibrary(row)
	if err != nil {
		return library.Library{}, err
	}
	// The roots table is the scanner's truth; the root column is the describe
	// line. Both get the folder.
	if _, err := store.AddRoot(saved.ID, abs); err != nil {
		return library.Library{}, err
	}
	s.registerLocal(saved)
	_ = s.RescanLibrary(saved.ID)

	if src, ok := s.librarySource(saved.ID); ok {
		return src.Describe(s.ctx)
	}

	return saved, nil
}

// ImportFolder adds a folder as one more root of an existing library and
// starts the scan that indexes it; when the scan lands, the folder's tracks
// become one playlist named after the folder. Re-adding a folder the library
// already watches is a no-op. A folder inside — or containing — another
// library's root is refused, because a file path can belong to one library
// only, and a folder nested in the target's own root would index files twice.
func (s *PlayerService) ImportFolder(libraryID, path string) (library.Library, error) {
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
	src, ok := s.librarySource(libraryID)
	if !ok {
		return library.Library{}, fmt.Errorf("library: unknown id %q", libraryID)
	}
	if _, isLocal := src.(*local.Local); !isLocal {
		return library.Library{}, fmt.Errorf("library: %q is not a local library", libraryID)
	}

	roots, err := store.Roots(libraryID)
	if err != nil {
		return library.Library{}, err
	}
	for _, root := range roots {
		if samePath(abs, root) {
			// Already watched: nothing new to index, no second playlist.
			return s.describeLibrary(libraryID)
		}
		if insidePath(abs, root) || insidePath(root, abs) {
			return library.Library{}, fmt.Errorf("library: folder overlaps %q", root)
		}
	}
	if owner, root, found, err := s.watchedElsewhere(store, libraryID, abs); err != nil {
		return library.Library{}, err
	} else if found {
		return library.Library{}, foreignFolderError(abs, owner, root)
	}

	added, err := store.AddRoot(libraryID, abs)
	if err != nil {
		return library.Library{}, err
	}
	if !added {
		// Already watched: nothing new to index, no second playlist.
		return s.describeLibrary(libraryID)
	}
	if err := s.scanLibrary(libraryID, abs); err != nil {
		return library.Library{}, err
	}

	return s.describeLibrary(libraryID)
}

// describeLibrary is the one row the caller sees after a change.
func (s *PlayerService) describeLibrary(id string) (library.Library, error) {
	if src, ok := s.librarySource(id); ok {
		if described, err := src.Describe(s.ctx); err == nil {
			return described, nil
		}
	}

	return library.Library{}, fmt.Errorf("library: unknown id %q", id)
}

// insidePath reports whether path equals or lives under root, compared the way
// the filesystem resolves paths: case-sensitively on Linux, insensitively on
// Windows and macOS.
func insidePath(path, root string) bool {
	sep := string(os.PathSeparator)
	prefix := root + sep
	if runtime.GOOS == "linux" {
		return path == root || strings.HasPrefix(path, prefix)
	}
	if strings.EqualFold(path, root) {
		return true
	}

	return len(path) > len(prefix) && strings.EqualFold(path[:len(prefix)], prefix)
}

// samePath compares two absolute paths the way the filesystem resolves them.
func samePath(a, b string) bool {
	if runtime.GOOS == "linux" {
		return a == b
	}

	return strings.EqualFold(a, b)
}

// watchedElsewhere finds a root another library watches that the path would
// overlap: the same folder, one inside the other. A folder belongs to one
// library only, because the ref a like or a playlist stores is the file path,
// so a second library over the same tree would address another library's rows.
func (s *PlayerService) watchedElsewhere(
	store *sqlite.Store, libraryID, path string,
) (library.Library, string, bool, error) {
	libs, err := store.Libraries()
	if err != nil {
		return library.Library{}, "", false, err
	}
	for _, lib := range libs {
		if lib.ID == libraryID {
			continue
		}
		roots, err := store.Roots(lib.ID)
		if err != nil {
			return library.Library{}, "", false, err
		}
		for _, root := range roots {
			if insidePath(path, root) || insidePath(root, path) {
				return lib, root, true, nil
			}
		}
	}

	return library.Library{}, "", false, nil
}

// foreignFolderError names the library that already owns the folder, so a
// refused import says where the folder went.
func foreignFolderError(path string, owner library.Library, root string) error {
	if samePath(path, root) {
		return fmt.Errorf(
			"library: folder %q already belongs to library %q", filepath.Base(path), owner.Name,
		)
	}

	return fmt.Errorf("library: folder overlaps library %q", owner.Name)
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

// RescanLibrary rescans every watched root on a worker. A scan already running
// is not duplicated.
func (s *PlayerService) RescanLibrary(id string) error {
	return s.scanLibrary(id, "")
}

// scanLibrary runs one scan on a worker, deduped per library. An importRoot
// names the folder an import just added: the scan indexes every root, and the
// refs it indexed under that folder become the import's playlist.
func (s *PlayerService) scanLibrary(id, importRoot string) error {
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

		res, err := scanner.ImportScan(s.ctx, importRoot)
		state := "ready"
		if err != nil {
			state = "error"
			s.log.Warn("library: scan failed", "id", id, "error", err)
		}
		s.emit(eventLibrary, LibraryProgress{LibraryID: id, Done: res.Count, Total: res.Count, State: state})
		if err == nil && importRoot != "" && len(res.Refs) > 0 {
			s.createImportPlaylist(id, importRoot, res.Refs)
		}
	}()

	return nil
}

// createImportPlaylist is the one-playlist-per-import rule: the folder's
// tracks, in walk order, under the folder's name. A failure only costs the
// playlist; the tracks are already in the catalogue.
func (s *PlayerService) createImportPlaylist(libraryID, root string, refs []string) {
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return
	}

	playlist, err := store.CreatePlaylist(libraryID, filepath.Base(root))
	if err != nil {
		s.log.Warn("library: import playlist", "error", err)

		return
	}
	if err := store.AddToPlaylist(playlist.ID, refs); err != nil {
		s.log.Warn("library: import playlist", "error", err)
	}
}

// YTMImport is the outcome of one YouTube Music import: how many tracks landed
// in the library, and the playlist row when the hit was a playlist.
type YTMImport struct {
	PlaylistID string `json:"playlistId,omitempty"`
	Name       string `json:"name"`
	Tracks     int    `json:"tracks"`
}

// ytmCoverWorkers bounds the artwork fetches one import keeps in flight. Each
// cover is a round trip, so a playlist import would otherwise spend most of its
// time waiting on one request at a time.
const ytmCoverWorkers = 6

// ImportYTM copies a YouTube Music hit into a library's catalogue. The hit is
// the row the palette listed: a song becomes one track, and a playlist becomes
// a playlist row carrying its remote id plus its entries. Everything is an
// ordinary catalogue row afterwards, so browsing, liking and playback need no
// special case.
//
// The hit carries the metadata the search already showed, so a song costs no
// second request; a playlist is one catalogue walk, bounded the same way
// queueing one is.
func (s *PlayerService) ImportYTM(libraryID string, hit YTMResult) (YTMImport, error) {
	src, ok := s.librarySource(libraryID)
	if !ok {
		return YTMImport{}, fmt.Errorf("library: unknown id %q", libraryID)
	}
	dst, ok := src.(*local.Local)
	if !ok {
		return YTMImport{}, fmt.Errorf("library: %q cannot import YouTube Music", libraryID)
	}
	if strings.TrimSpace(hit.VideoID) == "" {
		return YTMImport{}, fmt.Errorf("ytm: %q has no id", hit.Title)
	}

	name := strings.TrimSpace(hit.Title)

	// ytm-go has no album loader: an album browse id is walked by the playlist
	// parser, whose album layout makes it panic. Saying so is better than
	// landing half an album or a list of one.
	if hit.Kind == "Album" {
		return YTMImport{}, fmt.Errorf("ytm: albums cannot be imported yet")
	}

	if hit.Kind != "Playlist" {
		if !hit.Playable {
			return YTMImport{}, fmt.Errorf("ytm: %q is not a track", hit.Title)
		}
		if _, err := s.catalogYTM(dst, hit, s.storeYTMCover(hit.Thumbnail)); err != nil {
			return YTMImport{}, err
		}

		return YTMImport{Name: name, Tracks: 1}, nil
	}

	rows, name, err := s.ytmProv.playlist(s.ctx, ytmPlaylistID(hit.VideoID))
	if err != nil {
		return YTMImport{}, err
	}
	// The catalogue's own title wins when it has one: a playlist pasted as an
	// id or a URL reaches here with the palette's placeholder label, and the
	// row should carry the list's real name.
	if name == "" {
		name = strings.TrimSpace(hit.Title)
	}
	covers := s.storeYTMCovers(rows)
	refs := make([]string, 0, len(rows))
	for i, row := range rows {
		ref, err := s.catalogYTM(dst, row, covers[i])
		if err != nil {
			return YTMImport{}, err
		}
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return YTMImport{}, fmt.Errorf("ytm: %q has no tracks", name)
	}
	if name == "" {
		name = "YouTube Music playlist"
	}
	playlist, err := dst.CreateRemotePlaylist(name, hit.VideoID)
	if err != nil {
		return YTMImport{}, err
	}
	if err := dst.AddToPlaylist(s.ctx, playlist.ID, refs); err != nil {
		return YTMImport{}, err
	}

	return YTMImport{PlaylistID: playlist.ID, Name: name, Tracks: len(refs)}, nil
}

// storeYTMCover fetches one thumbnail into the art cache and returns its cover id. A
// row without art, or a fetch that fails, returns "": the art is decorative, the
// track is not.
func (s *PlayerService) storeYTMCover(url string) string {
	if url == "" {
		return ""
	}
	data := s.ytmProv.thumbFor(s.ctx, url)
	if len(data) == 0 {
		return ""
	}
	id := cover.ID(data)
	_ = artcache.Store(id, data)

	return id
}

// storeYTMCovers fetches the artwork for a batch of rows, several at a time, and
// returns it in row order. Tracks from one album share a thumbnail, so each
// distinct URL is fetched once and its cover is shared by every row that names
// it: a playlist of one album costs one request, not one per track.
func (s *PlayerService) storeYTMCovers(rows []YTMResult) []string {
	urls := make([]string, len(rows))
	slot := make(map[string]int, len(rows))
	distinct := make([]string, 0, len(rows))
	for i, row := range rows {
		if row.Thumbnail == "" {
			continue
		}
		urls[i] = row.Thumbnail
		if _, ok := slot[row.Thumbnail]; !ok {
			slot[row.Thumbnail] = len(distinct)
			distinct = append(distinct, row.Thumbnail)
		}
	}

	covers := make([]string, len(distinct))
	work := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < ytmCoverWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				covers[i] = s.storeYTMCover(distinct[i])
			}
		}()
	}
	for i := range distinct {
		work <- i
	}
	close(work)
	wg.Wait()

	out := make([]string, len(rows))
	for i, url := range urls {
		if url != "" {
			out[i] = covers[slot[url]]
		}
	}

	return out
}

// catalogYTM writes one search or playlist row into a library and returns its
// ref. The artwork is passed in, so a bulk import can fetch the covers in
// parallel and keep them in the cache beside the row: a library card or an album
// tile can then draw a cover the player never opened.
func (s *PlayerService) catalogYTM(dst *local.Local, row YTMResult, coverID string) (string, error) {
	// Index the metadata too, so a queue row built from this ref shows the real
	// title, artist and art instead of the reference string.
	s.ytm.put(ytmTrack{
		ID:           row.VideoID,
		Title:        row.Title,
		Artist:       row.Artist,
		Album:        row.Album,
		DurationMs:   row.DurationMs,
		ThumbnailURL: row.Thumbnail,
		ArtistID:     row.ArtistID,
	})

	return dst.ImportRemote(library.KindYTM, sqlite.TrackRow{
		NativeID:   row.VideoID,
		Title:      row.Title,
		Artist:     row.Artist,
		Album:      row.Album,
		DurationMs: row.DurationMs,
		CoverID:    coverID,
	})
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
