package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3/option"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// The ADK's OpenAI model (Responses API) rejects image parts. Images are
// therefore replaced by unique marker texts before the request is converted,
// and openAIImageMiddleware swaps each marker for an input_image item in the
// outgoing JSON. The markers and images travel in the request context, so
// nothing is shared between requests.

const imageMarkerPrefix = "⁣code-puppy-image:"

type openAIImagesKey struct{}

// openAIImages maps marker text to a data: URL.
type openAIImages map[string]string

// replaceImagesWithMarkers returns req with inline images replaced by
// markers, and the context carrying them. Requests without images are
// returned unchanged.
func replaceImagesWithMarkers(ctx context.Context, req *model.LLMRequest) (context.Context, *model.LLMRequest) {
	if req == nil {
		return ctx, req
	}
	var imgs openAIImages
	contents := req.Contents
	for i, c := range req.Contents {
		if c == nil {
			continue
		}
		var parts []*genai.Part
		for j, p := range c.Parts {
			if p == nil || p.InlineData == nil {
				if parts != nil {
					parts = append(parts, p)
				}
				continue
			}
			if imgs == nil {
				imgs = openAIImages{}
				contents = append([]*genai.Content{}, req.Contents...)
			}
			if parts == nil {
				parts = append([]*genai.Part{}, c.Parts[:j]...)
			}
			marker := imageMarkerPrefix + randomID()
			imgs[marker] = "data:" + p.InlineData.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(p.InlineData.Data)
			parts = append(parts, genai.NewPartFromText(marker))
		}
		if parts != nil {
			cp := *c
			cp.Parts = parts
			contents[i] = &cp
		}
	}
	if imgs == nil {
		return ctx, req
	}
	cp := *req
	cp.Contents = contents
	return context.WithValue(ctx, openAIImagesKey{}, imgs), &cp
}

func randomID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// openAIImageMiddleware rewrites marker input_text items into input_image
// items. Bodies without markers pass through untouched.
func openAIImageMiddleware(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	imgs, _ := req.Context().Value(openAIImagesKey{}).(openAIImages)
	if len(imgs) == 0 || req.Body == nil {
		return next(req)
	}
	body, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, err
	}
	if bytes.Contains(body, []byte(`code-puppy-image:`)) {
		if body, err = rewriteImageMarkers(body, imgs); err != nil {
			return nil, fmt.Errorf("openai: attach images: %w", err)
		}
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Length", fmt.Sprint(len(body)))
	return next(req)
}

func rewriteImageMarkers(body []byte, imgs openAIImages) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // keep numbers exactly as sent
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	items, _ := doc["input"].([]any)
	found := 0
	for _, it := range items {
		item, _ := it.(map[string]any)
		content, _ := item["content"].([]any)
		for i, c := range content {
			part, _ := c.(map[string]any)
			text, _ := part["text"].(string)
			if part["type"] != "input_text" || !strings.HasPrefix(text, imageMarkerPrefix) {
				continue
			}
			url, ok := imgs[text]
			if !ok {
				continue
			}
			content[i] = map[string]any{"type": "input_image", "image_url": url, "detail": "auto"}
			found++
		}
	}
	if found == 0 {
		return body, nil
	}
	return json.Marshal(doc)
}
