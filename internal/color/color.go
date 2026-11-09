// Package color extracts a deterministic palette from artwork.
//
// The method is the reference implementation in
// tools/dominant_color/dominant_color.py: a binned histogram over a bounded
// thumbnail. It does not cluster and has no random seed, so the same cover
// always yields the same ranked colours.
package color

import (
	"bytes"
	"fmt"
	"image"
	"sort"
	"strconv"

	// Decoders are registered here because this package is the one place that
	// decodes arbitrary artwork bytes.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	// Bits per channel. Five levels is fine enough to keep two colours apart
	// and coarse enough that shading inside one region votes as one colour.
	bits = 5

	defaultMaxEdge  = 256
	defaultAlphaMin = 128

	minSaturation = 0.25
	minLuma       = 0.10
	maxLuma       = 0.95
)

// Colour is one palette entry and its share of the sampled image.
type Colour struct {
	R     uint8 `json:"r"`
	G     uint8 `json:"g"`
	B     uint8 `json:"b"`
	Count int   `json:"count"`
	Total int   `json:"total"`
}

// Hex is the CSS form, always lowercase.
func (c Colour) Hex() string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

// Fraction is the share of opaque pixels, counted before the accent filter.
func (c Colour) Fraction() float64 {
	if c.Total == 0 {
		return 0
	}

	return float64(c.Count) / float64(c.Total)
}

// Luma is Rec. 709 luma in [0, 1].
func (c Colour) Luma() float64 {
	return (0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)) / 255
}

// Palette returns the n most populous colours, most populous first. With
// accent true, only colourful mid-bright pixels vote, which is the filter the
// album player's backdrop uses.
func Palette(data []byte, n int, accent bool) ([]Colour, error) {
	if n <= 0 {
		return nil, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("color: decode: %w", err)
	}

	img = thumbnail(img, defaultMaxEdge)
	b := img.Bounds()
	total := 0
	type bin struct {
		count   int
		r, g, b int
	}
	bins := make(map[uint32]*bin, 1<<(3*bits))

	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r16, g16, b16, a16 := img.At(x, y).RGBA()
			if a16>>8 < defaultAlphaMin {
				continue
			}
			r, g, bl := uint8(r16>>8), uint8(g16>>8), uint8(b16>>8)
			total++
			if accent && !isAccent(r, g, bl) {
				continue
			}
			key := uint32(r>>(8-bits))<<(2*bits) | uint32(g>>(8-bits))<<bits | uint32(bl>>(8-bits))
			v := bins[key]
			if v == nil {
				v = &bin{}
				bins[key] = v
			}
			v.count++
			v.r += int(r)
			v.g += int(g)
			v.b += int(bl)
		}
	}
	if total == 0 {
		return nil, nil
	}

	keys := make([]uint32, 0, len(bins))
	for k := range bins {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ci, cj := bins[keys[i]].count, bins[keys[j]].count
		if ci != cj {
			return ci > cj
		}

		return keys[i] < keys[j]
	})

	out := make([]Colour, 0, n)
	for _, k := range keys {
		v := bins[k]
		if v.count == 0 {
			continue
		}
		out = append(out, Colour{
			R:     uint8((v.r + v.count/2) / v.count),
			G:     uint8((v.g + v.count/2) / v.count),
			B:     uint8((v.b + v.count/2) / v.count),
			Count: v.count,
			Total: total,
		})
		if len(out) == n {
			break
		}
	}

	return out, nil
}

// isAccent is the filter: HSV saturation and Rec. 709 luma in the usable band.
func isAccent(r, g, b uint8) bool {
	high := r
	if g > high {
		high = g
	}
	if b > high {
		high = b
	}
	low := r
	if g < low {
		low = g
	}
	if b < low {
		low = b
	}
	max := float64(high)
	if max < 1 {
		max = 1
	}
	saturation := float64(high-low) / max
	luma := (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)) / 255

	return saturation >= minSaturation && luma >= minLuma && luma <= maxLuma
}

// thumbnail scales the longest edge to max, preserving aspect. A smaller image
// is left alone, as the Python reference does.
func thumbnail(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return src
	}
	scale := float64(max) / float64(w)
	if h > w {
		scale = float64(max) / float64(h)
	}
	dw := int(float64(w) * scale)
	dh := int(float64(h) * scale)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)

	return dst
}

// Gradient is the frame's two-stop read of a palette: the dark end is the most
// populous accent, and the light end is the first later accent far enough away
// in luma to read as a fall. It returns one colour when no fall exists.
func Gradient(accents []Colour) []Colour {
	if len(accents) == 0 {
		return nil
	}
	dark := accents[0]
	light := dark
	for _, c := range accents[1:] {
		if diff := c.Luma() - dark.Luma(); diff >= 0.15 || diff <= -0.15 {
			light = c
			break
		}
	}
	if light == dark {
		return []Colour{dark}
	}
	if light.Luma() >= dark.Luma() {
		return []Colour{light, dark}
	}

	return []Colour{dark, light}
}

// DebugHex renders a colour list for logs and tests.
func DebugHex(cs []Colour) string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Hex() + "@" + strconv.FormatFloat(c.Fraction()*100, 'f', 1, 64) + "%"
	}

	return fmt.Sprint(out)
}
