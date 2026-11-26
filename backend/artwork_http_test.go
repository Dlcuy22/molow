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

func testPNGSizedBytes(t *testing.T, w, h int, r, g, b uint8) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, imgcolor.NRGBA{R: r, G: g, B: b, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func testPNGBytes(t *testing.T, r, g, b uint8) []byte {
	t.Helper()

	return testPNGSizedBytes(t, 16, 16, r, g, b)
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

func TestArtworkMiddlewareSizedVariants(t *testing.T) {
	art, err := artwork.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := newPlayerService()
	svc.art = art
	fp := newFakePlayer()
	svc.player = fp

	bigRaw := testPNGSizedBytes(t, 600, 600, 0x35, 0x84, 0xe4)
	id, err := art.Put(bigRaw)
	if err != nil {
		t.Fatal(err)
	}
	handler := svc.artworkMiddleware()(http.NotFoundHandler())

	for _, tc := range []struct {
		target   string
		maxBound int
	}{
		{"/art/" + id + "?s=64", 64},
		{"/art/" + id + "?s=200", 256},
		{"/art/" + id + "?s=abc", artwork.MaxEdge},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.target, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", tc.target, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != artImmutableCacheHdr {
			t.Fatalf("GET %s Cache-Control = %q, want %q", tc.target, cc, artImmutableCacheHdr)
		}
		img, err := artwork.Decode(rec.Body.Bytes())
		if err != nil {
			t.Fatalf("Decode %s: %v", tc.target, err)
		}
		if b := img.Bounds(); b.Dx() > tc.maxBound || b.Dy() > tc.maxBound || b.Dx() == 0 {
			t.Fatalf("GET %s bounds = %dx%d, want <= %d", tc.target, b.Dx(), b.Dy(), tc.maxBound)
		}
	}

	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/art/"+id+"?s=128", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD ?s=128 = (%d, len=%d), want (200, 0)", head.Code, head.Body.Len())
	}

	// Active-track fallback persists canonical art and serves the requested tier.
	liveRaw := testPNGSizedBytes(t, 600, 600, 0xd1, 0x5b, 0x5b)
	liveID := artwork.ID(liveRaw)
	fp.meta = meta.Meta{Tags: meta.Tags{Cover: liveRaw}}
	liveRec := httptest.NewRecorder()
	handler.ServeHTTP(liveRec, httptest.NewRequest(http.MethodGet, "/art/"+liveID+"?s=64", nil))
	if liveRec.Code != http.StatusOK {
		t.Fatalf("active fallback ?s=64 = %d, want 200", liveRec.Code)
	}
	liveImg, err := artwork.Decode(liveRec.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if b := liveImg.Bounds(); b.Dx() > 64 || b.Dy() > 64 {
		t.Fatalf("active fallback bounds = %dx%d, want <= 64", b.Dx(), b.Dy())
	}

	// Artist avatar respects ?s=.
	avatarID, err := art.Put(testPNGSizedBytes(t, 600, 600, 0x40, 0xa0, 0x60))
	if err != nil {
		t.Fatal(err)
	}
	svc.ytmProv.art = art
	svc.ytmProv.avatarMu.Lock()
	svc.ytmProv.avatarIDs["UC123"] = avatarID
	svc.ytmProv.avatarMu.Unlock()

	avatarRec := httptest.NewRecorder()
	handler.ServeHTTP(avatarRec, httptest.NewRequest(http.MethodGet, "/art/avatar/UC123?s=128", nil))
	if avatarRec.Code != http.StatusOK {
		t.Fatalf("GET /art/avatar/UC123?s=128 = %d", avatarRec.Code)
	}
	avatarImg, err := artwork.Decode(avatarRec.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if b := avatarImg.Bounds(); b.Dx() > 128 || b.Dy() > 128 {
		t.Fatalf("avatar bounds = %dx%d, want <= 128", b.Dx(), b.Dy())
	}

	// Search thumbnail downscales below ytmSearchThumbEdge when ?s=64 is asked.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(testPNGSizedBytes(t, 300, 300, 0x90, 0x60, 0xc0))
	}))
	t.Cleanup(srv.Close)
	svc.ytmProv.http = srv.Client()
	svc.ytm.put(ytmTrack{ID: "vid64", Title: "Search 64", ThumbnailURL: srv.URL + "/thumb.png"})

	searchRec := httptest.NewRecorder()
	handler.ServeHTTP(searchRec, httptest.NewRequest(http.MethodGet, "/art/ytm-search/vid64?s=64", nil))
	if searchRec.Code != http.StatusOK {
		t.Fatalf("GET /art/ytm-search/vid64?s=64 = %d", searchRec.Code)
	}
	searchImg, err := artwork.Decode(searchRec.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if b := searchImg.Bounds(); b.Dx() > 64 || b.Dy() > 64 {
		t.Fatalf("search ?s=64 bounds = %dx%d, want <= 64", b.Dx(), b.Dy())
	}
}
