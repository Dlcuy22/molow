package artwork

import (
	"bytes"
	"image"
	imgcolor "image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func solidImage(w, h int, c imgcolor.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, c)
		}
	}

	return img
}

func encodeTestPNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestBackdropUniformCrimsonKeepsRedHue(t *testing.T) {
	src := solidImage(120, 80, imgcolor.NRGBA{R: 180, G: 24, B: 36, A: 255})
	bd, err := Backdrop(src)
	if err != nil {
		t.Fatalf("Backdrop: %v", err)
	}
	if b := bd.Bounds(); b.Dx() != backdropEdge || b.Dy() != backdropEdge {
		t.Fatalf("bounds = %dx%d, want %dx%d", b.Dx(), b.Dy(), backdropEdge, backdropEdge)
	}
	c := bd.NRGBAAt(32, 32)
	if c.R <= c.G+100 || c.R <= c.B+100 {
		t.Fatalf("center pixel = %+v, expected dominant red hue", c)
	}
	// A solid image stays solid after blur.
	corner := bd.NRGBAAt(0, 0)
	if corner != c {
		t.Fatalf("solid image varied across blur: corner=%+v center=%+v", corner, c)
	}
}

func TestBackdropMonochromeStaysGreyInRange(t *testing.T) {
	for _, val := range []uint8{0, 110, 255} {
		src := solidImage(64, 64, imgcolor.NRGBA{R: val, G: val, B: val, A: 255})
		bd, err := Backdrop(src)
		if err != nil {
			t.Fatalf("Backdrop(%d): %v", val, err)
		}
		c := bd.NRGBAAt(32, 32)
		if c.R != c.G || c.G != c.B {
			t.Fatalf("grey(%d) became chromatic: %+v", val, c)
		}
		if val == 110 && (c.R < 28 || c.R > 200) {
			t.Fatalf("mid-grey(%d) = %d, want in 28..200", val, c.R)
		}
	}
}

func TestBackdropMostlyBlackWithRedLogoBoundsTintShift(t *testing.T) {
	src := solidImage(64, 64, imgcolor.NRGBA{R: 8, G: 8, B: 8, A: 255})
	// Small 8x8 red logo (~1.5% of area).
	for y := 28; y < 36; y++ {
		for x := 28; x < 36; x++ {
			src.SetNRGBA(x, y, imgcolor.NRGBA{R: 220, G: 20, B: 20, A: 255})
		}
	}

	bd, err := Backdrop(src)
	if err != nil {
		t.Fatalf("Backdrop: %v", err)
	}
	// Far corner should remain very dark rather than washing bright red.
	corner := bd.NRGBAAt(0, 0)
	if corner.R > 25 {
		t.Fatalf("far corner over-tinted = %+v, want R <= 25", corner)
	}
}

func TestEncodeBackdropDecodesAs64x64PNG(t *testing.T) {
	src := solidImage(200, 100, imgcolor.NRGBA{R: 40, G: 110, B: 190, A: 255})
	data, mime, err := EncodeBackdrop(src)
	if err != nil {
		t.Fatalf("EncodeBackdrop: %v", err)
	}
	if mime != "image/png" {
		t.Fatalf("mime = %q, want image/png", mime)
	}
	decoded, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if b := decoded.Bounds(); b.Dx() != backdropEdge || b.Dy() != backdropEdge {
		t.Fatalf("decoded bounds = %dx%d, want %dx%d", b.Dx(), b.Dy(), backdropEdge, backdropEdge)
	}
}

func TestStoreBackdropPersistsAndPrunes(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Unknown ID returns empty path and nil error.
	if p, err := st.BackdropPath("missing"); err != nil || p != "" {
		t.Fatalf("BackdropPath(missing) = (%q, %v), want (\"\", nil)", p, err)
	}

	id, err := st.Put(encodeTestPNG(t, solidImage(80, 80, imgcolor.NRGBA{R: 90, G: 40, B: 160, A: 255})))
	if err != nil {
		t.Fatal(err)
	}

	p, err := st.BackdropPath(id)
	if err != nil || p == "" {
		t.Fatalf("BackdropPath(%s) = (%q, %v)", id, p, err)
	}
	wantPath := filepath.Join(dir, backdropSubdir, id+".png")
	if p != wantPath {
		t.Fatalf("path = %q, want %q", p, wantPath)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("persisted backdrop missing on disk: %v", err)
	}

	// Protected ID survives prune; unprotected ID is pruned alongside its cover.
	if _, err := st.Prune(map[string]struct{}{id: {}}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("protected backdrop was pruned: %v", err)
	}

	if _, err := st.Prune(nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("unprotected backdrop survived prune: err=%v", err)
	}
}
