// Package artwork decodes, normalizes, and caches cover images for every
// consumer in the app. Raw artwork is decoded and downscaled to at most
// [MaxEdge] pixels once, then shared across UI covers, accent extraction,
// Discord Rich Presence uploads, and OS media controls.
package artwork

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"image/jpeg"
	"image/png"
	"strconv"

	// Register every supported artwork format once in this package.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// MaxEdge is the longest side of a canonical cached cover. 512px stays sharp
// above 1x display scaling while keeping stored covers to a few tens of
// kilobytes.
const MaxEdge = 512

const (
	accentEdge  = 256
	jpegQuality = 82
)

// ErrNotImage reports that raw artwork bytes could not be decoded as an image.
var ErrNotImage = errors.New("artwork: not a decodable image")

// ID returns a stable FNV-64a identifier for raw artwork bytes, or "" when raw
// is empty.
func ID(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	h := fnv.New64a()
	_, _ = h.Write(raw)

	return strconv.FormatUint(h.Sum64(), 16)
}

// Decode decodes JPEG, PNG, WebP, or GIF bytes into an [image.Image].
func Decode(raw []byte) (image.Image, error) {
	if len(raw) == 0 {
		return nil, ErrNotImage
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotImage, err)
	}

	return img, nil
}

// Resize scales img to fit inside maxEdge x maxEdge using Catmull-Rom
// resampling while preserving aspect ratio. An image already within the box is
// returned unchanged.
func Resize(img image.Image, maxEdge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxEdge <= 0 || w <= 0 || h <= 0 || (w <= maxEdge && h <= maxEdge) {
		return img
	}

	nw, nh := maxEdge, h*maxEdge/w
	if h > w {
		nw, nh = w*maxEdge/h, maxEdge
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)

	return dst
}

// Encode encodes img as PNG when any pixel is transparent, or JPEG at quality
// 82 otherwise.
func Encode(img image.Image) (data []byte, mime string, err error) {
	var buf bytes.Buffer
	if hasTransparency(img) {
		mime = "image/png"
		err = png.Encode(&buf, img)
	} else {
		mime = "image/jpeg"
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality})
	}
	if err != nil {
		return nil, "", err
	}

	return buf.Bytes(), mime, nil
}

// FormatDataURL formats encoded image bytes and MIME type as a base64 data URL,
// or "" when data is empty.
func FormatDataURL(data []byte, mime string) string {
	if len(data) == 0 || mime == "" {
		return ""
	}

	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func hasTransparency(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return !o.Opaque()
	}

	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a < 0xffff {
				return true
			}
		}
	}

	return false
}
