package backend

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dlcuy22/molow/internal/artwork"
	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/sqlite"
	ytm "github.com/dlcuy22/ytm-go"
)

func TestLocalFolderWatcherDebouncesAndStopsOnUnwatch(t *testing.T) {
	dir := t.TempDir()
	var (
		mu    sync.Mutex
		calls []string
	)
	doneCh := make(chan string, 4)

	w, err := newLocalFolderWatcher(40*time.Millisecond, func(libID string) {
		mu.Lock()
		calls = append(calls, libID)
		mu.Unlock()
		doneCh <- libID
	}, nil)
	if err != nil {
		t.Fatalf("newLocalFolderWatcher: %v", err)
	}
	t.Cleanup(w.Close)

	w.SyncRoots("local:1", []string{dir})

	// Rapidly create two audio files -> debounced into a single callback.
	if err := os.WriteFile(filepath.Join(dir, "01.mp3"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "02.flac"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-doneCh:
		if got != "local:1" {
			t.Fatalf("got callback for %q, want local:1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for folder watcher callback")
	}

	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	countAfterFirst := len(calls)
	mu.Unlock()
	if countAfterFirst != 1 {
		t.Fatalf("expected 1 debounced callback, got %d", countAfterFirst)
	}

	// Unwatch the folder and write another file -> no callback should fire.
	w.SyncRoots("local:1", nil)
	if err := os.WriteFile(filepath.Join(dir, "03.mp3"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-doneCh:
		t.Fatalf("unexpected callback after unwatching: %q", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestLocalWatchedRemotePlaylistSyncAndToggle(t *testing.T) {
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
	t.Cleanup(svc.stopLocalSyncers)

	var (
		mu     sync.Mutex
		remote = []YTMResult{
			{VideoID: "v1", Title: "Song One", Artist: "Artist", Kind: "Song", Playable: true},
			{VideoID: "v2", Title: "Song Two", Artist: "Artist", Kind: "Song", Playable: true},
		}
	)
	svc.ytmProv.loadPlaylist = func(_ context.Context, _ string, _ *ytm.BuiltInContinuation) (*ytm.Playlist, error) {
		mu.Lock()
		defer mu.Unlock()
		items := make([]ytm.Song, len(remote))
		for i, r := range remote {
			items[i] = ytm.Song{
				ID:      r.VideoID,
				Name:    r.Title,
				Artists: []ytm.Artist{{Name: r.Artist}},
			}
		}
		return &ytm.Playlist{ID: "PLsrc", Name: "Author Mix", Items: items}, nil
	}
	svc.ytmProv.loadSong = func(_ context.Context, id string) (*ytm.Song, error) {
		return &ytm.Song{ID: id, Name: id}, nil
	}

	libRow, err := store.AddLibrary(library.Library{
		ID: "local:mix", Kind: library.KindLocal, Name: "Local Mix",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.registerLocal(libRow)

	// Before any watched source, CapRefresh is absent.
	desc, err := svc.describeLibrary(libRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(desc.Capabilities, library.CapRefresh) {
		t.Fatalf("empty local library capabilities = %v, should not include refresh", desc.Capabilities)
	}

	// Import a remote YTM playlist -> creates watched playlist and enables CapRefresh.
	imp, err := svc.ImportYTM(libRow.ID, YTMResult{
		VideoID: "VLPLsrc", Title: "Author Mix", Kind: "Playlist",
	})
	if err != nil {
		t.Fatalf("ImportYTM: %v", err)
	}
	desc, err = svc.describeLibrary(libRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(desc.Capabilities, library.CapRefresh) {
		t.Fatalf("local library with watched YTM playlist missing refresh capability: %v", desc.Capabilities)
	}

	// Simulate playlist author removing v1 and adding v3.
	mu.Lock()
	remote = []YTMResult{
		{VideoID: "v2", Title: "Song Two", Artist: "Artist", Kind: "Song", Playable: true},
		{VideoID: "v3", Title: "Song Three", Artist: "Artist", Kind: "Song", Playable: true},
	}
	mu.Unlock()

	if err := svc.syncLocalWatchedRemotePlaylists(context.Background(), libRow.ID); err != nil {
		t.Fatalf("syncLocalWatchedRemotePlaylists: %v", err)
	}
	tracks, err := store.PlaylistTracks(imp.PlaylistID, 10, 0)
	if err != nil || len(tracks) != 2 || tracks[0].NativeID != "v2" || tracks[1].NativeID != "v3" {
		t.Fatalf("after author update tracks = (%+v, %v), want [v2, v3]", tracks, err)
	}

	// Toggle watch OFF -> freezes playlist snapshot and drops CapRefresh.
	pl, err := svc.SetPlaylistWatched(libRow.ID, imp.PlaylistID, false)
	if err != nil || pl.Watched {
		t.Fatalf("SetPlaylistWatched(false) = (%+v, %v)", pl, err)
	}
	desc, err = svc.describeLibrary(libRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(desc.Capabilities, library.CapRefresh) {
		t.Fatalf("after unwatching all sources capabilities = %v, should not include refresh", desc.Capabilities)
	}

	mu.Lock()
	remote = []YTMResult{
		{VideoID: "v4", Title: "Song Four", Artist: "Artist", Kind: "Song", Playable: true},
	}
	mu.Unlock()

	if err := svc.syncLocalWatchedRemotePlaylists(context.Background(), libRow.ID); err != nil {
		t.Fatalf("sync while unwatched: %v", err)
	}
	tracks, err = store.PlaylistTracks(imp.PlaylistID, 10, 0)
	if err != nil || len(tracks) != 2 || tracks[0].NativeID != "v2" || tracks[1].NativeID != "v3" {
		t.Fatalf("unwatched playlist should stay frozen at [v2, v3], got (%+v, %v)", tracks, err)
	}
}
