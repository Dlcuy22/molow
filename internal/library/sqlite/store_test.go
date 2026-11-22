package sqlite

import (
	"errors"
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

// TestForeignPathStaysWithItsLibrary pins the one-owner rule: a path already
// catalogued under another library is refused, and the refusal leaves both
// catalogues as they were.
func TestForeignPathStaysWithItsLibrary(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)
	if _, err := s.AddLibrary(library.Library{
		ID: "local:second", Kind: library.KindLocal, Name: "Second", Root: "/other",
	}); err != nil {
		t.Fatalf("add library: %v", err)
	}

	_, err := s.UpsertTrack("local:second", File{
		Path: "/music/a/01.flac", Dev: 1, Ino: 999, Size: 20, Mtime: 2000,
	}, TrackRow{NativeID: "id:2", Title: "Taken", Album: "Alpha"})
	if !errors.Is(err, ErrForeignFile) {
		t.Fatalf("upsert = %v, want ErrForeignFile", err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM track WHERE library_id = 'local:second'`); n != 0 {
		t.Fatalf("second library has %d tracks, want none", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM track`); n != 3 {
		t.Fatalf("track count = %d, want the original 3", n)
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

// TestRemoteTrackKeepsItsRef pins the remote half of the catalogue: a row with
// no file is listed by a kind-prefixed ref, resolves from that ref, likes from
// it, and survives a scan's missing pass, which only walks track_file.
func TestRemoteTrackKeepsItsRef(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)

	row := TrackRow{
		NativeID: "vid123", Title: "Remote", Artist: "Artist", Album: "Beta",
		DurationMs: 200000, CoverID: "cover-Beta",
	}
	if _, err := s.UpsertRemoteTrack("local:test", library.KindYTM, row); err != nil {
		t.Fatalf("upsert remote: %v", err)
	}
	row.Title = "Remote (Remastered)"
	if _, err := s.UpsertRemoteTrack("local:test", library.KindYTM, row); err != nil {
		t.Fatalf("re-upsert remote: %v", err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM track WHERE native_id = 'vid123'`); n != 1 {
		t.Fatalf("importing the same hit twice made %d rows, want 1", n)
	}

	ref := library.RemoteRef(library.KindYTM, "vid123")
	tr, ok, err := s.TrackByRef(ref)
	if err != nil || !ok {
		t.Fatalf("resolve %q: ok=%v err=%v", ref, ok, err)
	}
	if tr.Kind != library.KindYTM || tr.Ref != ref || tr.Flags&library.FlagPlayable == 0 {
		t.Fatalf("remote row = %+v", tr)
	}
	if tr.Title != "Remote (Remastered)" {
		t.Fatalf("re-import did not refresh the row: %q", tr.Title)
	}

	if err := s.SetLiked(ref, true); err != nil {
		t.Fatalf("like: %v", err)
	}
	if tr, ok, _ = s.TrackByRef(ref); !ok || !tr.Liked {
		t.Fatal("remote row did not keep its like")
	}

	// The missing pass works on track_file, so a row without one is untouched.
	if err := s.MarkMissing("local:test", 1<<40); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.TrackByRef(ref); !ok {
		t.Fatal("a scan lost the remote row")
	}
}

// TestRemotePlaylistKeepsItsEntries covers an imported YouTube Music playlist:
// the row remembers its remote id, and its entries are remote refs in order.
func TestRemotePlaylistKeepsItsEntries(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)

	ids := []string{"vid1", "vid2"}
	refs := make([]string, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, library.RemoteRef(library.KindYTM, id))
		if _, err := s.UpsertRemoteTrack("local:test", library.KindYTM, TrackRow{
			NativeID: id, Title: id, Album: "Beta",
		}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}

	playlist, err := s.CreatePlaylistWithRemote("local:test", "Imported", "PL123")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.AddToPlaylist(playlist.ID, refs); err != nil {
		t.Fatalf("entries: %v", err)
	}

	lists, err := s.Playlists("local:test")
	if err != nil || len(lists) != 1 {
		t.Fatalf("playlists: %v %v", lists, err)
	}
	if lists[0].RemoteID != "PL123" || lists[0].TrackCount != len(refs) {
		t.Fatalf("playlist = %+v", lists[0])
	}
	tracks, err := s.PlaylistTracks(playlist.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != len(refs) || tracks[0].Ref != refs[0] || tracks[1].Ref != refs[1] {
		t.Fatalf("entries = %+v", tracks)
	}
}

// TestRemoteRefMeansTheEarliestRow pins the tie-break. The engine only ever
// sees a ref, so when two libraries hold the same provider id the ref means the
// row that imported it first, and a like lands on that row.
func TestRemoteRefMeansTheEarliestRow(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)
	if _, err := s.AddLibrary(library.Library{
		ID: "local:other", Kind: library.KindLocal, Name: "Other",
	}); err != nil {
		t.Fatalf("add library: %v", err)
	}

	for i, lib := range []string{"local:test", "local:other"} {
		if _, err := s.UpsertRemoteTrack(lib, library.KindYTM, TrackRow{
			NativeID: "vidX", Title: []string{"First", "Second"}[i],
		}); err != nil {
			t.Fatalf("upsert into %s: %v", lib, err)
		}
	}

	ref := library.RemoteRef(library.KindYTM, "vidX")
	tr, ok, err := s.TrackByRef(ref)
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if tr.LibraryID != "local:test" || tr.Title != "First" {
		t.Fatalf("ref resolved to %+v", tr)
	}

	if err := s.SetLiked(ref, true); err != nil {
		t.Fatalf("like: %v", err)
	}
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM like l JOIN track t ON t.id = l.track_id
		 WHERE t.library_id = 'local:other' AND t.native_id = 'vidX'`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("the like landed on the second library's row")
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

func TestAlbumTracksNormalizesZeroDiscAndUnnumbered(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.AddLibrary(library.Library{
		ID: "local:test", Kind: library.KindLocal, Name: "Test", Root: "/music",
	}); err != nil {
		t.Fatalf("add library: %v", err)
	}

	rows := []struct {
		path   string
		title  string
		discNo int
		track  int
	}{
		{"/a/08.flac", "Eight", 0, 8},
		{"/a/01.flac", "One", 1, 1},
		{"/a/09.flac", "Nine", 1, 9},
		{"/a/bonus.flac", "Bonus", 1, 0},
		{"/a/2-01.flac", "Disc2One", 2, 1},
	}
	for i, r := range rows {
		if _, err := s.UpsertTrack("local:test", File{
			Path: r.path, Dev: 1, Ino: uint64(i + 1), Size: 10, Mtime: 1000,
		}, TrackRow{
			NativeID: "id:" + r.path, Title: r.title, Artist: "Artist",
			Album: "Album", AlbumArtist: "Artist", DiscNo: r.discNo, TrackNo: r.track,
		}); err != nil {
			t.Fatalf("upsert %s: %v", r.path, err)
		}
	}

	got, err := s.AlbumTracks("local:test", "Album", "Artist")
	if err != nil {
		t.Fatalf("AlbumTracks: %v", err)
	}
	want := []string{"One", "Eight", "Nine", "Bonus", "Disc2One"}
	if gotTitles := titles(got); len(gotTitles) != len(want) {
		t.Fatalf("titles = %v, want %v", gotTitles, want)
	} else {
		for i := range want {
			if gotTitles[i] != want[i] {
				t.Fatalf("titles = %v, want %v", gotTitles, want)
			}
		}
	}
}

func TestRescanBackfillsMissingTrackIndex(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)

	// /music/a/01.flac was seeded with TrackNo=0, DiscNo=0.
	if _, err := s.UpsertTrack("local:test", File{
		Path: "/music/a/01.flac", Dev: 1, Ino: 101, Size: 10, Mtime: 1000,
	}, TrackRow{
		NativeID: "id:1:/music/a/01.flac", Title: "Ignored", Album: "Alpha",
		TrackNo: 1, DiscNo: 1,
	}); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	tr, ok, err := s.TrackByRef("/music/a/01.flac")
	if err != nil || !ok {
		t.Fatalf("TrackByRef: ok=%v err=%v", ok, err)
	}
	if tr.TrackNo != 1 || tr.DiscNo != 1 {
		t.Fatalf("backfill failed: track_no=%d disc_no=%d", tr.TrackNo, tr.DiscNo)
	}
	if tr.Title != "First" {
		t.Fatalf("title changed on unchanged file: %q", tr.Title)
	}
}

func TestTouchIfUnchanged(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)

	// Mark everything missing so a hit proves missing=0 and seen_at update.
	if err := s.MarkMissing("local:test", 1<<40); err != nil {
		t.Fatal(err)
	}

	// /music/a/02.flac has TrackNo=1, CoverID="cover-Alpha".
	f := File{Path: "/music/a/02.flac", Dev: 1, Ino: 102, Size: 10, Mtime: 1000}

	// Missing cover forces a miss so the scanner re-extracts artwork.
	if _, ok, err := s.TouchIfUnchanged("local:test", f, 2, 0, func(string) bool { return false }); err != nil || ok {
		t.Fatalf("missing cover: ok=%v err=%v, want miss", ok, err)
	}

	// Changed size or mtime misses.
	if _, ok, err := s.TouchIfUnchanged("local:test", File{Path: f.Path, Size: 11, Mtime: f.Mtime}, 0, 0, nil); err != nil || ok {
		t.Fatalf("changed size: ok=%v err=%v, want miss", ok, err)
	}
	if _, ok, err := s.TouchIfUnchanged("local:test", File{Path: f.Path, Size: f.Size, Mtime: f.Mtime + 1}, 0, 0, nil); err != nil || ok {
		t.Fatalf("changed mtime: ok=%v err=%v, want miss", ok, err)
	}

	// Unchanged file with present cover hits and clears missing.
	id, ok, err := s.TouchIfUnchanged("local:test", f, 2, 0, func(coverID string) bool { return coverID == "cover-Alpha" })
	if err != nil || !ok || id == 0 {
		t.Fatalf("unchanged hit: id=%d ok=%v err=%v", id, ok, err)
	}
	tr, found, err := s.TrackByRef(f.Path)
	if err != nil || !found || tr.Flags&library.FlagPlayable == 0 {
		t.Fatalf("TrackByRef after touch: found=%v flags=%v err=%v", found, tr.Flags, err)
	}

	// Missing track_no with a filename fallback misses so UpsertTrack can backfill.
	fZero := File{Path: "/music/a/01.flac", Dev: 1, Ino: 101, Size: 10, Mtime: 1000}
	if _, ok, err := s.TouchIfUnchanged("local:test", fZero, 1, 0, func(string) bool { return true }); err != nil || ok {
		t.Fatalf("backfill needed: ok=%v err=%v, want miss", ok, err)
	}

	// Foreign owner returns ErrForeignFile.
	if _, err := s.AddLibrary(library.Library{ID: "local:other", Kind: library.KindLocal, Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.TouchIfUnchanged("local:other", f, 0, 0, nil); !errors.Is(err, ErrForeignFile) {
		t.Fatalf("foreign owner: err=%v, want ErrForeignFile", err)
	}
}

func TestCoverIDsRespectCustomCoverAndProtectInGC(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)

	pl, err := s.CreatePlaylist("local:test", "Mix")
	if err != nil {
		t.Fatal(err)
	}

	// Automatic cover updates write when custom_cover = 0.
	if err := s.SetLibraryCover("local:test", "lib-auto", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlaylistCover(pl.ID, "pl-auto", false); err != nil {
		t.Fatal(err)
	}

	// Custom covers set custom_cover = 1 and block later automatic overwrites.
	if err := s.SetLibraryCover("local:test", "lib-custom", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLibraryCover("local:test", "lib-overwrite", false); err != nil {
		t.Fatal(err)
	}
	lib, err := s.Library("local:test")
	if err != nil {
		t.Fatal(err)
	}
	if lib.CoverID != "lib-custom" || !lib.CustomCover {
		t.Fatalf("library cover = (%q, custom=%v), want (lib-custom, true)", lib.CoverID, lib.CustomCover)
	}

	if err := s.SetPlaylistCover(pl.ID, "pl-custom", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPlaylistCover(pl.ID, "pl-overwrite", false); err != nil {
		t.Fatal(err)
	}
	lists, err := s.Playlists("local:test")
	if err != nil || len(lists) != 1 {
		t.Fatalf("Playlists = (%+v, %v)", lists, err)
	}
	if lists[0].CoverID != "pl-custom" || !lists[0].CustomCover {
		t.Fatalf("playlist cover = (%q, custom=%v), want (pl-custom, true)", lists[0].CoverID, lists[0].CustomCover)
	}

	refs, err := s.ReferencedCoverIDs()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cover-Alpha", "cover-Beta", "lib-custom", "pl-custom"} {
		if _, ok := refs[want]; !ok {
			t.Fatalf("ReferencedCoverIDs missing %q: %+v", want, refs)
		}
	}
}

func TestRemoteTrackMetaPersistsAndBackfills(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.AddLibrary(library.Library{
		ID: "local:ytm", Kind: library.KindLocal, Name: "Imported",
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate a pre-migration row inserted without thumbnail_url or artist_id.
	if _, err := s.UpsertRemoteTrack("local:ytm", library.KindYTM, TrackRow{
		NativeID: "vid1",
		Title:    "Mesmerizer",
		Artist:   "32ki",
		CoverID:  "cover-1",
	}); err != nil {
		t.Fatal(err)
	}

	meta, ok, err := s.TrackRemoteMeta("ytm:vid1")
	if err != nil || !ok {
		t.Fatalf("TrackRemoteMeta = (%+v, %v, %v)", meta, ok, err)
	}
	if meta.ThumbnailURL != "" || meta.ArtistID != "" {
		t.Fatalf("initial remote meta = %+v, want empty", meta)
	}

	if err := s.BackfillTrackRemoteMeta("ytm:vid1", "https://lh3.googleusercontent.com/cover", "UCartist1", "Mesmerizer"); err != nil {
		t.Fatal(err)
	}
	// A second backfill must not overwrite populated fields.
	if err := s.BackfillTrackRemoteMeta("ytm:vid1", "https://other", "UCother", "Other"); err != nil {
		t.Fatal(err)
	}

	meta, ok, err = s.TrackRemoteMeta("ytm:vid1")
	if err != nil || !ok {
		t.Fatalf("TrackRemoteMeta after backfill = (%+v, %v, %v)", meta, ok, err)
	}
	if meta.ThumbnailURL != "https://lh3.googleusercontent.com/cover" || meta.ArtistID != "UCartist1" {
		t.Fatalf("backfilled remote meta = %+v", meta)
	}
	tr, ok, err := s.TrackByRef("ytm:vid1")
	if err != nil || !ok {
		t.Fatalf("TrackByRef = (%+v, %v, %v)", tr, ok, err)
	}
	if tr.Album != "Mesmerizer" {
		t.Fatalf("backfilled album = %q, want Mesmerizer", tr.Album)
	}

	// Re-importing with blank remote metadata keeps the stored values.
	if _, err := s.UpsertRemoteTrack("local:ytm", library.KindYTM, TrackRow{
		NativeID: "vid1",
		Title:    "Mesmerizer",
		Artist:   "32ki",
	}); err != nil {
		t.Fatal(err)
	}
	meta, ok, err = s.TrackRemoteMeta("ytm:vid1")
	if err != nil || !ok || meta.ThumbnailURL != "https://lh3.googleusercontent.com/cover" || meta.ArtistID != "UCartist1" {
		t.Fatalf("re-import lost remote meta: (%+v, %v, %v)", meta, ok, err)
	}
}
