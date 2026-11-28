package navidrome

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dlcuy22/molow/internal/artwork"
	"github.com/dlcuy22/molow/internal/library"
	"github.com/dlcuy22/molow/internal/library/sqlite"
	nd "github.com/dlcuy22/molow/internal/navidrome"
)

func TestSourceSyncAndPlaylistMutation(t *testing.T) {
	var (
		starredID string
		created   bool
		renamed   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/rest/"), ".view")
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "getAlbumList2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status": "ok",
					"albumList2": map[string]any{
						"album": []map[string]any{
							{"id": "al1", "name": "Debut", "artist": "Band", "songCount": 2, "duration": 300},
						},
					},
				},
			})
		case "getAlbum":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status": "ok",
					"album": map[string]any{
						"id": "al1", "name": "Debut", "artist": "Band",
						"song": []map[string]any{
							{"id": "s1", "title": "Intro", "album": "Debut", "albumId": "al1", "artist": "Band", "track": 1, "duration": 120, "starred": "2026-01-01"},
							{"id": "s2", "title": "Outro", "album": "Debut", "albumId": "al1", "artist": "Band", "track": 2, "duration": 180},
						},
					},
				},
			})
		case "getPlaylists":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status": "ok",
					"playlists": map[string]any{
						"playlist": []map[string]any{
							{"id": "pl1", "name": "Favorites"},
						},
					},
				},
			})
		case "getPlaylist":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status": "ok",
					"playlist": map[string]any{
						"id": "pl1", "name": "Favorites",
						"entry": []map[string]any{{"id": "s1"}},
					},
				},
			})
		case "star":
			starredID = r.URL.Query().Get("id")
			_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{"status": "ok"}})
		case "createPlaylist":
			created = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status":   "ok",
					"playlist": map[string]any{"id": "pl2", "name": r.URL.Query().Get("name")},
				},
			})
		case "updatePlaylist":
			if name := r.URL.Query().Get("name"); name != "" {
				renamed = name
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{"status": "ok"}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{"status": "ok"}})
		}
	}))
	defer srv.Close()

	store, err := sqlite.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	art, err := artwork.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lib, err := store.AddLibrary(library.Library{
		ID:   "navidrome_srv1",
		Kind: library.KindNavidrome,
		Name: "Home Navidrome",
		Root: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := nd.NewClient(nd.Config{ServerURL: srv.URL, Username: "u", Password: "p"}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	src := New(store, lib, art, client, nil)

	n, err := src.Sync(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("Sync = (%d, %v), want (2, nil)", n, err)
	}

	page, err := src.Feed(context.Background(), "tracks", "", 50)
	if err != nil || len(page.Shelves) != 1 || len(page.Shelves[0].Items) != 2 {
		t.Fatalf("Feed tracks = %+v, err=%v", page, err)
	}
	if !page.Shelves[0].Items[0].Liked {
		t.Fatalf("expected starred track s1 to be liked")
	}

	ref2 := library.ScopedRemoteRef(library.KindNavidrome, lib.ID, "s2")
	if err := src.SetLiked(context.Background(), ref2, true); err != nil || starredID != "s2" {
		t.Fatalf("SetLiked = %v, starredID=%q", err, starredID)
	}

	pl, err := src.CreatePlaylist(context.Background(), "Roadtrip")
	if err != nil || !created || pl.RemoteID != "pl2" {
		t.Fatalf("CreatePlaylist = %+v, created=%v, err=%v", pl, created, err)
	}
	if err := src.RenamePlaylist(context.Background(), pl.ID, "Renamed"); err != nil || renamed != "Renamed" {
		t.Fatalf("RenamePlaylist = %v, renamed=%q", err, renamed)
	}
}

func TestNavidromeAlbumGroupingAndFallback(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	_ = png.Encode(&buf1, image.NewRGBA(image.Rect(0, 0, 10, 10)))
	_ = png.Encode(&buf2, image.NewRGBA(image.Rect(0, 0, 20, 20)))
	fakeCover1 := buf1.Bytes()
	fakeCover2 := buf2.Bytes()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/rest/"), ".view")
		switch method {
		case "getAlbumList2":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status": "ok",
					"albumList2": map[string]any{
						"album": []map[string]any{
							{"id": "al_queen", "name": "A Night at the Opera", "artist": "Queen", "coverArt": "cov_queen", "songCount": 2},
							{"id": "al_gaga", "name": "The Fame", "artist": "Lady Gaga", "coverArt": "cov_gaga", "songCount": 1},
						},
					},
				},
			})
		case "getAlbum":
			w.Header().Set("Content-Type", "application/json")
			albumID := r.URL.Query().Get("id")
			if albumID == "al_queen" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"subsonic-response": map[string]any{
						"status": "ok",
						"album": map[string]any{
							"id": "al_queen", "name": "A Night at the Opera", "artist": "Queen", "coverArt": "cov_queen",
							"song": []map[string]any{
								// Notice: album, albumId, and coverArt are omitted to simulate Subsonic child song elements.
								{"id": "s_queen_1", "title": "Bohemian Rhapsody", "artist": "Queen", "track": 1, "duration": 354},
								{"id": "s_queen_2", "title": "Under Pressure", "artist": "Queen, David Bowie", "track": 2, "duration": 248},
							},
						},
					},
				})
			} else if albumID == "al_gaga" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"subsonic-response": map[string]any{
						"status": "ok",
						"album": map[string]any{
							"id": "al_gaga", "name": "The Fame", "artist": "Lady Gaga", "coverArt": "cov_gaga",
							"song": []map[string]any{
								{"id": "s_gaga_1", "title": "Poker Face", "artist": "Lady Gaga", "track": 1, "duration": 238},
							},
						},
					},
				})
			}
		case "getCoverArt":
			coverID := r.URL.Query().Get("id")
			if coverID == "cov_queen" {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(fakeCover1)
			} else {
				w.Header().Set("Content-Type", "image/jpeg")
				_, _ = w.Write(fakeCover2)
			}
		case "getPlaylists":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status": "ok",
					"playlists": map[string]any{"playlist": []any{}},
				},
			})
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{"status": "ok"}})
		}
	}))
	defer srv.Close()

	store, err := sqlite.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	art, err := artwork.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lib, err := store.AddLibrary(library.Library{
		ID:   "navidrome_srv2",
		Kind: library.KindNavidrome,
		Name: "Test Remote",
		Root: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := nd.NewClient(nd.Config{ServerURL: srv.URL, Username: "u", Password: "p"}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	src := New(store, lib, art, client, nil)

	n, err := src.Sync(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("Sync = (%d, %v), want (3, nil)", n, err)
	}

	// Verify Albums query returns exactly 2 albums, not 3 (Queen + Bowie track must remain in Queen's album).
	albums, total, err := store.Albums(lib.ID, 10, 0)
	if err != nil {
		t.Fatalf("store.Albums: %v", err)
	}
	if total != 2 || len(albums) != 2 {
		t.Fatalf("expected 2 albums, got total=%d, len=%d (%+v)", total, len(albums), albums)
	}

	var queenAlbum, gagaAlbum library.Album
	for _, a := range albums {
		if a.Name == "A Night at the Opera" {
			queenAlbum = a
		} else if a.Name == "The Fame" {
			gagaAlbum = a
		}
	}
	if queenAlbum.Name == "" || queenAlbum.Artist != "Queen" || queenAlbum.SongCount != 2 {
		t.Fatalf("unexpected queen album: %+v", queenAlbum)
	}
	if queenAlbum.CoverID == "" {
		t.Fatalf("expected queen album to have cover_id, got empty")
	}
	if gagaAlbum.Name == "" || gagaAlbum.Artist != "Lady Gaga" || gagaAlbum.SongCount != 1 {
		t.Fatalf("unexpected gaga album: %+v", gagaAlbum)
	}
	if gagaAlbum.CoverID == "" {
		t.Fatalf("expected gaga album to have cover_id, got empty")
	}

	// Verify AlbumTracks returns all tracks including the multi-artist track.
	tracks, err := src.AlbumTracks(context.Background(), "A Night at the Opera", "Queen")
	if err != nil {
		t.Fatalf("src.AlbumTracks: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("expected 2 tracks for A Night at the Opera, got %d: %+v", len(tracks), tracks)
	}
	if tracks[0].Title != "Bohemian Rhapsody" || tracks[1].Title != "Under Pressure" {
		t.Fatalf("unexpected tracks: %+v", tracks)
	}
	if tracks[0].CoverID == "" || tracks[1].CoverID == "" {
		t.Fatalf("expected tracks to inherit album cover, got track0=%q, track1=%q", tracks[0].CoverID, tracks[1].CoverID)
	}

	// Verify Feed for "all" and "albums" returns the shelves correctly.
	feedAll, err := src.Feed(context.Background(), "all", "", 10)
	if err != nil {
		t.Fatalf("Feed(all): %v", err)
	}
	var albumsShelf *library.Shelf
	for i := range feedAll.Shelves {
		if feedAll.Shelves[i].Kind == "albums" {
			albumsShelf = &feedAll.Shelves[i]
			break
		}
	}
	if albumsShelf == nil || len(albumsShelf.Albums) != 2 {
		t.Fatalf("expected 2 albums on all feed albums shelf, got %+v", albumsShelf)
	}

	feedAlbums, err := src.Feed(context.Background(), "albums", "", 10)
	if err != nil || len(feedAlbums.Shelves) != 1 || len(feedAlbums.Shelves[0].Albums) != 2 {
		t.Fatalf("Feed(albums) = %+v, err=%v", feedAlbums, err)
	}
}

func TestSyncReconcilesUnstarredTracks(t *testing.T) {
	var starredValue string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/rest/"), ".view")
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "getAlbumList2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status": "ok",
					"albumList2": map[string]any{
						"album": []map[string]any{
							{"id": "al1", "name": "Debut", "artist": "Band", "songCount": 1},
						},
					},
				},
			})
		case "getAlbum":
			song := map[string]any{
				"id": "s1", "title": "Intro", "album": "Debut", "albumId": "al1", "artist": "Band", "track": 1, "duration": 120,
			}
			if starredValue != "" {
				song["starred"] = starredValue
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status": "ok",
					"album": map[string]any{
						"id": "al1", "name": "Debut", "artist": "Band",
						"song": []map[string]any{song},
					},
				},
			})
		case "getPlaylists":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{"status": "ok", "playlists": map[string]any{"playlist": []any{}}},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{"status": "ok"}})
		}
	}))
	defer srv.Close()

	store, err := sqlite.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	art, err := artwork.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lib, err := store.AddLibrary(library.Library{
		ID:   "navidrome_srv3",
		Kind: library.KindNavidrome,
		Name: "Test Lib",
		Root: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := nd.NewClient(nd.Config{ServerURL: srv.URL, Username: "u", Password: "p"}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	src := New(store, lib, art, client, nil)

	// 1. Initial sync with starred song.
	starredValue = "2026-01-01T12:00:00Z"
	if _, err := src.Sync(context.Background()); err != nil {
		t.Fatalf("Sync 1: %v", err)
	}
	page, err := src.Feed(context.Background(), "tracks", "", 10)
	if err != nil || len(page.Shelves) == 0 || len(page.Shelves[0].Items) == 0 {
		t.Fatalf("Feed tracks after sync 1: %+v, err=%v", page, err)
	}
	if !page.Shelves[0].Items[0].Liked {
		t.Fatal("expected track to be liked when starred on Navidrome")
	}

	// 2. Second sync where song was unstarred on Navidrome.
	starredValue = ""
	if _, err := src.Sync(context.Background()); err != nil {
		t.Fatalf("Sync 2: %v", err)
	}
	page, err = src.Feed(context.Background(), "tracks", "", 10)
	if err != nil || len(page.Shelves) == 0 || len(page.Shelves[0].Items) == 0 {
		t.Fatalf("Feed tracks after sync 2: %+v, err=%v", page, err)
	}
	if page.Shelves[0].Items[0].Liked {
		t.Fatal("expected track like to be cleared when unstarred on Navidrome")
	}
}

func TestSyncCancellationDoesNotDeleteTracks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/rest/"), ".view")
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "getAlbumList2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status": "ok",
					"albumList2": map[string]any{
						"album": []map[string]any{
							{"id": "al1", "name": "Debut", "artist": "Band", "songCount": 2},
						},
					},
				},
			})
		case "getAlbum":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status": "ok",
					"album": map[string]any{
						"id": "al1", "name": "Debut", "artist": "Band",
						"song": []map[string]any{
							{"id": "s1", "title": "Track 1", "album": "Debut", "albumId": "al1", "artist": "Band", "track": 1, "duration": 120},
							{"id": "s2", "title": "Track 2", "album": "Debut", "albumId": "al1", "artist": "Band", "track": 2, "duration": 180},
						},
					},
				},
			})
		case "getPlaylists":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{"status": "ok", "playlists": map[string]any{"playlist": []any{}}},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{"status": "ok"}})
		}
	}))
	defer srv.Close()

	store, err := sqlite.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	art, err := artwork.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lib, err := store.AddLibrary(library.Library{
		ID:   "navidrome_srv3",
		Kind: library.KindNavidrome,
		Name: "Test Lib",
		Root: srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := nd.NewClient(nd.Config{ServerURL: srv.URL, Username: "u", Password: "p"}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	src := New(store, lib, art, client, nil)

	// Initial sync populates 2 tracks.
	if n, err := src.Sync(context.Background()); err != nil || n != 2 {
		t.Fatalf("Initial sync: n=%d err=%v", n, err)
	}

	// Cancelled sync must return context error and NOT wipe existing tracks.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	_, err = src.Sync(ctx)
	if err == nil {
		t.Fatal("expected cancelled sync to return error")
	}

	// Verify both tracks are still in SQLite.
	page, err := src.Feed(context.Background(), "tracks", "", 10)
	if err != nil || len(page.Shelves) == 0 {
		t.Fatalf("Feed tracks after cancelled sync: %+v, err=%v", page, err)
	}
	if len(page.Shelves[0].Items) != 2 {
		t.Fatalf("tracks were wiped on cancelled sync! expected 2 tracks, got %d", len(page.Shelves[0].Items))
	}
}
