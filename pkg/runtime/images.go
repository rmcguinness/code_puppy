package runtime

import (
	"context"
	"iter"

	"github.com/retail-cortex/code_puppy/pkg/images"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// imageModel expands stored-image references in each request into the
// image bytes, so conversation history (and session files) only ever hold
// the references.
type imageModel struct {
	inner model.LLM
	store *images.Store
}

func withImages(llm model.LLM, store *images.Store) model.LLM {
	if llm == nil {
		return nil
	}
	if m, ok := llm.(*imageModel); ok {
		llm = m.inner
	}
	return &imageModel{inner: llm, store: store}
}

func (m *imageModel) Name() string { return m.inner.Name() }

func (m *imageModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if req != nil && images.HasRefs(req.Contents) {
		cp := *req
		cp.Contents = images.Expand(req.Contents, m.store)
		req = &cp
	}
	return m.inner.GenerateContent(ctx, req, stream)
}

// WithAttachments adds parts (usually images.Part references) to the
// prompt, ahead of its text as providers recommend.
func WithAttachments(parts ...*genai.Part) ExecOption {
	return func(s *runState) { s.attachments = append(s.attachments, parts...) }
}

func userContent(prompt string, attachments []*genai.Part) *genai.Content {
	if len(attachments) == 0 {
		return genai.NewContentFromText(prompt, genai.RoleUser)
	}
	parts := append([]*genai.Part{}, attachments...)
	if prompt != "" {
		parts = append(parts, genai.NewPartFromText(prompt))
	}
	return &genai.Content{Role: genai.RoleUser, Parts: parts}
}
