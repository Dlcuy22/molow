package backend

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
	"time"

	"github.com/dlcuy22/molow/internal/artwork"
	"github.com/dlcuy22/molow/internal/library"
	ndlib "github.com/dlcuy22/molow/internal/library/navidrome"
	"github.com/dlcuy22/molow/internal/library/sqlite"
	nd "github.com/dlcuy22/molow/internal/navidrome"
	"github.com/dlcuy22/molow/internal/trackcache"
)

func newNavidromeTestService(t *testing.T) (*PlayerService, *sqlite.Store, *CredentialStore) {
	t.Helper()
	store, err := sqlite.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	art, err := artwork.Open(t.TempDir())
	if err != nil {
		t.Fatalf("artwork.Open: %v", err)
	}
	tracks, err := trackcache.Open(t.TempDir(), 16<<20)
	if err != nil {
		t.Fatalf("trackcache.Open: %v", err)
	}

	ks := newMemKeyStore()
	cs := newCredentialStore(t.TempDir(), ks)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	svc := &PlayerService{
		ctx:       ctx,
		cancel:    cancel,
		libStore:  store,
		libs:      map[string]library.Source{},
		scanning:  map[string]bool{},
		art:       art,
		tracks:    tracks,
		credStore: cs,
	}
	svc.ensureSourceRouter()
	return svc, store, cs
}

func TestNavidromeManagerAddUpdateOpenAndSeams(t *testing.T) {
	var (
		scrobbleSubs []string
		coverBuf     bytes.Buffer
	)
	_ = png.Encode(&coverBuf, image.NewRGBA(image.Rect(0, 0, 16, 16)))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/rest/"), ".view")
		switch method {
		case "ping":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{"status": "ok"}})
		case "getAlbumList2":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status":     "ok",
					"albumList2": map[string]any{"album": []any{}},
				},
			})
		case "getPlaylists":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status":    "ok",
					"playlists": map[string]any{"playlist": []any{}},
				},
			})
		case "getSong":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status": "ok",
					"song": map[string]any{
						"id":       "s1",
						"title":    "Nightcall",
						"artist":   "Kavinsky",
						"album":    "OutRun",
						"coverArt": "cov1",
						"duration": 258,
					},
				},
			})
		case "getCoverArt":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(coverBuf.Bytes())
		case "getLyricsBySongId":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status": "ok",
					"lyricsList": map[string]any{
						"structuredLyrics": []map[string]any{
							{
								"synced": true,
								"line": []map[string]any{
									{"start": 1000, "value": "I'm giving you a nightcall"},
								},
							},
						},
					},
				},
			})
		case "scrobble":
			scrobbleSubs = append(scrobbleSubs, r.URL.Query().Get("submission"))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{"status": "ok"}})
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{"status": "ok"}})
		}
	}))
	defer srv.Close()

	svc, store, cs := newNavidromeTestService(t)

	// 1. AddNavidromeLibrary stores encrypted credentials and non-secret config.
	lib, err := svc.AddNavidromeLibrary("My Navidrome", "Home server", srv.URL, "kelvin", "secret123")
	if err != nil {
		t.Fatalf("AddNavidromeLibrary: %v", err)
	}
	if lib.Kind != library.KindNavidrome || lib.Name != "My Navidrome" {
		t.Fatalf("unexpected lib: %+v", lib)
	}

	row, err := store.LibraryRow(lib.ID)
	if err != nil {
		t.Fatalf("LibraryRow: %v", err)
	}
	if row.CredentialID == "" || !cs.Exists(row.CredentialID) {
		t.Fatalf("expected encrypted credential file for %q", row.CredentialID)
	}
	if strings.Contains(row.Config, "secret123") {
		t.Fatal("password leaked into library.config SQLite column")
	}

	info, err := svc.GetNavidromeConfig(lib.ID)
	if err != nil {
		t.Fatalf("GetNavidromeConfig: %v", err)
	}
	if info.ServerURL != srv.URL || info.Username != "kelvin" || !info.HasPassword {
		t.Fatalf("unexpected GetNavidromeConfig: %+v", info)
	}

	// 2. Scoped ref routes to CoverFetcher, LyricFetcher, and PlaybackObserver.
	ref := library.ScopedRemoteRef(library.KindNavidrome, lib.ID, "s1")
	data, mimeType, ok := svc.sources.CoverBytes(context.Background(), ref, 256)
	if !ok || len(data) == 0 || mimeType == "" {
		t.Fatalf("CoverBytes failed: ok=%v len=%d mime=%q", ok, len(data), mimeType)
	}

	doc, err := svc.sources.FetchLyrics(context.Background(), ref)
	if err != nil || doc == nil || len(doc.Lines) == 0 {
		t.Fatalf("FetchLyrics = (%+v, %v)", doc, err)
	}

	svc.sources.NotifyTrackStart(context.Background(), ref, 258000)
	svc.sources.NotifyTrackEnd(context.Background(), ref, 140000)
	if len(scrobbleSubs) != 2 || scrobbleSubs[0] != "false" || scrobbleSubs[1] != "true" {
		t.Fatalf("scrobble submissions = %v, want [false true]", scrobbleSubs)
	}

	// 3. UpdateNavidromeLibrary without password preserves stored password.
	updated, err := svc.UpdateNavidromeLibrary(lib.ID, "Renamed ND", "Updated", srv.URL, "kelvin", "")
	if err != nil {
		t.Fatalf("UpdateNavidromeLibrary: %v", err)
	}
	if updated.Name != "Renamed ND" {
		t.Fatalf("updated name = %q, want Renamed ND", updated.Name)
	}

	// 4. Re-opening via SourceAdapter.Open restores credentials cleanly.
	svc.navidromeManager().CloseLibrary(lib.ID)
	row, _ = store.LibraryRow(lib.ID)
	src, err := svc.navidromeManager().Open(context.Background(), row)
	if err != nil || src == nil {
		t.Fatalf("Open: src=%v err=%v", src, err)
	}
	if _, _, ok := svc.navidromeManager().clientForRef(ref); !ok {
		t.Fatal("expected restored client for scoped ref after Open")
	}

	// Wait for any background sync goroutines to finish before cleanup.
	svc.cancel()
	svc.wg.Wait()
}

func TestNavidromeProviderRejectsErrorPayloadsAndVerifiesDecoder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "stream.view") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"subsonic-response":{"status":"failed","error":{"code":40,"message":"Wrong username or password"}}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{"status": "ok"}})
	}))
	defer srv.Close()

	svc, _, _ := newNavidromeTestService(t)
	client, err := nd.NewClient(nd.Config{ServerURL: srv.URL, Username: "u", Password: "p"}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	svc.navidromeManager().setClient("navidrome_test", navidromeLibraryConfig{ServerURL: srv.URL, Username: "u"}, client)

	ref := library.ScopedRemoteRef(library.KindNavidrome, "navidrome_test", "s1")
	src, err := svc.sources.Open(context.Background(), ref)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := src.Opener(nil); err == nil {
		t.Fatal("expected Opener to reject JSON error stream response")
	}
	time.Sleep(20 * time.Millisecond)
	if svc.tracks.Has(navidromeStableHash(ref)) {
		t.Fatal("error payload must never be committed to trackcache")
	}
}

func TestNavidromeSourceImplementsCapabilities(t *testing.T) {
	store, err := sqlite.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	src := ndlib.New(store, library.Library{ID: "navidrome_1", Kind: library.KindNavidrome, Name: "ND"}, nil, nil, nil)
	caps := library.CapabilitiesOf(src)
	hasRefresh := false
	for _, c := range caps {
		if c == library.CapRefresh {
			hasRefresh = true
		}
	}
	if !hasRefresh {
		t.Fatalf("expected CapRefresh in capabilities, got %v", caps)
	}
}
