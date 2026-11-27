package backend

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dlcuy22/molo"
	"github.com/dlcuy22/molow/internal/library/sqlite"
	"github.com/fsnotify/fsnotify"
)

const localWatchDebounce = 750 * time.Millisecond

// localFolderWatcher monitors watched local library folders with fsnotify and
// triggers a debounced rescan for any library whose tree changes.
type localFolderWatcher struct {
	watcher  *fsnotify.Watcher
	debounce time.Duration
	onChange func(libraryID string)
	log      *slog.Logger
	exts     map[string]bool

	mu         sync.Mutex
	closed     bool
	rootsByLib map[string][]string
	dirLibs    map[string]map[string]struct{}
	timers     map[string]*time.Timer

	closeOnce sync.Once
	doneCh    chan struct{}
}

func newLocalFolderWatcher(debounce time.Duration, onChange func(string), log *slog.Logger) (*localFolderWatcher, error) {
	if debounce <= 0 {
		debounce = localWatchDebounce
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	exts := make(map[string]bool)
	for _, ext := range molo.SupportedExtensions() {
		exts[strings.ToLower(ext)] = true
	}
	lw := &localFolderWatcher{
		watcher:    w,
		debounce:   debounce,
		onChange:   onChange,
		log:        log,
		exts:       exts,
		rootsByLib: make(map[string][]string),
		dirLibs:    make(map[string]map[string]struct{}),
		timers:     make(map[string]*time.Timer),
		doneCh:     make(chan struct{}),
	}
	go lw.loop()
	return lw, nil
}

// SyncRoots replaces the set of watched root folders for libraryID and updates
// underlying fsnotify directory watches accordingly.
func (w *localFolderWatcher) SyncRoots(libraryID string, roots []string) {
	if w == nil || libraryID == "" {
		return
	}

	cleaned := make([]string, 0, len(roots))
	dirs := make(map[string]struct{})
	for _, root := range roots {
		r := filepath.Clean(strings.TrimSpace(root))
		if r == "" {
			continue
		}
		cleaned = append(cleaned, r)
		_ = filepath.WalkDir(r, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d == nil || !d.IsDir() {
				return nil
			}
			name := d.Name()
			if path != r && (strings.HasPrefix(name, ".") || name == "@eaDir") {
				return fs.SkipDir
			}
			dirs[filepath.Clean(path)] = struct{}{}
			return nil
		})
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}

	if len(cleaned) == 0 {
		delete(w.rootsByLib, libraryID)
		if t := w.timers[libraryID]; t != nil {
			t.Stop()
			delete(w.timers, libraryID)
		}
	} else {
		w.rootsByLib[libraryID] = cleaned
	}

	// Remove libraryID from directories it no longer watches.
	for dir, libs := range w.dirLibs {
		if _, keep := dirs[dir]; keep {
			continue
		}
		if _, had := libs[libraryID]; !had {
			continue
		}
		delete(libs, libraryID)
		if len(libs) == 0 {
			delete(w.dirLibs, dir)
			_ = w.watcher.Remove(dir)
		}
	}

	// Add directories newly watched by libraryID.
	for dir := range dirs {
		libs := w.dirLibs[dir]
		if libs == nil {
			if err := w.watcher.Add(dir); err != nil {
				if w.log != nil {
					w.log.Debug("library: watch dir failed", "dir", dir, "error", err)
				}
				continue
			}
			libs = make(map[string]struct{})
			w.dirLibs[dir] = libs
		}
		libs[libraryID] = struct{}{}
	}
}

func (w *localFolderWatcher) RemoveLibrary(libraryID string) {
	w.SyncRoots(libraryID, nil)
}

func (w *localFolderWatcher) Close() {
	if w == nil {
		return
	}
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		for _, t := range w.timers {
			t.Stop()
		}
		w.timers = make(map[string]*time.Timer)
		w.mu.Unlock()
		_ = w.watcher.Close()
		<-w.doneCh
	})
}

func (w *localFolderWatcher) loop() {
	defer close(w.doneCh)
	for {
		select {
		case ev, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(ev)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			if w.log != nil {
				w.log.Debug("library: watcher error", "error", err)
			}
		}
	}
}

func (w *localFolderWatcher) handleEvent(ev fsnotify.Event) {
	if ev.Op == fsnotify.Chmod || ev.Name == "" {
		return
	}
	path := filepath.Clean(ev.Name)
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") || base == "@eaDir" {
		return
	}

	if ev.Has(fsnotify.Create) {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			w.addCreatedTree(path)
			return
		}
	}

	isAudio := w.exts[strings.ToLower(filepath.Ext(path))]
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}

	// Non-audio regular file writes do not affect the catalogue unless a watched
	// directory itself was removed or renamed.
	_, wasDir := w.dirLibs[path]
	if !isAudio && !wasDir && !(ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename)) {
		return
	}
	if !isAudio && (ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename)) && filepath.Ext(path) != "" && !wasDir {
		return
	}

	if wasDir && (ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename)) {
		delete(w.dirLibs, path)
		_ = w.watcher.Remove(path)
	}

	for libID, roots := range w.rootsByLib {
		for _, root := range roots {
			if insidePath(path, root) {
				w.scheduleLocked(libID)
				break
			}
		}
	}
}

func (w *localFolderWatcher) addCreatedTree(dirPath string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}

	var matchingLibs []string
	for libID, roots := range w.rootsByLib {
		for _, root := range roots {
			if insidePath(dirPath, root) {
				matchingLibs = append(matchingLibs, libID)
				break
			}
		}
	}
	if len(matchingLibs) == 0 {
		return
	}

	_ = filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || !d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || name == "@eaDir" {
			return fs.SkipDir
		}
		clean := filepath.Clean(path)
		libs := w.dirLibs[clean]
		if libs == nil {
			if err := w.watcher.Add(clean); err != nil {
				return nil
			}
			libs = make(map[string]struct{})
			w.dirLibs[clean] = libs
		}
		for _, libID := range matchingLibs {
			libs[libID] = struct{}{}
		}
		return nil
	})

	for _, libID := range matchingLibs {
		w.scheduleLocked(libID)
	}
}

func (w *localFolderWatcher) scheduleLocked(libraryID string) {
	if w.onChange == nil {
		return
	}
	if t, ok := w.timers[libraryID]; ok {
		t.Reset(w.debounce)
		return
	}
	w.timers[libraryID] = time.AfterFunc(w.debounce, func() {
		w.mu.Lock()
		closed := w.closed
		delete(w.timers, libraryID)
		w.mu.Unlock()
		if !closed {
			w.onChange(libraryID)
		}
	})
}

func (s *PlayerService) ensureLocalWatcher() *localFolderWatcher {
	s.libMu.Lock()
	w := s.localWatcher
	s.libMu.Unlock()
	if w != nil {
		return w
	}

	created, err := newLocalFolderWatcher(localWatchDebounce, func(libraryID string) {
		_ = s.scanLibrary(libraryID, "")
	}, s.logger())
	if err != nil {
		s.logger().Warn("library: start folder watcher", "error", err)
		return nil
	}

	s.libMu.Lock()
	if s.localWatcher != nil {
		w = s.localWatcher
		s.libMu.Unlock()
		created.Close()
		return w
	}
	s.localWatcher = created
	s.libMu.Unlock()
	return created
}

func (s *PlayerService) syncWatchedFolderRoots(libraryID string) {
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return
	}
	w := s.ensureLocalWatcher()
	if w == nil {
		return
	}
	roots, err := store.WatchedRoots(libraryID)
	if err != nil {
		return
	}
	w.SyncRoots(libraryID, roots)
}

func (s *PlayerService) registerLocalSyncer(libraryID string) *ytmSyncer {
	s.libMu.Lock()
	if s.localSyncers == nil {
		s.localSyncers = make(map[string]*ytmSyncer)
	}
	if existing := s.localSyncers[libraryID]; existing != nil {
		s.libMu.Unlock()
		return existing
	}
	store := s.libStore
	s.libMu.Unlock()

	syn := newYTMSyncer(s.ctx, ytmSyncerConfig{
		id: libraryID,
		lastSyncFn: func() int64 {
			if store == nil {
				return 0
			}
			at, _ := store.SyncTime(libraryID, "local_remote_playlists")
			return at
		},
		canSyncFn: func() bool {
			if store == nil || s.ytmProv == nil {
				return false
			}
			ok, err := store.HasWatchedRemotePlaylists(libraryID)
			return err == nil && ok
		},
		runFn: func(ctx context.Context) error {
			return s.syncLocalWatchedRemotePlaylists(ctx, libraryID)
		},
		onErrorFn: func() {
			s.emit(eventLibrary, LibraryProgress{LibraryID: libraryID, State: "error"})
		},
	})

	s.libMu.Lock()
	if existing := s.localSyncers[libraryID]; existing != nil {
		s.libMu.Unlock()
		syn.stop()
		return existing
	}
	s.localSyncers[libraryID] = syn
	s.libMu.Unlock()
	return syn
}

func (s *PlayerService) triggerLocalRemoteSync(libraryID string, kind syncTrigger) {
	syn := s.registerLocalSyncer(libraryID)
	if syn != nil {
		syn.trigger(kind)
	}
}

func (s *PlayerService) stopLocalSyncers() {
	s.libMu.Lock()
	var syncers []*ytmSyncer
	for _, syn := range s.localSyncers {
		if syn != nil {
			syncers = append(syncers, syn)
		}
	}
	s.localSyncers = make(map[string]*ytmSyncer)
	w := s.localWatcher
	s.localWatcher = nil
	s.libMu.Unlock()

	for _, syn := range syncers {
		syn.stop()
	}
	if w != nil {
		w.Close()
	}
}

func (s *PlayerService) syncLocalWatchedRemotePlaylists(ctx context.Context, libraryID string) error {
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil || s.ytmProv == nil {
		return nil
	}
	if _, ok := s.librarySource(libraryID); !ok {
		return nil
	}

	watched, err := store.WatchedRemotePlaylists(libraryID)
	if err != nil || len(watched) == 0 {
		return err
	}

	var firstErr error
	for _, pl := range watched {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		actID := "local-ytm-sync:" + pl.ID
		actTitle := activityPlaylistTitle(pl.Name, "Playlist")
		s.emitActivity(ActivityEvent{
			ID:        actID,
			LibraryID: libraryID,
			Kind:      "sync",
			Title:     actTitle,
			Detail:    "Syncing playlist",
			State:     "running",
		})

		rows, remoteName, err := s.ytmProv.playlist(ctx, ytmPlaylistID(pl.RemoteID))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			s.emitActivity(ActivityEvent{
				ID:        actID,
				LibraryID: libraryID,
				Kind:      "sync",
				Title:     actTitle,
				Detail:    err.Error(),
				State:     "error",
			})
			continue
		}

		if remoteName = strings.TrimSpace(remoteName); remoteName != "" && remoteName != pl.Name {
			_ = store.RenamePlaylist(libraryID, pl.ID, remoteName)
			actTitle = remoteName
		}

		s.enrichYTMResults(ctx, rows)
		covers := s.storeYTMCovers(rows)
		refs := make([]string, 0, len(rows))
		for i, row := range rows {
			if row.VideoID == "" {
				continue
			}
			if s.ytm != nil {
				s.ytm.put(ytmTrack{
					ID:           row.VideoID,
					Title:        row.Title,
					Artist:       row.Artist,
					Album:        row.Album,
					DurationMs:   row.DurationMs,
					ThumbnailURL: row.Thumbnail,
					ArtistID:     row.ArtistID,
					CoverID:      covers[i],
				})
			}
			if _, err := store.UpsertRemoteTrack(libraryID, "ytm", sqlite.TrackRow{
				NativeID:     row.VideoID,
				Title:        row.Title,
				Artist:       row.Artist,
				Album:        row.Album,
				DurationMs:   row.DurationMs,
				CoverID:      covers[i],
				ThumbnailURL: row.Thumbnail,
				ArtistID:     row.ArtistID,
			}); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				s.emitActivity(ActivityEvent{
					ID:        actID,
					LibraryID: libraryID,
					Kind:      "sync",
					Title:     actTitle,
					Detail:    err.Error(),
					State:     "error",
				})
				refs = nil
				break
			}
			refs = append(refs, "ytm:"+row.VideoID)
		}
		if refs == nil && len(rows) > 0 {
			continue
		}
		if err := store.ReplacePlaylistEntries(pl.ID, refs); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			s.emitActivity(ActivityEvent{
				ID:        actID,
				LibraryID: libraryID,
				Kind:      "sync",
				Title:     actTitle,
				Detail:    err.Error(),
				State:     "error",
			})
			continue
		}
		if !pl.CustomCover {
			s.refreshPlaylistCover(pl.ID)
		}
		s.emitActivity(ActivityEvent{
			ID:        actID,
			LibraryID: libraryID,
			Kind:      "sync",
			Title:     actTitle,
			Detail:    fmt.Sprintf("%d tracks", len(refs)),
			Done:      len(refs),
			Total:     len(refs),
			State:     "done",
		})
	}

	s.refreshLibraryCover(libraryID)
	_ = store.SetSyncTime(libraryID, "local_remote_playlists", time.Now().Unix())
	s.emit(eventLibrary, LibraryProgress{LibraryID: libraryID, State: "ready"})
	return firstErr
}
