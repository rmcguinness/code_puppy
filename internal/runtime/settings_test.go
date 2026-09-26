package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// recorder keeps each request's generation config; failing makes it fail
// before answering (so a fallback takes over).
type recorder struct {
	name    string
	failing bool
	mu      sync.Mutex
	configs []*genai.GenerateContentConfig
}

func (r *recorder) Name() string { return r.name }
func (r *recorder) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		r.mu.Lock()
		r.configs = append(r.configs, req.Config)
		r.mu.Unlock()
		if r.failing {
			yield(nil, errors.New(r.name+" is down"))
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("from "+r.name, genai.RoleModel)}, nil)
	}
}

func (r *recorder) last(t *testing.T) *genai.GenerateContentConfig {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.configs) == 0 || r.configs[len(r.configs)-1] == nil {
		t.Fatalf("%s got no config", r.name)
	}
	return r.configs[len(r.configs)-1]
}

func ptr[T any](v T) *T { return &v }

func lookupOf(m map[string]config.ModelSettings) settingsLookup {
	return func(name string) (config.ModelSettings, bool) { s, ok := m[name]; return s, ok }
}

func TestEachFallbackModelGetsItsOwnSettings(t *testing.T) {
	primary := &recorder{name: "gemini-3.8-flash", failing: true}
	backup := &recorder{name: "gpt-5"}
	chain := newFallbackModel([]model.LLM{withModelSettings(primary, "gemini"), withModelSettings(backup, "openai")})
	ctx := withSettingsLookup(context.Background(), lookupOf(map[string]config.ModelSettings{
		"gemini-3.8-flash": {Temperature: ptr(0.9), Seed: ptr(7)},
		"gpt-5":            {TopP: ptr(0.5), Seed: ptr(7)}, // OpenAI can't take a seed
	}))
	global := &genai.GenerateContentConfig{Temperature: genai.Ptr[float32](0.2), MaxOutputTokens: 8192}
	for _, err := range chain.GenerateContent(ctx, &model.LLMRequest{Config: global}, false) {
		if err != nil {
			t.Fatal(err)
		}
	}

	p := primary.last(t)
	if *p.Temperature != 0.9 || *p.Seed != 7 || p.MaxOutputTokens != 8192 {
		t.Errorf("primary: temperature %v seed %v max %d", *p.Temperature, *p.Seed, p.MaxOutputTokens)
	}
	b := backup.last(t)
	if *b.Temperature != 0.2 || *b.TopP != 0.5 || b.Seed != nil || b.MaxOutputTokens != 8192 {
		t.Errorf("backup must keep the global temperature, get its top_p and no seed: %+v", b)
	}
	if *global.Temperature != 0.2 || global.Seed != nil || global.TopP != nil {
		t.Errorf("the shared request config was changed: %+v", global)
	}
}

func TestSettingsWithoutLookupOrEntryLeaveTheRequestAlone(t *testing.T) {
	r := &recorder{name: "m"}
	m := withModelSettings(r, "gemini")
	cfg := &genai.GenerateContentConfig{MaxOutputTokens: 5}
	for _, ctx := range []context.Context{
		context.Background(), // e.g. doctor
		withSettingsLookup(context.Background(), lookupOf(map[string]config.ModelSettings{"other": {Seed: ptr(1)}})),
	} {
		for range m.GenerateContent(ctx, &model.LLMRequest{Config: cfg}, false) {
		}
		if r.last(t) != cfg {
			t.Fatal("request was copied or changed without settings for the model")
		}
	}
}

func TestSettingSupported(t *testing.T) {
	cases := []struct {
		provider, model, key string
		want                 bool
	}{
		{"gemini", "gemini-3.8-flash", "seed", true},
		{"gemini", "gemini-3.8-flash", "top_p", true},
		{"openai", "gpt-5", "seed", false},
		{"ollama", "qwen2.5-coder:7b", "temperature", true},
		{"anthropic", "claude-sonnet-5", "temperature", false},
		{"anthropic", "claude-haiku-4-5", "temperature", true},
		{"anthropic", "claude-haiku-4-5", "top_p", false},
		{"anthropic", "claude-sonnet-5", "max_tokens", true},
		{"anthropic", "claude-sonnet-5", "seed", false},
	}
	for _, c := range cases {
		if got := SettingSupported(c.provider, c.model, c.key); got != c.want {
			t.Errorf("%s %s %s = %v", c.provider, c.model, c.key, got)
		}
	}
}

func TestEngineAppliesModelSettingsAndChangesTakeEffect(t *testing.T) {
	rec := &recorder{name: "gemini-3.8-flash"}
	f := newEngineWith(t, fixtureOpts{cfg: func(c *config.Config) {
		c.ModelSettings = map[string]config.ModelSettings{
			"gemini/gemini-3.8-flash": {Temperature: ptr(1.1)}, // a provider prefix is ignored
			"unused":                  {},
		}
	}})
	if err := f.eng.SetModel(context.Background(), withModelSettings(rec, "gemini")); err != nil {
		t.Fatal(err)
	}
	if _, err := collect(t, f.eng, "s", "hi"); err != nil {
		t.Fatal(err)
	}
	if got := rec.last(t); *got.Temperature != 1.1 || got.MaxOutputTokens != int32(f.cfg.CodePuppy.MaxTokens) {
		t.Fatalf("first call: temperature %v, max %d", *got.Temperature, got.MaxOutputTokens)
	}
	if all := f.eng.AllModelSettings(); len(all) != 1 || all["gemini-3.8-flash"].Temperature == nil {
		t.Fatalf("AllModelSettings = %v", all)
	}

	f.eng.SetModelSettings("gemini-3.8-flash", config.ModelSettings{MaxTokens: ptr(100)})
	if _, err := collect(t, f.eng, "s", "again"); err != nil {
		t.Fatal(err)
	}
	if got := rec.last(t); got.MaxOutputTokens != 100 || *got.Temperature != float32(f.cfg.CodePuppy.Temperature) {
		t.Fatalf("after change: max %d temperature %v", got.MaxOutputTokens, *got.Temperature)
	}

	f.eng.SetModelSettings("gemini-3.8-flash", config.ModelSettings{})
	if s := f.eng.ModelSettings("gemini-3.8-flash"); !s.IsZero() {
		t.Fatalf("not removed: %+v", s)
	}
}

func TestSubagentUsesModelSettings(t *testing.T) {
	sub := &recorder{name: "claude-haiku-4-5"}
	f := newEngineWith(t, fixtureOpts{
		cfg: func(c *config.Config) {
			c.ModelSettings = map[string]config.ModelSettings{"claude-haiku-4-5": {Temperature: ptr(0.3)}}
		},
		opts: []Option{WithAgentModel("qa-kitten", withModelSettings(sub, "anthropic"))},
	})
	// Called directly, as a hook would, without a run context.
	if _, err := f.eng.InvokeSubagent(context.Background(), "qa-kitten", "review"); err != nil {
		t.Fatal(err)
	}
	if got := sub.last(t); got.Temperature == nil || *got.Temperature != 0.3 {
		t.Fatalf("sub-agent temperature %v", got.Temperature)
	}
}

// The settings reach the wire through the real OpenAI adapter, and a seed
// (which that adapter rejects) is left out instead of failing the call.
func TestOpenAIRequestCarriesModelSettings(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, openAIOK)
	}))
	t.Cleanup(srv.Close)
	cfg := config.DefaultConfig()
	cfg.LLM.Provider, cfg.LLM.OpenAI.BaseURL, cfg.LLM.OpenAI.APIKey, cfg.LLM.MaxRetries = "openai", srv.URL+"/v1", "sk-test", 0
	m, err := NewModel(context.Background(), cfg, "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := withSettingsLookup(context.Background(), lookupOf(map[string]config.ModelSettings{
		"gpt-test": {Temperature: ptr(0.25), TopP: ptr(0.75), MaxTokens: ptr(321), Seed: ptr(42)},
	}))
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}}
	for _, err := range m.GenerateContent(ctx, req, false) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if body["temperature"] != 0.25 || body["top_p"] != 0.75 || body["max_output_tokens"] != 321.0 {
		t.Fatalf("request body: temperature %v top_p %v max_output_tokens %v", body["temperature"], body["top_p"], body["max_output_tokens"])
	}
	if _, ok := body["seed"]; ok {
		t.Fatal("seed was sent")
	}
}

// When both a bare and a "provider/" key name the same model, the bare one
// wins, whatever the map order.
func TestBareModelSettingsKeyWins(t *testing.T) {
	for range 20 {
		f := newEngineWith(t, fixtureOpts{cfg: func(c *config.Config) {
			c.ModelSettings = map[string]config.ModelSettings{
				"openai/gpt-5": {Seed: ptr(1)},
				"gpt-5":        {Seed: ptr(2)},
			}
		}})
		if s := f.eng.ModelSettings("gpt-5"); s.Seed == nil || *s.Seed != 2 {
			t.Fatalf("seed %v", s.Seed)
		}
	}
}
