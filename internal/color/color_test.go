package color

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func encode(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

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

	cs, err := Palette(encode(t, img), 2, false)
	if err != nil {
		t.Fatal(err)
	}
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

	cs, err := Palette(encode(t, img), 1, true)
	if err != nil {
		t.Fatal(err)
	}
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
