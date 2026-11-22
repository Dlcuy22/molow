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
	"time"
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

	s.accentMu.Lock()
	accentCount := len(s.accentLRU.items)
	s.accentMu.Unlock()
	if accentCount != cacheMax {
		t.Fatalf("accentLRU len = %d, want %d", accentCount, cacheMax)
	}

	// Delete the disk files: DataURL reads directly from disk so removed files
	// miss, while the newest accent palette still serves from accentLRU.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
	if _, ok := s.DataURL(ids[0]); ok {
		t.Fatal("removed disk file still served by DataURL")
	}
	if cs, err := s.Accents(ids[len(ids)-1], 5); err != nil || len(cs) == 0 {
		t.Fatalf("most recent entry missed accentLRU: cs=%v err=%v", cs, err)
	}
	if cs, err := s.Accents(ids[0], 5); err != nil || len(cs) != 0 {
		t.Fatalf("evicted entry still served by accentLRU: cs=%v err=%v", cs, err)
	}
}

func TestMigrateDirMovesArtworkWhenTargetEmpty(t *testing.T) {
	from := t.TempDir()
	to := t.TempDir()

	jpg := []byte("fake-jpg")
	pngBytes := []byte("fake-png")
	if err := os.WriteFile(filepath.Join(from, "aaa.jpg"), jpg, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "bbb.png"), pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "stale.tmp"), []byte("ignore"), 0o644); err != nil {
		t.Fatal(err)
	}

	migrateDir(from, to)

	if got, err := os.ReadFile(filepath.Join(to, "aaa.jpg")); err != nil || !bytes.Equal(got, jpg) {
		t.Fatalf("migrated aaa.jpg = (%q, %v)", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(to, "bbb.png")); err != nil || !bytes.Equal(got, pngBytes) {
		t.Fatalf("migrated bbb.png = (%q, %v)", got, err)
	}
	if _, err := os.Stat(filepath.Join(to, "stale.tmp")); !os.IsNotExist(err) {
		t.Fatalf("stale.tmp should not be migrated, stat err = %v", err)
	}

	// Non-empty target skips migration.
	if err := os.WriteFile(filepath.Join(from, "ccc.jpg"), []byte("later"), 0o644); err != nil {
		t.Fatal(err)
	}
	migrateDir(from, to)
	if _, err := os.Stat(filepath.Join(to, "ccc.jpg")); !os.IsNotExist(err) {
		t.Fatalf("migration should skip non-empty target, stat err = %v", err)
	}
}

func TestPruneProtectsReferencedAndEvictsOldestOrphans(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	writeArt := func(name string, size int, mod time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, bytes.Repeat([]byte("x"), size), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}

	// Protected files exceed the orphan budget on their own and must survive.
	writeArt("keep1.jpg", 100, base)
	writeArt("keep2.png", 100, base.Add(time.Minute))
	// Three orphans (40 bytes each = 120 bytes) against a 50-byte budget.
	writeArt("orphanOld.jpg", 40, base.Add(2*time.Minute))
	writeArt("orphanMid.jpg", 40, base.Add(3*time.Minute))
	writeArt("orphanNew.png", 40, base.Add(4*time.Minute))
	// Non-image sidecar (.url) must never be deleted.
	writeArt("artist-1.url", 80, base)

	protected := map[string]struct{}{
		"keep1": {},
		"keep2": {},
	}
	removed, err := s.Prune(protected, 50)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	if !s.Has("keep1") || !s.Has("keep2") {
		t.Fatal("protected artwork was removed by Prune")
	}
	if s.Has("orphanOld") || s.Has("orphanMid") {
		t.Fatal("oldest orphans should have been evicted")
	}
	if !s.Has("orphanNew") {
		t.Fatal("newest orphan within budget should remain")
	}
	if _, err := os.Stat(filepath.Join(dir, "artist-1.url")); err != nil {
		t.Fatalf("non-image sidecar should remain: %v", err)
	}
}
