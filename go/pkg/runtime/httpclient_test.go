package runtime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/config"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/genai"
)

func TestStalledBodyFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: partial\n")
		w.(http.Flusher).Flush()
		select { // then go quiet
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	client := retryPolicy{stall: 200 * time.Millisecond}.httpClient()
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	start := time.Now()
	_, err = io.ReadAll(resp.Body)
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("want ErrStalled, got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("stall detected after %v", d)
	}
}

func TestSlowButSteadyStreamIsNotCutOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for range 8 { // 800 ms in total, never quiet for 300 ms
			io.WriteString(w, "x")
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer srv.Close()

	resp, err := retryPolicy{stall: 300 * time.Millisecond}.httpClient().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if b, err := io.ReadAll(resp.Body); err != nil || string(b) != "xxxxxxxx" {
		t.Fatalf("got %q, %v", b, err)
	}
}

func TestNoHeadersFailsFast(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	start := time.Now()
	if _, err := (retryPolicy{stall: 200 * time.Millisecond}).httpClient().Get(srv.URL); err == nil {
		t.Fatal("request without headers succeeded")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("header timeout took %v", d)
	}
}

// flaky answers the first `fail` requests with status, then with ok.
func flaky(t *testing.T, fail int, status int, ok string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		if int(calls.Add(1)) <= fail {
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"message":"try again"}}`)
			return
		}
		io.WriteString(w, ok)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func generateText(t *testing.T, m model.LLM) (string, error) {
	t.Helper()
	req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}}
	var text string
	for resp, err := range m.GenerateContent(context.Background(), req, false) {
		if err != nil {
			return text, err
		}
		if resp.Content != nil {
			for _, p := range resp.Content.Parts {
				text += p.Text
			}
		}
	}
	return text, nil
}

const openAIOK = `{"id":"resp_1","model":"gpt-test","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"input_tokens_details":{"cached_tokens":0},"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":2}}`

const geminiOK = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`

func TestProvidersRetryTransientErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		ok     string
		build  func(t *testing.T, url string, retries int) model.LLM
	}{
		{"anthropic 529 overloaded", 529, message("end_turn", `{"type":"text","text":"ok"}`), func(t *testing.T, url string, retries int) model.LLM {
			cfg := config.DefaultConfig()
			cfg.LLM.Provider, cfg.LLM.Anthropic.BaseURL, cfg.LLM.Anthropic.APIKey, cfg.LLM.MaxRetries = "anthropic", url, "sk-ant-test", retries
			cfg.LLM.Anthropic.Fallbacks = "off"
			m, err := NewModel(context.Background(), cfg, "")
			if err != nil {
				t.Fatal(err)
			}
			return m
		}},
		{"openai 503", 503, openAIOK, func(t *testing.T, url string, retries int) model.LLM {
			cfg := config.DefaultConfig()
			cfg.LLM.Provider, cfg.LLM.OpenAI.BaseURL, cfg.LLM.OpenAI.APIKey, cfg.LLM.MaxRetries = "openai", url+"/v1", "sk-test", retries
			m, err := NewModel(context.Background(), cfg, "gpt-test")
			if err != nil {
				t.Fatal(err)
			}
			return m
		}},
		{"gemini 429", 429, geminiOK, func(t *testing.T, url string, retries int) model.LLM {
			old := geminiInitialDelay
			geminiInitialDelay = 0.01
			t.Cleanup(func() { geminiInitialDelay = old })
			gc := retryPolicy{maxRetries: retries, stall: time.Minute}.geminiConfig("test-key")
			gc.Backend = genai.BackendGeminiAPI
			gc.HTTPOptions.BaseURL = url
			m, err := gemini.NewModel(context.Background(), "gemini-2.5-flash", gc)
			if err != nil {
				t.Fatal(err)
			}
			return m
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, calls := flaky(t, 2, c.status, c.ok)
			text, err := generateText(t, c.build(t, srv.URL, 2))
			if err != nil || text != "ok" {
				t.Fatalf("with 2 retries: %q, %v", text, err)
			}
			if n := calls.Load(); n != 3 {
				t.Fatalf("want 3 attempts, got %d", n)
			}

			srv, calls = flaky(t, 1, c.status, c.ok)
			if _, err := generateText(t, c.build(t, srv.URL, 0)); err == nil {
				t.Fatal("max_retries = 0 still retried")
			}
			if n := calls.Load(); n != 1 {
				t.Fatalf("max_retries = 0: want 1 attempt, got %d", n)
			}
		})
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	srv, calls := flaky(t, 5, http.StatusBadRequest, openAIOK)
	cfg := config.DefaultConfig()
	cfg.LLM.Provider, cfg.LLM.OpenAI.BaseURL, cfg.LLM.OpenAI.APIKey = "openai", srv.URL+"/v1", "sk-test"
	m, err := NewModel(context.Background(), cfg, "gpt-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := generateText(t, m); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("want a 400 error, got %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("a 400 was retried: %d attempts", n)
	}
}

func TestPolicyDefaults(t *testing.T) {
	p := policyFrom(config.LLMConfig{MaxRetries: -1})
	if p.maxRetries != 0 || p.stall != defaultStallTimeout {
		t.Fatalf("policy = %+v", p)
	}
	if d := config.DefaultConfig().LLM; d.MaxRetries != 3 || d.StallTimeoutSeconds != 600 {
		t.Fatalf("config defaults = %d retries, %ds stall", d.MaxRetries, d.StallTimeoutSeconds)
	}
}
