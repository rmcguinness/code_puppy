package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"strings"
	"sync"

	"github.com/openai/openai-go/v3/option"
	"github.com/retail-cortex/code_puppy/internal/config"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/genai"
)

// NewModel builds an ADK model.LLM based on configuration.
//
// The model (overrideModel, else the configured one) may name its provider,
// "anthropic/claude-sonnet-5", to use a provider other than llm.provider.
// Bare fallback names use llm.provider.
func NewModel(ctx context.Context, cfg *config.Config, overrideModel string) (model.LLM, error) {
	ref := cfg.ModelName()
	if overrideModel != "" {
		ref = overrideModel
	}
	provider := strings.ToLower(cfg.LLM.Provider)
	p, modelName := ParseModelRef(ref, provider)
	primary, err := newProviderModel(ctx, cfg, p, modelName)
	if err != nil || len(cfg.LLM.FallbackModels) == 0 {
		return primary, err
	}
	chain := []model.LLM{primary}
	for _, ref := range cfg.LLM.FallbackModels {
		p, name := ParseModelRef(ref, provider)
		m, err := newProviderModel(ctx, cfg, p, name)
		if err != nil {
			// Usually missing credentials; `code-puppy doctor` lists each fallback.
			slog.WarnContext(ctx, "fallback model unavailable", "model", ref, "error", err)
			continue
		}
		chain = append(chain, m)
	}
	return newFallbackModel(chain), nil
}

// NewModelRef builds the single model a fallback reference names (no
// chain), e.g. for `doctor` to check each fallback on its own.
func NewModelRef(ctx context.Context, cfg *config.Config, ref string) (model.LLM, error) {
	p, name := ParseModelRef(ref, cfg.LLM.Provider)
	return newProviderModel(ctx, cfg, p, name)
}

// knownProviders are the provider prefixes recognised in model references.
var knownProviders = map[string]bool{"gemini": true, "anthropic": true, "openai": true, "ollama": true}

// ParseModelRef splits "provider/model" into its parts. Without a known
// provider prefix the whole reference is a model of defaultProvider, so
// OpenRouter-style names ("anthropic/claude-…" on the openai provider) need
// the provider spelled out: "openai/anthropic/claude-…".
func ParseModelRef(ref, defaultProvider string) (provider, name string) {
	ref = strings.TrimSpace(ref)
	if p, rest, ok := strings.Cut(ref, "/"); ok && knownProviders[strings.ToLower(p)] && rest != "" {
		return strings.ToLower(p), rest
	}
	return strings.ToLower(defaultProvider), ref
}

const (
	defaultOpenAIBaseURL = "https://api.openai.com/v1"
	defaultOllamaBaseURL = "http://localhost:11434/v1"
)

// openAICompatEndpoint returns the key and base URL for the openai and
// ollama providers, which share the [llm.openai] section. Its defaults point
// at OpenAI, so Ollama uses localhost unless another base_url was set, and
// never receives the OpenAI key.
func openAICompatEndpoint(c config.OpenAIConfig, provider string) (apiKey, baseURL string) {
	apiKey, baseURL = c.APIKey, c.BaseURL
	if provider == "ollama" {
		if baseURL == "" || baseURL == defaultOpenAIBaseURL {
			baseURL = defaultOllamaBaseURL
		}
		apiKey = "ollama"
	}
	if apiKey == "" {
		apiKey = "ollama"
	}
	return apiKey, baseURL
}

// newProviderModel builds one model of the given provider, applying its
// [model_settings] on each call.
func newProviderModel(ctx context.Context, cfg *config.Config, provider, modelName string) (model.LLM, error) {
	m, err := buildProviderModel(ctx, cfg, provider, modelName)
	if err != nil {
		return nil, err
	}
	return withModelSettings(m, providerOf(m)), nil
}

// providerOf names the API a built model talks to; with provider "" the
// choice was made from the credentials present.
func providerOf(m model.LLM) string {
	switch m.(type) {
	case *anthropicModel:
		return "anthropic"
	case *toolCallParsingModel:
		return "openai"
	}
	return "gemini"
}

func buildProviderModel(ctx context.Context, cfg *config.Config, provider, modelName string) (model.LLM, error) {
	pol := policyFrom(cfg.LLM)
	switch provider {
	case "gemini":
		apiKey := cfg.LLM.Gemini.APIKey
		clientCfg := pol.geminiConfig(apiKey)
		if cfg.LLM.Gemini.ProjectID != "" {
			clientCfg.Project = cfg.LLM.Gemini.ProjectID
		}
		if cfg.LLM.Gemini.Location != "" {
			clientCfg.Location = cfg.LLM.Gemini.Location
		}
		if apiKey != "" {
			clientCfg.Backend = genai.BackendGeminiAPI
		}

		return gemini.NewModel(ctx, modelName, clientCfg)

	case "openai", "ollama":
		apiKey, baseURL := openAICompatEndpoint(cfg.LLM.OpenAI, provider)
		return newOpenAIModel(ctx, modelName, apiKey, baseURL, pol.openAIOptions()...)

	case "anthropic":
		return newAnthropicModel(cfg.LLM.Anthropic, modelName, pol.anthropicOptions()...), nil

	case "":
		// If Gemini key is set and provider is empty, try gemini, else fallback to openai/ollama
		if cfg.LLM.Gemini.APIKey != "" {
			gc := pol.geminiConfig(cfg.LLM.Gemini.APIKey)
			gc.Backend = genai.BackendGeminiAPI
			return gemini.NewModel(ctx, modelName, gc)
		}
		if cfg.LLM.Anthropic.APIKey != "" {
			if cfg.CodePuppy.DefaultModel == "" {
				modelName = cfg.LLM.Anthropic.Model
			}
			return newAnthropicModel(cfg.LLM.Anthropic, modelName, pol.anthropicOptions()...), nil
		}
		if cfg.LLM.OpenAI.APIKey != "" || cfg.LLM.OpenAI.BaseURL != "" {
			apiKey := cfg.LLM.OpenAI.APIKey
			if apiKey == "" {
				apiKey = "ollama"
			}
			return newOpenAIModel(ctx, modelName, apiKey, cfg.LLM.OpenAI.BaseURL, pol.openAIOptions()...)
		}
		// Default to local Ollama if available
		return newOpenAIModel(ctx, modelName, "ollama", defaultOllamaBaseURL, pol.openAIOptions()...)

	default:
		// Fallback to Gemini if configured
		if cfg.LLM.Gemini.APIKey != "" {
			return gemini.NewModel(ctx, modelName, pol.geminiConfig(cfg.LLM.Gemini.APIKey))
		}
		return nil, fmt.Errorf("unsupported or unconfigured LLM provider '%s'", provider)
	}
}

// MockLLM provides an in-memory LLM implementation for tests and offline validation.
// It is safe for concurrent use; read CallCount via Calls() while runs are in flight.
type MockLLM struct {
	ModelName string
	Responses []*genai.Content
	CallCount int

	mu       sync.Mutex
	Requests []*model.LLMRequest
	// Usage, if set, is attached to every response.
	Usage *genai.GenerateContentResponseUsageMetadata
	// ServedBy and Metadata, if set, become each response's ModelVersion and
	// CustomMetadata.
	ServedBy string
	Metadata map[string]any
}

// Calls returns the number of GenerateContent calls so far.
func (m *MockLLM) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.CallCount
}

func NewMockLLM(name string, responses ...*genai.Content) *MockLLM {
	if name == "" {
		name = "mock-llm"
	}
	return &MockLLM{
		ModelName: name,
		Responses: responses,
	}
}

func (m *MockLLM) Name() string {
	return m.ModelName
}

func (m *MockLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.mu.Lock()
		idx := m.CallCount
		m.CallCount++
		m.Requests = append(m.Requests, req)
		m.mu.Unlock()

		var content *genai.Content
		if idx < len(m.Responses) {
			content = m.Responses[idx]
		} else {
			content = genai.NewContentFromText("Woof! Task completed.", genai.RoleModel)
		}

		resp := &model.LLMResponse{
			Content:        content,
			UsageMetadata:  m.Usage,
			ModelVersion:   m.ServedBy,
			CustomMetadata: m.Metadata,
		}
		yield(resp, nil)
	}
}

// toolCallParsingModel wraps any model.LLM to detect and convert JSON-encoded tool calls in text
// (such as those emitted by Ollama models) into native Google ADK FunctionCalls.
// newOpenAIModel builds an OpenAI-compatible (Responses API) model with
// image support and text-encoded tool call parsing.
func newOpenAIModel(ctx context.Context, name, apiKey, baseURL string, opts ...option.RequestOption) (model.LLM, error) {
	m, err := openaimodel.NewModel(ctx, name, &openaimodel.ClientConfig{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Options: append([]option.RequestOption{option.WithMiddleware(openAIImageMiddleware)}, opts...),
	})
	if err != nil {
		return nil, err
	}
	return &toolCallParsingModel{inner: m}, nil
}

type toolCallParsingModel struct {
	inner model.LLM
}

func (m *toolCallParsingModel) Name() string {
	return m.inner.Name()
}

func (m *toolCallParsingModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		// Text-encoded tool calls can only be recognised in complete responses,
		// so this wrapper never streams.
		ctx, req := replaceImagesWithMarkers(ctx, req)
		for resp, err := range m.inner.GenerateContent(ctx, req, false) {
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}
			if resp != nil && resp.Content != nil {
				parseTextToolCalls(resp.Content, offeredTools(req))
			}
			if !yield(resp, nil) {
				return
			}
		}
	}
}

// offeredTools returns the names of tools declared in the request. Only these
// may be synthesised from text, so a model that merely quotes a JSON snippet
// (for example, echoing a file it read) cannot trigger an arbitrary tool.
func offeredTools(req *model.LLMRequest) map[string]bool {
	names := make(map[string]bool)
	if req == nil {
		return names
	}
	for name := range req.Tools {
		names[name] = true
	}
	if req.Config != nil {
		for _, t := range req.Config.Tools {
			if t == nil {
				continue
			}
			for _, fd := range t.FunctionDeclarations {
				if fd != nil {
					names[fd.Name] = true
				}
			}
		}
	}
	return names
}

func parseTextToolCalls(content *genai.Content, allowed map[string]bool) {
	if len(allowed) == 0 {
		return
	}
	for _, part := range content.Parts {
		if part.FunctionCall != nil || part.Text == "" {
			continue
		}
		trimmed := strings.TrimSpace(part.Text)

		// Strip markdown code fences if wrapped
		if strings.HasPrefix(trimmed, "```") {
			lines := strings.Split(trimmed, "\n")
			if len(lines) >= 3 && strings.HasPrefix(lines[0], "```") && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
				trimmed = strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
			}
		}

		if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
			var call struct {
				Name       string         `json:"name"`
				Arguments  map[string]any `json:"arguments"`
				Args       map[string]any `json:"args"`
				Parameters map[string]any `json:"parameters"`
			}
			if err := json.Unmarshal([]byte(trimmed), &call); err == nil && allowed[call.Name] {
				args := call.Arguments
				if len(args) == 0 {
					args = call.Args
				}
				if len(args) == 0 {
					args = call.Parameters
				}
				if args == nil {
					args = make(map[string]any)
				}
				// Normalize aliases: "file_path" -> "path"
				if fp, ok := args["file_path"]; ok && args["path"] == nil {
					args["path"] = fp
				}

				part.Text = ""
				part.FunctionCall = &genai.FunctionCall{
					Name: call.Name,
					Args: args,
				}
			}
		}
	}
}
