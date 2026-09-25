package runtime

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/breaker"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// scripted answers with text unless failing; midStream fails after one
// partial response.
type scripted struct {
	name      string
	mu        sync.Mutex
	failing   bool
	midStream bool
	calls     int
}

func (s *scripted) Name() string { return s.name }
func (s *scripted) set(failing bool) {
	s.mu.Lock()
	s.failing = failing
	s.mu.Unlock()
}
func (s *scripted) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}
func (s *scripted) GenerateContent(ctx context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		s.mu.Lock()
		s.calls++
		failing, mid := s.failing, s.midStream
		s.mu.Unlock()
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}
		if mid {
			if !yield(&model.LLMResponse{Partial: true, Content: genai.NewContentFromText("par", genai.RoleModel)}, nil) {
				return
			}
			yield(nil, errors.New("stream reset"))
			return
		}
		if failing {
			yield(nil, errors.New(s.name+" is overloaded (529)"))
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("from "+s.name, genai.RoleModel)}, nil)
	}
}

func call(t *testing.T, m model.LLM, ctx context.Context) (text string, served, from string, err error) {
	t.Helper()
	for resp, e := range m.GenerateContent(ctx, &model.LLMRequest{}, false) {
		if e != nil {
			return text, served, from, e
		}
		if resp.Content != nil {
			text += resp.Content.Parts[0].Text
		}
		served = resp.ModelVersion
		from, _ = resp.CustomMetadata[FallbackFromKey].(string)
	}
	return
}

func chainOf(t *testing.T, ms ...*scripted) (*fallbackModel, *fakeClock) {
	t.Helper()
	var chain []model.LLM
	for _, m := range ms {
		chain = append(chain, m)
	}
	f := newFallbackModel(chain).(*fallbackModel)
	clk := &fakeClock{t: time.Unix(0, 0)}
	for _, h := range f.health {
		h.SetClock(clk.now)
	}
	return f, clk
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestFallbackTakesOverThenPrimaryReturns(t *testing.T) {
	primary, backup := &scripted{name: "gemini-x", failing: true}, &scripted{name: "claude-y"}
	f, clk := chainOf(t, primary, backup)
	ctx := context.Background()

	text, served, from, err := call(t, f, ctx)
	if err != nil || text != "from claude-y" || served != "claude-y" || from != "gemini-x" {
		t.Fatalf("first call: %q served=%q from=%q err=%v", text, served, from, err)
	}
	// The primary is paused: the next call goes straight to the backup.
	if _, _, _, err := call(t, f, ctx); err != nil || primary.count() != 1 {
		t.Fatalf("primary retried during its cooldown (%d calls), err %v", primary.count(), err)
	}
	// After the cooldown the primary gets a trial and takes over again.
	primary.set(false)
	clk.advance(breaker.InitialCooldown)
	text, served, from, _ = call(t, f, ctx)
	if text != "from gemini-x" || from != "" || primary.count() != 2 {
		t.Fatalf("primary not back: %q from=%q calls=%d", text, from, primary.count())
	}
	if f.Name() != "gemini-x" || strings.Join(f.Models(), ",") != "gemini-x,claude-y" {
		t.Fatalf("name %q models %v", f.Name(), f.Models())
	}
}

func TestNoFallbackAfterOutputOrOnCancel(t *testing.T) {
	primary, backup := &scripted{name: "p", midStream: true}, &scripted{name: "b"}
	f, _ := chainOf(t, primary, backup)
	if _, _, _, err := call(t, f, context.Background()); err == nil || !strings.Contains(err.Error(), "stream reset") {
		t.Fatalf("mid-stream failure: %v", err)
	}
	if backup.count() != 0 {
		t.Fatal("fell back after part of the answer was out")
	}

	primary2, backup2 := &scripted{name: "p"}, &scripted{name: "b"}
	f2, _ := chainOf(t, primary2, backup2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := call(t, f2, ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call: %v", err)
	}
	if backup2.count() != 0 {
		t.Fatal("fell back on cancellation")
	}
	if ok, _ := f2.health[0].Allow(); !ok {
		t.Fatal("cancellation counted against the primary")
	}
}

func TestAllModelsFailing(t *testing.T) {
	a, b := &scripted{name: "a", failing: true}, &scripted{name: "b", failing: true}
	f, _ := chainOf(t, a, b)
	_, _, _, err := call(t, f, context.Background())
	if err == nil || !strings.Contains(err.Error(), "every model failed") || !strings.Contains(err.Error(), "a is overloaded") || !strings.Contains(err.Error(), "b is overloaded") {
		t.Fatalf("err = %v", err)
	}
	// Every breaker is open now; the next call still tries (all of them)
	// instead of failing without an attempt.
	b.set(false)
	if text, _, _, err := call(t, f, context.Background()); err != nil || text != "from b" {
		t.Fatalf("with every breaker open: %q %v", text, err)
	}
}

// A half-open trial cancelled midway must not leave the model skipped
// for good.
func TestCancelledTrialIsReleased(t *testing.T) {
	primary, backup := &scripted{name: "p", failing: true}, &scripted{name: "b"}
	f, clk := chainOf(t, primary, backup)
	call(t, f, context.Background()) // opens the primary
	clk.advance(breaker.InitialCooldown)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	call(t, f, ctx) // trial cancelled
	primary.set(false)
	if text, _, _, _ := call(t, f, context.Background()); text != "from p" {
		t.Fatalf("primary still skipped after an abandoned trial: %q", text)
	}
}

func TestParseModelRef(t *testing.T) {
	cases := []struct{ ref, def, p, n string }{
		{"anthropic/claude-sonnet-5", "gemini", "anthropic", "claude-sonnet-5"},
		{"Gemini/gemini-2.5-flash", "openai", "gemini", "gemini-2.5-flash"},
		{"gemini-2.5-flash-lite", "gemini", "gemini", "gemini-2.5-flash-lite"},
		{"meta-llama/llama-4", "openai", "openai", "meta-llama/llama-4"},        // not a provider prefix
		{"openai/anthropic/claude-3", "openai", "openai", "anthropic/claude-3"}, // OpenRouter, explicit
		{"ollama/qwen2.5-coder:7b", "gemini", "ollama", "qwen2.5-coder:7b"},
		{"anthropic/", "gemini", "gemini", "anthropic/"},
	}
	for _, c := range cases {
		if p, n := ParseModelRef(c.ref, c.def); p != c.p || n != c.n {
			t.Errorf("%q: (%s, %s), want (%s, %s)", c.ref, p, n, c.p, c.n)
		}
	}
}

func TestOllamaDoesNotUseOpenAIDefaults(t *testing.T) {
	d := config.DefaultConfig().LLM.OpenAI
	d.APIKey = "sk-openai-secret"
	key, url := openAICompatEndpoint(d, "ollama")
	if url != defaultOllamaBaseURL || key != "ollama" {
		t.Fatalf("ollama with default config: key %q url %q", key, url)
	}
	d.BaseURL = "http://gpu-box:11434/v1"
	if _, url := openAICompatEndpoint(d, "ollama"); url != "http://gpu-box:11434/v1" {
		t.Fatalf("configured base_url ignored: %q", url)
	}
	if key, url := openAICompatEndpoint(config.DefaultConfig().LLM.OpenAI, "openai"); url != defaultOpenAIBaseURL || key == "" {
		t.Fatalf("openai: %q %q", key, url)
	}
}

// Across providers, through the real factory: the primary (OpenAI API)
// keeps failing, the Anthropic fallback answers.
func TestNewModelBuildsACrossProviderChain(t *testing.T) {
	oai, oaiCalls := flaky(t, 100, 500, openAIOK)
	ant, _ := flaky(t, 0, 200, message("end_turn", `{"type":"text","text":"ok"}`))
	cfg := config.DefaultConfig()
	cfg.LLM.Provider, cfg.LLM.OpenAI.BaseURL, cfg.LLM.OpenAI.APIKey = "openai", oai.URL+"/v1", "sk-test"
	cfg.LLM.Anthropic.BaseURL, cfg.LLM.Anthropic.APIKey, cfg.LLM.Anthropic.Fallbacks = ant.URL, "sk-ant-test", "off"
	cfg.LLM.MaxRetries = 0
	cfg.LLM.FallbackModels = []string{"anthropic/claude-sonnet-5", "nosuchprovider/x"}

	m, err := NewModel(context.Background(), cfg, "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	fm, ok := m.(*fallbackModel)
	if !ok || strings.Join(fm.Models(), ",") != "gpt-test,claude-sonnet-5,nosuchprovider/x" {
		t.Fatalf("chain = %T %v", m, fm)
	}
	text, err := generateText(t, m)
	if err != nil || text != "ok" || oaiCalls.Load() != 1 {
		t.Fatalf("got %q, %v after %d primary calls", text, err, oaiCalls.Load())
	}
}

func TestEngineNoticesFallbackOnceAndRecovery(t *testing.T) {
	primary, backup := &scripted{name: "gemini-2.5-flash", failing: true}, &scripted{name: "claude-sonnet-5"}
	var notices []string
	f := newEngineWith(t, fixtureOpts{opts: []Option{WithNotice(func(s string) { notices = append(notices, s) })}})
	chain, clk := chainOf(t, primary, backup)
	if err := f.eng.SetModel(context.Background(), chain); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := collect(t, f.eng, "s", "hi"); err != nil {
			t.Fatal(err)
		}
	}
	primary.set(false)
	clk.advance(breaker.InitialCooldown)
	if _, err := collect(t, f.eng, "s", "hi"); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 2 || !strings.Contains(notices[0], "claude-sonnet-5") || !strings.Contains(notices[1], "answering again") {
		t.Fatalf("notices = %q", notices)
	}
}

// Breakers are asked just before each attempt: when the primary answers a
// trial, the backup's own trial must not stay reserved (it would then be
// skipped for good once the primary fails again).
func TestUnusedBackupTrialIsNotReserved(t *testing.T) {
	primary, backup := &scripted{name: "p", failing: true}, &scripted{name: "b", failing: true}
	f, clk := chainOf(t, primary, backup)
	call(t, f, context.Background()) // both fail: both open
	clk.advance(breaker.InitialCooldown)
	primary.set(false)
	backup.set(false)
	if text, _, _, _ := call(t, f, context.Background()); text != "from p" {
		t.Fatalf("primary trial: %q", text)
	}
	primary.set(true)
	if text, _, _, err := call(t, f, context.Background()); text != "from b" {
		t.Fatalf("backup not used after the primary failed again: %q %v", text, err)
	}
}
