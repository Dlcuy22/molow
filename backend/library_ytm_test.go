package backend

import (
	"context"
	"testing"

	"github.com/dlcuy22/molow/internal/library"
	ytm "github.com/dlcuy22/ytm-go"
)

// ytmTestProvider builds a provider whose playlist walk returns ids under name,
// so an import runs without a network. The rows carry no thumbnail, which keeps
// the cover pass a no-op instead of reaching for one.
func ytmTestProvider(name string, ids ...string) *ytmProvider {
	items := make([]ytm.Song, 0, len(ids))
	for _, id := range ids {
		items = append(items, ytm.Song{ID: id, Name: id, DurationMs: 200_000})
	}
	prov := &ytmProvider{}
	prov.loadPlaylist = func(context.Context, string, *ytm.BuiltInContinuation) (*ytm.Playlist, error) {
		return &ytm.Playlist{Name: name, Items: items}, nil
	}

	return prov
}

// TestImportYTMPlaylistBecomesAPlaylist pins the playlist half: the row carries
// the remote id, the entries are remote refs in catalogue order, and the name is
// the catalogue's rather than the palette's label.
func TestImportYTMPlaylistBecomesAPlaylist(t *testing.T) {
	svc, store := newTestLibraries(t, t.TempDir())
	svc.ytm = newYTMIndex()
	svc.ytmProv = ytmTestProvider("Imported", "vid1", "vid2")

	res, err := svc.ImportYTM(defaultLibraryID, YTMResult{
		VideoID: "PL42", Title: "Pasted title", Kind: "Playlist",
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.PlaylistID == "" || res.Tracks != 2 || res.Name != "Imported" {
		t.Fatalf("result = %+v", res)
	}

	lists, err := store.Playlists(defaultLibraryID)
	if err != nil || len(lists) != 1 {
		t.Fatalf("playlists = %+v, err = %v", lists, err)
	}
	if lists[0].RemoteID != "PL42" || lists[0].TrackCount != 2 {
		t.Fatalf("playlist row = %+v", lists[0])
	}

	entries, err := store.PlaylistTracks(lists[0].ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		library.RemoteRef(library.KindYTM, "vid1"),
		library.RemoteRef(library.KindYTM, "vid2"),
	}
	if len(entries) != len(want) {
		t.Fatalf("entries = %+v", entries)
	}
	for i, ref := range want {
		if entries[i].Ref != ref || entries[i].Kind != library.KindYTM {
			t.Fatalf("entry %d = %+v", i, entries[i])
		}
	}
}

// TestImportYTMSongIsOneTrack covers the hit the palette searched: it becomes
// one remote row from the metadata the row already showed, with no playlist.
func TestImportYTMSongIsOneTrack(t *testing.T) {
	svc, store := newTestLibraries(t, t.TempDir())
	svc.ytm = newYTMIndex()
	svc.ytmProv = ytmTestProvider("unused")

	res, err := svc.ImportYTM(defaultLibraryID, YTMResult{
		VideoID: "vid9", Title: "Song", Artist: "Artist", Album: "Album",
		DurationMs: 123000, Kind: "Song", Playable: true,
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Tracks != 1 || res.PlaylistID != "" {
		t.Fatalf("result = %+v", res)
	}

	tr, ok, err := store.TrackByRef(library.RemoteRef(library.KindYTM, "vid9"))
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if tr.Title != "Song" || tr.Artist != "Artist" || tr.DurationMs != 123000 {
		t.Fatalf("track = %+v", tr)
	}
}

// TestImportYTMRejectsAnAlbum pins the refusal: the catalogue client has no
// album loader, so an album is turned away instead of importing a fragment.
func TestImportYTMRejectsAnAlbum(t *testing.T) {
	svc, store := newTestLibraries(t, t.TempDir())
	svc.ytm = newYTMIndex()
	svc.ytmProv = ytmTestProvider("unused")

	if _, err := svc.ImportYTM(defaultLibraryID, YTMResult{
		VideoID: "MPREb_x", Title: "Album", Kind: "Album",
	}); err == nil {
		t.Fatal("an album imported")
	}
	if lists, _ := store.Playlists(defaultLibraryID); len(lists) != 0 {
		t.Fatalf("rejected import left %+v", lists)
	}
}

func TestImportYTMPlaylistHydratesMissingAlbumsAndArtists(t *testing.T) {
	svc, store := newTestLibraries(t, t.TempDir())
	svc.ytm = newYTMIndex()
	prov := ytmTestProvider("Favorit", "vid1", "vid2")
	prov.loadSong = func(_ context.Context, id string) (*ytm.Song, error) {
		return &ytm.Song{
			ID:      id,
			Name:    "Song " + id,
			Artists: []ytm.Artist{{ID: "UC" + id, Name: "Artist " + id}},
			Album:   &ytm.Playlist{ID: "MPREb_" + id, Name: "Album " + id},
		}, nil
	}
	svc.ytmProv = prov

	if _, err := svc.ImportYTM(defaultLibraryID, YTMResult{
		VideoID: "PL99", Title: "Favorit", Kind: "Playlist",
	}); err != nil {
		t.Fatalf("import: %v", err)
	}

	albums, totalAlbums, err := store.Albums(defaultLibraryID, 10, 0)
	if err != nil || totalAlbums != 2 || len(albums) != 2 {
		t.Fatalf("albums = %+v (total=%d), err=%v", albums, totalAlbums, err)
	}
	artists, totalArtists, err := store.Artists(defaultLibraryID, 10, 0)
	if err != nil || totalArtists != 2 || len(artists) != 2 {
		t.Fatalf("artists = %+v (total=%d), err=%v", artists, totalArtists, err)
	}
}
