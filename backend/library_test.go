package backend

import (
	"bytes"
	"context"
	"image"
	imgcolor "image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dlcuy22/molo"
	"github.com/dlcuy22/molow/internal/artwork"
	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/sqlite"
)

const defaultLibraryID = "local:default"

// newTestLibraries builds a PlayerService whose library half runs against a
// fresh in-memory store: one default library watching one root.
func newTestLibraries(t *testing.T, root string) (*PlayerService, *sqlite.Store) {
	t.Helper()
	store, err := sqlite.OpenMemory()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := store.AddLibrary(library.Library{
		ID: defaultLibraryID, Kind: library.KindLocal, Name: "Library",
	}); err != nil {
		t.Fatalf("add library: %v", err)
	}
	if added, err := store.AddRoot(defaultLibraryID, root); err != nil || !added {
		t.Fatalf("add root: added=%v err=%v", added, err)
	}

	svc := &PlayerService{
		log:      quietLog,
		ctx:      context.Background(),
		libStore: store,
		libs:     map[string]library.Source{},
		scanning: map[string]bool{},
	}
	svc.registerLocal(library.Library{ID: defaultLibraryID, Kind: library.KindLocal, Name: "Library"})

	return svc, store
}

// moloTestExt is one extension the running build can decode; the scanner
// filters on the extension list, so a dummy file named with one of them is
// indexed. The content is irrelevant: meta resolution falls back to the path.
func moloTestExt(t *testing.T) string {
	t.Helper()
	exts := molo.SupportedExtensions()
	if len(exts) == 0 {
		t.Fatal("no supported extensions to test with")
	}

	return exts[0]
}

func writeTestFiles(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("not audio, but the scanner only needs the name"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// startScan is what an existing library looks like to a test: its first root
// already indexed before the import happens.
func startScan(t *testing.T, svc *PlayerService, id string) {
	t.Helper()
	if err := svc.RescanLibrary(id); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	svc.wg.Wait()
}

func trackCount(t *testing.T, store *sqlite.Store, libraryID string) int {
	t.Helper()
	_, total, err := store.Tracks(libraryID, 100, 0)
	if err != nil {
		t.Fatalf("tracks: %v", err)
	}

	return total
}

// TestImportFolderMergesIntoTheLibrary pins the merge: the folder becomes a
// root of the existing library, its files land in the same catalogue, and the
// import's playlist holds exactly those tracks under the folder's name. The
// worker is joined before the asserts, so the test sees the finished state.
func TestImportFolderMergesIntoTheLibrary(t *testing.T) {
	ext := moloTestExt(t)

	rootA := t.TempDir()
	writeTestFiles(t,
		filepath.Join(rootA, "01"+ext),
		filepath.Join(rootA, "02"+ext),
	)
	svc, store := newTestLibraries(t, rootA)
	startScan(t, svc, defaultLibraryID)
	before := trackCount(t, store, defaultLibraryID)

	rootB := t.TempDir()
	b1 := filepath.Join(rootB, "01"+ext)
	writeTestFiles(t, b1)

	if _, err := svc.ImportFolder(defaultLibraryID, rootB); err != nil {
		t.Fatalf("import: %v", err)
	}
	svc.wg.Wait()

	roots, err := store.Roots(defaultLibraryID)
	if err != nil {
		t.Fatalf("roots: %v", err)
	}
	if len(roots) != 2 {
		t.Fatalf("roots = %v, want both", roots)
	}
	if got := trackCount(t, store, defaultLibraryID); got != before+1 {
		t.Fatalf("tracks = %d, want %d after the import", got, before+1)
	}

	playlists, err := store.Playlists(defaultLibraryID)
	if err != nil {
		t.Fatalf("playlists: %v", err)
	}
	if len(playlists) != 1 || playlists[0].Name != filepath.Base(rootB) {
		t.Fatalf("playlists = %+v, want one named after the folder", playlists)
	}
	page, err := store.PlaylistTracks(playlists[0].ID, 10, 0)
	if err != nil {
		t.Fatalf("playlist tracks: %v", err)
	}
	if len(page) != 1 || page[0].Ref != b1 {
		t.Fatalf("playlist = %+v, want [%s]", page, b1)
	}
}

// TestCreateUpdateAndIndependentFolderImport pins virtual library creation,
// editing, and independent per-library folder ownership across deletes.
func TestCreateUpdateAndIndependentFolderImport(t *testing.T) {
	ext := moloTestExt(t)

	rootA := t.TempDir()
	writeTestFiles(t, filepath.Join(rootA, "01"+ext))
	svc, store := newTestLibraries(t, rootA)
	startScan(t, svc, defaultLibraryID)

	if _, err := svc.CreateLibrary("   ", "ignored"); err == nil {
		t.Fatal("CreateLibrary accepted blank name")
	}

	second, err := svc.CreateLibrary("  Anime OST  ", "  Soundtracks  ")
	if err != nil {
		t.Fatalf("CreateLibrary: %v", err)
	}
	if second.Name != "Anime OST" || second.Description != "Soundtracks" {
		t.Fatalf("created = %+v", second)
	}
	if len(second.Roots) != 0 {
		t.Fatalf("virtual library should start with no roots, got %v", second.Roots)
	}

	updated, err := svc.UpdateLibrary(second.ID, "Game OST", "Updated note")
	if err != nil {
		t.Fatalf("UpdateLibrary: %v", err)
	}
	if updated.Name != "Game OST" || updated.Description != "Updated note" {
		t.Fatalf("updated = %+v", updated)
	}

	// Both libraries can import the same local folder independently.
	shared := t.TempDir()
	sharedFile := filepath.Join(shared, "track"+ext)
	writeTestFiles(t, sharedFile)

	if _, err := svc.ImportFolder(defaultLibraryID, shared); err != nil {
		t.Fatalf("import into first: %v", err)
	}
	svc.wg.Wait()
	if _, err := svc.ImportFolder(second.ID, shared); err != nil {
		t.Fatalf("import into second: %v", err)
	}
	svc.wg.Wait()

	// Removing the first library must not wipe the second library's playlist.
	if err := svc.RemoveLibrary(defaultLibraryID); err != nil {
		t.Fatalf("RemoveLibrary: %v", err)
	}
	lists, err := store.Playlists(second.ID)
	if err != nil || len(lists) != 1 || lists[0].TrackCount != 1 {
		t.Fatalf("second library playlists after first removed = (%+v, %v)", lists, err)
	}
	tracks, err := store.PlaylistTracks(lists[0].ID, 10, 0)
	if err != nil || len(tracks) != 1 || tracks[0].Ref != sharedFile {
		t.Fatalf("second library playlist tracks = (%+v, %v)", tracks, err)
	}
}

// TestBackfillClearsStaleCoverOnEmptyPlaylist verifies startup backfill removes
// stale non-custom covers from empty playlists while preserving custom art.
func TestBackfillClearsStaleCoverOnEmptyPlaylist(t *testing.T) {
	store, err := sqlite.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	art, err := artwork.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.AddLibrary(library.Library{
		ID: "local:a", Kind: library.KindLocal, Name: "A",
	}); err != nil {
		t.Fatal(err)
	}
	autoPl, err := store.CreatePlaylist("local:a", "EmptyAuto")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPlaylistCover(autoPl.ID, "stale-auto", false); err != nil {
		t.Fatal(err)
	}
	customPl, err := store.CreatePlaylist("local:a", "EmptyCustom")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPlaylistCover(customPl.ID, "kept-custom", true); err != nil {
		t.Fatal(err)
	}

	svc := &PlayerService{log: quietLog, art: art, libStore: store}
	svc.backfillCompositeCovers()

	lists, err := store.Playlists("local:a")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]library.Playlist{}
	for _, pl := range lists {
		byID[pl.ID] = pl
	}
	if byID[autoPl.ID].CoverID != "" {
		t.Fatalf("empty non-custom playlist kept stale cover %q", byID[autoPl.ID].CoverID)
	}
	if byID[customPl.ID].CoverID != "kept-custom" {
		t.Fatalf("empty custom playlist lost cover: %q", byID[customPl.ID].CoverID)
	}
}

// TestImportFolderRejectsOverlapAndReAdd pins the guard rails: a folder inside
// a watched root is refused, as is one containing it, and re-adding a watched
// root neither rescans into a second playlist nor errors.
func TestImportFolderRejectsOverlapAndReAdd(t *testing.T) {
	ext := moloTestExt(t)

	rootA := t.TempDir()
	writeTestFiles(t, filepath.Join(rootA, "01"+ext))
	svc, store := newTestLibraries(t, rootA)
	startScan(t, svc, defaultLibraryID)

	nested := filepath.Join(rootA, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ImportFolder(defaultLibraryID, nested); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("nested import = %v, want an overlap error", err)
	}
	if _, err := svc.ImportFolder(defaultLibraryID, rootA); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	svc.wg.Wait()

	playlists, err := store.Playlists(defaultLibraryID)
	if err != nil {
		t.Fatalf("playlists: %v", err)
	}
	if len(playlists) != 0 {
		t.Fatalf("playlists = %+v, want none from a rejected or duplicate import", playlists)
	}
}

func TestCoverAccentsCachesByCoverID(t *testing.T) {
	artDir := t.TempDir()
	art, err := artwork.Open(artDir)
	if err != nil {
		t.Fatal(err)
	}

	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, imgcolor.NRGBA{R: 0xd1, G: 0x5b, B: 0x5b, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	id, err := art.Put(buf.Bytes())
	if err != nil {
		t.Fatalf("Put art: %v", err)
	}

	svc := newPlayerService()
	svc.art = art
	first, err := svc.CoverAccents(id)
	if err != nil || len(first) == 0 {
		t.Fatalf("first CoverAccents: colors=%v err=%v", first, err)
	}

	// Remove cached art bytes from disk to prove the second call serves from memory.
	if p, ok := art.FilePath(id); ok {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}

	second, err := svc.CoverAccents(id)
	if err != nil || len(second) != len(first) || second[0] != first[0] {
		t.Fatalf("cached CoverAccents = %v (err=%v), want %v", second, err, first)
	}
}
