package color

import (
	"image"
	"image/color"
	"testing"
)

func TestPaletteRanksByArea(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			if y < 6 {
				img.Set(x, y, color.RGBA{R: 0xd1, G: 0x5b, B: 0x5b, A: 0xff})
			} else {
				img.Set(x, y, color.RGBA{R: 0x35, G: 0x84, B: 0xe4, A: 0xff})
			}
		}
	}

	cs := PaletteImage(img, 2, false)
	if len(cs) != 2 {
		t.Fatalf("palette: %v", cs)
	}
	if cs[0].Hex() != "#d15b5b" {
		t.Fatalf("first: %s", cs[0].Hex())
	}
	if got := cs[0].Fraction(); got < 0.59 || got > 0.61 {
		t.Fatalf("fraction: %f", got)
	}
	if cs[1].Hex() != "#3584e4" {
		t.Fatalf("second: %s", cs[1].Hex())
	}
}

func TestAccentDropsNeutrals(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			if x < 5 {
				img.Set(x, y, color.RGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xff})
			} else {
				img.Set(x, y, color.RGBA{R: 0xd1, G: 0x5b, B: 0x5b, A: 0xff})
			}
		}
	}

	cs := PaletteImage(img, 1, true)
	if len(cs) != 1 || cs[0].Hex() != "#d15b5b" {
		t.Fatalf("accent: %v", cs)
	}
	// Coverage is still counted against the whole image, not the filtered set.
	if got := cs[0].Fraction(); got < 0.49 || got > 0.51 {
		t.Fatalf("fraction: %f", got)
	}
}

func TestGradientPutsLightFirstAndSkipsCloseSteps(t *testing.T) {
	dark := Colour{R: 0xd1, G: 0x5b, B: 0x5b, Count: 10, Total: 100}
	close := Colour{R: 0xce, G: 0x5a, B: 0x59, Count: 9, Total: 100}
	light := Colour{R: 0xec, G: 0xd3, B: 0x93, Count: 8, Total: 100}

	got := Gradient([]Colour{dark, close, light})
	if len(got) != 2 || got[0].Hex() != "#ecd393" || got[1].Hex() != "#d15b5b" {
		t.Fatalf("gradient: %v", got)
	}

	// No other accent is far enough away: a flat field is the honest answer.
	same := Gradient([]Colour{dark, close})
	if len(same) != 1 || same[0].Hex() != "#d15b5b" {
		t.Fatalf("flat: %v", same)
	}
}

func TestAccentClampsBrightCoverAndSynthesizesSecondStop(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			// Neon yellow (#fbe504, luma ~0.85) across the whole cover.
			img.Set(x, y, color.RGBA{R: 0xfb, G: 0xe5, B: 0x04, A: 0xff})
		}
	}

	cs := PaletteImage(img, 5, true)
	if len(cs) < 2 {
		t.Fatalf("expected synthesized second stop, got %v", cs)
	}
	stops := Gradient(cs)
	if len(stops) != 2 {
		t.Fatalf("expected 2 gradient stops, got %v", stops)
	}
	if stops[0].Luma() > maxLightLuma+0.01 {
		t.Fatalf("top stop luma = %.3f, want <= %.2f", stops[0].Luma(), maxLightLuma)
	}
	if stops[1].Luma() > maxDarkLuma+0.01 {
		t.Fatalf("bottom stop luma = %.3f, want <= %.2f", stops[1].Luma(), maxDarkLuma)
	}
	if diff := stops[0].Luma() - stops[1].Luma(); diff < minLumaDiff {
		t.Fatalf("luma diff = %.3f, want >= %.2f", diff, minLumaDiff)
	}
}

func TestAccentMergesAdjacentBinsToSurfaceSecondaryColour(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 20, 10))
	// Fill 5 slightly different 5-bit bins of pastel pink (3 rows each = 150 px)
	// plus 50 px of crimson red (#b41720). Without bin merging, the 5 pink bins
	// would crowd crimson out of a top-5 request.
	pinks := []color.RGBA{
		{R: 254, G: 181, B: 220, A: 255},
		{R: 252, G: 185, B: 221, A: 255},
		{R: 246, G: 175, B: 214, A: 255},
		{R: 254, G: 189, B: 229, A: 255},
		{R: 245, G: 185, B: 210, A: 255},
	}
	idx := 0
	for y := 0; y < 10; y++ {
		for x := 0; x < 20; x++ {
			if idx < 150 {
				img.Set(x, y, pinks[(idx/30)%len(pinks)])
			} else {
				img.Set(x, y, color.RGBA{R: 0xb4, G: 0x17, B: 0x20, A: 0xff})
			}
			idx++
		}
	}

	cs := PaletteImage(img, 5, true)
	stops := Gradient(cs)
	if len(stops) != 2 {
		t.Fatalf("expected 2 gradient stops, got %v", stops)
	}
	if stops[1].Hex() != "#b41720" {
		t.Fatalf("bottom stop = %s, want crimson #b41720 surfaced after merging pink bins", stops[1].Hex())
	}
}
