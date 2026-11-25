package artwork

import (
	"bytes"
	"image"
	"image/png"
	"math"

	"golang.org/x/image/draw"
)

const (
	backdropEdge       = 64
	backdropBins       = 8
	backdropBlurPasses = 3
	backdropBlurRadius = 6
	backdropSubdir     = "bg"
)

// Backdrop crops img to a centered square, scales it to 64x64, tints dark
// pixels toward the cover's dominant hue (or clamped grey for monochrome art),
// and applies three separable box-blur passes.
func Backdrop(img image.Image) (*image.NRGBA, error) {
	if img == nil {
		return nil, ErrNotImage
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, ErrNotImage
	}

	// Center-crop to a square before scaling so non-square covers do not
	// stretch the backdrop shader UVs.
	side := min(w, h)
	srcRect := image.Rect(
		b.Min.X+(w-side)/2,
		b.Min.Y+(h-side)/2,
		b.Min.X+(w-side)/2+side,
		b.Min.Y+(h-side)/2+side,
	)

	small := image.NewNRGBA(image.Rect(0, 0, backdropEdge, backdropEdge))
	draw.CatmullRom.Scale(small, small.Bounds(), img, srcRect, draw.Src, nil)

	px := make([][3]float32, backdropEdge*backdropEdge)
	for y := range backdropEdge {
		row := small.Pix[y*small.Stride:]
		for x := range backdropEdge {
			off := x * 4
			px[y*backdropEdge+x] = [3]float32{
				float32(row[off]),
				float32(row[off+1]),
				float32(row[off+2]),
			}
		}
	}

	tint, share := backdropPalette(px)
	strength := float32(0.45) * clamp32(share/0.15, 0.05, 1.0)
	for i := range px {
		p := px[i]
		luma := (0.299*p[0] + 0.587*p[1] + 0.114*p[2]) / 255.0
		dark := 1.0 - smoothstep32(0.0, 0.5, luma)
		for c := range 3 {
			p[c] += (tint[c] - p[c]) * dark * strength
		}
		px[i] = p
	}

	tmp := make([][3]float32, len(px))
	for range backdropBlurPasses {
		boxBlurPass(px, tmp, backdropEdge, backdropBlurRadius, true)
		boxBlurPass(tmp, px, backdropEdge, backdropBlurRadius, false)
	}

	out := image.NewNRGBA(image.Rect(0, 0, backdropEdge, backdropEdge))
	for y := range backdropEdge {
		row := out.Pix[y*out.Stride:]
		for x := range backdropEdge {
			p := px[y*backdropEdge+x]
			off := x * 4
			row[off] = clampByte(p[0])
			row[off+1] = clampByte(p[1])
			row[off+2] = clampByte(p[2])
			row[off+3] = 0xff
		}
	}

	return out, nil
}

// EncodeBackdrop generates the 64x64 blurred backdrop and encodes it as
// lossless PNG so smooth dark gradients avoid 4:2:0 chroma banding when
// upscaled full-screen.
func EncodeBackdrop(img image.Image) ([]byte, string, error) {
	bd, err := Backdrop(img)
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, bd); err != nil {
		return nil, "", err
	}

	return buf.Bytes(), "image/png", nil
}

func backdropPalette(px [][3]float32) (tint [3]float32, share float32) {
	var bins [backdropBins * backdropBins * backdropBins][4]float32
	var sum [3]float32
	var n, lit float32

	for _, rgb := range px {
		sum[0] += rgb[0]
		sum[1] += rgb[1]
		sum[2] += rgb[2]
		n++

		mx := max(rgb[0], max(rgb[1], rgb[2]))
		mn := min(rgb[0], min(rgb[1], rgb[2]))
		if mx < 46.0 || mx-mn < 28.0 {
			continue
		}
		lit++

		ri := min(int(rgb[0])*backdropBins/256, backdropBins-1)
		gi := min(int(rgb[1])*backdropBins/256, backdropBins-1)
		bi := min(int(rgb[2])*backdropBins/256, backdropBins-1)
		idx := ri*(backdropBins*backdropBins) + gi*backdropBins + bi
		bins[idx][0] += rgb[0]
		bins[idx][1] += rgb[1]
		bins[idx][2] += rgb[2]
		bins[idx][3]++
	}

	avg := [3]float32{sum[0] / n, sum[1] / n, sum[2] / n}
	share = lit / n

	bestIdx := -1
	bestScore := float32(-1)
	for i := range bins {
		if bins[i][3] <= 0 {
			continue
		}
		s := backdropBinScore(bins[i])
		if s > bestScore {
			bestScore = s
			bestIdx = i
		}
	}

	if bestIdx < 0 {
		g := clamp32((avg[0]+avg[1]+avg[2])/3.0, 28.0, 200.0)

		return [3]float32{g, g, g}, 0
	}

	best := bins[bestIdx]
	mean := [3]float32{best[0] / best[3], best[1] / best[3], best[2] / best[3]}
	strong := clamp32(share/0.12, 0.0, 1.0)
	tint = [3]float32{
		mean[0]*strong + avg[0]*(1.0-strong),
		mean[1]*strong + avg[1]*(1.0-strong),
		mean[2]*strong + avg[2]*(1.0-strong),
	}

	return tint, share
}

func backdropBinScore(b [4]float32) float32 {
	r, g, bl := b[0]/b[3], b[1]/b[3], b[2]/b[3]
	mx := max(r, max(g, bl))
	mn := min(r, min(g, bl))
	var sat float32
	if mx > 0 {
		sat = (mx - mn) / mx
	}

	return b[3] * (0.35 + sat)
}

func boxBlurPass(src, dst [][3]float32, n, r int, horizontal bool) {
	inv := float32(1.0) / float32(2*r+1)
	for y := range n {
		for x := range n {
			var acc [3]float32
			for o := -r; o <= r; o++ {
				sx, sy := x, y
				if horizontal {
					sx = clampInt(x+o, 0, n-1)
				} else {
					sy = clampInt(y+o, 0, n-1)
				}
				p := src[sy*n+sx]
				acc[0] += p[0]
				acc[1] += p[1]
				acc[2] += p[2]
			}
			dst[y*n+x] = [3]float32{acc[0] * inv, acc[1] * inv, acc[2] * inv}
		}
	}
}

func smoothstep32(a, b, x float32) float32 {
	t := clamp32((x-a)/(b-a), 0, 1)

	return t * t * (3.0 - 2.0*t)
}

func clamp32(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}

	return v
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}

	return v
}

func clampByte(v float32) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}

	return uint8(math.Round(float64(v)))
}
