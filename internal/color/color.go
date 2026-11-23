// Package color extracts a deterministic palette from a decoded image.
package color

import (
	"fmt"
	"image"
	"math"
	"sort"
	"strconv"
)

const (
	// Bits per channel. Five levels is fine enough to keep two colours apart
	// and coarse enough that shading inside one region votes as one colour.
	bits = 5

	defaultAlphaMin = 128

	minSaturation = 0.18
	minLuma       = 0.10
	maxLuma       = 0.92

	// Adjacent 5-bit bins within mergeDistSq belong to the same shaded/compressed
	// colour region and are merged so one flat background cannot fill every slot.
	mergeDistSq     = 28 * 28
	partnerDistSq   = 45 * 45
	minPartnerShare = 0.015

	minLumaDiff  = 0.15
	minLumaStep  = 0.17
	maxLightLuma = 0.52
	maxDarkLuma  = 0.35
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

// PaletteImage returns the n most populous colours in img, most populous first.
// Callers should downscale large artwork before sampling. With accent true,
// only colourful mid-bright pixels vote, which is the filter the album player's
// backdrop uses.
func PaletteImage(img image.Image, n int, accent bool) []Colour {
	if img == nil || n <= 0 {
		return nil
	}

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
	if total == 0 || len(bins) == 0 {
		return nil
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

	if !accent {
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

		return out
	}

	type cluster struct {
		lead       Colour
		r, g, b, c int
	}
	var groups []cluster
	for _, k := range keys {
		v := bins[k]
		if v.count == 0 {
			continue
		}
		cand := Colour{
			R:     uint8((v.r + v.count/2) / v.count),
			G:     uint8((v.g + v.count/2) / v.count),
			B:     uint8((v.b + v.count/2) / v.count),
			Count: v.count,
			Total: total,
		}
		merged := false
		for i := range groups {
			if rgbDistSq(cand, groups[i].lead) <= mergeDistSq {
				groups[i].r += v.r
				groups[i].g += v.g
				groups[i].b += v.b
				groups[i].c += v.count
				merged = true
				break
			}
		}
		if !merged {
			groups = append(groups, cluster{lead: cand, r: v.r, g: v.g, b: v.b, c: v.count})
		}
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].c > groups[j].c })

	raw := make([]Colour, 0, len(groups))
	for _, g := range groups {
		raw = append(raw, Colour{
			R:     uint8((g.r + g.c/2) / g.c),
			G:     uint8((g.g + g.c/2) / g.c),
			B:     uint8((g.b + g.c/2) / g.c),
			Count: g.c,
			Total: total,
		})
	}

	return normalizeAccents(raw, n)
}

func rgbDistSq(a, b Colour) int {
	dr := int(a.R) - int(b.R)
	dg := int(a.G) - int(b.G)
	db := int(a.B) - int(b.B)

	return dr*dr + dg*dg + db*db
}

func clampLuma(c Colour, target float64) Colour {
	l := c.Luma()
	if l <= target || l <= 0 {
		return c
	}
	if target <= 0 {
		target = 0.05
	}
	k := target / l

	return Colour{
		R:     uint8(math.Round(float64(c.R) * k)),
		G:     uint8(math.Round(float64(c.G) * k)),
		B:     uint8(math.Round(float64(c.B) * k)),
		Count: c.Count,
		Total: c.Total,
	}
}

// normalizeAccents bounds accent lightness so light UI text stays readable over
// the backdrop and synthesizes or darkens a second stop when the cover's top
// colours all sit at similar brightness.
func normalizeAccents(raw []Colour, n int) []Colour {
	if len(raw) == 0 {
		return nil
	}
	if n == 1 {
		return []Colour{clampLuma(raw[0], maxLightLuma)}
	}

	primary := raw[0]
	partnerIdx := pickPartner(raw)
	second := primary
	if partnerIdx >= 0 {
		second = raw[partnerIdx]
	}

	if primary.Luma() >= second.Luma() {
		primary = clampLuma(primary, maxLightLuma)
		second = clampLuma(second, min(maxDarkLuma, primary.Luma()-minLumaStep))
	} else {
		second = clampLuma(second, maxLightLuma)
		primary = clampLuma(primary, min(maxDarkLuma, second.Luma()-minLumaStep))
	}

	out := []Colour{primary, second}
	for i := 1; i < len(raw) && len(out) < n; i++ {
		if i == partnerIdx {
			continue
		}
		out = append(out, clampLuma(raw[i], maxLightLuma))
	}
	if len(out) > n {
		out = out[:n]
	}

	return out
}

func pickPartner(raw []Colour) int {
	primary := raw[0]
	// First look among regions with meaningful area (>= 1.5%) so tiny line-art
	// outlines do not beat a major secondary colour field.
	for _, minShare := range []float64{minPartnerShare, 0} {
		for i := 1; i < len(raw); i++ {
			if raw[i].Fraction() >= minShare && math.Abs(raw[i].Luma()-primary.Luma()) >= minLumaDiff {
				return i
			}
		}
		for i := 1; i < len(raw); i++ {
			if raw[i].Fraction() >= minShare && rgbDistSq(raw[i], primary) >= partnerDistSq {
				return i
			}
		}
	}

	return -1
}

// isAccent is the filter: HSV saturation and Rec. 709 luma in the usable band.
func isAccent(r, g, b uint8) bool {
	high := max(r, g, b)
	low := min(r, g, b)
	hi := float64(high)
	if hi < 1 {
		hi = 1
	}
	saturation := float64(high-low) / hi
	luma := (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)) / 255

	return saturation >= minSaturation && luma >= minLuma && luma <= maxLuma
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
