package backend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/dlcuy22/molo"
	"github.com/dlcuy22/molow/internal/artwork"
	"github.com/dlcuy22/molow/internal/color"
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

// eventActivity carries per-task progress updates for the library header indicator.
const eventActivity = "molo:activity"

const (
	maxLibraryNameRunes = 80
	maxLibraryDescRunes = 240
)

// LibraryProgress is the scan event payload.
type LibraryProgress struct {
	LibraryID string `json:"libraryId"`
	Done      int    `json:"done"`
	Total     int    `json:"total"`
	State     string `json:"state"`
}

// ActivityEvent describes a single background or user-initiated library task.
type ActivityEvent struct {
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Detail    string `json:"detail,omitempty"`
	Done      int    `json:"done,omitempty"`
	Total     int    `json:"total,omitempty"`
	State     string `json:"state"`
}

func (s *PlayerService) emitActivity(ev ActivityEvent) {
	s.emit(eventActivity, ev)
}

func activityPlaylistTitle(name, fallback string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || strings.EqualFold(trimmed, "Add YouTube Music playlist") {
		return fallback
	}
	return trimmed
}

// libraryDBPath is the catalogue database, beside settings.json.
func libraryDBPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(base, "molow", "library.db"), nil
}

// initLibraries opens the catalogue, migrates any legacy single-account YTM
// session, and opens every registered library through its SourceAdapter.
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

	if cs, cerr := s.credentials(); cerr == nil {
		legacyPath, _ := credentialsPath()
		if merr := migrateLegacyCredentials(store, cs, legacyPath, s.log); merr != nil {
			s.log.Warn("auth: legacy credential migration deferred", "error", merr)
		}
	}

	router := s.ensureSourceRouter()
	rows, err := store.LibraryRows()
	if err != nil {
		s.log.Warn("library: list", "error", err)

		return
	}
	for _, row := range rows {
		if adapter, ok := router.Adapter(row.Kind); ok {
			if _, err := adapter.Open(s.ctx, row); err != nil {
				s.log.Warn("library: open adapter", "id", row.ID, "kind", row.Kind, "error", err)
			}
			continue
		}
		if row.Kind == library.KindLocal {
			s.registerLocal(row.Library)
		}
	}

	go func() {
		s.backfillCompositeCovers()
		s.pruneOrphanArtwork()
	}()
}

// backfillCompositeCovers generates missing pre-rendered covers for existing
// libraries and playlists once on startup so databases created before migration
// 003 render single composite covers without waiting for a rescan.
func (s *PlayerService) backfillCompositeCovers() {
	if s.art == nil {
		return
	}
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return
	}
	libs, err := store.Libraries()
	if err != nil {
		return
	}
	libsUpdated := 0
	playlistsUpdated := 0
	for _, lib := range libs {
		if !lib.CustomCover {
			if lib.TrackCount == 0 && lib.CoverID != "" {
				_ = store.SetLibraryCover(lib.ID, "", false)
			} else if lib.TrackCount > 0 && (lib.CoverID == "" || !s.art.Has(lib.CoverID)) {
				if s.refreshLibraryCover(lib.ID) {
					libsUpdated++
				}
			}
		}
		lists, err := store.Playlists(lib.ID)
		if err != nil {
			continue
		}
		for _, pl := range lists {
			if pl.CustomCover {
				continue
			}
			if pl.TrackCount == 0 && pl.CoverID != "" {
				_ = store.SetPlaylistCover(pl.ID, "", false)
			} else if pl.TrackCount > 0 && (pl.CoverID == "" || !s.art.Has(pl.CoverID)) {
				if s.refreshPlaylistCover(pl.ID) {
					playlistsUpdated++
				}
			}
		}
	}
	if libsUpdated > 0 || playlistsUpdated > 0 {
		s.logger().Info("artwork: backfilled composite covers", "libraries", libsUpdated, "playlists", playlistsUpdated)
	}
}

func (s *PlayerService) composeCoverID(coverIDs []string) string {
	if len(coverIDs) == 0 {
		return ""
	}
	if len(coverIDs) < 4 || s.art == nil {
		return coverIDs[0]
	}
	tiles := make([][]byte, 0, 4)
	for _, cid := range coverIDs[:4] {
		if data, _, ok := s.art.Bytes(cid); ok {
			tiles = append(tiles, data)
		}
	}
	if len(tiles) < 4 {
		return coverIDs[0]
	}
	mosaic, err := artwork.ComposeMosaic(tiles)
	if err != nil || len(mosaic) == 0 {
		return coverIDs[0]
	}
	id, err := s.art.Put(mosaic)
	if err != nil || id == "" {
		return coverIDs[0]
	}

	return id
}

func (s *PlayerService) refreshLibraryCover(libraryID string) bool {
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil || libraryID == "" {
		return false
	}
	ids, err := store.LibraryLogoCovers(libraryID, 4)
	if err != nil {
		return false
	}
	cid := s.composeCoverID(ids)
	if err := store.SetLibraryCover(libraryID, cid, false); err != nil {
		return false
	}
	s.logger().Debug("artwork: updated library cover", "library", libraryID, "coverId", cid)

	return cid != ""
}

func (s *PlayerService) refreshPlaylistCover(playlistID string) bool {
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil || playlistID == "" {
		return false
	}
	ids, err := store.PlaylistCoverIDs(playlistID, 4)
	if err != nil {
		return false
	}
	cid := s.composeCoverID(ids)
	if err := store.SetPlaylistCover(playlistID, cid, false); err != nil {
		return false
	}
	s.logger().Debug("artwork: updated playlist cover", "playlist", playlistID, "coverId", cid)

	return cid != ""
}

// pruneOrphanArtwork evicts oldest unreferenced artwork files above budget once
// on startup while keeping every cover referenced by library.db or the active
// track intact.
func (s *PlayerService) pruneOrphanArtwork() {
	if s.art == nil {
		return
	}
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return
	}

	protected, err := store.ReferencedCoverIDs()
	if err != nil {
		s.logger().Warn("artwork: skipped prune, could not read protected ids", "error", err)

		return
	}
	if s.player != nil {
		if cid := s.snapshotCoverID(s.player.Snapshot()); cid != "" {
			protected[cid] = struct{}{}
		}
	}
	removed, err := s.art.Prune(protected, artwork.DefaultOrphanMaxBytes)
	if err != nil {
		s.logger().Warn("artwork: prune error", "removed", removed, "error", err)

		return
	}
	if removed > 0 {
		s.logger().Info("artwork: pruned unreferenced files", "removed", removed)
	}
}

func (s *PlayerService) registerLocal(row library.Library) *local.Local {
	src := local.New(s.libStore, row, s.art, func(p local.ScanProgress) {
		s.emit(eventLibrary, LibraryProgress{
			LibraryID: p.LibraryID, Done: p.Done, Total: p.Total, State: "scanning",
		})
		s.emitActivity(ActivityEvent{
			ID:        "local-scan:" + p.LibraryID,
			LibraryID: p.LibraryID,
			Kind:      "scan",
			Title:     firstNonEmpty(row.Name, "Local library"),
			Detail:    "Scanning files",
			Done:      p.Done,
			Total:     p.Total,
			State:     "running",
		})
	})
	s.libMu.Lock()
	if s.libs == nil {
		s.libs = map[string]library.Source{}
	}
	s.libs[row.ID] = src
	s.libMu.Unlock()

	return src
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

func newLocalLibraryID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}

	return "local:" + hex.EncodeToString(raw[:]), nil
}

func validateLibraryFields(name, description string) (string, string, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if name == "" {
		return "", "", fmt.Errorf("library: a name is required")
	}
	if utf8.RuneCountInString(name) > maxLibraryNameRunes {
		return "", "", fmt.Errorf("library: name must be %d characters or fewer", maxLibraryNameRunes)
	}
	if utf8.RuneCountInString(description) > maxLibraryDescRunes {
		return "", "", fmt.Errorf("library: description must be %d characters or fewer", maxLibraryDescRunes)
	}

	return name, description, nil
}

// CreateLibrary registers a new virtual local library with name and optional
// description. Content is added afterwards through ImportFolder or ImportYTM.
func (s *PlayerService) CreateLibrary(name, description string) (library.Library, error) {
	name, description, err := validateLibraryFields(name, description)
	if err != nil {
		return library.Library{}, err
	}

	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return library.Library{}, fmt.Errorf("library: store is not available")
	}

	id, err := newLocalLibraryID()
	if err != nil {
		return library.Library{}, fmt.Errorf("library: generate id: %w", err)
	}
	saved, err := store.AddLibrary(library.Library{
		ID:          id,
		Kind:        library.KindLocal,
		Name:        name,
		Description: description,
		Status:      library.StatusReady,
	})
	if err != nil {
		return library.Library{}, err
	}
	s.registerLocal(saved)

	return s.describeLibrary(saved.ID)
}

// UpdateLibrary changes a library's name and optional description.
func (s *PlayerService) UpdateLibrary(id, name, description string) (library.Library, error) {
	name, description, err := validateLibraryFields(name, description)
	if err != nil {
		return library.Library{}, err
	}

	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return library.Library{}, fmt.Errorf("library: store is not available")
	}
	if _, err := store.UpdateLibrary(id, name, description); err != nil {
		return library.Library{}, err
	}

	return s.describeLibrary(id)
}

// ImportFolder adds a folder as one more root of an existing library and
// starts the scan that indexes it; when the scan lands, the folder's tracks
// become one playlist named after the folder. Re-adding a folder the library
// already watches is a no-op. A folder nested in or containing one of the same
// library's roots is refused so files are not indexed twice within one library.
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

	added, err := store.AddRoot(libraryID, abs)
	if err != nil {
		return library.Library{}, err
	}
	if !added {
		// Already watched: nothing new to index, no second playlist.
		return s.describeLibrary(libraryID)
	}
	s.syncWatchedFolderRoots(libraryID)
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

func (s *PlayerService) RemoveLibrary(id string) error {
	s.libMu.Lock()
	store := s.libStore
	active := s.activeLibraryID
	s.libMu.Unlock()
	if store == nil {
		return fmt.Errorf("library: store is not available")
	}

	row, rowErr := store.LibraryRow(id)
	if rowErr == nil && row.CredentialID != "" {
		cs, err := s.credentials()
		if err != nil {
			return err
		}
		if err := cs.Delete(row.CredentialID); err != nil {
			return err
		}
	}

	if s.sources != nil && rowErr == nil {
		if adapter, ok := s.sources.Adapter(row.Kind); ok {
			adapter.CloseLibrary(id)
		}
	}

	s.libMu.Lock()
	old, _ := s.libs[id].(*ytmSource)
	localSyn := s.localSyncers[id]
	delete(s.localSyncers, id)
	watcher := s.localWatcher
	delete(s.libs, id)
	if active == id {
		s.activeLibraryID = ""
	}
	s.libMu.Unlock()
	if old != nil {
		old.syncer.stop()
	}
	if localSyn != nil {
		localSyn.stop()
	}
	if watcher != nil {
		watcher.RemoveLibrary(id)
	}

	return store.RemoveLibrary(id)
}

// RescanLibrary rescans watched folders on a worker and triggers sync for any
// watched remote playlists (or re-syncs a YTM account library).
func (s *PlayerService) RescanLibrary(id string) error {
	src, ok := s.librarySource(id)
	if !ok {
		return fmt.Errorf("library: unknown id %q", id)
	}
	if _, isYTM := src.(*ytmSource); isYTM {
		if _, err := s.ytmManager().sessionClient(id); err != nil {
			switch {
			case errors.Is(err, ytm.ErrSessionExpired):
				return fmt.Errorf("session expired, sign in again to refresh this library")
			case errors.Is(err, ErrNoCredentials):
				return fmt.Errorf("signed out, sign in to refresh this library")
			default:
				return err
			}
		}
		s.triggerYTMSync(id, syncTriggerManual)
		return nil
	}
	if _, isLocal := src.(*local.Local); !isLocal {
		return fmt.Errorf("library: %q cannot refresh", id)
	}

	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return fmt.Errorf("library: store is not available")
	}

	triggered := false
	if watchedRoots, err := store.WatchedRoots(id); err == nil && len(watchedRoots) > 0 {
		if err := s.scanLibrary(id, ""); err != nil {
			return err
		}
		triggered = true
	}
	if hasRemote, err := store.HasWatchedRemotePlaylists(id); err == nil && hasRemote {
		s.triggerLocalRemoteSync(id, syncTriggerManual)
		triggered = true
	}
	if !triggered {
		return fmt.Errorf("no watched folders or playlists to refresh")
	}
	return nil
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

		scanTitle := "Local library"
		scanDetail := "Scanning files"
		if importRoot != "" {
			scanTitle = firstNonEmpty(filepath.Base(importRoot), "Local library")
			scanDetail = "Scanning folder"
		}
		s.emitActivity(ActivityEvent{
			ID:        "local-scan:" + id,
			LibraryID: id,
			Kind:      "scan",
			Title:     scanTitle,
			Detail:    scanDetail,
			State:     "running",
		})

		res, err := scanner.ImportScan(s.ctx, importRoot)
		state := "ready"
		if err != nil {
			state = "error"
			s.log.Warn("library: scan failed", "id", id, "error", err)
			s.emitActivity(ActivityEvent{
				ID:        "local-scan:" + id,
				LibraryID: id,
				Kind:      "scan",
				Title:     scanTitle,
				Detail:    err.Error(),
				State:     "error",
			})
		} else {
			if importRoot != "" && len(res.Refs) > 0 {
				s.createImportPlaylist(id, importRoot, res.Refs)
			}
			s.reconcileWatchedFolderPlaylists(id, importRoot, res.RefsByRoot)
			s.refreshLibraryCover(id)
			s.emitActivity(ActivityEvent{
				ID:        "local-scan:" + id,
				LibraryID: id,
				Kind:      "scan",
				Title:     scanTitle,
				Detail:    fmt.Sprintf("%d tracks", res.Count),
				Done:      res.Count,
				Total:     res.Count,
				State:     "done",
			})
		}
		s.emit(eventLibrary, LibraryProgress{LibraryID: id, Done: res.Count, Total: res.Count, State: state})
	}()

	return nil
}

// reconcileWatchedFolderPlaylists updates entries of watched folder-backed
// playlists after a rescan so newly added or removed files appear immediately.
func (s *PlayerService) reconcileWatchedFolderPlaylists(libraryID, skipRoot string, refsByRoot map[string][]string) {
	if len(refsByRoot) == 0 {
		return
	}
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return
	}
	pls, err := store.WatchedFolderPlaylists(libraryID)
	if err != nil {
		return
	}
	for _, pl := range pls {
		if pl.SourcePath == "" || pl.SourcePath == skipRoot {
			continue
		}
		refs, scanned := refsByRoot[pl.SourcePath]
		if !scanned {
			continue
		}
		if err := store.ReplacePlaylistEntries(pl.ID, refs); err != nil {
			continue
		}
		if !pl.CustomCover {
			s.refreshPlaylistCover(pl.ID)
		}
	}
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

	if existing, ok, err := store.PlaylistBySourcePath(libraryID, root); err == nil && ok {
		if err := store.ReplacePlaylistEntries(existing.ID, refs); err != nil {
			s.log.Warn("library: import playlist", "error", err)
			return
		}
		if !existing.CustomCover {
			s.refreshPlaylistCover(existing.ID)
		}
		return
	}

	playlist, err := store.CreatePlaylistWithSource(libraryID, filepath.Base(root), root, true)
	if err != nil {
		s.log.Warn("library: import playlist", "error", err)

		return
	}
	if err := store.AddToPlaylist(playlist.ID, refs); err != nil {
		s.log.Warn("library: import playlist", "error", err)

		return
	}
	s.refreshPlaylistCover(playlist.ID)
}

// YTMImport is the outcome of one YouTube Music import: how many tracks landed
// in the library, and the playlist row when the hit was a playlist.
type YTMImport struct {
	PlaylistID  string `json:"playlistId,omitempty"`
	Name        string `json:"name"`
	Tracks      int    `json:"tracks"`
	NeedsTarget bool   `json:"needsTarget,omitempty"`
	Duplicate   bool   `json:"duplicate,omitempty"`
}

// ytmCoverWorkers bounds the artwork fetches one import keeps in flight. Each
// cover is a round trip, so a playlist import would otherwise spend most of its
// time waiting on one request at a time.
const ytmCoverWorkers = 6

// ImportYTM copies a YouTube Music hit into a library's catalogue. For local
// libraries it creates a local-only copy; for a KindYTM account library it
// saves playlists directly to YouTube Music and signals NeedsTarget for single
// songs so the UI can ask where the song should go.
func (s *PlayerService) ImportYTM(libraryID string, hit YTMResult) (YTMImport, error) {
	src, ok := s.librarySource(libraryID)
	if !ok {
		return YTMImport{}, fmt.Errorf("library: unknown id %q", libraryID)
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

	if ytmSrc, isYTM := src.(*ytmSource); isYTM {
		if hit.Kind == "Playlist" {
			return ytmSrc.importPlaylist(s.ctx, hit)
		}
		if !hit.Playable {
			return YTMImport{}, fmt.Errorf("ytm: %q is not a track", hit.Title)
		}
		return YTMImport{Name: name, NeedsTarget: true}, nil
	}

	dst, ok := src.(*local.Local)
	if !ok {
		return YTMImport{}, fmt.Errorf("library: %q cannot import YouTube Music", libraryID)
	}

	if hit.Kind != "Playlist" {
		if !hit.Playable {
			return YTMImport{}, fmt.Errorf("ytm: %q is not a track", hit.Title)
		}
		actID := "ytm-import:" + hit.VideoID
		actTitle := firstNonEmpty(name, "YouTube Music song")
		s.emitActivity(ActivityEvent{
			ID:        actID,
			LibraryID: libraryID,
			Kind:      "import",
			Title:     actTitle,
			Detail:    "Importing song",
			State:     "running",
		})
		single := []YTMResult{hit}
		s.enrichYTMResults(s.ctx, single)
		hit = single[0]
		if _, err := s.catalogYTM(dst, hit, s.storeYTMCover(hit.Thumbnail)); err != nil {
			s.emitActivity(ActivityEvent{
				ID:        actID,
				LibraryID: libraryID,
				Kind:      "import",
				Title:     actTitle,
				Detail:    err.Error(),
				State:     "error",
			})
			return YTMImport{}, err
		}
		s.refreshLibraryCover(libraryID)
		s.emit(eventLibrary, LibraryProgress{LibraryID: libraryID, Done: 1, Total: 1, State: "ready"})
		s.emitActivity(ActivityEvent{
			ID:        actID,
			LibraryID: libraryID,
			Kind:      "import",
			Title:     actTitle,
			Detail:    "Added",
			Done:      1,
			Total:     1,
			State:     "done",
		})

		return YTMImport{Name: name, Tracks: 1}, nil
	}

	rid := strings.TrimPrefix(ytmPlaylistID(hit.VideoID), "VL")
	actID := "ytm-import:" + firstNonEmpty(rid, hit.VideoID)
	actTitle := activityPlaylistTitle(name, "Playlist")
	s.emitActivity(ActivityEvent{
		ID:        actID,
		LibraryID: libraryID,
		Kind:      "import",
		Title:     actTitle,
		Detail:    "Loading playlist",
		State:     "running",
	})
	emitLocalPlaylistErr := func(err error) {
		s.emitActivity(ActivityEvent{
			ID:        actID,
			LibraryID: libraryID,
			Kind:      "import",
			Title:     actTitle,
			Detail:    err.Error(),
			State:     "error",
		})
	}

	rows, name, err := s.ytmProv.playlist(s.ctx, ytmPlaylistID(hit.VideoID))
	if err != nil {
		emitLocalPlaylistErr(err)
		return YTMImport{}, err
	}
	// The catalogue's own title wins when it has one: a playlist pasted as an
	// id or a URL reaches here with the palette's placeholder label, and the
	// row should carry the list's real name.
	if name == "" {
		name = strings.TrimSpace(hit.Title)
	}
	actTitle = activityPlaylistTitle(name, "Playlist")
	s.emitActivity(ActivityEvent{
		ID:        actID,
		LibraryID: libraryID,
		Kind:      "import",
		Title:     actTitle,
		Detail:    "Importing tracks",
		Total:     len(rows),
		State:     "running",
	})
	s.enrichYTMResults(s.ctx, rows)
	covers := s.storeYTMCovers(rows)
	refs := make([]string, 0, len(rows))
	for i, row := range rows {
		ref, err := s.catalogYTM(dst, row, covers[i])
		if err != nil {
			emitLocalPlaylistErr(err)
			return YTMImport{}, err
		}
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		err := fmt.Errorf("ytm: %q has no tracks", name)
		emitLocalPlaylistErr(err)
		return YTMImport{}, err
	}
	if name == "" {
		name = "YouTube Music playlist"
	}
	actTitle = name
	playlist, err := dst.CreateRemotePlaylist(name, hit.VideoID)
	if err != nil {
		emitLocalPlaylistErr(err)
		return YTMImport{}, err
	}
	if err := dst.AddToPlaylist(s.ctx, playlist.ID, refs); err != nil {
		emitLocalPlaylistErr(err)
		return YTMImport{}, err
	}
	s.refreshPlaylistCover(playlist.ID)
	s.refreshLibraryCover(libraryID)
	s.registerLocalSyncer(libraryID)
	s.emit(eventLibrary, LibraryProgress{LibraryID: libraryID, Done: len(refs), Total: len(refs), State: "ready"})
	s.emitActivity(ActivityEvent{
		ID:        actID,
		LibraryID: libraryID,
		Kind:      "import",
		Title:     actTitle,
		Detail:    fmt.Sprintf("%d tracks", len(refs)),
		Done:      len(refs),
		Total:     len(refs),
		State:     "done",
	})

	return YTMImport{PlaylistID: playlist.ID, Name: name, Tracks: len(refs)}, nil
}

// enrichYTMResults hydrates missing album, artist, artistId, duration, and
// thumbnail metadata for imported tracks via loadSong so albums and artists
// populate the library immediately without waiting for playback.
func (s *PlayerService) enrichYTMResults(ctx context.Context, rows []YTMResult) {
	if s.ytmProv == nil || len(rows) == 0 {
		return
	}
	loadSong := s.ytmProv.calls().loadSong
	if loadSong == nil {
		return
	}

	var need []int
	for i := range rows {
		r := &rows[i]
		if r.VideoID != "" && (r.Album == "" || r.Artist == "" || r.ArtistID == "" || r.Thumbnail == "") {
			need = append(need, i)
		}
	}
	if len(need) == 0 {
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, ytmHTTPTimeout)
	defer cancel()

	sem := make(chan struct{}, ytmCoverWorkers)
	var wg sync.WaitGroup
	for _, idx := range need {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-reqCtx.Done():
				return
			}
			defer func() { <-sem }()

			song, err := loadSong(reqCtx, rows[i].VideoID)
			if err != nil || song == nil {
				return
			}
			rows[i].Album = firstNonEmpty(rows[i].Album, ytmAlbum(song.Album))
			rows[i].Artist = firstNonEmpty(rows[i].Artist, ytmArtists(song.Artists))
			rows[i].ArtistID = firstNonEmpty(rows[i].ArtistID, ytmFirstArtistID(song.Artists))
			rows[i].Thumbnail = firstNonEmpty(rows[i].Thumbnail, ytmThumb(song.Thumbnail))
			if rows[i].DurationMs == 0 && song.DurationMs > 0 {
				rows[i].DurationMs = song.DurationMs
			}
		}(idx)
	}
	wg.Wait()
}

// storeYTMCover fetches one thumbnail into the art cache and returns its cover id. A
// row without art, or a fetch that fails, returns "": the art is decorative, the
// track is not.
func (s *PlayerService) storeYTMCover(url string) string {
	if url == "" || s.art == nil {
		return ""
	}
	data := s.ytmProv.thumbFor(s.ctx, url)
	if len(data) == 0 {
		return ""
	}
	id, err := s.art.Put(data)
	if err != nil {
		return ""
	}

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
		CoverID:      coverID,
	})

	return dst.ImportRemote(library.KindYTM, sqlite.TrackRow{
		NativeID:     row.VideoID,
		Title:        row.Title,
		Artist:       row.Artist,
		Album:        row.Album,
		DurationMs:   row.DurationMs,
		CoverID:      coverID,
		ThumbnailURL: row.Thumbnail,
		ArtistID:     row.ArtistID,
	})
}

// catalogYTMTrack reads a catalogued YTM row from SQLite so playback, queue
// rows and Discord presence keep their metadata, cached CoverID and remote
// artwork links across restarts.
func (s *PlayerService) catalogYTMTrack(ref string) (ytmTrack, bool) {
	if !isYTMRef(ref) {
		return ytmTrack{}, false
	}
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return ytmTrack{}, false
	}
	tr, ok, err := store.TrackByRef(ref)
	if err != nil || !ok {
		return ytmTrack{}, false
	}
	remote, _, _ := store.TrackRemoteMeta(ref)

	return ytmTrack{
		ID:           tr.NativeID,
		Title:        tr.Title,
		Artist:       tr.Artist,
		Album:        tr.Album,
		DurationMs:   tr.DurationMs,
		CoverID:      tr.CoverID,
		ThumbnailURL: remote.ThumbnailURL,
		ArtistID:     remote.ArtistID,
	}, true
}

// catalogLocalTrack reads a catalogued local track's tags and CoverID from
// SQLite so queue indexing and transient track-switch snapshots do not wait for
// asynchronous file tag parsing when the track is already in a library.
func (s *PlayerService) catalogLocalTrack(path string) (trackTags, bool) {
	if path == "" || isYTMRef(path) {
		return trackTags{}, false
	}
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return trackTags{}, false
	}
	tr, ok, err := store.TrackByRef(path)
	if err != nil || !ok {
		return trackTags{}, false
	}

	return trackTags{
		Title:   tr.Title,
		Artist:  tr.Artist,
		Album:   tr.Album,
		CoverID: tr.CoverID,
	}, true
}

// backfillYTMTrackMeta persists hydrated remote metadata onto pre-migration
// catalogued tracks so later restarts do not need another song lookup.
func (s *PlayerService) backfillYTMTrackMeta(ref, thumbnailURL, artistID, album string) {
	if !isYTMRef(ref) {
		return
	}
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return
	}
	if err := store.BackfillTrackRemoteMeta(ref, thumbnailURL, artistID, album); err != nil {
		s.logger().Debug("library: backfill remote meta failed", "ref", ref, "error", err)
	}
}

func (s *PlayerService) resolveRemoteLikeTarget(path string) (library.Likable, string, bool) {
	kind, libID, nativeID, ok := library.ParseRemoteRef(path)
	if !ok || kind != library.KindYTM || nativeID == "" {
		return nil, "", false
	}
	if libID == "" {
		activeID := s.activeLibrary()
		src, exists := s.librarySource(activeID)
		if !exists {
			return nil, "", false
		}
		if _, isYTM := src.(*ytmSource); !isYTM {
			return nil, "", false
		}
		libID = activeID
	}
	src, exists := s.librarySource(libID)
	if !exists {
		return nil, "", false
	}
	likable, ok := src.(library.Likable)
	if !ok {
		return nil, "", false
	}
	return likable, library.ScopedRemoteRef(kind, libID, nativeID), true
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
	target := path
	if _, scopedRef, ok := s.resolveRemoteLikeTarget(path); ok {
		target = scopedRef
	}
	liked, err := store.Liked(target)
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
	if likable, scopedRef, ok := s.resolveRemoteLikeTarget(path); ok {
		cur, err := store.Liked(scopedRef)
		if err != nil {
			return false, err
		}
		next := !cur
		if err := likable.SetLiked(s.ctx, scopedRef, next); err != nil {
			return cur, err
		}
		return next, nil
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

type albumArtistSource interface {
	AlbumTracks(ctx context.Context, album, artist string) ([]library.Track, error)
	ArtistTracks(ctx context.Context, artist string) ([]library.Track, error)
}

// LibraryAlbumTracks reads one album's tracks in order.
func (s *PlayerService) LibraryAlbumTracks(id, album, artist string) ([]library.Track, error) {
	src, ok := s.librarySource(id)
	if !ok {
		return nil, fmt.Errorf("library: unknown id %q", id)
	}
	if reader, ok := src.(albumArtistSource); ok {
		return reader.AlbumTracks(s.ctx, album, artist)
	}

	return nil, fmt.Errorf("library: %q cannot list an album", id)
}

// LibraryArtistTracks reads one artist's tracks in order.
func (s *PlayerService) LibraryArtistTracks(id, artist string) ([]library.Track, error) {
	src, ok := s.librarySource(id)
	if !ok {
		return nil, fmt.Errorf("library: unknown id %q", id)
	}
	if reader, ok := src.(albumArtistSource); ok {
		return reader.ArtistTracks(s.ctx, artist)
	}

	return nil, fmt.Errorf("library: %q cannot list an artist", id)
}

// PlayRefs replaces the queue with refs and starts the first. It is the library
// "play this album" action; playing one row passes a single ref.
func (s *PlayerService) PlayRefs(refs []string) error {
	return s.command(func() error { return s.player.PlayQueue(refs) })
}

// QueueRefsNext inserts refs after the active track, or starts playback if idle.
func (s *PlayerService) QueueRefsNext(refs []string) error {
	refs = queuedRefs(refs)
	if len(refs) == 0 {
		return nil
	}

	return s.command(func() error {
		snap := s.player.Snapshot()
		if snap.QueueIndex >= 0 && (snap.State == molo.Playing || snap.State == molo.Paused) {
			return s.player.InsertQueue(snap.QueueIndex+1, refs)
		}

		return s.player.InsertQueueAndPlay(0, refs)
	})
}

// QueueRefsLast appends refs to the end of the queue without interrupting playback.
func (s *PlayerService) QueueRefsLast(refs []string) error {
	refs = queuedRefs(refs)
	if len(refs) == 0 {
		return nil
	}

	return s.command(func() error {
		return s.player.InsertQueue(len(s.player.Queue()), refs)
	})
}

// queuedRefs filters empty refs (missing local files) so InsertQueue does not fail.
func queuedRefs(refs []string) []string {
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref != "" {
			out = append(out, ref)
		}
	}

	return out
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

	if err := writer.AddToPlaylist(s.ctx, playlistID, refs); err != nil {
		return err
	}
	s.refreshPlaylistCover(playlistID)

	return nil
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
	if err := writer.RemoveFromPlaylist(s.ctx, playlistID, refs); err != nil {
		return err
	}
	s.refreshPlaylistCover(playlistID)

	return nil
}

// RenamePlaylist changes a playlist's name, rejecting blank input.
func (s *PlayerService) RenamePlaylist(libraryID, playlistID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("playlist: a name is required")
	}
	src, ok := s.librarySource(libraryID)
	if !ok {
		return fmt.Errorf("library: unknown id %q", libraryID)
	}
	writer, ok := src.(library.PlaylistWriter)
	if !ok {
		return fmt.Errorf("library: %q cannot edit playlists", libraryID)
	}

	return writer.RenamePlaylist(s.ctx, playlistID, name)
}

// DeletePlaylist removes a playlist and its entries while leaving catalogue tracks intact.
func (s *PlayerService) DeletePlaylist(libraryID, playlistID string) error {
	src, ok := s.librarySource(libraryID)
	if !ok {
		return fmt.Errorf("library: unknown id %q", libraryID)
	}
	writer, ok := src.(library.PlaylistWriter)
	if !ok {
		return fmt.Errorf("library: %q cannot edit playlists", libraryID)
	}

	return writer.DeletePlaylist(s.ctx, playlistID)
}

// SetPlaylistWatched toggles automatic watching for a folder-backed or
// remote-backed playlist inside a local library.
func (s *PlayerService) SetPlaylistWatched(libraryID, playlistID string, watched bool) (library.Playlist, error) {
	src, ok := s.librarySource(libraryID)
	if !ok {
		return library.Playlist{}, fmt.Errorf("library: unknown id %q", libraryID)
	}
	if _, isLocal := src.(*local.Local); !isLocal {
		return library.Playlist{}, fmt.Errorf("library: %q does not support playlist watch toggles", libraryID)
	}

	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return library.Playlist{}, fmt.Errorf("library: store is not available")
	}

	pl, err := store.SetPlaylistWatched(libraryID, playlistID, watched)
	if err != nil {
		return library.Playlist{}, err
	}
	if pl.SourcePath != "" {
		s.syncWatchedFolderRoots(libraryID)
		if watched {
			_ = s.scanLibrary(libraryID, "")
		}
	}
	if pl.RemoteID != "" {
		s.registerLocalSyncer(libraryID)
		if watched {
			s.triggerLocalRemoteSync(libraryID, syncTriggerManual)
		}
	}
	s.emit(eventLibrary, LibraryProgress{LibraryID: libraryID, State: "ready"})
	return pl, nil
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

	return s.romanizeLyrics(s.instrumentalMarks(path, doc)), nil
}

// readLyricsOrJoin checks disk first, then joins any active or completed prefetch.
func (s *PlayerService) readLyricsOrJoin(path string) (*lyrics.Document, error) {
	if doc, err := s.readLyrics(path); doc != nil || err != nil {
		return doc, err
	}
	// Ensure a prefetch is armed if Lyrics() arrived before the next pump tick.
	if s.player != nil {
		if snap := s.Snapshot(); snap.Path == path {
			s.syncLyrics(snap)
		}
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
	doc := &lyrics.Document{Synced: sheet.Synced, Source: "ytmusic", Provider: "ytmusic"}
	for _, line := range sheet.Lines {
		if line.Text == "" || line.Text == "♪" {
			continue
		}
		doc.Lines = append(doc.Lines, lyrics.Line{TimeMs: line.StartMs, Text: line.Text})
	}

	return doc
}

// CoverAccents returns the ranked accent palette for coverID, backed by the
// shared artwork store so tracks sharing album art reuse the extracted palette.
func (s *PlayerService) CoverAccents(coverID string) ([]color.Colour, error) {
	if coverID == "" || s.art == nil {
		return nil, nil
	}
	if !s.art.Has(coverID) && s.player != nil {
		if data := s.player.Snapshot().Meta.Tags.Cover; artwork.ID(data) == coverID {
			_, _ = s.art.Put(data)
		}
	}

	return s.art.Accents(coverID, 5)
}
