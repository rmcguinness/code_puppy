// Package images prepares pictures for vision models and keeps them out of
// session files.
//
// An image is normalised once (format checked, oversized pictures scaled
// down and re-encoded), written to a content-addressed Store and referred to
// in conversation history by a FileData part whose URI starts with URIScheme.
// Session files therefore hold a short reference instead of megabytes of
// base64; Expand swaps the references for the bytes just before each model
// request.
package images

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // registers the GIF decoder
	"image/jpeg"
	"image/png"
	"net/http"
	"path"
	"strings"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // registers the WebP decoder
)

// URIScheme prefixes references to stored images ("code-puppy-image:<sha256>").
const URIScheme = "code-puppy-image:"

// Defaults suit every supported provider: Anthropic recommends at most 1568
// pixels on the long edge and caps images at 5 MB (3.75 MB before base64).
const (
	DefaultMaxDimension = 1568
	DefaultMaxInput     = 20 << 20
	MaxEncodedBytes     = 3_750_000
	// maxPixels rejects decompression bombs before decoding: a tiny file
	// can declare a gigantic canvas.
	maxPixels = 100_000_000
)

// Options bound what Prepare accepts and produces.
type Options struct {
	MaxDimension int   // longest edge after scaling (default 1568)
	MaxInput     int64 // largest file accepted (default 20 MB)
}

func (o Options) withDefaults() Options {
	if o.MaxDimension <= 0 {
		o.MaxDimension = DefaultMaxDimension
	}
	if o.MaxInput <= 0 {
		o.MaxInput = DefaultMaxInput
	}
	return o
}

// Image is a prepared picture.
type Image struct {
	Name          string // file name for people (no directory)
	Data          []byte
	MIME          string
	Width, Height int
	SHA256        string // of Data
	OriginalBytes int
	Resized       bool
	// Size is len(Data), kept for an image whose data stays elsewhere (one
	// held by the Code Puppy service).
	Size int
}

// URI is the reference stored in conversation history.
func (img *Image) URI() string { return URIScheme + img.SHA256 }

// Summary is a short human description: "shot.png 1280×720, 210 KB".
func (img *Image) Summary() string {
	n := len(img.Data)
	if n == 0 {
		n = img.Size
	}
	return fmt.Sprintf("%s %d×%d, %s", img.Name, img.Width, img.Height, humanBytes(n))
}

// ErrNotImage is returned for data that isn't a supported image.
var ErrNotImage = errors.New("not a PNG, JPEG, GIF or WebP image")

var supported = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// IsImagePath reports whether a file name has a supported image extension.
func IsImagePath(p string) bool {
	switch strings.ToLower(path.Ext(strings.ReplaceAll(p, `\`, "/"))) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

// Prepare validates data and scales it down when it is larger than the
// limits allow. The format is detected from the bytes, never the name.
func Prepare(name string, data []byte, o Options) (*Image, error) {
	o = o.withDefaults()
	if int64(len(data)) > o.MaxInput {
		return nil, fmt.Errorf("%s is %s; the limit is %s", name, humanBytes(len(data)), humanBytes(int(o.MaxInput)))
	}
	mime := http.DetectContentType(data)
	if !supported[mime] {
		return nil, fmt.Errorf("%s: %w", name, ErrNotImage)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: unreadable image: %w", name, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, fmt.Errorf("%s: %d×%d pixels is too large", name, cfg.Width, cfg.Height)
	}
	// Decode fully even when the image is kept as is: providers reject
	// corrupt images, and the header alone proves little.
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: unreadable image: %w", name, err)
	}
	img := &Image{Name: path.Base(strings.ReplaceAll(name, `\`, "/")), Data: data, MIME: mime,
		Width: cfg.Width, Height: cfg.Height, OriginalBytes: len(data)}

	if cfg.Width > o.MaxDimension || cfg.Height > o.MaxDimension || len(data) > MaxEncodedBytes {
		if err := img.shrink(src, o.MaxDimension); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	sum := sha256.Sum256(img.Data)
	img.SHA256 = hex.EncodeToString(sum[:])
	return img, nil
}

// shrink scales the image to fit maxDim and re-encodes it: PNG for
// lossless sources (screenshots stay crisp) unless that is still too big,
// otherwise JPEG.
func (img *Image) shrink(src image.Image, maxDim int) error {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > maxDim || h > maxDim {
		if w >= h {
			w, h = maxDim, max(1, h*maxDim/w)
		} else {
			w, h = max(1, w*maxDim/h), maxDim
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
		src = dst
	}

	var buf bytes.Buffer
	if img.MIME == "image/png" || img.MIME == "image/gif" {
		if err := png.Encode(&buf, src); err != nil {
			return err
		}
		if buf.Len() <= MaxEncodedBytes {
			img.set(buf.Bytes(), "image/png", w, h)
			return nil
		}
		buf.Reset()
	}
	// JPEG has no alpha channel: flatten onto white.
	flat := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), src, src.Bounds().Min, draw.Over)
	for _, q := range []int{85, 70, 50} {
		buf.Reset()
		if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: q}); err != nil {
			return err
		}
		if buf.Len() <= MaxEncodedBytes {
			img.set(buf.Bytes(), "image/jpeg", w, h)
			return nil
		}
	}
	return fmt.Errorf("still %s after compression", humanBytes(buf.Len()))
}

func (img *Image) set(data []byte, mime string, w, h int) {
	img.Data = bytes.Clone(data)
	img.MIME, img.Width, img.Height, img.Resized = mime, w, h, true
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
