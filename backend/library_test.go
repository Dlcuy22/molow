package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dlcuy22/molo"
	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/sqlite"
)

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

// TestImportRefusesAnotherLibrarysFolder pins the one-owner rule at the import
// entry points: a folder another library watches is refused with the owner's
// name, no track lands in the target, and no playlist is created for it.
func TestImportRefusesAnotherLibrarysFolder(t *testing.T) {
	ext := moloTestExt(t)

	rootA := t.TempDir()
	writeTestFiles(t, filepath.Join(rootA, "01"+ext))
	svc, store := newTestLibraries(t, rootA)
	startScan(t, svc, defaultLibraryID)

	rootB := t.TempDir()
	writeTestFiles(t, filepath.Join(rootB, "01"+ext))
	owner, err := svc.AddLocalLibrary(rootB)
	if err != nil {
		t.Fatalf("add library: %v", err)
	}
	svc.wg.Wait()

	if _, err := svc.ImportFolder(defaultLibraryID, rootB); err == nil ||
		!strings.Contains(err.Error(), "already belongs to library") ||
		!strings.Contains(err.Error(), owner.Name) {
		t.Fatalf("import = %v, want the owner named", err)
	}
	nested := filepath.Join(rootA, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddLocalLibrary(rootA); err == nil ||
		!strings.Contains(err.Error(), "already belongs to library") {
		t.Fatalf("add = %v, want the owner named", err)
	}
	if _, err := svc.AddLocalLibrary(nested); err == nil ||
		!strings.Contains(err.Error(), "overlaps library") {
		t.Fatalf("add nested = %v, want an overlap error", err)
	}

	playlists, err := store.Playlists(defaultLibraryID)
	if err != nil {
		t.Fatalf("playlists: %v", err)
	}
	if len(playlists) != 0 {
		t.Fatalf("playlists = %+v, want none from a refused import", playlists)
	}
	if got := trackCount(t, store, defaultLibraryID); got != 1 {
		t.Fatalf("tracks = %d, want the original 1", got)
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
