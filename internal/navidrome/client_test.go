package navidrome

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestNormalizeServerURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
		err  bool
	}{
		{"https://music.example.com/", "https://music.example.com", false},
		{"https://music.example.com/navidrome/rest", "https://music.example.com/navidrome", false},
		{"192.168.1.10:4533", "http://192.168.1.10:4533", false},
		{"", "", true},
		{"ftp://example.com", "", true},
	}
	for _, tc := range tests {
		got, err := NormalizeServerURL(tc.in)
		if (err != nil) != tc.err {
			t.Fatalf("NormalizeServerURL(%q) err=%v wantErr=%v", tc.in, err, tc.err)
		}
		if got != tc.want {
			t.Fatalf("NormalizeServerURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func newTestServer(t *testing.T, handler func(method string, q map[string][]string, r *http.Request) (int, any)) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/") || !strings.HasSuffix(r.URL.Path, ".view") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		method := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/rest/"), ".view")
		q := r.URL.Query()
		if q.Get("u") != "alice" || q.Get("t") == "" || q.Get("s") == "" || q.Get("f") != "json" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{
					"status": "failed",
					"error":  map[string]any{"code": 40, "message": "Wrong username or password"},
				},
			})
			return
		}
		code, payload := handler(method, q, r)
		if code != 0 && code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		if b, ok := payload.([]byte); ok {
			_, _ = w.Write(b)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": payload})
	}))
	t.Cleanup(srv.Close)

	client, err := NewClient(Config{
		ServerURL: srv.URL,
		Username:  "alice",
		Password:  "secret",
	}, srv.Client())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return srv, client
}

func TestClientEndpoints(t *testing.T) {
	var starred bool
	_, c := newTestServer(t, func(method string, q map[string][]string, r *http.Request) (int, any) {
		switch method {
		case "ping":
			return 200, map[string]any{
				"status":        "ok",
				"version":       "1.16.1",
				"type":          "navidrome",
				"serverVersion": "0.64.0",
				"openSubsonic":  true,
			}
		case "getAlbumList2":
			return 200, map[string]any{
				"status": "ok",
				"albumList2": map[string]any{
					"album": []map[string]any{
						{"id": "al-1", "name": "Test Album", "artist": "Test Artist", "songCount": 1, "duration": 180, "year": 2024, "coverArt": "al-1"},
					},
				},
			}
		case "getAlbum":
			return 200, map[string]any{
				"status": "ok",
				"album": map[string]any{
					"id": "al-1", "name": "Test Album", "artist": "Test Artist", "coverArt": "al-1",
					"song": []map[string]any{
						{"id": "sg-1", "title": "Track One", "album": "Test Album", "artist": "Test Artist", "track": 1, "duration": 180, "suffix": "flac", "coverArt": "al-1"},
					},
				},
			}
		case "star":
			starred = true
			return 200, map[string]any{"status": "ok"}
		case "unstar":
			starred = false
			return 200, map[string]any{"status": "ok"}
		case "getCoverArt":
			return 200, []byte("PNGDATA")
		case "getLyricsBySongId":
			return 200, map[string]any{
				"status": "ok",
				"lyricsList": map[string]any{
					"structuredLyrics": []map[string]any{
						{
							"synced": true,
							"line": []map[string]any{
								{"start": 1500, "value": "Hello world"},
							},
						},
					},
				},
			}
		case "stream":
			return 200, []byte("AUDIOBYTES")
		default:
			return 200, map[string]any{"status": "ok"}
		}
	})

	ctx := context.Background()
	info, err := c.Ping(ctx)
	if err != nil || info.Type != "navidrome" || !info.OpenSubsonic {
		t.Fatalf("Ping = %+v, err=%v", info, err)
	}

	albums, err := c.AlbumList(ctx, 50, 0)
	if err != nil || len(albums) != 1 || albums[0].ID != "al-1" {
		t.Fatalf("AlbumList = %+v, err=%v", albums, err)
	}

	album, err := c.GetAlbum(ctx, "al-1")
	if err != nil || len(album.Songs) != 1 || album.Songs[0].Title != "Track One" {
		t.Fatalf("GetAlbum = %+v, err=%v", album, err)
	}

	if err := c.SetStarred(ctx, "sg-1", true); err != nil || !starred {
		t.Fatalf("SetStarred(true) starred=%v err=%v", starred, err)
	}
	if err := c.SetStarred(ctx, "sg-1", false); err != nil || starred {
		t.Fatalf("SetStarred(false) starred=%v err=%v", starred, err)
	}

	art, _, err := c.GetCoverArt(ctx, "al-1", 512)
	if err != nil || string(art) != "PNGDATA" {
		t.Fatalf("GetCoverArt = %q, err=%v", art, err)
	}

	lrc, synced, err := c.GetLyrics(ctx, "sg-1", "Test Artist", "Track One")
	if err != nil || !synced || !strings.Contains(lrc, "[00:01.50]Hello world") {
		t.Fatalf("GetLyrics = (%q, %v), err=%v", lrc, synced, err)
	}

	resp, err := c.OpenStream(ctx, "sg-1", nil)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	resp.Body.Close()
}

func TestOpenStreamRejectsJSONError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"subsonic-response": map[string]any{
				"status": "failed",
				"error":  map[string]any{"code": 40, "message": "Wrong username or password"},
			},
		})
	}))
	defer srv.Close()

	client, err := NewClient(Config{ServerURL: srv.URL, Username: "u", Password: "p"}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.OpenStream(context.Background(), "song1", nil)
	if err == nil {
		t.Fatal("expected OpenStream to fail on JSON error body")
	}
	if !strings.Contains(err.Error(), "Wrong username or password") {
		t.Fatalf("expected error message to contain Subsonic API error, got: %v", err)
	}
}

func TestSanitizeURLError(t *testing.T) {
	rawURL := "http://example.com/rest/ping.view?c=molow&f=json&s=1234salt&t=5678token&u=alice&v=1.16.1"
	origErr := &url.Error{
		Op:  "Get",
		URL: rawURL,
		Err: errors.New("connection refused"),
	}
	sanitized := sanitizeURLError(origErr)
	if strings.Contains(sanitized.Error(), "1234salt") || strings.Contains(sanitized.Error(), "5678token") {
		t.Fatalf("token/salt leaked in sanitized error: %v", sanitized)
	}
	if !strings.Contains(sanitized.Error(), "s=REDACTED") || !strings.Contains(sanitized.Error(), "t=REDACTED") {
		t.Fatalf("expected s=REDACTED and t=REDACTED, got: %v", sanitized)
	}
}

func TestClientBlocksCrossOriginRedirect(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("cross-origin redirect target should never be requested: %s", r.URL.String())
	}))
	defer other.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/secret", http.StatusFound)
	}))
	defer srv.Close()

	client, err := NewClient(Config{ServerURL: srv.URL, Username: "u", Password: "p"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Ping(context.Background()); err == nil {
		t.Fatal("expected Ping to block cross-origin redirect")
	} else if !strings.Contains(err.Error(), "blocked cross-origin redirect") {
		t.Fatalf("expected blocked cross-origin redirect error, got: %v", err)
	}
}
