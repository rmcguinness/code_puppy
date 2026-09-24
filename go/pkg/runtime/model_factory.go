package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"

	"github.com/retail-cortex/code_puppy/pkg/config"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/genai"
)

// NewModel builds an ADK model.LLM based on configuration.
func NewModel(ctx context.Context, cfg *config.Config, overrideModel string) (model.LLM, error) {
	provider := strings.ToLower(cfg.LLM.Provider)
	modelName := cfg.CodePuppy.DefaultModel
	if overrideModel != "" {
		modelName = overrideModel
	}

	switch provider {
	case "gemini":
		apiKey := cfg.LLM.Gemini.APIKey
		clientCfg := &genai.ClientConfig{
			APIKey: apiKey,
		}
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
		apiKey := cfg.LLM.OpenAI.APIKey
		if apiKey == "" {
			apiKey = "ollama"
		}
		baseURL := cfg.LLM.OpenAI.BaseURL
		if baseURL == "" && provider == "ollama" {
			baseURL = "http://localhost:11434/v1"
		}
		clientCfg := &openaimodel.ClientConfig{
			APIKey:  apiKey,
			BaseURL: baseURL,
		}
		m, err := openaimodel.NewModel(ctx, modelName, clientCfg)
		if err != nil {
			return nil, err
		}
		return &toolCallParsingModel{inner: m}, nil

	case "":
		// If Gemini key is set and provider is empty, try gemini, else fallback to openai/ollama
		if cfg.LLM.Gemini.APIKey != "" {
			return gemini.NewModel(ctx, modelName, &genai.ClientConfig{APIKey: cfg.LLM.Gemini.APIKey, Backend: genai.BackendGeminiAPI})
		}
		if cfg.LLM.OpenAI.APIKey != "" || cfg.LLM.OpenAI.BaseURL != "" {
			apiKey := cfg.LLM.OpenAI.APIKey
			if apiKey == "" {
				apiKey = "ollama"
			}
			m, err := openaimodel.NewModel(ctx, modelName, &openaimodel.ClientConfig{APIKey: apiKey, BaseURL: cfg.LLM.OpenAI.BaseURL})
			if err != nil {
				return nil, err
			}
			return &toolCallParsingModel{inner: m}, nil
		}
		// Default to local Ollama if available
		m, err := openaimodel.NewModel(ctx, modelName, &openaimodel.ClientConfig{APIKey: "ollama", BaseURL: "http://localhost:11434/v1"})
		if err != nil {
			return nil, err
		}
		return &toolCallParsingModel{inner: m}, nil

	default:
		// Fallback to Gemini if configured
		if cfg.LLM.Gemini.APIKey != "" {
			return gemini.NewModel(ctx, modelName, &genai.ClientConfig{APIKey: cfg.LLM.Gemini.APIKey})
		}
		return nil, fmt.Errorf("unsupported or unconfigured LLM provider '%s'", provider)
	}
}

// MockLLM provides an in-memory LLM implementation for tests and offline validation.
type MockLLM struct {
	ModelName string
	Responses []*genai.Content
	CallCount int
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
		idx := m.CallCount
		m.CallCount++

		var content *genai.Content
		if idx < len(m.Responses) {
			content = m.Responses[idx]
		} else {
			content = genai.NewContentFromText("Woof! Task completed.", genai.RoleModel)
		}

		resp := &model.LLMResponse{
			Content: content,
		}
		yield(resp, nil)
	}
}

// toolCallParsingModel wraps any model.LLM to detect and convert JSON-encoded tool calls in text
// (such as those emitted by Ollama models) into native Google ADK FunctionCalls.
type toolCallParsingModel struct {
	inner model.LLM
}

func (m *toolCallParsingModel) Name() string {
	return m.inner.Name()
}

func (m *toolCallParsingModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		for resp, err := range m.inner.GenerateContent(ctx, req, stream) {
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}
			if resp != nil && resp.Content != nil {
				parseTextToolCalls(resp.Content)
			}
			if !yield(resp, nil) {
				return
			}
		}
	}
}

func parseTextToolCalls(content *genai.Content) {
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
			if err := json.Unmarshal([]byte(trimmed), &call); err == nil && call.Name != "" {
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
