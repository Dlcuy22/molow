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

func TestComposeMosaicSingleAndQuad(t *testing.T) {
	if data, err := ComposeMosaic(nil); err != nil || data != nil {
		t.Fatalf("ComposeMosaic(nil) = (%d bytes, %v), want (nil, nil)", len(data), err)
	}

	red := makePNG(t, 80, 60, imgcolor.NRGBA{R: 220, G: 30, B: 30, A: 255})
	green := makePNG(t, 60, 80, imgcolor.NRGBA{R: 30, G: 210, B: 40, A: 255})
	blue := makePNG(t, 64, 64, imgcolor.NRGBA{R: 30, G: 60, B: 220, A: 255})
	yellow := makePNG(t, 64, 64, imgcolor.NRGBA{R: 230, G: 210, B: 30, A: 255})

	// Fewer than four tiles returns the first valid tile normalized.
	single, err := ComposeMosaic([][]byte{[]byte("bad"), red, green})
	if err != nil || len(single) == 0 {
		t.Fatalf("ComposeMosaic(<4) = (%d bytes, %v)", len(single), err)
	}
	simg, err := Decode(single)
	if err != nil {
		t.Fatal(err)
	}
	if b := simg.Bounds(); b.Dx() != 80 || b.Dy() != 60 {
		t.Fatalf("single mosaic bounds = %dx%d, want 80x60", b.Dx(), b.Dy())
	}

	// Four valid tiles compose into a MaxEdge x MaxEdge 2x2 square.
	quad, err := ComposeMosaic([][]byte{red, green, blue, yellow})
	if err != nil || len(quad) == 0 {
		t.Fatalf("ComposeMosaic(4) = (%d bytes, %v)", len(quad), err)
	}
	qimg, err := Decode(quad)
	if err != nil {
		t.Fatal(err)
	}
	if b := qimg.Bounds(); b.Dx() != MaxEdge || b.Dy() != MaxEdge {
		t.Fatalf("quad mosaic bounds = %dx%d, want %dx%d", b.Dx(), b.Dy(), MaxEdge, MaxEdge)
	}

	// Verify each quadrant carries its expected dominant channel.
	checkQuad := func(name string, x, y int, wantR, wantG, wantB uint8) {
		r, g, b, _ := qimg.At(x, y).RGBA()
		gotR, gotG, gotB := uint8(r>>8), uint8(g>>8), uint8(b>>8)
		diff := func(a, b uint8) int {
			if a > b {
				return int(a - b)
			}

			return int(b - a)
		}
		if diff(gotR, wantR) > 30 || diff(gotG, wantG) > 30 || diff(gotB, wantB) > 30 {
			t.Errorf("%s pixel (%d,%d) = (%d,%d,%d), want ~(%d,%d,%d)", name, x, y, gotR, gotG, gotB, wantR, wantG, wantB)
		}
	}
	checkQuad("top-left", 64, 64, 220, 30, 30)
	checkQuad("top-right", 384, 64, 30, 210, 40)
	checkQuad("bottom-left", 64, 384, 30, 60, 220)
	checkQuad("bottom-right", 384, 384, 230, 210, 30)
}

func TestNormalizeThumbEdge(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{-10, MaxEdge},
		{0, MaxEdge},
		{32, 64},
		{64, 64},
		{80, 128},
		{166, 256},
		{256, 256},
		{400, MaxEdge},
		{1024, MaxEdge},
	}
	for _, tc := range cases {
		if got := NormalizeThumbEdge(tc.in); got != tc.want {
			t.Errorf("NormalizeThumbEdge(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestStoreThumbPathAndBytes(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Large cover generates quantized variants on demand and leaves 512px canonical.
	largeRaw := makePNG(t, 800, 800, imgcolor.NRGBA{R: 120, G: 80, B: 200, A: 255})
	largeID, err := s.Put(largeRaw)
	if err != nil {
		t.Fatal(err)
	}
	canonPath, ok := s.FilePath(largeID)
	if !ok {
		t.Fatal("missing canonical file")
	}

	p512, err := s.ThumbPath(largeID, 512)
	if err != nil || p512 != canonPath {
		t.Fatalf("ThumbPath(512) = (%q, %v), want %q", p512, err, canonPath)
	}

	for _, edge := range []int{64, 256} {
		p, err := s.ThumbPath(largeID, edge)
		if err != nil || p == "" || p == canonPath {
			t.Fatalf("ThumbPath(%d) = (%q, %v), want variant path", edge, p, err)
		}
		data, mime, ok := s.ThumbBytes(largeID, edge)
		if !ok || len(data) == 0 || mime == "" {
			t.Fatalf("ThumbBytes(%d) = (%d bytes, %q, %v)", edge, len(data), mime, ok)
		}
		img, err := Decode(data)
		if err != nil {
			t.Fatalf("Decode(%d): %v", edge, err)
		}
		if b := img.Bounds(); b.Dx() > edge || b.Dy() > edge {
			t.Fatalf("variant %d bounds = %dx%d, want <= %d", edge, b.Dx(), b.Dy(), edge)
		}
	}

	// Small source cover stays on canonical path instead of upscaling into thumb/.
	smallRaw := makePNG(t, 48, 48, imgcolor.NRGBA{R: 40, G: 180, B: 90, A: 255})
	smallID, err := s.Put(smallRaw)
	if err != nil {
		t.Fatal(err)
	}
	smallCanon, ok := s.FilePath(smallID)
	if !ok {
		t.Fatal("missing small canonical file")
	}
	pSmall, err := s.ThumbPath(smallID, 64)
	if err != nil || pSmall != smallCanon {
		t.Fatalf("small cover ThumbPath(64) = (%q, %v), want canonical %q", pSmall, err, smallCanon)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), thumbSubdir, "64", smallID+".jpg")); !os.IsNotExist(err) {
		t.Fatalf("expected no duplicated 64px variant for 48px source")
	}
}

func TestStorePruneIncludesThumbVariants(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	keepThumb64 := filepath.Join(dir, thumbSubdir, "64", "keep1.jpg")
	orphanThumb64 := filepath.Join(dir, thumbSubdir, "64", "orphan.jpg")
	orphanThumb128 := filepath.Join(dir, thumbSubdir, "128", "orphan.jpg")
	for _, path := range []string{keepThumb64, orphanThumb64, orphanThumb128} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, 128), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := s.Prune(map[string]struct{}{"keep1": {}}, 0)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	if _, err := os.Stat(keepThumb64); err != nil {
		t.Fatalf("protected thumb variant removed: %v", err)
	}
	for _, orphan := range []string{orphanThumb64, orphanThumb128} {
		if _, err := os.Stat(orphan); !os.IsNotExist(err) {
			t.Fatalf("orphan %s should be removed", orphan)
		}
	}
}
