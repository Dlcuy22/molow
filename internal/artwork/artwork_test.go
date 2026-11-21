package artwork

import (
	"bytes"
	"errors"
	"image"
	imgcolor "image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func makePNG(t *testing.T, w, h int, c imgcolor.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestPutNormalizesFormatAndSize(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Large opaque image becomes <= 512px JPEG.
	bigOpaque := makePNG(t, 900, 600, imgcolor.NRGBA{R: 0xd1, G: 0x5b, B: 0x5b, A: 0xff})
	id, err := s.Put(bigOpaque)
	if err != nil {
		t.Fatalf("Put opaque: %v", err)
	}
	if !s.Has(id) {
		t.Fatal("Has returned false after Put")
	}
	p, ok := s.FilePath(id)
	if !ok || !strings.HasSuffix(p, ".jpg") {
		t.Fatalf("FilePath = (%q, %v), want .jpg", p, ok)
	}
	data, mime, ok := s.Bytes(id)
	if !ok || mime != "image/jpeg" || len(data) == 0 {
		t.Fatalf("Bytes = (len=%d, mime=%q, ok=%v)", len(data), mime, ok)
	}
	img, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != MaxEdge || b.Dy() != 341 {
		t.Fatalf("resized bounds = %dx%d, want %dx341", b.Dx(), b.Dy(), MaxEdge)
	}

	// Small image with alpha stays PNG and is not upscaled.
	smallAlpha := makePNG(t, 64, 48, imgcolor.NRGBA{R: 0x40, G: 0x80, B: 0xc0, A: 0x80})
	aid, err := s.Put(smallAlpha)
	if err != nil {
		t.Fatalf("Put alpha: %v", err)
	}
	ap, ok := s.FilePath(aid)
	if !ok || !strings.HasSuffix(ap, ".png") {
		t.Fatalf("FilePath alpha = (%q, %v), want .png", ap, ok)
	}
	adata, amime, ok := s.Bytes(aid)
	if !ok || amime != "image/png" {
		t.Fatalf("Bytes alpha = (mime=%q, ok=%v)", amime, ok)
	}
	aimg, err := Decode(adata)
	if err != nil {
		t.Fatal(err)
	}
	if b := aimg.Bounds(); b.Dx() != 64 || b.Dy() != 48 {
		t.Fatalf("small alpha upscaled to %dx%d", b.Dx(), b.Dy())
	}
}

func TestPutIsIdempotentAndRejectsInvalid(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	raw := makePNG(t, 32, 32, imgcolor.NRGBA{R: 0x35, G: 0x84, B: 0xe4, A: 0xff})
	id, err := s.Put(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := s.FilePath(id)
	if !ok {
		t.Fatal("missing file after Put")
	}
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}

	// Second Put hits the existing file on disk without rewriting.
	id2, err := s.Put(raw)
	if err != nil || id2 != id {
		t.Fatalf("second Put = (%q, %v), want %q", id2, err, id)
	}
	after, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("second Put rewrote an already cached cover")
	}

	if _, err := s.Put([]byte("not an image")); !errors.Is(err, ErrNotImage) {
		t.Fatalf("invalid bytes err = %v, want ErrNotImage", err)
	}
}

func TestDecodeSupportsGIFAndJPEG(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	img := image.NewPaletted(image.Rect(0, 0, 12, 12), imgcolor.Palette{
		imgcolor.RGBA{R: 0xd1, G: 0x5b, B: 0x5b, A: 0xff},
	})
	var gbuf bytes.Buffer
	if err := gif.Encode(&gbuf, img, nil); err != nil {
		t.Fatal(err)
	}
	if id, err := s.Put(gbuf.Bytes()); err != nil || !s.Has(id) {
		t.Fatalf("Put GIF = (%q, %v)", id, err)
	}

	var jbuf bytes.Buffer
	if err := jpeg.Encode(&jbuf, img, nil); err != nil {
		t.Fatal(err)
	}
	if id, err := s.Put(jbuf.Bytes()); err != nil || !s.Has(id) {
		t.Fatalf("Put JPEG = (%q, %v)", id, err)
	}
}

func TestDataURLAndAccentsLRUEvictionAndConcurrency(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	raw := makePNG(t, 40, 40, imgcolor.NRGBA{R: 0xd1, G: 0x5b, B: 0x5b, A: 0xff})
	id := ID(raw)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gotID, err := s.Put(raw)
			if err != nil || gotID != id {
				t.Errorf("concurrent Put: id=%q err=%v", gotID, err)

				return
			}
			if url, ok := s.DataURL(gotID); !ok || !strings.HasPrefix(url, "data:image/jpeg;base64,") {
				t.Errorf("concurrent DataURL: ok=%v url=%q", ok, url)
			}
			if cs, err := s.Accents(gotID, 5); err != nil || len(cs) == 0 {
				t.Errorf("concurrent Accents: cs=%v err=%v", cs, err)
			}
		}()
	}
	wg.Wait()

	// Fill past cacheMax to verify LRU eviction bounds both caches.
	ids := make([]string, 0, cacheMax+10)
	for i := range cacheMax + 10 {
		b := makePNG(t, 8, 8, imgcolor.NRGBA{
			R: uint8(180 + i%70),
			G: uint8(40 + (i*7)%120),
			B: uint8(40 + (i*13)%120),
			A: 0xff,
		})
		cid, err := s.Put(b)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := s.DataURL(cid); !ok {
			t.Fatalf("DataURL miss for %s", cid)
		}
		if _, err := s.Accents(cid, 5); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, cid)
	}

	s.urlMu.Lock()
	urlCount := len(s.urlLRU.items)
	s.urlMu.Unlock()
	if urlCount != cacheMax {
		t.Fatalf("urlLRU len = %d, want %d", urlCount, cacheMax)
	}

	s.accentMu.Lock()
	accentCount := len(s.accentLRU.items)
	s.accentMu.Unlock()
	if accentCount != cacheMax {
		t.Fatalf("accentLRU len = %d, want %d", accentCount, cacheMax)
	}

	// Delete the disk files: the newest entry must still serve from LRU, while
	// the oldest evicted entry must miss.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
	if _, ok := s.DataURL(ids[0]); ok {
		t.Fatal("evicted entry still served after disk removal")
	}
	if _, ok := s.DataURL(ids[len(ids)-1]); !ok {
		t.Fatal("most recent entry missed LRU")
	}
}
