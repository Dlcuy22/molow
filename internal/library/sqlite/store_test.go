package sqlite

import (
	"testing"

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
