package images

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y += 7 {
		for x := 0; x < w; x += 5 {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 99, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// noisyPNG compresses badly, to exceed the byte limit at a small size.
func noisyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		img.Pix[i] = byte(r.IntN(256))
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func TestPrepareKeepsSmallImages(t *testing.T) {
	data := pngBytes(t, 200, 100)
	img, err := Prepare("dir/shot.png", data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if img.Resized || !bytes.Equal(img.Data, data) || img.MIME != "image/png" || img.Width != 200 || img.Height != 100 {
		t.Errorf("small image changed: %+v", img)
	}
	if img.Name != "shot.png" || len(img.SHA256) != 64 || !strings.HasPrefix(img.URI(), URIScheme) {
		t.Errorf("metadata: %q %q", img.Name, img.URI())
	}
	if s := img.Summary(); !strings.Contains(s, "shot.png 200×100") {
		t.Errorf("summary %q", s)
	}
}

func TestPrepareScalesLargeImages(t *testing.T) {
	img, err := Prepare("wide.png", pngBytes(t, 3200, 800), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !img.Resized || img.Width != 1568 || img.Height != 392 || img.MIME != "image/png" {
		t.Errorf("got %dx%d %s resized=%v", img.Width, img.Height, img.MIME, img.Resized)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(img.Data))
	if err != nil || cfg.Width != 1568 {
		t.Errorf("stored data doesn't match: %+v %v", cfg, err)
	}
	tall, _ := Prepare("tall.png", pngBytes(t, 300, 900), Options{MaxDimension: 300})
	if tall.Width != 100 || tall.Height != 300 {
		t.Errorf("portrait: %dx%d", tall.Width, tall.Height)
	}
}

// A picture within the pixel limit but over the byte limit is re-encoded as
// JPEG rather than sent too large.
func TestPrepareCompressesHeavyImages(t *testing.T) {
	data := noisyPNG(t, 1500, 1000)
	if len(data) <= MaxEncodedBytes {
		t.Fatalf("fixture too small: %d", len(data))
	}
	img, err := Prepare("noise.png", data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if img.MIME != "image/jpeg" || len(img.Data) > MaxEncodedBytes || img.Width != 1500 {
		t.Errorf("got %s %d bytes %dx%d", img.MIME, len(img.Data), img.Width, img.Height)
	}
}

func TestPrepareRejects(t *testing.T) {
	cases := map[string][]byte{
		"text":      []byte("hello, not an image"),
		"svg":       []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`),
		"truncated": pngBytes(t, 50, 50)[:40],
	}
	for name, data := range cases {
		if _, err := Prepare(name+".png", data, Options{}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := Prepare("big.png", pngBytes(t, 10, 10), Options{MaxInput: 10}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("size limit: %v", err)
	}
	if _, err := Prepare("x.txt", []byte("plain"), Options{}); !errors.Is(err, ErrNotImage) {
		t.Errorf("want ErrNotImage, got %v", err)
	}
}

// A decompression bomb declares a huge canvas in a tiny file; it must be
// refused from the header, before any pixels are allocated.
func TestPrepareRejectsDecompressionBomb(t *testing.T) {
	data := pngBytes(t, 1, 1)
	// Patch the IHDR width/height (bytes 16..23) to 50000×50000 and fix
	// the chunk's CRC (bytes 29..32, over type and data).
	bomb := bytes.Clone(data)
	copy(bomb[16:24], []byte{0, 0, 0xC3, 0x50, 0, 0, 0xC3, 0x50})
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	if _, err := Prepare("bomb.png", bomb, Options{}); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("bomb: %v", err)
	}
}

func TestPrepareFlattensTransparencyForJPEG(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 40, 40)) // fully transparent
	var buf bytes.Buffer
	png.Encode(&buf, src)
	// A JPEG source takes the JPEG path.
	img := &Image{Data: buf.Bytes(), MIME: "image/jpeg", Width: 40, Height: 40}
	if err := img.shrink(src, 20); err != nil {
		t.Fatal(err)
	}
	dec, err := jpeg.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatal(err)
	}
	if r, g, b, _ := dec.At(5, 5).RGBA(); r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Errorf("transparent pixels should become white, got %v %v %v", r>>8, g>>8, b>>8)
	}
}

func TestStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "images")
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", info.Mode().Perm())
	}
	img, _ := Prepare("a.png", pngBytes(t, 20, 20), Options{})
	if err := s.Put(img); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(img); err != nil { // idempotent
		t.Fatal(err)
	}
	file := filepath.Join(dir, img.SHA256+".png")
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("file: %v %v", info, err)
	}
	data, mime, err := s.Get(img.URI())
	if err != nil || mime != "image/png" || !bytes.Equal(data, img.Data) {
		t.Errorf("Get: %s %v", mime, err)
	}
	for _, bad := range []string{"", "code-puppy-image:../../etc/passwd", URIScheme + strings.Repeat("A", 64), "file:///x.png"} {
		if _, _, err := s.Get(bad); err == nil {
			t.Errorf("Get(%q) should fail", bad)
		}
	}
	if _, _, err := s.Get(URIScheme + strings.Repeat("0", 64)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing image: %v", err)
	}

	// Prune removes old images only; Put refreshes the age of reused ones.
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(file, old, old)
	other, _ := Prepare("b.png", pngBytes(t, 30, 30), Options{})
	s.Put(other)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600)
	os.Chtimes(filepath.Join(dir, "notes.txt"), old, old)
	if n, err := s.Prune(24 * time.Hour); err != nil || n != 1 {
		t.Errorf("Prune = %d, %v", n, err)
	}
	if _, _, err := s.Get(img.URI()); err == nil {
		t.Error("old image should be pruned")
	}
	if _, _, err := s.Get(other.URI()); err != nil {
		t.Error("recent image pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Error("prune must only touch image files")
	}
}

func TestExpand(t *testing.T) {
	s, _ := OpenStore(t.TempDir())
	img, _ := Prepare("shot.png", pngBytes(t, 20, 20), Options{})
	s.Put(img)
	missing := &Image{Name: "gone.png", MIME: "image/png", SHA256: strings.Repeat("1", 64)}

	user := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{Part(img), genai.NewPartFromText("what is this?")}}
	tool := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
		Name: "view_image", Response: map[string]any{ToolResultKey: img.URI(), "path": "shot.png"}}}}}
	gone := &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{Part(missing)}}
	plain := genai.NewContentFromText("hi", genai.RoleUser)
	in := []*genai.Content{plain, user, tool, gone}
	snapshot := []*genai.Part{user.Parts[0], user.Parts[1]}

	out := Expand(in, s)
	if out[0] != plain {
		t.Error("contents without images should be passed through")
	}
	if b := out[1].Parts[0].InlineData; b == nil || b.MIMEType != "image/png" || !bytes.Equal(b.Data, img.Data) || out[1].Parts[1].Text != "what is this?" {
		t.Errorf("user image not expanded: %+v", out[1].Parts)
	}
	if len(out[2].Parts) != 2 || out[2].Parts[0].FunctionResponse == nil || out[2].Parts[1].InlineData == nil {
		t.Errorf("tool image should follow its result: %+v", out[2].Parts)
	}
	if !strings.Contains(out[3].Parts[0].Text, "gone.png is no longer available") {
		t.Errorf("missing image: %+v", out[3].Parts[0])
	}
	// The session's own contents are untouched.
	if !reflect.DeepEqual(user.Parts, snapshot) || user.Parts[0].InlineData != nil || len(tool.Parts) != 1 {
		t.Error("Expand modified its input")
	}
	if got := Expand([]*genai.Content{plain}, s); got[0] != plain {
		t.Error("no refs: same slice expected")
	}
	if nilStore := Expand([]*genai.Content{user}, nil); nilStore[0].Parts[0].Text == "" {
		t.Error("nil store should give a placeholder")
	}
}

func TestMentions(t *testing.T) {
	got := Mentions(`look at @shot.png and @"My Screens/a b.JPG", not me@example.png, @src/main.go, again @shot.png. And @ui/x.webp,`)
	want := []string{"shot.png", "My Screens/a b.JPG", "ui/x.webp"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Mentions = %q", got)
	}
	if Mentions("no mentions here") != nil {
		t.Error("expected none")
	}
}

func TestReadClipboardIsReplaceable(t *testing.T) {
	defer func(f func(context.Context) ([]byte, error)) { ReadClipboard = f }(ReadClipboard)
	ReadClipboard = func(context.Context) ([]byte, error) { return nil, ErrNoClipboardImage }
	if _, err := ReadClipboard(context.Background()); !errors.Is(err, ErrNoClipboardImage) {
		t.Error(err)
	}
}
