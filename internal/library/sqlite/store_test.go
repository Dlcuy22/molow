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

// TestSharedPathOwnedIndependentlyPerLibrary pins per-library file ownership:
// two libraries can import the same file path with separate track/playlist rows,
// and removing one library never touches the other's tracks or playlists.
func TestSharedPathOwnedIndependentlyPerLibrary(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)
	if _, err := s.AddLibrary(library.Library{
		ID: "local:second", Kind: library.KindLocal, Name: "Second", Description: "Copy",
	}); err != nil {
		t.Fatalf("add library: %v", err)
	}

	if _, err := s.UpsertTrack("local:second", File{
		Path: "/music/a/01.flac", Dev: 1, Ino: 101, Size: 10, Mtime: 1000,
	}, TrackRow{NativeID: "id:2:/music/a/01.flac", Title: "First", Album: "Alpha"}); err != nil {
		t.Fatalf("upsert into second library: %v", err)
	}
	pl, err := s.CreatePlaylist("local:second", "Kept")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddToPlaylist(pl.ID, []string{"/music/a/01.flac"}); err != nil {
		t.Fatal(err)
	}

	// Removing the first library must not cascade into the second library's playlist.
	if err := s.RemoveLibrary("local:test"); err != nil {
		t.Fatal(err)
	}
	tracks, err := s.PlaylistTracks(pl.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[0].Ref != "/music/a/01.flac" {
		t.Fatalf("second library playlist after removing first = %+v, want 1 track", tracks)
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

// TestLikesApplyGloballyAcrossLibraries pins global ref-keyed likes: liking a
// track marks every library copy of that ref liked and survives removing one
// of those libraries.
func TestLikesApplyGloballyAcrossLibraries(t *testing.T) {
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
	if err := s.SetLiked(ref, true); err != nil {
		t.Fatalf("like: %v", err)
	}

	for _, lib := range []string{"local:test", "local:other"} {
		rows, _, err := s.Tracks(lib, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range rows {
			if r.Ref == ref {
				found = true
				if !r.Liked {
					t.Fatalf("library %s copy of %s was not liked", lib, ref)
				}
			}
		}
		if !found {
			t.Fatalf("library %s missing %s", lib, ref)
		}
	}

	if err := s.RemoveLibrary("local:test"); err != nil {
		t.Fatal(err)
	}
	liked, err := s.Liked(ref)
	if err != nil || !liked {
		t.Fatalf("Liked after removing first library = (%v, %v), want true", liked, err)
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

	// Another library does not hit a file indexed only under local:test.
	if _, err := s.AddLibrary(library.Library{ID: "local:other", Kind: library.KindLocal, Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.TouchIfUnchanged("local:other", f, 0, 0, nil); err != nil || ok {
		t.Fatalf("other library touch = (ok=%v, err=%v), want miss", ok, err)
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

func TestUpdateLibraryPersistsNameAndDescription(t *testing.T) {
	s := newTestStore(t)
	created, err := s.AddLibrary(library.Library{
		ID:          "local:custom",
		Kind:        library.KindLocal,
		Name:        "Original",
		Description: "First note",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "Original" || created.Description != "First note" {
		t.Fatalf("created = %+v", created)
	}

	updated, err := s.UpdateLibrary("local:custom", "Renamed", "Updated description")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Renamed" || updated.Description != "Updated description" {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestMigration005MigratesTrackFilesAndLikes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "library.db")
	dsn := "file:" + filepath.ToSlash(p) + "?_pragma=foreign_keys(1)"
	db, err := driver.Open(dsn, func(c *sqlite3.Conn) error {
		return fts5.Register(c)
	})
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	for i, name := range []string{
		"migrations/001_init.sql",
		"migrations/002_library_roots.sql",
		"migrations/003_cover_ids.sql",
		"migrations/004_track_remote_meta.sql",
	} {
		body, err := migrationFS.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := db.Exec("PRAGMA user_version = " + string(rune('1'+i))); err != nil {
			t.Fatalf("version %d: %v", i+1, err)
		}
	}

	now := time.Now().Unix()
	if _, err := db.Exec(`INSERT INTO library (id, kind, name, root, created_at) VALUES ('local:a', 'local', 'A', '/a', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO track (id, library_id, kind, native_id, title, added_at) VALUES (1, 'local:a', 'local', 'n1', 'LocalSong', ?), (2, 'local:a', 'ytm', 'vid9', 'RemoteSong', ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO track_file (path, track_id, size, mtime, seen_at) VALUES ('/a/song.flac', 1, 10, 20, ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO like (track_id, created_at) VALUES (1, ?), (2, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(p)
	if err != nil {
		t.Fatalf("open migrated: %v", err)
	}
	defer func() { _ = s.Close() }()

	for _, ref := range []string{"/a/song.flac", "ytm:vid9"} {
		liked, err := s.Liked(ref)
		if err != nil || !liked {
			t.Fatalf("Liked(%q) after migration 005 = (%v, %v), want true", ref, liked, err)
		}
		tr, ok, err := s.TrackByRef(ref)
		if err != nil || !ok || !tr.Liked {
			t.Fatalf("TrackByRef(%q) after migration 005 = (%+v, %v, %v)", ref, tr, ok, err)
		}
	}
}

func TestYTMSyncAndRemotePlaylistHelpers(t *testing.T) {
	s := newTestStore(t)
	lib, err := s.AddLibrary(library.Library{
		ID: "ytm_UC123", Kind: library.KindYTM, Name: "Kasaki",
	})
	if err != nil {
		t.Fatalf("AddLibrary: %v", err)
	}
	if lib.Kind != library.KindYTM || lib.Name != "Kasaki" {
		t.Fatalf("lib = %+v", lib)
	}

	if at, err := s.SyncTime(lib.ID, "likes"); err != nil || at != 0 {
		t.Fatalf("initial SyncTime = (%d, %v), want (0, nil)", at, err)
	}
	if err := s.SetSyncTime(lib.ID, "likes", 1700000000); err != nil {
		t.Fatalf("SetSyncTime: %v", err)
	}
	if at, err := s.SyncTime(lib.ID, "likes"); err != nil || at != 1700000000 {
		t.Fatalf("SyncTime = (%d, %v), want 1700000000", at, err)
	}

	if _, err := s.UpsertRemoteTrack(lib.ID, library.KindYTM, TrackRow{NativeID: "v1", Title: "One"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRemoteTrack(lib.ID, library.KindYTM, TrackRow{NativeID: "v2", Title: "Two"}); err != nil {
		t.Fatal(err)
	}

	pl1, err := s.UpsertRemotePlaylist(lib.ID, "My List", "PL1", true)
	if err != nil || !pl1.Owned {
		t.Fatalf("UpsertRemotePlaylist insert: (%+v, %v)", pl1, err)
	}
	pl1Renamed, err := s.UpsertRemotePlaylist(lib.ID, "My Renamed List", "PL1", false)
	if err != nil || pl1Renamed.ID != pl1.ID || pl1Renamed.Name != "My Renamed List" || pl1Renamed.Owned {
		t.Fatalf("UpsertRemotePlaylist update = (%+v, %v)", pl1Renamed, err)
	}
	if _, err := s.UpsertRemotePlaylist(lib.ID, "Dropped List", "PL2", false); err != nil {
		t.Fatalf("UpsertRemotePlaylist PL2: %v", err)
	}

	r1 := library.ScopedRemoteRef(library.KindYTM, lib.ID, "v1")
	r2 := library.ScopedRemoteRef(library.KindYTM, lib.ID, "v2")
	if err := s.ReplacePlaylistEntries(pl1.ID, []string{r2, r1, r2}); err != nil {
		t.Fatalf("ReplacePlaylistEntries: %v", err)
	}
	if has, err := s.PlaylistHasTrack(pl1.ID, r1); err != nil || !has {
		t.Fatalf("PlaylistHasTrack(r1) = (%v, %v), want true", has, err)
	}
	if has, err := s.PlaylistHasTrack(pl1.ID, library.RemoteRef(library.KindYTM, "missing")); err != nil || has {
		t.Fatalf("PlaylistHasTrack(missing) = (%v, %v), want false", has, err)
	}
	tracks, err := s.PlaylistTracks(pl1.ID, 10, 0)
	if err != nil || len(tracks) != 2 || tracks[0].Ref != r2 || tracks[1].Ref != r1 {
		t.Fatalf("PlaylistTracks after replace = (%+v, %v)", tracks, err)
	}

	if err := s.PruneRemotePlaylists(lib.ID, map[string]bool{"PL1": true}); err != nil {
		t.Fatalf("PruneRemotePlaylists: %v", err)
	}
	lists, err := s.Playlists(lib.ID)
	if err != nil || len(lists) != 1 || lists[0].RemoteID != "PL1" || lists[0].Owned {
		t.Fatalf("Playlists after prune = (%+v, %v)", lists, err)
	}

	// Tombstones stay active until expired or absent from remoteKeys.
	if err := s.AddTombstone(lib.ID, "playlist:PLdel", 1000); err != nil {
		t.Fatalf("AddTombstone PLdel: %v", err)
	}
	if err := s.AddTombstone(lib.ID, "playlist:PLold", 100); err != nil {
		t.Fatalf("AddTombstone PLold: %v", err)
	}
	if err := s.AddTombstone(lib.ID, "playlist:PLgone", 1000); err != nil {
		t.Fatalf("AddTombstone PLgone: %v", err)
	}
	active, err := s.ActiveTombstones(lib.ID, 500)
	if err != nil || !active["playlist:PLdel"] || active["playlist:PLold"] {
		t.Fatalf("ActiveTombstones = (%+v, %v)", active, err)
	}
	if err := s.ReconcileTombstones(lib.ID, map[string]bool{"playlist:PLdel": true, "playlist:PLold": true}, 500); err != nil {
		t.Fatalf("ReconcileTombstones: %v", err)
	}
	afterReconcile, err := s.ActiveTombstones(lib.ID, 0)
	if err != nil || len(afterReconcile) != 1 || !afterReconcile["playlist:PLdel"] {
		t.Fatalf("after ReconcileTombstones = (%+v, %v)", afterReconcile, err)
	}
}

func TestWatchSourcesAndScopedMissing(t *testing.T) {
	s := newTestStore(t)
	lib, err := s.AddLibrary(library.Library{
		ID: "local:watch", Kind: library.KindLocal, Name: "Watch",
	})
	if err != nil {
		t.Fatal(err)
	}

	if has, err := s.HasWatchedSources(lib.ID); err != nil || has {
		t.Fatalf("empty HasWatchedSources = (%v, %v), want false", has, err)
	}

	rootA := filepath.Join("/music", "folderA")
	rootB := filepath.Join("/music", "folderB")
	if _, err := s.AddRoot(lib.ID, rootA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRoot(lib.ID, rootB); err != nil {
		t.Fatal(err)
	}

	plA, err := s.CreatePlaylistWithSource(lib.ID, "folderA", rootA, true)
	if err != nil {
		t.Fatal(err)
	}
	plRemote, err := s.CreatePlaylistWithRemote(lib.ID, "Remote", "PLremote")
	if err != nil {
		t.Fatal(err)
	}
	if !plA.Watched || !plRemote.Watched {
		t.Fatalf("expected watched defaults true: plA=%+v plRemote=%+v", plA, plRemote)
	}

	if has, err := s.HasWatchedSources(lib.ID); err != nil || !has {
		t.Fatalf("HasWatchedSources = (%v, %v), want true", has, err)
	}

	fileA := filepath.Join(rootA, "01.flac")
	fileB := filepath.Join(rootB, "02.flac")
	if _, err := s.UpsertTrack(lib.ID, File{Path: fileA, Size: 10, Mtime: 100}, TrackRow{NativeID: "a1", Title: "A1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertTrack(lib.ID, File{Path: fileB, Size: 10, Mtime: 100}, TrackRow{NativeID: "b1", Title: "B1"}); err != nil {
		t.Fatal(err)
	}

	// Unwatch folderA -> library_root.watched flips to 0, MarkMissingInRoots for
	// watched roots leaves fileA intact.
	updatedA, err := s.SetPlaylistWatched(lib.ID, plA.ID, false)
	if err != nil || updatedA.Watched {
		t.Fatalf("SetPlaylistWatched(plA, false) = (%+v, %v)", updatedA, err)
	}
	watchedRoots, err := s.WatchedRoots(lib.ID)
	if err != nil || len(watchedRoots) != 1 || watchedRoots[0] != rootB {
		t.Fatalf("WatchedRoots = (%+v, %v), want [%q]", watchedRoots, err, rootB)
	}
	if err := s.MarkMissingInRoots(lib.ID, watchedRoots, time.Now().Unix()+100); err != nil {
		t.Fatal(err)
	}
	trA, okA, _ := s.TrackByRef(fileA)
	trB, okB, _ := s.TrackByRef(fileB)
	if !okA || trA.Ref == "" {
		t.Fatalf("unwatched fileA should remain playable: ok=%v tr=%+v", okA, trA)
	}
	if !okB || trB.Ref != "" {
		t.Fatalf("watched stale fileB should be marked missing: ok=%v tr=%+v", okB, trB)
	}

	// Unwatch remote playlist -> HasWatchedSources still true because rootB is watched.
	if _, err := s.SetPlaylistWatched(lib.ID, plRemote.ID, false); err != nil {
		t.Fatal(err)
	}
	if has, err := s.HasWatchedRemotePlaylists(lib.ID); err != nil || has {
		t.Fatalf("HasWatchedRemotePlaylists = (%v, %v), want false", has, err)
	}
	if err := s.SetRootWatched(lib.ID, rootB, false); err != nil {
		t.Fatal(err)
	}
	if has, err := s.HasWatchedSources(lib.ID); err != nil || has {
		t.Fatalf("HasWatchedSources after unwatching all = (%v, %v), want false", has, err)
	}
}

func TestMigration009AndNormalizeLegacyLibraryID(t *testing.T) {
	s := newTestStore(t)

	row, err := s.AddLibraryRow(LibraryRow{
		Library: library.Library{
			ID: "ytm:UCold", Kind: library.KindYTM, Name: "Old YTM",
		},
		AccountID:    "UCold",
		CredentialID: "cred_old",
		Config:       `{"email":"a@b.com"}`,
	})
	if err != nil {
		t.Fatalf("AddLibraryRow: %v", err)
	}
	if row.CredentialID != "cred_old" || row.Config != `{"email":"a@b.com"}` {
		t.Fatalf("row = %+v", row)
	}

	if _, err := s.UpsertRemoteTrack("ytm:UCold", library.KindYTM, TrackRow{NativeID: "vid1", Title: "One"}); err != nil {
		t.Fatal(err)
	}
	pl, err := s.UpsertRemotePlaylist("ytm:UCold", "Liked Music", "LM", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplacePlaylistEntries(pl.ID, []string{"ytm:vid1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSyncTime("ytm:UCold", "playlists", 12345); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTombstone("ytm:UCold", "playlist:PLx", 999); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLiked("ytm:vid1", true); err != nil {
		t.Fatal(err)
	}

	// Also put vid1 into a local library plus vid2 only in YTM, so
	// MigrateUnscopedRemoteLikes preserves local unscoped likes while scoping YTM.
	if _, err := s.AddLibrary(library.Library{ID: "local:mix", Kind: library.KindLocal, Name: "Local"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRemoteTrack("local:mix", library.KindYTM, TrackRow{NativeID: "vid1", Title: "One"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRemoteTrack("ytm:UCold", library.KindYTM, TrackRow{NativeID: "vid2", Title: "Two"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLiked("ytm:vid2", true); err != nil {
		t.Fatal(err)
	}

	if err := s.NormalizeLegacyLibraryID("ytm:UCold", "ytm_deadbeef"); err != nil {
		t.Fatalf("NormalizeLegacyLibraryID: %v", err)
	}
	if err := s.MigrateUnscopedRemoteLikes("ytm_deadbeef"); err != nil {
		t.Fatalf("MigrateUnscopedRemoteLikes: %v", err)
	}

	gotRow, err := s.LibraryRow("ytm_deadbeef")
	if err != nil {
		t.Fatalf("LibraryRow(ytm_deadbeef): %v", err)
	}
	if gotRow.CredentialID != "cred_old" || gotRow.AccountID != "UCold" || gotRow.TrackCount != 2 {
		t.Fatalf("normalized row = %+v", gotRow)
	}
	if _, err := s.LibraryRow("ytm:UCold"); err == nil {
		t.Fatal("old library id ytm:UCold still exists")
	}
	if at, err := s.SyncTime("ytm_deadbeef", "playlists"); err != nil || at != 12345 {
		t.Fatalf("SyncTime = (%d, %v), want 12345", at, err)
	}
	if active, err := s.ActiveTombstones("ytm_deadbeef", 0); err != nil || !active["playlist:PLx"] {
		t.Fatalf("ActiveTombstones = (%+v, %v)", active, err)
	}
	if pls, err := s.Playlists("ytm_deadbeef"); err != nil || len(pls) != 1 || pls[0].TrackCount != 1 {
		t.Fatalf("Playlists = (%+v, %v)", pls, err)
	}
	if ok, _ := s.Liked("ytm:ytm_deadbeef:vid1"); !ok {
		t.Fatal("missing migrated scoped like for vid1")
	}
	if ok, _ := s.Liked("ytm:vid1"); !ok {
		t.Fatal("local library unscoped like for vid1 was wrongly removed")
	}
	if ok, _ := s.Liked("ytm:ytm_deadbeef:vid2"); !ok {
		t.Fatal("missing migrated scoped like for vid2")
	}
	if ok, _ := s.Liked("ytm:vid2"); ok {
		t.Fatal("YTM-only unscoped like for vid2 should have been removed")
	}
}

func TestScopedRemoteRefsAndPerLibraryLikeIsolation(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.AddLibrary(library.Library{ID: "ytm_a", Kind: library.KindYTM, Name: "YTM A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddLibrary(library.Library{ID: "ytm_b", Kind: library.KindYTM, Name: "YTM B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddLibrary(library.Library{ID: "local_1", Kind: library.KindLocal, Name: "Local"}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.UpsertRemoteTrack("ytm_a", library.KindYTM, TrackRow{NativeID: "vidShared", Title: "A Track"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRemoteTrack("ytm_b", library.KindYTM, TrackRow{NativeID: "vidShared", Title: "B Track"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRemoteTrack("local_1", library.KindYTM, TrackRow{NativeID: "vidShared", Title: "Local Copy"}); err != nil {
		t.Fatal(err)
	}

	// Remote libraries emit scoped refs; local imports emit unscoped refs.
	tracksA, _, err := s.Tracks("ytm_a", 10, 0)
	if err != nil || len(tracksA) != 1 || tracksA[0].Ref != "ytm:ytm_a:vidShared" {
		t.Fatalf("tracksA = (%+v, %v)", tracksA, err)
	}
	tracksB, _, err := s.Tracks("ytm_b", 10, 0)
	if err != nil || len(tracksB) != 1 || tracksB[0].Ref != "ytm:ytm_b:vidShared" {
		t.Fatalf("tracksB = (%+v, %v)", tracksB, err)
	}
	tracksL, _, err := s.Tracks("local_1", 10, 0)
	if err != nil || len(tracksL) != 1 || tracksL[0].Ref != "ytm:vidShared" {
		t.Fatalf("tracksL = (%+v, %v)", tracksL, err)
	}

	// Like in ytm_a only; ytm_b and local_1 stay unliked.
	if err := s.SetLiked("ytm:ytm_a:vidShared", true); err != nil {
		t.Fatal(err)
	}
	tracksA, _, _ = s.Tracks("ytm_a", 10, 0)
	tracksB, _, _ = s.Tracks("ytm_b", 10, 0)
	tracksL, _, _ = s.Tracks("local_1", 10, 0)
	if !tracksA[0].Liked || tracksB[0].Liked || tracksL[0].Liked {
		t.Fatalf("like isolation failed: A=%v B=%v L=%v", tracksA[0].Liked, tracksB[0].Liked, tracksL[0].Liked)
	}

	// Scoped TrackByRef resolves to the exact owning library; unscoped prefers local.
	gotB, ok, err := s.TrackByRef("ytm:ytm_b:vidShared")
	if err != nil || !ok || gotB.LibraryID != "ytm_b" || gotB.Title != "B Track" {
		t.Fatalf("TrackByRef scoped B = (%+v, %v, %v)", gotB, ok, err)
	}
	gotU, ok, err := s.TrackByRef("ytm:vidShared")
	if err != nil || !ok || gotU.LibraryID != "local_1" {
		t.Fatalf("TrackByRef unscoped = (%+v, %v, %v)", gotU, ok, err)
	}
}

func TestAddLibraryAccountIDFallback(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.AddLibrary(library.Library{
		ID:   "ytm_deadbeef01234567",
		Kind: library.KindYTM,
		Name: "Modern ID",
	}); err != nil {
		t.Fatal(err)
	}
	modern, err := s.LibraryRow("ytm_deadbeef01234567")
	if err != nil {
		t.Fatal(err)
	}
	if modern.AccountID != "" {
		t.Fatalf("modern AccountID = %q, want empty", modern.AccountID)
	}

	if _, err := s.AddLibrary(library.Library{
		ID:   "ytm:UClegacy",
		Kind: library.KindYTM,
		Name: "Legacy ID",
	}); err != nil {
		t.Fatal(err)
	}
	legacy, err := s.LibraryRow("ytm:UClegacy")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.AccountID != "UClegacy" {
		t.Fatalf("legacy AccountID = %q, want UClegacy", legacy.AccountID)
	}
}
