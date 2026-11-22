package artwork

import (
	"image"

	"golang.org/x/image/draw"
)

// ComposeMosaic combines up to four encoded images into one canonical artwork
// image. Four valid tiles render as a 2x2 MaxEdge x MaxEdge square mosaic with
// each quadrant center-cropped to fill without distortion; one to three valid
// tiles normalize the first valid image. Empty or undecodable inputs return
// (nil, nil).
func ComposeMosaic(tiles [][]byte) ([]byte, error) {
	imgs := make([]image.Image, 0, 4)
	for _, raw := range tiles {
		if len(raw) == 0 {
			continue
		}
		img, err := Decode(raw)
		if err != nil {
			continue
		}
		imgs = append(imgs, img)
		if len(imgs) == 4 {
			break
		}
	}
	if len(imgs) == 0 {
		return nil, nil
	}
	if len(imgs) < 4 {
		data, _, err := Encode(Resize(imgs[0], MaxEdge))

		return data, err
	}

	half := MaxEdge / 2
	dst := image.NewRGBA(image.Rect(0, 0, MaxEdge, MaxEdge))
	quads := [4]image.Rectangle{
		image.Rect(0, 0, half, half),
		image.Rect(half, 0, MaxEdge, half),
		image.Rect(0, half, half, MaxEdge),
		image.Rect(half, half, MaxEdge, MaxEdge),
	}
	for i, q := range quads {
		drawSquareCover(dst, q, imgs[i])
	}

	data, _, err := Encode(dst)

	return data, err
}

// drawSquareCover scales the largest centered square of src into dstRect so
// non-square thumbnails (such as 16:9 stills) fill their quadrant cleanly.
func drawSquareCover(dst *image.RGBA, dstRect image.Rectangle, src image.Image) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	crop := b
	if w > h {
		off := (w - h) / 2
		crop = image.Rect(b.Min.X+off, b.Min.Y, b.Min.X+off+h, b.Max.Y)
	} else if h > w {
		off := (h - w) / 2
		crop = image.Rect(b.Min.X, b.Min.Y+off, b.Max.X, b.Min.Y+off+w)
	}
	draw.CatmullRom.Scale(dst, dstRect, src, crop, draw.Src, nil)
}
