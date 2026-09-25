package images

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"google.golang.org/genai"
)

// Store keeps prepared images on disk, named by their SHA-256, readable only
// by the owner. Identical images are stored once.
type Store struct {
	dir string
}

var shaRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

var extByMIME = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

// OpenStore creates dir (mode 700) if needed.
func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("image store directory not set")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Dir returns the store's directory.
func (s *Store) Dir() string { return s.dir }

// Put saves img unless an identical image is already stored, in which case
// its age is reset so pruning keeps it.
func (s *Store) Put(img *Image) error {
	ext, ok := extByMIME[img.MIME]
	if !ok || !shaRE.MatchString(img.SHA256) {
		return fmt.Errorf("cannot store %s image", img.MIME)
	}
	final := filepath.Join(s.dir, img.SHA256+ext)
	if _, err := os.Stat(final); err == nil {
		now := time.Now()
		return os.Chtimes(final, now, now)
	}
	tmp, err := os.CreateTemp(s.dir, ".img-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(img.Data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), final)
}

// Get loads a stored image by the URI from Image.URI.
func (s *Store) Get(uri string) ([]byte, string, error) {
	sha, ok := strings.CutPrefix(uri, URIScheme)
	if !ok || !shaRE.MatchString(sha) {
		return nil, "", fmt.Errorf("invalid image reference %q", uri)
	}
	for mime, ext := range extByMIME {
		data, err := os.ReadFile(filepath.Join(s.dir, sha+ext))
		if err == nil {
			return data, mime, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("image %s: %w", sha[:12], os.ErrNotExist)
}

// Prune deletes images not used for maxAge and reports how many went.
func (s *Store) Prune(maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-maxAge)
	n := 0
	for _, e := range entries {
		base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if e.IsDir() || !shaRE.MatchString(base) {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			if os.Remove(filepath.Join(s.dir, e.Name())) == nil {
				n++
			}
		}
	}
	return n, nil
}

// Part is the history reference for img.
func Part(img *Image) *genai.Part {
	return &genai.Part{FileData: &genai.FileData{FileURI: img.URI(), MIMEType: img.MIME, DisplayName: img.Name}}
}

// ToolResultKey marks a tool result that carries an image: its value is an
// Image.URI, and Expand places the picture right after the result.
const ToolResultKey = "image_uri"

func isRef(p *genai.Part) bool {
	return p != nil && p.FileData != nil && strings.HasPrefix(p.FileData.FileURI, URIScheme)
}

func toolImageRef(p *genai.Part) (string, bool) {
	if p == nil || p.FunctionResponse == nil {
		return "", false
	}
	uri, ok := p.FunctionResponse.Response[ToolResultKey].(string)
	return uri, ok && strings.HasPrefix(uri, URIScheme)
}

// HasRefs reports whether any content refers to a stored image.
func HasRefs(contents []*genai.Content) bool {
	for _, c := range contents {
		if c == nil {
			continue
		}
		for _, p := range c.Parts {
			if _, ok := toolImageRef(p); ok || isRef(p) {
				return true
			}
		}
	}
	return false
}

// Expand returns contents with stored-image references replaced by the
// image bytes (and tool-result images appended after their result). Inputs
// are never modified: they are usually the session's own events. A missing
// image, or a nil store, becomes a short note instead so the request still
// works.
func Expand(contents []*genai.Content, s *Store) []*genai.Content {
	if !HasRefs(contents) {
		return contents
	}
	out := make([]*genai.Content, len(contents))
	for i, c := range contents {
		out[i] = c
		if c == nil {
			continue
		}
		changed := false
		parts := make([]*genai.Part, 0, len(c.Parts)+1)
		for _, p := range c.Parts {
			switch uri, isTool := toolImageRef(p); {
			case isRef(p):
				parts = append(parts, s.load(p.FileData.FileURI, p.FileData.DisplayName))
				changed = true
			case isTool:
				parts = append(parts, p, s.load(uri, p.FunctionResponse.Name))
				changed = true
			default:
				parts = append(parts, p)
			}
		}
		if changed {
			cp := *c
			cp.Parts = parts
			out[i] = &cp
		}
	}
	return out
}

func (s *Store) load(uri, name string) *genai.Part {
	if s != nil {
		if data, mime, err := s.Get(uri); err == nil {
			// No DisplayName: the Gemini Developer API rejects it.
			return &genai.Part{InlineData: &genai.Blob{Data: data, MIMEType: mime}}
		}
	}
	if name == "" {
		name = "image"
	}
	return genai.NewPartFromText(fmt.Sprintf("[%s is no longer available]", name))
}
