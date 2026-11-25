package backend

import (
	"bytes"
	"image"
	imgcolor "image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dlcuy22/molo/meta"
	"github.com/dlcuy22/molow/internal/artwork"
)

func testPNGBytes(t *testing.T, r, g, b uint8) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := range 16 {
		for x := range 16 {
			img.Set(x, y, imgcolor.NRGBA{R: r, G: g, B: b, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestArtworkMiddlewareServesCachedAndActiveCovers(t *testing.T) {
	art, err := artwork.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := newPlayerService()
	svc.art = art
	fp := newFakePlayer()
	svc.player = fp

	raw := testPNGBytes(t, 0xd1, 0x5b, 0x5b)
	id, err := art.Put(raw)
	if err != nil {
		t.Fatal(err)
	}

	fallbackCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalled = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("app-asset"))
	})
	handler := svc.artworkMiddleware()(next)

	// Non-/art/ request passes straight through to next.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.html", nil))
	if !fallbackCalled || rec.Body.String() != "app-asset" {
		t.Fatalf("non-/art/ request did not pass through: called=%v body=%q", fallbackCalled, rec.Body.String())
	}

	// Cached /art/<id> returns raw JPEG bytes with immutable Cache-Control.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/art/"+id, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /art/%s status = %d, want 200", id, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/jpeg") {
		t.Fatalf("Content-Type = %q, want image/jpeg", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != artImmutableCacheHdr {
		t.Fatalf("Cache-Control = %q, want %q", cc, artImmutableCacheHdr)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("empty artwork body")
	}

	// Active track fallback caches and serves uncached embedded cover bytes.
	liveRaw := testPNGBytes(t, 0x35, 0x84, 0xe4)
	liveID := artwork.ID(liveRaw)
	fp.meta = meta.Meta{Tags: meta.Tags{Cover: liveRaw}}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/art/"+liveID, nil))
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("active cover fallback = (%d, len=%d), want 200 non-empty", rec.Code, rec.Body.Len())
	}
	if !art.Has(liveID) {
		t.Fatal("active cover fallback did not persist to artwork.Store")
	}

	// Unsafe IDs and unknown IDs return 404; non-GET returns 405.
	for _, bad := range []string{"/art/", "/art/../etc", "/art/a.jpg", "/art/missingid"} {
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bad, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", bad, rec.Code)
		}
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/art/"+id, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /art/%s status = %d, want 405", id, rec.Code)
	}
}

func TestYTMSearchThumbnailStaysInMemoryOnly(t *testing.T) {
	artDir := t.TempDir()
	art, err := artwork.Open(artDir)
	if err != nil {
		t.Fatal(err)
	}

	// Build a 300x300 PNG so we also verify downscaling to <= ytmSearchThumbEdge.
	big := image.NewNRGBA(image.Rect(0, 0, 300, 300))
	for y := range 300 {
		for x := range 300 {
			big.Set(x, y, imgcolor.NRGBA{R: 0x35, G: 0x84, B: 0xe4, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, big); err != nil {
		t.Fatal(err)
	}
	bigBytes := buf.Bytes()

	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(bigBytes)
	}))
	t.Cleanup(srv.Close)

	svc := newPlayerService()
	svc.art = art
	svc.ytmProv.art = art
	svc.ytmProv.http = srv.Client()
	svc.ytm.put(ytmTrack{ID: "vid123", Title: "Search Hit", ThumbnailURL: srv.URL + "/thumb.png"})

	handler := svc.artworkMiddleware()(http.NotFoundHandler())

	// First request fetches, downscales to <= 128px, and writes zero disk files.
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/art/ytm-search/vid123", nil))
	if rec1.Code != http.StatusOK || rec1.Body.Len() == 0 {
		t.Fatalf("GET /art/ytm-search/vid123 = (%d, len=%d)", rec1.Code, rec1.Body.Len())
	}
	img, err := artwork.Decode(rec1.Body.Bytes())
	if err != nil {
		t.Fatalf("Decode response: %v", err)
	}
	if b := img.Bounds(); b.Dx() > ytmSearchThumbEdge || b.Dy() > ytmSearchThumbEdge {
		t.Fatalf("search thumb bounds = %dx%d, want <= %d", b.Dx(), b.Dy(), ytmSearchThumbEdge)
	}

	entries, err := os.ReadDir(artDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("search thumbnail wrote %d files to art dir, want 0", len(entries))
	}

	// Second request hits the in-memory LRU without another HTTP fetch.
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/art/ytm-search/vid123", nil))
	if rec2.Code != http.StatusOK || fetches.Load() != 1 {
		t.Fatalf("second request = (status=%d, fetches=%d), want (200, 1)", rec2.Code, fetches.Load())
	}
}

func TestArtworkMiddlewareServesBackdrops(t *testing.T) {
	art, err := artwork.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := newPlayerService()
	svc.art = art
	fp := newFakePlayer()
	svc.player = fp

	raw := testPNGBytes(t, 0xb8, 0x3b, 0x4b)
	id, err := art.Put(raw)
	if err != nil {
		t.Fatal(err)
	}

	handler := svc.artworkMiddleware()(http.NotFoundHandler())

	// Cached cover generates and serves a 64x64 PNG backdrop with immutable cache headers.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/art/bg/"+id, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /art/bg/%s status = %d, want 200", id, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != artImmutableCacheHdr {
		t.Fatalf("Cache-Control = %q, want %q", cc, artImmutableCacheHdr)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("empty backdrop body")
	}

	// HEAD returns headers with no body.
	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/art/bg/"+id, nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD /art/bg/%s = (%d, len=%d), want (200, 0)", id, head.Code, head.Body.Len())
	}
	if ct := head.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Fatalf("HEAD Content-Type = %q, want image/png", ct)
	}

	// Active-track fallback caches and generates the backdrop on demand.
	liveRaw := testPNGBytes(t, 0x2a, 0x72, 0xc8)
	liveID := artwork.ID(liveRaw)
	fp.meta = meta.Meta{Tags: meta.Tags{Cover: liveRaw}}
	live := httptest.NewRecorder()
	handler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/art/bg/"+liveID, nil))
	if live.Code != http.StatusOK || live.Body.Len() == 0 {
		t.Fatalf("active backdrop fallback = (%d, len=%d), want 200 non-empty", live.Code, live.Body.Len())
	}

	// Unknown or unsafe IDs return 404.
	for _, bad := range []string{"/art/bg/", "/art/bg/../etc", "/art/bg/a.png", "/art/bg/missing"} {
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bad, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", bad, rec.Code)
		}
	}
}
