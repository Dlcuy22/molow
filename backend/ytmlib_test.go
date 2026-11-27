package backend

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/dlcuy22/molow/internal/artwork"
	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/local"
	"github.com/dlcuy22/molow/internal/library/sqlite"
	ytm "github.com/dlcuy22/ytm-go"
)

func newYTMTestService(t *testing.T) (*PlayerService, *sqlite.Store) {
	t.Helper()
	dir := isolateConfig(t)
	store, err := sqlite.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	art, err := artwork.Open(dir)
	if err != nil {
		t.Fatalf("artwork.Open: %v", err)
	}
	svc := &PlayerService{
		ctx:      context.Background(),
		art:      art,
		ytm:      newYTMIndex(),
		ytmProv:  newYTMProvider(newYTMIndex(), art, false, nil),
		libStore: store,
		libs:     map[string]library.Source{},
		scanning: map[string]bool{},
	}
	svc.libMu.Lock()
	for _, srcAny := range svc.libs {
		if ytmSrc, ok := srcAny.(*ytmSource); ok {
			defer ytmSrc.syncer.stop()
		}
	}
	svc.libMu.Unlock()
	t.Cleanup(svc.stopYTMSyncers)
	return svc, store
}

func TestYTMLibraryAutoCreationAndHybridSync(t *testing.T) {
	svc, store := newYTMTestService(t)

	var pngBuf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 200, G: 80, B: 80, A: 255})
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}
	pngBytes := pngBuf.Bytes()
	thumbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer thumbSrv.Close()

	creds := &Credentials{
		CookieHeader: "SAPISID=s; SID=id",
		ChannelID:    "UCtest",
		Name:         "Kasaki Nomaraba",
	}
	if err := saveCredentials(creds); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	svc.ytmOpsOverride = func(ops *ytmAccountOps) {
		ops.getAccounts = func(context.Context) ([]ytm.Account, error) {
			return []ytm.Account{{PageID: "UCtest", Name: "Kasaki Nomaraba", ThumbnailURL: thumbSrv.URL + "/avatar.png", IsSelected: true}}, nil
		}
		ops.getLikedSongs = func(context.Context) ([]ytm.Song, *ytm.BuiltInContinuation, error) {
			return []ytm.Song{
				{
					ID: "vid1", Name: "Liked Track",
					Artists:   []ytm.Artist{{ID: "UCart1", Name: "Artist One"}},
					Album:     &ytm.Playlist{ID: "MPRE1", Name: "Album One"},
					Thumbnail: ytm.NewThumbnailProviderFromURL(thumbSrv.URL + "/t1.png"),
				},
			}, nil, nil
		}
		ops.getLibraryPlaylists = func(context.Context) ([]ytm.Playlist, *ytm.BuiltInContinuation, error) {
			return []ytm.Playlist{
				{ID: "LM", Name: "Liked Music"},
				{
					ID:        "PLabc",
					Name:      "Night Drive",
					OwnerID:   "UCtest",
					Thumbnail: ytm.NewThumbnailProviderFromURL(thumbSrv.URL + "/pl.png"),
				},
			}, nil, nil
		}
		ops.loadPlaylist = func(_ context.Context, id string) ([]YTMResult, string, error) {
			return []YTMResult{{
				VideoID: "vid2", Title: "Drive Track", Artist: "Driver", Album: "Midnight",
				Thumbnail: thumbSrv.URL + "/t2.png", Kind: "Song", Playable: true,
			}}, "Night Drive", nil
		}
	}

	libID, err := svc.ensureYTMLibrary(creds)
	if err != nil {
		t.Fatalf("ensureYTMLibrary: %v", err)
	}
	if libID != "ytm:UCtest" {
		t.Fatalf("libID = %q, want ytm:UCtest", libID)
	}

	src := svc.libs[libID].(*ytmSource)
	playlists, err := src.syncStage1(context.Background())
	if err != nil {
		t.Fatalf("syncStage1: %v", err)
	}
	if len(playlists) != 1 || playlists[0].RemoteID != "PLabc" || playlists[0].CoverID == "" {
		t.Fatalf("stage 1 playlists = %+v, want 1 playlist with cover", playlists)
	}

	lib, err := src.Describe(context.Background())
	if err != nil || lib.Name != "Kasaki Nomaraba" || lib.CoverID == "" || lib.Status != library.StatusReady {
		t.Fatalf("Describe = (%+v, %v)", lib, err)
	}

	// Opening PLabc triggers on-demand hydration before or instead of background worker.
	page, err := src.Feed(context.Background(), "playlist:"+playlists[0].ID, "", 50)
	if err != nil {
		t.Fatalf("Feed playlist: %v", err)
	}
	if len(page.Shelves) != 1 || len(page.Shelves[0].Items) != 1 || page.Shelves[0].Items[0].Ref != "ytm:vid2" {
		t.Fatalf("hydrated playlist page = %+v", page)
	}

	// Liked Music ("LM") was populated immediately in Stage 1.
	lm, ok, err := store.PlaylistByRemoteID(libID, "LM")
	if err != nil || !ok || lm.TrackCount != 1 {
		t.Fatalf("Liked Music row = (%+v, %v, %v)", lm, ok, err)
	}
}

func TestYTMSignOutKeepOrDeleteLibrary(t *testing.T) {
	svc, store := newYTMTestService(t)
	creds := &Credentials{CookieHeader: "SAPISID=s; SID=id", ChannelID: "UCkeep", Name: "Keep Me"}
	if err := saveCredentials(creds); err != nil {
		t.Fatal(err)
	}
	libID, err := svc.ensureYTMLibrary(creds)
	if err != nil {
		t.Fatal(err)
	}

	// Sign out with deleteLibrary=false -> keeps library with StatusSignedOut.
	if err := svc.SignOutWithOptions(false); err != nil {
		t.Fatalf("SignOutWithOptions(false): %v", err)
	}
	libs, err := svc.Libraries()
	if err != nil || len(libs) != 1 || libs[0].ID != libID || libs[0].Status != library.StatusSignedOut {
		t.Fatalf("after soft sign-out libs = (%+v, %v)", libs, err)
	}

	// Sign out with deleteLibrary=true -> deletes YTM library from SQLite and registry.
	if err := svc.SignOutWithOptions(true); err != nil {
		t.Fatalf("SignOutWithOptions(true): %v", err)
	}
	libs, err = store.Libraries()
	if err != nil || len(libs) != 0 {
		t.Fatalf("after delete sign-out libs = (%+v, %v), want empty", libs, err)
	}
	if slices.ContainsFunc(libs, func(l library.Library) bool { return strings.HasPrefix(l.ID, "ytm:") }) {
		t.Fatal("ytm library survived deleteLibrary=true")
	}
}

func TestYTMRemoteWrites(t *testing.T) {
	svc, store := newYTMTestService(t)
	creds := &Credentials{CookieHeader: "SAPISID=s; SID=id", ChannelID: "UCw", Name: "Writer"}
	if err := saveCredentials(creds); err != nil {
		t.Fatal(err)
	}
	libID, err := svc.ensureYTMLibrary(creds)
	if err != nil {
		t.Fatal(err)
	}
	src := svc.libs[libID].(*ytmSource)
	if _, err := store.UpsertRemoteTrack(libID, library.KindYTM, sqlite.TrackRow{NativeID: "v1", Title: "Song 1"}); err != nil {
		t.Fatal(err)
	}

	var likedCalls []ytm.SongLikedStatus
	var createdTitle string
	svc.ytmOpsOverride = func(ops *ytmAccountOps) {
		ops.setSongLiked = func(_ context.Context, id string, st ytm.SongLikedStatus) error {
			likedCalls = append(likedCalls, st)
			return nil
		}
		ops.createPlaylist = func(_ context.Context, opts ytm.CreatePlaylistOptions) (string, error) {
			createdTitle = opts.Title
			return "PLnew", nil
		}
		ops.addSongsToPlaylist = func(context.Context, string, []string, ytm.DedupeMode) error { return nil }
	}

	if err := src.SetLiked(context.Background(), "ytm:v1", true); err != nil {
		t.Fatalf("SetLiked: %v", err)
	}
	if len(likedCalls) != 1 || likedCalls[0] != ytm.SongLikedStatusLiked {
		t.Fatalf("likedCalls = %+v", likedCalls)
	}
	if ok, _ := store.Liked("ytm:v1"); !ok {
		t.Fatal("Liked(ytm:v1) = false, want true")
	}

	pl, err := src.CreatePlaylist(context.Background(), "Roadtrip")
	if err != nil || createdTitle != "Roadtrip" || pl.RemoteID != "PLnew" {
		t.Fatalf("CreatePlaylist = (%+v, %v)", pl, err)
	}
	if err := src.AddToPlaylist(context.Background(), pl.ID, []string{"ytm:v1"}); err != nil {
		t.Fatalf("AddToPlaylist: %v", err)
	}
	if n, _ := store.PlaylistTrackCount(pl.ID); n != 1 {
		t.Fatalf("PlaylistTrackCount = %d, want 1", n)
	}
}

func TestYTMOwnershipTombstonesAndLoudErrors(t *testing.T) {
	svc, store := newYTMTestService(t)
	creds := &Credentials{CookieHeader: "SAPISID=s; SID=id", ChannelID: "UCme", Name: "Me"}
	if err := saveCredentials(creds); err != nil {
		t.Fatal(err)
	}

	remoteForeignPresent := true
	var unsavedIDs, deletedIDs []string
	svc.ytmOpsOverride = func(ops *ytmAccountOps) {
		ops.getLikedSongs = func(context.Context) ([]ytm.Song, *ytm.BuiltInContinuation, error) {
			return nil, nil, nil
		}
		ops.getLibraryPlaylists = func(context.Context) ([]ytm.Playlist, *ytm.BuiltInContinuation, error) {
			pls := []ytm.Playlist{
				{ID: "PLown", Name: "Mine", OwnerID: "UCme"},
			}
			if remoteForeignPresent {
				pls = append(pls, ytm.Playlist{ID: "PLother", Name: "Foreign"})
			}
			return pls, nil, nil
		}
		ops.setPlaylistSaved = func(_ context.Context, id string, saved bool) error {
			if !saved {
				unsavedIDs = append(unsavedIDs, id)
			}
			return nil
		}
		ops.deletePlaylist = func(_ context.Context, id string) error {
			deletedIDs = append(deletedIDs, id)
			return nil
		}
	}

	libID, err := svc.ensureYTMLibrary(creds)
	if err != nil {
		t.Fatal(err)
	}
	src := svc.libs[libID].(*ytmSource)
	if _, err := src.syncStage1(context.Background()); err != nil {
		t.Fatalf("syncStage1: %v", err)
	}

	own, ok, _ := store.PlaylistByRemoteID(libID, "PLown")
	foreign, ok2, _ := store.PlaylistByRemoteID(libID, "PLother")
	if !ok || !own.Owned || !ok2 || foreign.Owned {
		t.Fatalf("ownership mismatch: own=(%+v,%v) foreign=(%+v,%v)", own, ok, foreign, ok2)
	}

	// Foreign playlists reject write mutations.
	if err := src.RenamePlaylist(context.Background(), foreign.ID, "Nope"); err == nil {
		t.Fatal("expected RenamePlaylist on foreign playlist to fail")
	}
	if err := src.AddToPlaylist(context.Background(), foreign.ID, []string{"ytm:v1"}); err == nil {
		t.Fatal("expected AddToPlaylist on foreign playlist to fail")
	}
	if err := src.RemoveFromPlaylist(context.Background(), foreign.ID, []string{"ytm:v1"}); err == nil {
		t.Fatal("expected RemoveFromPlaylist on foreign playlist to fail")
	}

	// Local track ref into an owned YTM playlist must fail loudly.
	if err := src.AddToPlaylist(context.Background(), own.ID, []string{`C:\music\track.mp3`}); err == nil {
		t.Fatal("expected error adding local ref to YTM playlist")
	}

	// Deleting a foreign playlist calls SetPlaylistSaved(false) and records a tombstone.
	if err := src.DeletePlaylist(context.Background(), foreign.ID); err != nil {
		t.Fatalf("DeletePlaylist foreign: %v", err)
	}
	if len(unsavedIDs) != 1 || unsavedIDs[0] != "PLother" || len(deletedIDs) != 0 {
		t.Fatalf("unsavedIDs=%v deletedIDs=%v", unsavedIDs, deletedIDs)
	}

	// A stale pull that still sees PLother must not resurrect it.
	if _, err := src.syncStage1(context.Background()); err != nil {
		t.Fatalf("stale syncStage1: %v", err)
	}
	if _, ok, _ := store.PlaylistByRemoteID(libID, "PLother"); ok {
		t.Fatal("foreign playlist was resurrected despite active tombstone")
	}

	// Once remote drops PLother, tombstone is reconciled away.
	remoteForeignPresent = false
	if _, err := src.syncStage1(context.Background()); err != nil {
		t.Fatalf("reconcile syncStage1: %v", err)
	}
	if active, _ := store.ActiveTombstones(libID, 0); len(active) != 0 {
		t.Fatalf("expected tombstone reconciled, got %+v", active)
	}

	// Deleting owned calls deletePlaylist.
	if err := src.DeletePlaylist(context.Background(), own.ID); err != nil {
		t.Fatalf("DeletePlaylist own: %v", err)
	}
	if len(deletedIDs) != 1 || deletedIDs[0] != "PLown" {
		t.Fatalf("deletedIDs=%v, want [PLown]", deletedIDs)
	}
}

func TestYTMImportAndAddTargets(t *testing.T) {
	svc, store := newYTMTestService(t)
	creds := &Credentials{CookieHeader: "SAPISID=s; SID=id", ChannelID: "UCimp", Name: "Importer"}
	if err := saveCredentials(creds); err != nil {
		t.Fatal(err)
	}
	localLib, err := store.AddLibrary(library.Library{ID: "local:main", Kind: library.KindLocal, Name: "Local"})
	if err != nil {
		t.Fatal(err)
	}
	svc.libs[localLib.ID] = local.New(store, localLib, svc.art, nil)

	ytmLibID, err := svc.ensureYTMLibrary(creds)
	if err != nil {
		t.Fatal(err)
	}

	var savedCalls []string
	var likedCalls []string
	var createdOpts []ytm.CreatePlaylistOptions
	var addCalls []ytm.DedupeMode
	var returnDupOnAsk bool

	svc.ytmOpsOverride = func(ops *ytmAccountOps) {
		ops.setPlaylistSaved = func(_ context.Context, id string, saved bool) error {
			if saved {
				savedCalls = append(savedCalls, id)
			}
			return nil
		}
		ops.loadPlaylist = func(_ context.Context, id string) ([]YTMResult, string, error) {
			return []YTMResult{{VideoID: "tr1", Title: "Track One", Artist: "Art", Kind: "Song", Playable: true}}, "Remote Mix", nil
		}
		ops.getAddToPlaylistTargets = func(context.Context, string) ([]ytm.AddToPlaylistTarget, error) {
			return []ytm.AddToPlaylistTarget{
				{PlaylistID: "LM", Title: "Liked music"},
				{PlaylistID: "PLmine", Title: "My Mix", Subtitle: "3 tracks"},
				{PlaylistID: "OLAK5uy_alb", Title: "Album"},
			}, nil
		}
		ops.setSongLiked = func(_ context.Context, id string, _ ytm.SongLikedStatus) error {
			likedCalls = append(likedCalls, id)
			return nil
		}
		ops.createPlaylist = func(_ context.Context, opts ytm.CreatePlaylistOptions) (string, error) {
			createdOpts = append(createdOpts, opts)
			return "PLcreated", nil
		}
		ops.addSongsToPlaylist = func(_ context.Context, id string, vids []string, mode ytm.DedupeMode) error {
			addCalls = append(addCalls, mode)
			if returnDupOnAsk && mode == ytm.DedupeAsk {
				return ytm.ErrDuplicates
			}
			return nil
		}
	}

	// Importing a song into local stays local-only and never returns NeedsTarget.
	locRes, err := svc.ImportYTM(localLib.ID, YTMResult{VideoID: "locS1", Title: "Local Copy", Kind: "Song", Playable: true})
	if err != nil || locRes.NeedsTarget || len(savedCalls) != 0 || len(likedCalls) != 0 {
		t.Fatalf("local import = (%+v, %v), touched remote?", locRes, err)
	}

	// Importing a playlist into KindYTM saves it on YTM (owned=false) and hydrates tracks.
	plRes, err := svc.ImportYTM(ytmLibID, YTMResult{VideoID: "VLPLimp", Title: "Add YouTube Music playlist", Kind: "Playlist"})
	if err != nil || plRes.Tracks != 1 || plRes.Name != "Remote Mix" || len(savedCalls) != 1 || savedCalls[0] != "PLimp" {
		t.Fatalf("YTM playlist import = (%+v, %v), saved=%v", plRes, err, savedCalls)
	}
	if row, ok, _ := store.PlaylistByRemoteID(ytmLibID, "PLimp"); !ok || row.Owned || row.Name != "Remote Mix" {
		t.Fatalf("expected imported foreign playlist owned=false and remote name, got (%+v, %v)", row, ok)
	}

	// Importing a song into KindYTM asks the UI for a target.
	songHit := YTMResult{VideoID: "vidX", Title: "Choice Song", Artist: "Singer", Kind: "Song", Playable: true}
	songRes, err := svc.ImportYTM(ytmLibID, songHit)
	if err != nil || !songRes.NeedsTarget {
		t.Fatalf("YTM song import = (%+v, %v), want NeedsTarget=true", songRes, err)
	}

	targets, err := svc.YTMAddTargets(ytmLibID, "vidX")
	if err != nil || len(targets) != 1 || targets[0].RemoteID != "PLmine" || targets[0].HasVideo {
		t.Fatalf("YTMAddTargets = (%+v, %v)", targets, err)
	}

	// Duplicate handling: DedupeAsk -> Duplicate: true; AllowDuplicate -> DedupeAdd succeeds.
	returnDupOnAsk = true
	dupRes, err := svc.ImportYTMSong(ytmLibID, songHit, YTMSongTarget{PlaylistID: targets[0].PlaylistID})
	if err != nil || !dupRes.Duplicate {
		t.Fatalf("expected Duplicate=true, got (%+v, %v)", dupRes, err)
	}
	addRes, err := svc.ImportYTMSong(ytmLibID, songHit, YTMSongTarget{PlaylistID: targets[0].PlaylistID, AllowDuplicate: true})
	if err != nil || addRes.Duplicate || addRes.Tracks != 1 {
		t.Fatalf("AllowDuplicate import = (%+v, %v)", addRes, err)
	}
	if len(addCalls) != 2 || addCalls[0] != ytm.DedupeAsk || addCalls[1] != ytm.DedupeAdd {
		t.Fatalf("addCalls = %v", addCalls)
	}

	// HasVideo now reports true from local SQLite mirror.
	targets2, err := svc.YTMAddTargets(ytmLibID, "vidX")
	if err != nil || len(targets2) != 1 || !targets2[0].HasVideo {
		t.Fatalf("expected HasVideo=true after add, got (%+v, %v)", targets2, err)
	}

	// NewPlaylist creates PRIVATE playlist with vidX.
	newRes, err := svc.ImportYTMSong(ytmLibID, songHit, YTMSongTarget{NewPlaylist: "Fresh List"})
	if err != nil || newRes.PlaylistID == "" || len(createdOpts) != 1 || createdOpts[0].Privacy != ytm.PlaylistPrivate {
		t.Fatalf("NewPlaylist import = (%+v, %v), created=%+v", newRes, err, createdOpts)
	}

	// Liked puts song in Liked Music.
	if _, err := store.UpsertRemotePlaylist(ytmLibID, "Liked Music", "LM", false); err != nil {
		t.Fatal(err)
	}
	likeRes, err := svc.ImportYTMSong(ytmLibID, songHit, YTMSongTarget{Liked: true})
	if err != nil || likeRes.Tracks != 1 || len(likedCalls) != 1 || likedCalls[0] != "vidX" {
		t.Fatalf("Liked import = (%+v, %v), likedCalls=%v", likeRes, err, likedCalls)
	}
}
