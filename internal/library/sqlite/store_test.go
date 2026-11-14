package sqlite

import (
	"path/filepath"
	"testing"
	"time"

	sqlite3 "github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"

	"github.com/dlcuy22/molow/internal/library"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	return s
}

func seed(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.AddLibrary(library.Library{
		ID: "local:test", Kind: library.KindLocal, Name: "Test", Root: "/music",
	}); err != nil {
		t.Fatalf("add library: %v", err)
	}

	rows := []struct {
		path  string
		ino   uint64
		title string
		album string
	}{
		{"/music/a/01.flac", 101, "First", "Alpha"},
		{"/music/a/02.flac", 102, "Second", "Alpha"},
		{"/music/b/01.flac", 201, "Third", "Beta"},
	}
	for i, r := range rows {
		if _, err := s.UpsertTrack("local:test", File{
			Path: r.path, Dev: 1, Ino: r.ino, Size: 10, Mtime: 1000,
		}, TrackRow{
			NativeID: "id:1:" + r.path, Title: r.title, Artist: "Artist",
			Album: r.album, AlbumArtist: "Artist", TrackNo: i % 2,
			DurationMs: 180000, CoverID: "cover-" + r.album,
		}); err != nil {
			t.Fatalf("upsert %s: %v", r.path, err)
		}
	}
}

func TestRescanIsNoOp(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)

	before := count(t, s, `SELECT COUNT(*) FROM track`)
	if _, err := s.UpsertTrack("local:test", File{
		Path: "/music/a/01.flac", Dev: 1, Ino: 101, Size: 10, Mtime: 1000,
	}, TrackRow{NativeID: "id:1:/music/a/01.flac", Title: "Changed", Album: "Alpha"}); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if after := count(t, s, `SELECT COUNT(*) FROM track`); after != before {
		t.Fatalf("track count changed: %d -> %d", before, after)
	}
	// Unchanged size+mtime means the title is not re-read; that is the whole
	// point of the identity check.
	var title string
	if err := s.db.QueryRow(`SELECT title FROM track WHERE native_id = ?`,
		"id:1:/music/a/01.flac").Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "First" {
		t.Fatalf("title was rewritten on an unchanged file: %q", title)
	}
}

func TestRenameKeepsTrackAndLike(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)

	track, ok, err := s.TrackByRef("/music/a/01.flac")
	if err != nil || !ok {
		t.Fatalf("resolve: %v %v", err, ok)
	}
	if err := s.SetLiked("/music/a/01.flac", true); err != nil {
		t.Fatalf("like: %v", err)
	}

	// Same inode, new path: a move.
	if _, err := s.UpsertTrack("local:test", File{
		Path: "/music/a/renamed.flac", Dev: 1, Ino: 101, Size: 10, Mtime: 1000,
	}, TrackRow{NativeID: "id:1:101", Title: "First", Artist: "Artist", Album: "Alpha"}); err != nil {
		t.Fatalf("move: %v", err)
	}

	if n := count(t, s, `SELECT COUNT(*) FROM track`); n != 3 {
		t.Fatalf("move created a track: %d", n)
	}
	moved, ok, err := s.TrackByRef("/music/a/renamed.flac")
	if err != nil || !ok {
		t.Fatalf("resolve moved: %v %v", err, ok)
	}
	if !moved.Liked {
		t.Fatal("like was lost on rename")
	}
	if moved.Title != track.Title {
		t.Fatalf("track changed: %q -> %q", track.Title, moved.Title)
	}
	if _, ok, _ := s.TrackByRef("/music/a/01.flac"); ok {
		t.Fatal("old path still resolves")
	}
}

func TestSearchFindsByEachColumn(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)

	for _, q := range []string{"second", "artist", "beta"} {
		got, err := s.Search("local:test", q, 10)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		if len(got) == 0 {
			t.Fatalf("search %q found nothing", q)
		}
	}
}

func TestAlbumsGroupByAlbumArtist(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)

	albums, total, err := s.Albums("local:test", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(albums) != 2 {
		t.Fatalf("albums: total=%d len=%d", total, len(albums))
	}
	byName := map[string]library.Album{}
	for _, a := range albums {
		byName[a.Name] = a
	}
	if byName["Alpha"].SongCount != 2 {
		t.Fatalf("Alpha count: %d", byName["Alpha"].SongCount)
	}
	if byName["Alpha"].CoverID != "cover-Alpha" {
		t.Fatalf("Alpha cover: %q", byName["Alpha"].CoverID)
	}
}

func TestPlaylistOrderAndGapClosure(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)

	p, err := s.CreatePlaylist("local:test", "Mix")
	if err != nil {
		t.Fatal(err)
	}
	refs := []string{"/music/a/01.flac", "/music/a/02.flac", "/music/b/01.flac"}
	if err := s.AddToPlaylist(p.ID, refs); err != nil {
		t.Fatal(err)
	}
	// A duplicate is ignored rather than appended twice.
	if err := s.AddToPlaylist(p.ID, refs[:1]); err != nil {
		t.Fatal(err)
	}
	items, err := s.PlaylistTracks(p.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("playlist length: %d", len(items))
	}
	if items[0].Title != "First" || items[2].Title != "Third" {
		t.Fatalf("order: %v", titles(items))
	}

	if err := s.RemoveFromPlaylist(p.ID, refs[:1]); err != nil {
		t.Fatal(err)
	}
	items, err = s.PlaylistTracks(p.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Title != "Second" {
		t.Fatalf("after remove: %v", titles(items))
	}
	var pos int
	if err := s.db.QueryRow(
		`SELECT position FROM playlist_entry WHERE playlist_id = ? AND position = 0`, p.ID,
	).Scan(&pos); err != nil {
		t.Fatalf("positions not dense: %v", err)
	}
}

func TestMarkMissingHidesRef(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)

	if err := s.MarkMissing("local:test", 1<<40); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Tracks("local:test", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range got {
		if tr.Ref != "" {
			t.Fatalf("missing file still has a ref: %q", tr.Ref)
		}
		if tr.Flags&library.FlagPlayable != 0 {
			t.Fatal("missing file marked playable")
		}
	}
}

func count(t *testing.T, s *Store, query string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}

	return n
}

func titles(items []library.Track) []string {
	out := make([]string, len(items))
	for i, t := range items {
		out[i] = t.Title
	}

	return out
}

func TestLibraryRoots(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.AddLibrary(library.Library{
		ID: "local:mix", Kind: library.KindLocal, Name: "Mix", Root: "",
	}); err != nil {
		t.Fatalf("add library: %v", err)
	}

	added, err := s.AddRoot("local:mix", `/music`)
	if err != nil || !added {
		t.Fatalf("first add: added=%v err=%v", added, err)
	}
	added, err = s.AddRoot("local:mix", `/music`)
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if added {
		t.Fatal("re-adding the same root reported a new one")
	}
	added, err = s.AddRoot("local:mix", `/more`)
	if err != nil || !added {
		t.Fatalf("second add: added=%v err=%v", added, err)
	}

	roots, err := s.Roots("local:mix")
	if err != nil {
		t.Fatalf("roots: %v", err)
	}
	if len(roots) != 2 || roots[0] != `/music` || roots[1] != `/more` {
		t.Fatalf("roots = %v, want [/music /more]", roots)
	}
	if _, err := s.Roots("local:none"); err != nil {
		t.Fatalf("roots of an unknown library: %v", err)
	}

	row, err := s.Library("local:mix")
	if err != nil {
		t.Fatalf("library: %v", err)
	}
	if len(row.Roots) != 2 {
		t.Fatalf("read back %d roots, want 2", len(row.Roots))
	}
	rows, err := s.Libraries()
	if err != nil {
		t.Fatalf("libraries: %v", err)
	}
	if len(rows) != 1 || len(rows[0].Roots) != 2 {
		t.Fatalf("list = %+v, want one library with 2 roots", rows)
	}
}

// TestLibraryLogoCovers pins the card's artwork: distinct covers only, the
// most recently added first, and an empty library returns nothing.
func TestLibraryLogoCovers(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)

	covers, err := s.LibraryLogoCovers("local:test", 4)
	if err != nil {
		t.Fatalf("logo covers: %v", err)
	}
	// The seed's rows share one added_at second, so the pin is the id
	// tie-break: two distinct covers, alphabetical within the tie.
	if len(covers) != 2 {
		t.Fatalf("covers = %v, want the two distinct album covers", covers)
	}
	if covers[0] != "cover-Alpha" || covers[1] != "cover-Beta" {
		t.Fatalf("covers = %v, want [cover-Alpha cover-Beta]", covers)
	}

	none, err := s.LibraryLogoCovers("local:none", 4)
	if err != nil {
		t.Fatalf("covers of an unknown library: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("unknown library returned %v, want none", none)
	}
}

// TestMigrationSeedsTheRoots builds a version-1 database by hand and proves
// migration 002 turns its root column into the roots row it must become.
func TestMigrationSeedsTheRoots(t *testing.T) {
	p := filepath.Join(t.TempDir(), "library.db")
	dsn := "file:" + filepath.ToSlash(p)
	db, err := driver.Open(dsn, func(c *sqlite3.Conn) error {
		return fts5.Register(c)
	})
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	body, err := migrationFS.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatalf("read 001: %v", err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("apply 001: %v", err)
	}
	now := time.Now().Unix()
	if _, err := db.Exec(
		`INSERT INTO library (id, kind, name, root, created_at) VALUES ('local:old', 'local', 'Old', '/music', ?)`,
		now,
	); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatalf("pin version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	s, err := Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()

	roots, err := s.Roots("local:old")
	if err != nil {
		t.Fatalf("roots: %v", err)
	}
	if len(roots) != 1 || roots[0] != "/music" {
		t.Fatalf("seeded roots = %v, want [/music]", roots)
	}
}
