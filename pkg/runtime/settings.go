package runtime

import (
	"context"
	"iter"
	"log/slog"

	"github.com/retail-cortex/code_puppy/pkg/config"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// modelSettingsKey carries the engine's per-model settings lookup in a
// run's context, so each model (including each member of a fallback chain)
// applies its own settings, and changes apply to the next call.
type modelSettingsKey struct{}

type settingsLookup func(model string) (config.ModelSettings, bool)

func withSettingsLookup(ctx context.Context, f settingsLookup) context.Context {
	return context.WithValue(ctx, modelSettingsKey{}, f)
}

// settingsModel applies [model_settings] for the model it wraps. Settings
// the provider can't take are dropped rather than sent: the OpenAI adapter
// fails the request on a seed, and current Anthropic models reject sampling
// parameters.
type settingsModel struct {
	inner    model.LLM
	provider string
}

func withModelSettings(m model.LLM, provider string) model.LLM {
	if m == nil {
		return nil
	}
	return &settingsModel{inner: m, provider: provider}
}

func (m *settingsModel) Name() string { return m.inner.Name() }

func (m *settingsModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if lookup, _ := ctx.Value(modelSettingsKey{}).(settingsLookup); lookup != nil && req != nil {
		if s, ok := lookup(m.inner.Name()); ok {
			req = m.apply(ctx, req, s)
		}
	}
	return m.inner.GenerateContent(ctx, req, stream)
}

// apply returns a copy of req with s applied; req itself is shared with
// the other models of a fallback chain, so it is never changed.
func (m *settingsModel) apply(ctx context.Context, req *model.LLMRequest, s config.ModelSettings) *model.LLMRequest {
	cp := *req
	gc := genai.GenerateContentConfig{}
	if req.Config != nil {
		gc = *req.Config
	}
	var dropped []string
	use := func(key string) bool {
		if SettingSupported(m.provider, m.inner.Name(), key) {
			return true
		}
		dropped = append(dropped, key)
		return false
	}
	if s.Temperature != nil && use("temperature") {
		gc.Temperature = genai.Ptr(float32(*s.Temperature))
	}
	if s.TopP != nil && use("top_p") {
		gc.TopP = genai.Ptr(float32(*s.TopP))
	}
	if s.MaxTokens != nil && use("max_tokens") {
		gc.MaxOutputTokens = int32(*s.MaxTokens)
	}
	if s.Seed != nil && use("seed") {
		gc.Seed = genai.Ptr(int32(*s.Seed))
	}
	if len(dropped) > 0 {
		slog.DebugContext(ctx, "model settings not supported by this model; not sent", "model", m.inner.Name(), "settings", dropped)
	}
	cp.Config = &gc
	return &cp
}

// SettingSupported reports whether a model of provider accepts a
// [model_settings] key. Unsupported settings are left out of requests.
func SettingSupported(provider, modelName, key string) bool {
	switch provider {
	case "openai", "ollama":
		return key != "seed" // the Responses API has no seed
	case "anthropic":
		switch key {
		case "max_tokens":
			return true
		case "temperature":
			return supportsSampling(modelName)
		}
		return false // no seed; top_p isn't mapped (some models reject it with temperature)
	}
	return true
}
