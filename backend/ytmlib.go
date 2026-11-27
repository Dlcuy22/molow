package backend

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/sqlite"
	ytm "github.com/dlcuy22/ytm-go"
)

const (
	ytmLikedPlaylistRemoteID = "LM"
	ytmTickInterval          = 15 * time.Minute
	ytmFocusStaleAge         = 5 * time.Minute
	ytmOpenStaleAge          = 10 * time.Minute
	ytmTracksStaleAge        = 1 * time.Hour
	ytmBackoffInitial        = 1 * time.Minute
	ytmBackoffMax            = 1 * time.Hour
	ytmTombstoneTTL          = 10 * time.Minute
	ytmHydrateWorkers        = 2
	ytmMaxLikedSongs         = 1000
	ytmMaxPlaylistPages      = 20
)

type ytmAccountOps struct {
	getAccounts             func(context.Context) ([]ytm.Account, error)
	getLikedSongs           func(context.Context) ([]ytm.Song, *ytm.BuiltInContinuation, error)
	getLikedSongsCont       func(context.Context, string) ([]ytm.Song, *ytm.BuiltInContinuation, error)
	getLibraryPlaylists     func(context.Context) ([]ytm.Playlist, *ytm.BuiltInContinuation, error)
	getLibraryPlaylistsCont func(context.Context, string) ([]ytm.Playlist, *ytm.BuiltInContinuation, error)
	loadPlaylist            func(context.Context, string) ([]YTMResult, string, error)
	setSongLiked            func(context.Context, string, ytm.SongLikedStatus) error
	setPlaylistSaved        func(context.Context, string, bool) error
	createPlaylist          func(context.Context, ytm.CreatePlaylistOptions) (string, error)
	addSongsToPlaylist      func(context.Context, string, []string, ytm.DedupeMode) error
	loadPlaylistForEdit     func(context.Context, string) (*ytm.Playlist, error)
	editPlaylist            func(context.Context, string, []ytm.PlaylistEditAction) error
	deletePlaylist          func(context.Context, string) error
	getAddToPlaylistTargets func(context.Context, string) ([]ytm.AddToPlaylistTarget, error)
}

func (s *PlayerService) ytmAccountClient(libraryID string) (*ytm.Client, error) {
	return s.ytmManager().accountClient(libraryID)
}

func (s *PlayerService) accountOps(client *ytm.Client) ytmAccountOps {
	ops := ytmAccountOps{
		loadPlaylist: func(ctx context.Context, id string) ([]YTMResult, string, error) {
			p := s.ytmManager().activeProvider()
			if p == nil {
				return nil, "", fmt.Errorf("ytm: provider unavailable")
			}
			if client != nil {
				return p.playlistWith(ctx, id, callsFromClient(client, s.logger()))
			}
			return p.playlist(ctx, id)
		},
	}
	if client != nil {
		ops.getAccounts = client.GetAccounts
		ops.getLikedSongs = client.GetLikedSongs
		ops.getLikedSongsCont = client.GetLikedSongsContinuation
		ops.getLibraryPlaylists = client.GetLibraryPlaylists
		ops.getLibraryPlaylistsCont = client.GetLibraryPlaylistsContinuation
		ops.setSongLiked = client.SetSongLiked
		ops.setPlaylistSaved = client.SetPlaylistSaved
		ops.createPlaylist = client.CreatePlaylist
		ops.addSongsToPlaylist = client.AddSongsToPlaylistOpts
		ops.loadPlaylistForEdit = func(ctx context.Context, id string) (*ytm.Playlist, error) {
			return client.LoadPlaylist(ctx, id, nil, nil, nil, false)
		}
		ops.editPlaylist = client.EditPlaylist
		ops.deletePlaylist = client.DeleteAccountPlaylist
		ops.getAddToPlaylistTargets = client.GetAddToPlaylistTargets
	}
	if s.ytmOpsOverride != nil {
		s.ytmOpsOverride(&ops)
	}
	return ops
}

func ytmLibraryID(channelID string) string {
	id := strings.TrimSpace(channelID)
	if id == "" {
		return "ytm_default"
	}
	return "ytm_" + id
}

func isYTMBuiltInLikedID(remoteID string) bool {
	return remoteID == ytmLikedPlaylistRemoteID || remoteID == "VLLM"
}

// ytmSource adapts a signed-in YouTube Music account and its SQLite mirror to
// the shared library.Source contract.
type ytmSource struct {
	svc    *PlayerService
	store  *sqlite.Store
	id     string
	syncer *ytmSyncer

	hydrateMu sync.Mutex
	inFlight  map[string]chan struct{}
}

func newYTMSource(svc *PlayerService, store *sqlite.Store, id string) *ytmSource {
	src := &ytmSource{
		svc:      svc,
		store:    store,
		id:       id,
		inFlight: make(map[string]chan struct{}),
	}
	if svc != nil {
		src.syncer = newYTMSyncer(src.syncCtx(nil), ytmSyncerConfig{
			id: id,
			lastSyncFn: func() int64 {
				if src.store == nil {
					return 0
				}
				at, _ := src.store.SyncTime(id, "playlists")
				return at
			},
			canSyncFn: func() bool {
				if src.isExpired() {
					return false
				}
				_, err := svc.ytmAccountClient(id)
				return err == nil || svc.ytmOpsOverride != nil
			},
			runFn: func(ctx context.Context) error {
				return svc.runYTMLibrarySync(ctx, id)
			},
			onErrorFn: func() {
				svc.emit(eventLibrary, LibraryProgress{LibraryID: id, State: "error"})
			},
		})
	}
	return src
}

func (s *PlayerService) registerYTM(row library.Library) *ytmSource {
	s.libMu.Lock()
	old, _ := s.libs[row.ID].(*ytmSource)
	s.libMu.Unlock()
	if old != nil {
		old.syncer.stop()
	}

	src := newYTMSource(s, s.libStore, row.ID)
	s.libMu.Lock()
	s.libs[row.ID] = src
	s.libMu.Unlock()
	return src
}

func (s *PlayerService) stopYTMSyncers() {
	s.libMu.Lock()
	var syncers []*ytmSyncer
	for _, srcAny := range s.libs {
		if src, ok := srcAny.(*ytmSource); ok && src.syncer != nil {
			syncers = append(syncers, src.syncer)
		}
	}
	s.libMu.Unlock()
	for _, syn := range syncers {
		syn.stop()
	}
}

func (s *PlayerService) triggerYTMSync(libraryID string, kind syncTrigger) {
	if src, ok := s.librarySource(libraryID); ok {
		if ytmSrc, ok := src.(*ytmSource); ok && ytmSrc.syncer != nil {
			ytmSrc.syncer.trigger(kind)
		}
	}
}

// notifyWindowFocused triggers a staleness-gated pull for signed-in YTM
// libraries and watched remote playlists in local libraries when the app window
// regains focus.
func (s *PlayerService) notifyWindowFocused() {
	s.libMu.Lock()
	var syncers []*ytmSyncer
	for _, srcAny := range s.libs {
		if src, ok := srcAny.(*ytmSource); ok && src.syncer != nil {
			syncers = append(syncers, src.syncer)
		}
	}
	for _, syn := range s.localSyncers {
		if syn != nil {
			syncers = append(syncers, syn)
		}
	}
	s.libMu.Unlock()
	for _, syn := range syncers {
		syn.trigger(syncTriggerFocus)
	}
}

// ensureYTMLibrary upserts the KindYTM library row for creds, stores its
// encrypted credentials, and registers its source runtime.
func (s *PlayerService) ensureYTMLibrary(creds *Credentials) (string, error) {
	if creds == nil {
		return "", ErrNoCredentials
	}
	s.libMu.Lock()
	store := s.libStore
	s.libMu.Unlock()
	if store == nil {
		return "", fmt.Errorf("library: store is not available")
	}

	id := ytmLibraryID(creds.ChannelID)
	credID := ""
	if existing, err := store.LibraryRow(id); err == nil && existing.CredentialID != "" {
		credID = existing.CredentialID
	} else if cid, err := newCredentialID(); err == nil {
		credID = cid
	}
	cs, csErr := s.credentials()
	if csErr == nil && credID != "" {
		if err := cs.Save(credID, library.KindYTM, creds); err != nil {
			return "", err
		}
	}

	name := firstNonEmpty(strings.TrimSpace(creds.Name), strings.TrimSpace(creds.Email), "YouTube Music")
	row, err := store.AddLibraryRow(sqlite.LibraryRow{
		Library: library.Library{
			ID:          id,
			Kind:        library.KindYTM,
			Name:        name,
			Description: "YouTube Music account",
			Status:      library.StatusReady,
		},
		AccountID:    creds.ChannelID,
		CredentialID: credID,
		Config:       encodeYTMConfig(creds),
	})
	if err != nil {
		return "", err
	}
	_, _, _ = s.ytmManager().installSessionCredentials(row, creds)
	s.registerYTM(row.Library)
	return row.ID, nil
}

func (s *PlayerService) isScanningLibrary(id string) bool {
	s.libMu.Lock()
	defer s.libMu.Unlock()
	return s.scanning[id]
}

func (y *ytmSource) syncCtx(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	if y.svc != nil && y.svc.ctx != nil {
		return y.svc.ctx
	}
	return context.Background()
}

// CanRefresh reports that a YTM account library can always manually sync.
func (y *ytmSource) CanRefresh() bool { return true }

func (y *ytmSource) session() *ytmSession {
	if y == nil || y.svc == nil {
		return nil
	}
	return y.svc.ytmManager().session(y.id)
}

func (y *ytmSource) isExpired() bool {
	sess := y.session()
	return sess != nil && sess.authExpired.Load()
}

func (y *ytmSource) markExpired() {
	if sess := y.session(); sess != nil {
		sess.authExpired.Store(true)
	}
}

func (y *ytmSource) Describe(context.Context) (library.Library, error) {
	lib, err := y.store.Library(y.id)
	if err != nil {
		return library.Library{ID: y.id, Kind: library.KindYTM}, err
	}
	lib.Kind = library.KindYTM
	lib.Capabilities = library.CapabilitiesOf(y)

	sess := y.session()
	var (
		signedIn   bool
		sessionErr string
	)
	if sess != nil {
		sess.mu.RLock()
		signedIn = sess.signedIn
		sessionErr = sess.sessionErr
		sess.mu.RUnlock()
	}

	switch {
	case y.svc.isScanningLibrary(y.id):
		lib.Status = library.StatusScanning
	case sess != nil && sess.authExpired.Load():
		lib.Status = library.StatusExpired
		lib.Message = "YouTube Music session expired. Sign in again to sync or edit."
	case sessionErr != "":
		lib.Status = library.StatusError
		lib.Message = sessionErr
	case !signedIn:
		lib.Status = library.StatusSignedOut
		lib.Message = "Signed out. Browsing cached library."
	default:
		if until, lastErr := y.syncer.backoffStatus(); lastErr != nil && until.After(time.Now()) {
			mins := int(math.Ceil(until.Sub(time.Now()).Minutes()))
			if mins < 1 {
				mins = 1
			}
			lib.Status = library.StatusError
			lib.Message = fmt.Sprintf("Couldn't sync with YouTube Music · retrying in %d min", mins)
		} else {
			lib.Status = library.StatusReady
		}
	}
	return lib, nil
}

func (y *ytmSource) Feed(ctx context.Context, tab, cursor string, limit int) (library.Page, error) {
	offset, _ := strconv.Atoi(cursor)
	if limit <= 0 {
		limit = 60
	}

	switch {
	case tab == "all":
		y.maybeRefreshIfStale()
		playlists, err := y.store.Playlists(y.id)
		if err != nil {
			return library.Page{}, err
		}
		albums, total, err := y.store.Albums(y.id, limit, offset)
		if err != nil {
			return library.Page{}, err
		}
		shelves := make([]library.Shelf, 0, 2)
		if len(playlists) > 0 {
			shelves = append(shelves, library.Shelf{
				ID: "playlists", Title: "Playlists", Layout: "grid", Kind: "playlists",
				Playlists: playlists, Total: len(playlists),
			})
		}
		shelves = append(shelves, library.Shelf{
			ID: "albums", Title: "Albums", Layout: "grid", Kind: "albums",
			Albums: albums, Total: total, Cursor: ytmNextCursor(offset+len(albums), total),
		})
		return library.Page{Shelves: shelves}, nil

	case tab == "" || tab == "home":
		return y.home(limit)

	case tab == "tracks":
		items, total, err := y.store.Tracks(y.id, limit, offset)
		if err != nil {
			return library.Page{}, err
		}
		return library.Page{Shelves: []library.Shelf{{
			ID: "tracks", Title: "Songs", Layout: "list", Kind: "tracks",
			Items: items, Total: total, Cursor: ytmNextCursor(offset+len(items), total),
		}}}, nil

	case tab == "albums":
		albums, total, err := y.store.Albums(y.id, limit, offset)
		if err != nil {
			return library.Page{}, err
		}
		return library.Page{Shelves: []library.Shelf{{
			ID: "albums", Title: "Albums", Layout: "grid", Kind: "albums",
			Albums: albums, Total: total, Cursor: ytmNextCursor(offset+len(albums), total),
		}}}, nil

	case tab == "artists":
		artists, total, err := y.store.Artists(y.id, limit, offset)
		if err != nil {
			return library.Page{}, err
		}
		return library.Page{Shelves: []library.Shelf{{
			ID: "artists", Title: "Artists", Layout: "grid", Kind: "artists",
			Artists: artists, Total: total, Cursor: ytmNextCursor(offset+len(artists), total),
		}}}, nil

	case tab == "playlists":
		list, err := y.store.Playlists(y.id)
		if err != nil {
			return library.Page{}, err
		}
		return library.Page{Shelves: []library.Shelf{{
			ID: "playlists", Title: "Playlists", Layout: "grid", Kind: "playlists",
			Playlists: list, Total: len(list),
		}}}, nil

	case strings.HasPrefix(tab, "playlist:"):
		id := strings.TrimPrefix(tab, "playlist:")
		_ = y.ensurePlaylistHydrated(ctx, id)
		total, err := y.store.PlaylistTrackCount(id)
		if err != nil {
			return library.Page{}, err
		}
		items, err := y.store.PlaylistTracks(id, limit, offset)
		if err != nil {
			return library.Page{}, err
		}
		return library.Page{Shelves: []library.Shelf{{
			ID: id, Title: "Playlist", Layout: "list", Kind: "tracks",
			Items: items, Total: total, Cursor: ytmNextCursor(offset+len(items), total),
		}}}, nil
	}

	return library.Page{}, fmt.Errorf("library: unknown tab %q", tab)
}

func (y *ytmSource) maybeRefreshIfStale() {
	if y.isExpired() || y.svc.isScanningLibrary(y.id) {
		return
	}
	if _, err := y.svc.ytmAccountClient(y.id); err != nil && y.svc.ytmOpsOverride == nil {
		return
	}
	at, err := y.store.SyncTime(y.id, "playlists")
	if err != nil {
		return
	}
	if at == 0 || time.Since(time.Unix(at, 0)) > ytmOpenStaleAge {
		y.syncer.trigger(syncTriggerOpen)
	}
}

func (y *ytmSource) home(limit int) (library.Page, error) {
	if limit > 9 {
		limit = 9
	}
	tracks, err := y.store.RecentTracks(y.id, limit)
	if err != nil {
		return library.Page{}, err
	}
	if len(tracks) == 0 {
		return library.Page{}, nil
	}
	albums, _, err := y.store.Albums(y.id, 5, 0)
	if err != nil {
		return library.Page{}, err
	}
	artists, _, err := y.store.Artists(y.id, 5, 0)
	if err != nil {
		return library.Page{}, err
	}
	return library.Page{Shelves: []library.Shelf{
		{ID: "recent", Title: "Recently added", Layout: "grid", Kind: "tracks", Items: tracks},
		{ID: "albums", Title: "Albums", Layout: "grid", Kind: "albums", Albums: albums},
		{ID: "artists", Title: "Artists", Layout: "grid", Kind: "artists", Artists: artists},
	}}, nil
}

func (y *ytmSource) Tracks(_ context.Context, refs []string) ([]library.Track, error) {
	out := make([]library.Track, 0, len(refs))
	for _, ref := range refs {
		t, ok, err := y.store.TrackByRef(ref)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, t)
		}
	}
	return out, nil
}

func (y *ytmSource) AlbumTracks(_ context.Context, album, artist string) ([]library.Track, error) {
	return y.store.AlbumTracks(y.id, album, artist)
}

func (y *ytmSource) ArtistTracks(_ context.Context, artist string) ([]library.Track, error) {
	return y.store.ArtistTracks(y.id, artist)
}

func (y *ytmSource) Search(_ context.Context, query string, limit int) (library.Page, error) {
	if limit <= 0 {
		limit = 40
	}
	items, err := y.store.Search(y.id, query, limit)
	if err != nil {
		return library.Page{}, err
	}
	return library.Page{Shelves: []library.Shelf{{
		ID: "search", Title: "Songs", Layout: "list", Kind: "tracks", Items: items,
	}}}, nil
}

func (y *ytmSource) Playlists(context.Context) ([]library.Playlist, error) {
	return y.store.Playlists(y.id)
}

func (y *ytmSource) PlaylistTracks(ctx context.Context, id, cursor string, limit int) (library.Page, error) {
	return y.Feed(ctx, "playlist:"+id, cursor, limit)
}

func (y *ytmSource) findPlaylist(playlistID string) (library.Playlist, error) {
	lists, err := y.store.Playlists(y.id)
	if err != nil {
		return library.Playlist{}, err
	}
	for _, pl := range lists {
		if pl.ID == playlistID {
			return pl, nil
		}
	}
	return library.Playlist{}, fmt.Errorf("playlist: unknown id %q", playlistID)
}

func (y *ytmSource) signedInOps() (ytmAccountOps, error) {
	if y.isExpired() {
		return ytmAccountOps{}, ytm.ErrSessionExpired
	}
	client, err := y.svc.ytmAccountClient(y.id)
	if err != nil && y.svc.ytmOpsOverride == nil {
		return ytmAccountOps{}, err
	}
	return y.svc.accountOps(client), nil
}

func (y *ytmSource) SetLiked(ctx context.Context, ref string, liked bool) error {
	videoID := ytmID(ref)
	if videoID == "" {
		return fmt.Errorf("ytm: %q has no video id", ref)
	}
	ops, err := y.signedInOps()
	if err != nil {
		return err
	}
	status := ytm.SongLikedStatusNeutral
	if liked {
		status = ytm.SongLikedStatusLiked
	}
	if err := ops.setSongLiked(y.syncCtx(ctx), videoID, status); err != nil {
		if errors.Is(err, ytm.ErrSessionExpired) {
			y.markExpired()
		}
		return err
	}
	scopedRef := library.ScopedRemoteRef(library.KindYTM, y.id, videoID)
	if liked && y.store != nil {
		if _, exists, err := y.store.TrackByRef(scopedRef); err == nil && !exists {
			var meta ytmTrack
			if y.svc != nil && y.svc.ytm != nil {
				meta, _ = y.svc.ytm.lookup(scopedRef)
			}
			coverID := meta.CoverID
			if coverID == "" && y.svc != nil {
				if len(meta.Thumb) > 0 && y.svc.art != nil {
					coverID, _ = y.svc.art.Put(meta.Thumb)
				} else if meta.ThumbnailURL != "" {
					coverID = y.svc.storeYTMCover(meta.ThumbnailURL)
				}
			}
			_, _ = y.upsertYTMTrack(YTMResult{
				VideoID:    videoID,
				Title:      firstNonEmpty(meta.Title, videoID),
				Artist:     meta.Artist,
				Album:      meta.Album,
				DurationMs: meta.DurationMs,
				Thumbnail:  meta.ThumbnailURL,
				ArtistID:   meta.ArtistID,
			}, coverID)
		}
	}
	if err := y.store.SetLiked(scopedRef, liked); err != nil {
		return err
	}
	if lm, ok, err := y.store.PlaylistByRemoteID(y.id, ytmLikedPlaylistRemoteID); err == nil && ok {
		if liked {
			_ = y.store.PrependToPlaylist(lm.ID, []string{scopedRef})
		} else {
			_ = y.store.RemoveFromPlaylist(lm.ID, []string{scopedRef})
		}
		y.svc.refreshPlaylistCover(lm.ID)
	}
	return nil
}

func (y *ytmSource) CreatePlaylist(ctx context.Context, name string) (library.Playlist, error) {
	ops, err := y.signedInOps()
	if err != nil {
		return library.Playlist{}, err
	}
	remoteID, err := ops.createPlaylist(y.syncCtx(ctx), ytm.CreatePlaylistOptions{
		Title:   name,
		Privacy: ytm.PlaylistPrivate,
	})
	if err != nil {
		if errors.Is(err, ytm.ErrSessionExpired) {
			y.markExpired()
		}
		return library.Playlist{}, err
	}
	return y.store.UpsertRemotePlaylist(y.id, name, remoteID, true)
}

func (y *ytmSource) AddToPlaylist(ctx context.Context, playlistID string, refs []string) error {
	pl, err := y.findPlaylist(playlistID)
	if err != nil {
		return err
	}
	if isYTMBuiltInLikedID(pl.RemoteID) {
		return fmt.Errorf("ytm: Liked Music is managed through likes")
	}
	if !pl.Owned {
		return fmt.Errorf("ytm: this playlist belongs to another account")
	}
	if pl.RemoteID == "" {
		return fmt.Errorf("ytm: playlist %q has no remote id", pl.Name)
	}
	ops, err := y.signedInOps()
	if err != nil {
		return err
	}

	videoIDs := make([]string, 0, len(refs))
	for _, ref := range refs {
		vid := ytmID(ref)
		if vid == "" {
			return fmt.Errorf("ytm: local tracks cannot be added to a YouTube Music playlist")
		}
		videoIDs = append(videoIDs, vid)
	}
	if len(videoIDs) == 0 {
		return nil
	}
	if err := ops.addSongsToPlaylist(y.syncCtx(ctx), pl.RemoteID, videoIDs, ytm.DedupeDrop); err != nil {
		if errors.Is(err, ytm.ErrSessionExpired) {
			y.markExpired()
		}
		return err
	}

	for _, ref := range refs {
		vid := ytmID(ref)
		if tr, ok, err := y.store.TrackByRef(ref); err == nil && ok {
			_, _ = y.store.UpsertRemoteTrack(y.id, library.KindYTM, sqlite.TrackRow{
				NativeID:   vid,
				Title:      tr.Title,
				Artist:     tr.Artist,
				Album:      tr.Album,
				DurationMs: tr.DurationMs,
				CoverID:    tr.CoverID,
			})
		}
	}
	return y.store.AddToPlaylist(playlistID, refs)
}

func (y *ytmSource) RemoveFromPlaylist(ctx context.Context, playlistID string, refs []string) error {
	pl, err := y.findPlaylist(playlistID)
	if err != nil {
		return err
	}
	if isYTMBuiltInLikedID(pl.RemoteID) {
		return fmt.Errorf("ytm: Liked Music is managed through likes")
	}
	if !pl.Owned {
		return fmt.Errorf("ytm: this playlist belongs to another account")
	}
	if pl.RemoteID == "" {
		return fmt.Errorf("ytm: playlist %q has no remote id", pl.Name)
	}
	ops, err := y.signedInOps()
	if err != nil {
		return err
	}

	wanted := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if vid := ytmID(ref); vid != "" {
			wanted[vid] = true
		}
	}
	if len(wanted) == 0 {
		return nil
	}

	remotePl, err := ops.loadPlaylistForEdit(y.syncCtx(ctx), pl.RemoteID)
	if err != nil {
		if errors.Is(err, ytm.ErrSessionExpired) {
			y.markExpired()
		}
		return err
	}
	var actions []ytm.PlaylistEditAction
	for i, song := range remotePl.Items {
		if !wanted[song.ID] || i >= len(remotePl.ItemSetIDs) {
			continue
		}
		actions = append(actions, ytm.PlaylistEditAction{
			Action:         ytm.PlaylistEditActionRemoveVideo,
			RemovedVideoID: song.ID,
			SetVideoID:     remotePl.ItemSetIDs[i],
		})
	}
	if len(actions) > 0 {
		if err := ops.editPlaylist(y.syncCtx(ctx), pl.RemoteID, actions); err != nil {
			if errors.Is(err, ytm.ErrSessionExpired) {
				y.markExpired()
			}
			return err
		}
	}
	return y.store.RemoveFromPlaylist(playlistID, refs)
}

func (y *ytmSource) RenamePlaylist(ctx context.Context, playlistID, name string) error {
	pl, err := y.findPlaylist(playlistID)
	if err != nil {
		return err
	}
	if isYTMBuiltInLikedID(pl.RemoteID) {
		return fmt.Errorf("ytm: Liked Music cannot be renamed")
	}
	if !pl.Owned {
		return fmt.Errorf("ytm: this playlist belongs to another account")
	}
	ops, err := y.signedInOps()
	if err != nil {
		return err
	}
	if pl.RemoteID != "" {
		err := ops.editPlaylist(y.syncCtx(ctx), pl.RemoteID, []ytm.PlaylistEditAction{{
			Action:       ytm.PlaylistEditActionSetName,
			PlaylistName: name,
		}})
		if err != nil {
			if errors.Is(err, ytm.ErrSessionExpired) {
				y.markExpired()
			}
			return err
		}
	}
	return y.store.RenamePlaylist(y.id, playlistID, name)
}

func (y *ytmSource) DeletePlaylist(ctx context.Context, playlistID string) error {
	pl, err := y.findPlaylist(playlistID)
	if err != nil {
		return err
	}
	if isYTMBuiltInLikedID(pl.RemoteID) {
		return fmt.Errorf("ytm: Liked Music cannot be deleted")
	}
	ops, err := y.signedInOps()
	if err != nil {
		return err
	}
	if pl.RemoteID != "" {
		if pl.Owned {
			err = ops.deletePlaylist(y.syncCtx(ctx), pl.RemoteID)
		} else {
			if ops.setPlaylistSaved == nil {
				return fmt.Errorf("ytm: unsaving playlists is not available")
			}
			err = ops.setPlaylistSaved(y.syncCtx(ctx), pl.RemoteID, false)
		}
		if err != nil {
			if errors.Is(err, ytm.ErrSessionExpired) {
				y.markExpired()
			}
			return err
		}
		_ = y.store.AddTombstone(y.id, "playlist:"+pl.RemoteID, time.Now().Unix())
	}
	return y.store.DeletePlaylist(y.id, playlistID)
}

func ytmNextCursor(shown, total int) string {
	if shown >= total {
		return ""
	}
	return strconv.Itoa(shown)
}

func (s *PlayerService) syncYTMLibrary(libraryID string) {
	s.triggerYTMSync(libraryID, syncTriggerManual)
}

func (s *PlayerService) runYTMLibrarySync(ctx context.Context, libraryID string) error {
	if s.libStore == nil {
		return nil
	}
	srcAny, ok := s.librarySource(libraryID)
	if !ok {
		return nil
	}
	src, ok := srcAny.(*ytmSource)
	if !ok {
		return nil
	}

	s.libMu.Lock()
	if s.scanning[libraryID] {
		s.libMu.Unlock()
		return nil
	}
	s.scanning[libraryID] = true
	s.libMu.Unlock()

	s.emit(eventLibrary, LibraryProgress{LibraryID: libraryID, Done: 0, Total: 2, State: "scanning"})

	syncCtx := src.syncCtx(ctx)
	playlists, err := src.syncStage1(syncCtx)

	s.libMu.Lock()
	delete(s.scanning, libraryID)
	s.libMu.Unlock()

	if err != nil {
		s.logger().Warn("ytm: library sync failed", "library", libraryID, "error", err)
		return err
	}

	s.emit(eventLibrary, LibraryProgress{LibraryID: libraryID, Done: 2, Total: 2, State: "ready"})
	if len(playlists) > 0 {
		go src.hydrateRemainingPlaylists(syncCtx, playlists)
	}
	return nil
}

func (y *ytmSource) syncStage1(ctx context.Context) ([]library.Playlist, error) {
	ops, err := y.signedInOps()
	if err != nil {
		return nil, err
	}

	y.syncAccountCover(ctx, ops)
	y.svc.emitActivity(ActivityEvent{
		ID:        "ytm-sync-likes:" + y.id,
		LibraryID: y.id,
		Kind:      "sync",
		Title:     "Liked Music",
		Detail:    "Syncing liked songs",
		State:     "running",
	})
	if err := y.syncLikedSongs(ctx, ops); err != nil {
		y.svc.emitActivity(ActivityEvent{
			ID:        "ytm-sync-likes:" + y.id,
			LibraryID: y.id,
			Kind:      "sync",
			Title:     "Liked Music",
			Detail:    err.Error(),
			State:     "error",
		})
		return nil, err
	}
	y.svc.emitActivity(ActivityEvent{
		ID:        "ytm-sync-likes:" + y.id,
		LibraryID: y.id,
		Kind:      "sync",
		Title:     "Liked Music",
		Detail:    "Up to date",
		State:     "done",
	})

	y.svc.emitActivity(ActivityEvent{
		ID:        "ytm-sync-playlists:" + y.id,
		LibraryID: y.id,
		Kind:      "sync",
		Title:     "Playlists",
		Detail:    "Syncing playlists",
		State:     "running",
	})
	playlists, err := y.syncAccountPlaylists(ctx, ops)
	if err != nil {
		y.svc.emitActivity(ActivityEvent{
			ID:        "ytm-sync-playlists:" + y.id,
			LibraryID: y.id,
			Kind:      "sync",
			Title:     "Playlists",
			Detail:    err.Error(),
			State:     "error",
		})
		return nil, err
	}
	y.svc.emitActivity(ActivityEvent{
		ID:        "ytm-sync-playlists:" + y.id,
		LibraryID: y.id,
		Kind:      "sync",
		Title:     "Playlists",
		Detail:    fmt.Sprintf("%d playlists", len(playlists)),
		Done:      len(playlists),
		Total:     len(playlists),
		State:     "done",
	})
	y.svc.refreshLibraryCover(y.id)
	return playlists, nil
}

func (y *ytmSource) syncAccountCover(ctx context.Context, ops ytmAccountOps) {
	if ops.getAccounts == nil || y.svc.art == nil {
		return
	}
	accounts, err := ops.getAccounts(ctx)
	if err != nil {
		return
	}
	var targetChannel string
	if sess := y.session(); sess != nil {
		sess.mu.RLock()
		targetChannel = sess.config.ChannelID
		sess.mu.RUnlock()
	}
	var avatarURL string
	for _, a := range accounts {
		if (targetChannel != "" && a.PageID == targetChannel) || (targetChannel == "" && a.IsSelected) {
			avatarURL = a.ThumbnailURL
			break
		}
	}
	if avatarURL == "" && len(accounts) > 0 {
		avatarURL = accounts[0].ThumbnailURL
	}
	if avatarURL == "" {
		return
	}
	if cid := y.svc.storeYTMCover(avatarURL); cid != "" {
		_ = y.store.SetLibraryCover(y.id, cid, true)
	}
}

func ytmSongToResult(s ytm.Song) YTMResult {
	return YTMResult{
		VideoID:    ytm.CleanSongID(s.ID),
		Title:      s.Name,
		Artist:     ytmArtists(s.Artists),
		ArtistID:   ytmFirstArtistID(s.Artists),
		Album:      ytmAlbum(s.Album),
		DurationMs: s.DurationMs,
		Thumbnail:  ytmThumb(s.Thumbnail),
		Kind:       "Song",
		Playable:   s.ID != "",
	}
}

func (y *ytmSource) upsertYTMTrack(row YTMResult, coverID string) (string, error) {
	if y.svc.ytm != nil {
		y.svc.ytm.put(ytmTrack{
			ID:           row.VideoID,
			Title:        row.Title,
			Artist:       row.Artist,
			Album:        row.Album,
			DurationMs:   row.DurationMs,
			ThumbnailURL: row.Thumbnail,
			ArtistID:     row.ArtistID,
			CoverID:      coverID,
		})
	}
	if _, err := y.store.UpsertRemoteTrack(y.id, library.KindYTM, sqlite.TrackRow{
		NativeID:     row.VideoID,
		Title:        row.Title,
		Artist:       row.Artist,
		Album:        row.Album,
		DurationMs:   row.DurationMs,
		CoverID:      coverID,
		ThumbnailURL: row.Thumbnail,
		ArtistID:     row.ArtistID,
	}); err != nil {
		return "", err
	}
	return library.ScopedRemoteRef(library.KindYTM, y.id, row.VideoID), nil
}

func (y *ytmSource) syncLikedSongs(ctx context.Context, ops ytmAccountOps) error {
	var songs []ytm.Song
	seen := make(map[string]bool)

	songsPage, cont, err := ops.getLikedSongs(ctx)
	if err != nil {
		if errors.Is(err, ytm.ErrSessionExpired) {
			y.markExpired()
		}
		return err
	}
	appendPage := func(page []ytm.Song) {
		for _, s := range page {
			id := ytm.CleanSongID(s.ID)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			songs = append(songs, s)
			if len(songs) >= ytmMaxLikedSongs {
				return
			}
		}
	}
	appendPage(songsPage)

	for page := 1; cont != nil && cont.Token != "" && len(songs) < ytmMaxLikedSongs && page < ytmMaxPlaylistPages; page++ {
		next, nextCont, err := ops.getLikedSongsCont(ctx, cont.Token)
		if err != nil {
			break
		}
		appendPage(next)
		cont = nextCont
	}

	rows := make([]YTMResult, 0, len(songs))
	for _, s := range songs {
		r := ytmSongToResult(s)
		if r.VideoID != "" {
			rows = append(rows, r)
		}
	}

	covers := y.svc.storeYTMCovers(rows)
	refs := make([]string, 0, len(rows))
	for i, r := range rows {
		ref, err := y.upsertYTMTrack(r, covers[i])
		if err != nil {
			return err
		}
		refs = append(refs, ref)
		_ = y.store.SetLiked(ref, true)
	}

	lm, err := y.store.UpsertRemotePlaylist(y.id, "Liked Music", ytmLikedPlaylistRemoteID, false)
	if err != nil {
		return err
	}
	if err := y.store.ReplacePlaylistEntries(lm.ID, refs); err != nil {
		return err
	}
	y.svc.refreshPlaylistCover(lm.ID)

	now := time.Now().Unix()
	_ = y.store.SetSyncTime(y.id, "likes", now)
	_ = y.store.SetSyncTime(y.id, "playlist:"+ytmLikedPlaylistRemoteID, now)
	return nil
}

func (y *ytmSource) syncAccountPlaylists(ctx context.Context, ops ytmAccountOps) ([]library.Playlist, error) {
	var all []ytm.Playlist
	seen := make(map[string]bool)

	first, cont, err := ops.getLibraryPlaylists(ctx)
	if err != nil {
		if errors.Is(err, ytm.ErrSessionExpired) {
			y.markExpired()
		}
		return nil, err
	}
	appendPls := func(page []ytm.Playlist) {
		for _, pl := range page {
			id := strings.TrimSpace(pl.ID)
			if id == "" || isYTMBuiltInLikedID(id) || seen[id] {
				continue
			}
			seen[id] = true
			all = append(all, pl)
		}
	}
	appendPls(first)

	for page := 1; cont != nil && cont.Token != "" && page < ytmMaxPlaylistPages; page++ {
		next, nextCont, err := ops.getLibraryPlaylistsCont(ctx, cont.Token)
		if err != nil {
			break
		}
		appendPls(next)
		cont = nextCont
	}

	var channelID string
	if sess := y.session(); sess != nil {
		sess.mu.RLock()
		channelID = sess.config.ChannelID
		sess.mu.RUnlock()
	}
	if channelID == "" && y.store != nil {
		if row, err := y.store.LibraryRow(y.id); err == nil {
			channelID = recordedYTMChannelID(row)
		}
	}

	now := time.Now().Unix()
	cutoff := now - int64(ytmTombstoneTTL.Seconds())
	tombstones, _ := y.store.ActiveTombstones(y.id, cutoff)

	remoteKeys := make(map[string]bool, len(all))
	keep := map[string]bool{ytmLikedPlaylistRemoteID: true}
	saved := make([]library.Playlist, 0, len(all))
	for _, pl := range all {
		rid := strings.TrimSpace(pl.ID)
		key := "playlist:" + rid
		remoteKeys[key] = true
		if tombstones[key] {
			continue
		}
		keep[rid] = true
		owned := pl.Owned(channelID) && ytm.IsEditablePlaylistID(rid)
		row, err := y.store.UpsertRemotePlaylist(y.id, firstNonEmpty(strings.TrimSpace(pl.Name), "Untitled playlist"), rid, owned)
		if err != nil {
			return nil, err
		}
		if thumbURL := ytmThumb(pl.Thumbnail); thumbURL != "" {
			if cid := y.svc.storeYTMCover(thumbURL); cid != "" {
				_ = y.store.SetPlaylistCover(row.ID, cid, false)
				row.CoverID = cid
			}
		}
		saved = append(saved, row)
	}
	if err := y.store.PruneRemotePlaylists(y.id, keep); err != nil {
		return nil, err
	}
	_ = y.store.ReconcileTombstones(y.id, remoteKeys, cutoff)
	_ = y.store.SetSyncTime(y.id, "playlists", now)
	return saved, nil
}

func (y *ytmSource) hydrateRemainingPlaylists(ctx context.Context, playlists []library.Playlist) {
	var stale []library.Playlist
	for _, pl := range playlists {
		if pl.RemoteID == "" || isYTMBuiltInLikedID(pl.RemoteID) {
			continue
		}
		at, err := y.store.SyncTime(y.id, "playlist:"+pl.RemoteID)
		if err != nil || at == 0 || time.Since(time.Unix(at, 0)) > ytmTracksStaleAge {
			stale = append(stale, pl)
		}
	}
	if len(stale) == 0 {
		return
	}

	limit := min(len(stale), 50)
	for _, pl := range stale[:limit] {
		y.svc.emitActivity(ActivityEvent{
			ID:        "ytm-hydrate:" + pl.RemoteID,
			LibraryID: y.id,
			Kind:      "hydrate",
			Title:     firstNonEmpty(pl.Name, "Playlist"),
			Detail:    "Queued",
			State:     "running",
		})
	}

	work := make(chan library.Playlist)
	var wg sync.WaitGroup
	workers := ytmHydrateWorkers
	if len(stale) < workers {
		workers = len(stale)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pl := range work {
				actID := "ytm-hydrate:" + pl.RemoteID
				actTitle := firstNonEmpty(pl.Name, "Playlist")
				y.svc.emitActivity(ActivityEvent{
					ID:        actID,
					LibraryID: y.id,
					Kind:      "hydrate",
					Title:     actTitle,
					Detail:    "Hydrating tracks",
					State:     "running",
				})
				err := y.hydratePlaylist(ctx, pl)
				switch {
				case err == nil:
					y.svc.emitActivity(ActivityEvent{
						ID:        actID,
						LibraryID: y.id,
						Kind:      "hydrate",
						Title:     actTitle,
						Detail:    "Synced",
						State:     "done",
					})
					y.svc.emit(eventLibrary, LibraryProgress{LibraryID: y.id, State: "ready"})
				case ctx.Err() != nil:
					y.svc.emitActivity(ActivityEvent{
						ID:        actID,
						LibraryID: y.id,
						Kind:      "hydrate",
						Title:     actTitle,
						Detail:    "Stopped",
						State:     "done",
					})
				default:
					y.svc.emitActivity(ActivityEvent{
						ID:        actID,
						LibraryID: y.id,
						Kind:      "hydrate",
						Title:     actTitle,
						Detail:    err.Error(),
						State:     "error",
					})
				}
			}
		}()
	}
	for _, pl := range stale {
		select {
		case work <- pl:
		case <-ctx.Done():
			close(work)
			wg.Wait()
			return
		}
	}
	close(work)
	wg.Wait()
}

func (y *ytmSource) ensurePlaylistHydrated(ctx context.Context, playlistID string) error {
	if y.isExpired() {
		return nil
	}
	pl, err := y.findPlaylist(playlistID)
	if err != nil || pl.RemoteID == "" {
		return nil
	}
	at, err := y.store.SyncTime(y.id, "playlist:"+pl.RemoteID)
	if err == nil && at != 0 && time.Since(time.Unix(at, 0)) <= ytmOpenStaleAge {
		return nil
	}
	if _, err := y.signedInOps(); err != nil {
		return nil
	}
	if pl.RemoteID == ytmLikedPlaylistRemoteID {
		ops, err := y.signedInOps()
		if err != nil {
			return err
		}
		return y.syncLikedSongs(y.syncCtx(ctx), ops)
	}
	return y.hydratePlaylist(y.syncCtx(ctx), pl)
}

func (y *ytmSource) hydratePlaylist(ctx context.Context, pl library.Playlist) error {
	if pl.RemoteID == "" {
		return nil
	}

	y.hydrateMu.Lock()
	if ch, active := y.inFlight[pl.ID]; active {
		y.hydrateMu.Unlock()
		<-ch
		return nil
	}
	done := make(chan struct{})
	y.inFlight[pl.ID] = done
	y.hydrateMu.Unlock()

	defer func() {
		close(done)
		y.hydrateMu.Lock()
		delete(y.inFlight, pl.ID)
		y.hydrateMu.Unlock()
	}()

	ops, err := y.signedInOps()
	if err != nil {
		return err
	}
	rows, remoteName, err := ops.loadPlaylist(ctx, pl.RemoteID)
	if err != nil {
		if errors.Is(err, ytm.ErrSessionExpired) {
			y.markExpired()
		}
		return err
	}
	if remoteName = strings.TrimSpace(remoteName); remoteName != "" && remoteName != pl.Name && !isYTMBuiltInLikedID(pl.RemoteID) {
		_ = y.store.RenamePlaylist(y.id, pl.ID, remoteName)
	}

	y.svc.enrichYTMResults(ctx, rows)
	covers := y.svc.storeYTMCovers(rows)
	refs := make([]string, 0, len(rows))
	for i, row := range rows {
		if row.VideoID == "" {
			continue
		}
		ref, err := y.upsertYTMTrack(row, covers[i])
		if err != nil {
			return err
		}
		refs = append(refs, ref)
	}
	if err := y.store.ReplacePlaylistEntries(pl.ID, refs); err != nil {
		return err
	}
	if pl.CoverID == "" {
		y.svc.refreshPlaylistCover(pl.ID)
	}
	_ = y.store.SetSyncTime(y.id, "playlist:"+pl.RemoteID, time.Now().Unix())
	return nil
}

// YTMAddTarget describes a destination playlist for importing a single song
// into a YouTube Music account library.
type YTMAddTarget struct {
	PlaylistID string `json:"playlistId"`
	RemoteID   string `json:"remoteId"`
	Title      string `json:"title"`
	Subtitle   string `json:"subtitle,omitempty"`
	Thumbnail  string `json:"thumbnail,omitempty"`
	HasVideo   bool   `json:"hasVideo,omitempty"`
}

// YTMSongTarget selects where a single YouTube Music song import lands.
type YTMSongTarget struct {
	Liked          bool   `json:"liked,omitempty"`
	PlaylistID     string `json:"playlistId,omitempty"`
	NewPlaylist    string `json:"newPlaylist,omitempty"`
	AllowDuplicate bool   `json:"allowDuplicate,omitempty"`
}

func (y *ytmSource) importPlaylist(ctx context.Context, hit YTMResult) (YTMImport, error) {
	rid := strings.TrimPrefix(ytmPlaylistID(hit.VideoID), "VL")
	if rid == "" {
		return YTMImport{}, fmt.Errorf("ytm: %q has no playlist id", hit.Title)
	}
	actID := "ytm-import:" + rid
	actTitle := activityPlaylistTitle(hit.Title, "Playlist")
	emitErr := func(err error) {
		y.svc.emitActivity(ActivityEvent{
			ID:        actID,
			LibraryID: y.id,
			Kind:      "import",
			Title:     actTitle,
			Detail:    err.Error(),
			State:     "error",
		})
	}
	y.svc.emitActivity(ActivityEvent{
		ID:        actID,
		LibraryID: y.id,
		Kind:      "import",
		Title:     actTitle,
		Detail:    "Saving to YouTube Music",
		State:     "running",
	})

	ops, err := y.signedInOps()
	if err != nil {
		emitErr(err)
		return YTMImport{}, err
	}
	if ops.setPlaylistSaved == nil {
		err := fmt.Errorf("ytm: saving playlists is not available")
		emitErr(err)
		return YTMImport{}, err
	}
	if err := ops.setPlaylistSaved(y.syncCtx(ctx), rid, true); err != nil {
		if errors.Is(err, ytm.ErrSessionExpired) {
			y.markExpired()
		}
		emitErr(err)
		return YTMImport{}, err
	}

	y.svc.emitActivity(ActivityEvent{
		ID:        actID,
		LibraryID: y.id,
		Kind:      "import",
		Title:     actTitle,
		Detail:    "Loading tracks",
		State:     "running",
	})

	name := firstNonEmpty(strings.TrimSpace(hit.Title), "Untitled playlist")
	row, err := y.store.UpsertRemotePlaylist(y.id, name, rid, false)
	if err != nil {
		emitErr(err)
		return YTMImport{}, err
	}
	if cid := y.svc.storeYTMCover(hit.Thumbnail); cid != "" {
		_ = y.store.SetPlaylistCover(row.ID, cid, false)
	}
	if err := y.hydratePlaylist(y.syncCtx(ctx), row); err != nil {
		emitErr(err)
		return YTMImport{}, err
	}

	count := 0
	if updated, ok, err := y.store.PlaylistByRemoteID(y.id, rid); err == nil && ok {
		count = updated.TrackCount
		if updated.Name != "" {
			name = updated.Name
		}
	}
	y.svc.refreshLibraryCover(y.id)
	y.svc.emit(eventLibrary, LibraryProgress{LibraryID: y.id, Done: count, Total: count, State: "ready"})
	y.svc.emitActivity(ActivityEvent{
		ID:        actID,
		LibraryID: y.id,
		Kind:      "import",
		Title:     name,
		Detail:    fmt.Sprintf("%d tracks", count),
		Done:      count,
		Total:     count,
		State:     "done",
	})
	return YTMImport{PlaylistID: row.ID, Name: name, Tracks: count}, nil
}

// YTMAddTargets lists the owned playlists that can receive videoID in libraryID.
func (s *PlayerService) YTMAddTargets(libraryID, videoID string) ([]YTMAddTarget, error) {
	srcAny, ok := s.librarySource(libraryID)
	if !ok {
		return nil, fmt.Errorf("library: unknown id %q", libraryID)
	}
	ytmSrc, ok := srcAny.(*ytmSource)
	if !ok {
		return nil, fmt.Errorf("library: %q is not a YouTube Music library", libraryID)
	}
	vid := ytm.CleanSongID(strings.TrimSpace(videoID))
	if vid == "" {
		return nil, fmt.Errorf("ytm: video id is required")
	}
	ops, err := ytmSrc.signedInOps()
	if err != nil {
		return nil, err
	}

	localPls, err := s.libStore.Playlists(libraryID)
	if err != nil {
		return nil, err
	}
	byRemote := make(map[string]library.Playlist, len(localPls))
	for _, pl := range localPls {
		if pl.RemoteID != "" {
			byRemote[pl.RemoteID] = pl
		}
	}
	ref := library.ScopedRemoteRef(library.KindYTM, libraryID, vid)

	var remoteTargets []ytm.AddToPlaylistTarget
	var remoteErr error
	if ops.getAddToPlaylistTargets != nil {
		remoteTargets, remoteErr = ops.getAddToPlaylistTargets(ytmSrc.syncCtx(s.ctx), vid)
		if errors.Is(remoteErr, ytm.ErrSessionExpired) {
			ytmSrc.markExpired()
			return nil, remoteErr
		}
	}

	if remoteErr == nil && len(remoteTargets) > 0 {
		out := make([]YTMAddTarget, 0, len(remoteTargets))
		for _, t := range remoteTargets {
			rid := strings.TrimPrefix(strings.TrimSpace(t.PlaylistID), "VL")
			if rid == "" || isYTMBuiltInLikedID(rid) || !ytm.IsEditablePlaylistID(rid) {
				continue
			}
			pl, ok := byRemote[rid]
			if !ok {
				up, err := s.libStore.UpsertRemotePlaylist(libraryID, firstNonEmpty(t.Title, "Untitled playlist"), rid, true)
				if err == nil {
					pl = up
				}
			}
			hasVideo := false
			if pl.ID != "" {
				hasVideo, _ = s.libStore.PlaylistHasTrack(pl.ID, ref)
			}
			out = append(out, YTMAddTarget{
				PlaylistID: firstNonEmpty(pl.ID, rid),
				RemoteID:   rid,
				Title:      t.Title,
				Subtitle:   t.Subtitle,
				Thumbnail:  t.Thumbnail,
				HasVideo:   hasVideo,
			})
		}
		return out, nil
	}

	out := make([]YTMAddTarget, 0, len(localPls))
	for _, pl := range localPls {
		if !pl.Owned || pl.RemoteID == "" || isYTMBuiltInLikedID(pl.RemoteID) || !ytm.IsEditablePlaylistID(pl.RemoteID) {
			continue
		}
		hasVideo, _ := s.libStore.PlaylistHasTrack(pl.ID, ref)
		subtitle := ""
		if pl.TrackCount == 1 {
			subtitle = "1 track"
		} else if pl.TrackCount > 1 {
			subtitle = fmt.Sprintf("%d tracks", pl.TrackCount)
		}
		out = append(out, YTMAddTarget{
			PlaylistID: pl.ID,
			RemoteID:   pl.RemoteID,
			Title:      pl.Name,
			Subtitle:   subtitle,
			HasVideo:   hasVideo,
		})
	}
	return out, nil
}

// ImportYTMSong pushes a single song import into a KindYTM account library:
// either liking it (Liked Music), adding it to an existing owned playlist with
// duplicate checking, or creating a new private playlist holding it.
func (s *PlayerService) ImportYTMSong(libraryID string, hit YTMResult, to YTMSongTarget) (YTMImport, error) {
	srcAny, ok := s.librarySource(libraryID)
	if !ok {
		return YTMImport{}, fmt.Errorf("library: unknown id %q", libraryID)
	}
	ytmSrc, ok := srcAny.(*ytmSource)
	if !ok {
		return YTMImport{}, fmt.Errorf("library: %q is not a YouTube Music library", libraryID)
	}
	vid := ytm.CleanSongID(strings.TrimSpace(hit.VideoID))
	if vid == "" {
		return YTMImport{}, fmt.Errorf("ytm: %q has no id", hit.Title)
	}
	hit.VideoID = vid
	ref := library.ScopedRemoteRef(library.KindYTM, libraryID, vid)
	actID := "ytm-import-song:" + vid
	actTitle := firstNonEmpty(strings.TrimSpace(hit.Title), "Song")
	emitSongErr := func(err error) {
		s.emitActivity(ActivityEvent{
			ID:        actID,
			LibraryID: libraryID,
			Kind:      "import",
			Title:     actTitle,
			Detail:    err.Error(),
			State:     "error",
		})
	}
	emitSongDone := func(detail string, done, total int) {
		s.emitActivity(ActivityEvent{
			ID:        actID,
			LibraryID: libraryID,
			Kind:      "import",
			Title:     actTitle,
			Detail:    detail,
			Done:      done,
			Total:     total,
			State:     "done",
		})
	}
	s.emitActivity(ActivityEvent{
		ID:        actID,
		LibraryID: libraryID,
		Kind:      "import",
		Title:     actTitle,
		Detail:    "Adding to YouTube Music",
		State:     "running",
	})

	cacheTrack := func() {
		single := []YTMResult{hit}
		s.enrichYTMResults(s.ctx, single)
		hit = single[0]
		_, _ = ytmSrc.upsertYTMTrack(hit, s.storeYTMCover(hit.Thumbnail))
	}

	switch {
	case to.Liked:
		ops, err := ytmSrc.signedInOps()
		if err != nil {
			emitSongErr(err)
			return YTMImport{}, err
		}
		if err := ops.setSongLiked(ytmSrc.syncCtx(s.ctx), vid, ytm.SongLikedStatusLiked); err != nil {
			if errors.Is(err, ytm.ErrSessionExpired) {
				ytmSrc.markExpired()
			}
			emitSongErr(err)
			return YTMImport{}, err
		}
		cacheTrack()
		if err := s.libStore.SetLiked(ref, true); err != nil {
			emitSongErr(err)
			return YTMImport{}, err
		}
		if lm, ok, err := s.libStore.PlaylistByRemoteID(libraryID, ytmLikedPlaylistRemoteID); err == nil && ok {
			_ = s.libStore.PrependToPlaylist(lm.ID, []string{ref})
			s.refreshPlaylistCover(lm.ID)
		}
		s.refreshLibraryCover(libraryID)
		s.emit(eventLibrary, LibraryProgress{LibraryID: libraryID, Done: 1, Total: 1, State: "ready"})
		emitSongDone("Added", 1, 1)
		return YTMImport{Name: firstNonEmpty(strings.TrimSpace(hit.Title), "Liked Music"), Tracks: 1}, nil

	case strings.TrimSpace(to.NewPlaylist) != "":
		title := strings.TrimSpace(to.NewPlaylist)
		ops, err := ytmSrc.signedInOps()
		if err != nil {
			emitSongErr(err)
			return YTMImport{}, err
		}
		remoteID, err := ops.createPlaylist(ytmSrc.syncCtx(s.ctx), ytm.CreatePlaylistOptions{
			Title:    title,
			Privacy:  ytm.PlaylistPrivate,
			VideoIDs: []string{vid},
		})
		if err != nil {
			if errors.Is(err, ytm.ErrSessionExpired) {
				ytmSrc.markExpired()
			}
			emitSongErr(err)
			return YTMImport{}, err
		}
		pl, err := s.libStore.UpsertRemotePlaylist(libraryID, title, remoteID, true)
		if err != nil {
			emitSongErr(err)
			return YTMImport{}, err
		}
		cacheTrack()
		_ = s.libStore.AddToPlaylist(pl.ID, []string{ref})
		s.refreshPlaylistCover(pl.ID)
		s.refreshLibraryCover(libraryID)
		s.emit(eventLibrary, LibraryProgress{LibraryID: libraryID, Done: 1, Total: 1, State: "ready"})
		emitSongDone("Added", 1, 1)
		return YTMImport{PlaylistID: pl.ID, Name: pl.Name, Tracks: 1}, nil

	case strings.TrimSpace(to.PlaylistID) != "":
		pl, err := ytmSrc.findPlaylist(to.PlaylistID)
		if err != nil {
			emitSongErr(err)
			return YTMImport{}, err
		}
		if isYTMBuiltInLikedID(pl.RemoteID) {
			err := fmt.Errorf("ytm: Liked Music is managed through likes")
			emitSongErr(err)
			return YTMImport{}, err
		}
		if !pl.Owned {
			err := fmt.Errorf("ytm: this playlist belongs to another account")
			emitSongErr(err)
			return YTMImport{}, err
		}
		if pl.RemoteID == "" {
			err := fmt.Errorf("ytm: playlist %q has no remote id", pl.Name)
			emitSongErr(err)
			return YTMImport{}, err
		}
		ops, err := ytmSrc.signedInOps()
		if err != nil {
			emitSongErr(err)
			return YTMImport{}, err
		}
		mode := ytm.DedupeAsk
		if to.AllowDuplicate {
			mode = ytm.DedupeAdd
		}
		err = ops.addSongsToPlaylist(ytmSrc.syncCtx(s.ctx), pl.RemoteID, []string{vid}, mode)
		if errors.Is(err, ytm.ErrDuplicates) {
			emitSongDone("Already in playlist", 0, 0)
			return YTMImport{PlaylistID: pl.ID, Name: pl.Name, Duplicate: true}, nil
		}
		if err != nil {
			if errors.Is(err, ytm.ErrSessionExpired) {
				ytmSrc.markExpired()
			}
			emitSongErr(err)
			return YTMImport{}, err
		}
		cacheTrack()
		_ = s.libStore.AddToPlaylist(pl.ID, []string{ref})
		s.refreshPlaylistCover(pl.ID)
		s.refreshLibraryCover(libraryID)
		s.emit(eventLibrary, LibraryProgress{LibraryID: libraryID, Done: 1, Total: 1, State: "ready"})
		emitSongDone("Added", 1, 1)
		return YTMImport{PlaylistID: pl.ID, Name: pl.Name, Tracks: 1}, nil

	default:
		err := fmt.Errorf("ytm: choose a destination for %q", hit.Title)
		emitSongErr(err)
		return YTMImport{}, err
	}
}
