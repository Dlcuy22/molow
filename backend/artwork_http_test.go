package backend

import (
	"bytes"
	"image"
	imgcolor "image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
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
