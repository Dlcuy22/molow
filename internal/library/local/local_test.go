package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dlcuy22/molo"
	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/sqlite"
)

// newTestLocal builds a Local over a fresh in-memory store and one library row.
func newTestLocal(t *testing.T) (*Local, *sqlite.Store) {
	t.Helper()
	store, err := sqlite.OpenMemory()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	row, err := store.AddLibrary(library.Library{
		ID: "local:test", Kind: library.KindLocal, Name: "Test",
	})
	if err != nil {
		t.Fatalf("add library: %v", err)
	}

	return New(store, row, nil), store
}

// testExt is one extension the running build can decode; the scanner filters on
// the extension list, so a dummy file named with one of them is indexed. The
// content is irrelevant: meta resolution falls back to the path and the probe
// simply fails to produce a duration.
func testExt(t *testing.T) string {
	t.Helper()
	exts := molo.SupportedExtensions()
	if len(exts) == 0 {
		t.Fatal("no supported extensions to test with")
	}

	return exts[0]
}

func writeFiles(t *testing.T, paths ...string) {
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

// TestImportScanReportsOnlyTheImportedRoot proves the multi-root walk and the
// import filter: every root is indexed, and the refs returned name only the
// folder the import just added, in walk order. The Local was built before the
// second root existed, so this also pins the live roots read.
func TestImportScanReportsOnlyTheImportedRoot(t *testing.T) {
	src, store := newTestLocal(t)
	ext := testExt(t)

	rootA := t.TempDir()
	rootB := t.TempDir()
	a1 := filepath.Join(rootA, "01"+ext)
	a2 := filepath.Join(rootA, "02"+ext)
	b1 := filepath.Join(rootB, "01"+ext)
	writeFiles(t, a1, a2, b1)

	if _, err := store.AddRoot("local:test", rootA); err != nil {
		t.Fatalf("add root a: %v", err)
	}
	if _, err := store.AddRoot("local:test", rootB); err != nil {
		t.Fatalf("add root b: %v", err)
	}

	plain, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if plain != 3 {
		t.Fatalf("scan indexed %d files, want 3", plain)
	}

	res, err := src.ImportScan(context.Background(), rootB)
	if err != nil {
		t.Fatalf("import scan: %v", err)
	}
	if res.Count != 3 {
		t.Fatalf("import scan indexed %d files, want 3", res.Count)
	}
	if len(res.Refs) != 1 || res.Refs[0] != b1 {
		t.Fatalf("import refs = %v, want [%s]", res.Refs, b1)
	}
}

// TestScanSkipsHiddenAndSystemDirs pins the skip rules across roots: a hidden
// folder and Synology's @eaDir are never indexed.
func TestScanSkipsHiddenAndSystemDirs(t *testing.T) {
	src, store := newTestLocal(t)
	ext := testExt(t)

	root := t.TempDir()
	writeFiles(t,
		filepath.Join(root, "01"+ext),
		filepath.Join(root, ".hidden", "01"+ext),
		filepath.Join(root, "@eaDir", "01"+ext),
	)
	if _, err := store.AddRoot("local:test", root); err != nil {
		t.Fatalf("add root: %v", err)
	}

	if _, err := src.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}

	_, total, err := store.Tracks("local:test", 100, 0)
	if err != nil {
		t.Fatalf("tracks: %v", err)
	}
	if total != 1 {
		t.Fatalf("indexed %d tracks, want only the visible one", total)
	}
}

// TestScanOnAnEmptyLibrary is the defensive case: no roots, no walk, no error.
func TestScanOnAnEmptyLibrary(t *testing.T) {
	src, _ := newTestLocal(t)

	n, err := src.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if n != 0 {
		t.Fatalf("indexed %d files, want 0", n)
	}
}
