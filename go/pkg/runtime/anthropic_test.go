package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/retail-cortex/code_puppy/pkg/agents"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/skills"
	"github.com/retail-cortex/code_puppy/pkg/tools"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// fakeAnthropic is a minimal Messages API: it records request bodies and
// headers and replies with the queued responses in order.
type fakeAnthropic struct {
	mu        sync.Mutex
	requests  []map[string]any
	headers   []http.Header
	responses []func(w http.ResponseWriter)
}

func (f *fakeAnthropic) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.headers = append(f.headers, r.Header.Clone())
	var respond func(http.ResponseWriter)
	if n := len(f.requests) - 1; n < len(f.responses) {
		respond = f.responses[n]
	}
	f.mu.Unlock()
	if respond == nil {
		http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"no more responses"}}`, 400)
		return
	}
	respond(w)
}

func jsonReply(body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}
}

func newFake(t *testing.T, responses ...func(http.ResponseWriter)) (*fakeAnthropic, []option.RequestOption) {
	t.Helper()
	f := &fakeAnthropic{responses: responses}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	return f, []option.RequestOption{option.WithBaseURL(srv.URL), option.WithMaxRetries(0), option.WithAPIKey("sk-ant-test"), option.WithRequestTimeout(10 * time.Second)}
}

func message(stop string, content string) string {
	return fmt.Sprintf(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[%s],
		"stop_reason":%q,"stop_sequence":null,
		"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":400,"cache_creation_input_tokens":50}}`, content, stop)
}

func userText(s string) *genai.Content { return genai.NewContentFromText(s, genai.RoleUser) }

func collectResponses(t *testing.T, m model.LLM, req *model.LLMRequest, stream bool) ([]*model.LLMResponse, error) {
	t.Helper()
	var out []*model.LLMResponse
	for r, err := range m.GenerateContent(context.Background(), req, stream) {
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

func TestConvertContents(t *testing.T) {
	contents := []*genai.Content{
		userText("list the files"),
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{Text: "reasoning", Thought: true, ThoughtSignature: []byte("sig-1")},
			{Thought: true, ThoughtSignature: []byte(redactedPrefix + "opaque")},
			{Text: "unsigned thought", Thought: true}, // dropped
			{Text: "Let me look."},
			{FunctionCall: &genai.FunctionCall{ID: "toolu_a", Name: "list_files", Args: map[string]any{"directory": "."}}},
			{FunctionCall: &genai.FunctionCall{ID: "toolu_b", Name: "grep"}},
		}},
		// ADK sends parallel results as separate user contents; they must merge.
		{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "toolu_a", Name: "list_files", Response: map[string]any{"files": []any{"a.go"}}}}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "toolu_b", Name: "grep", Response: map[string]any{"error": "bad regex"}}}}},
	}
	msgs, err := convertContents(contents)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(msgs)
	got := string(raw)
	if len(msgs) != 3 {
		t.Fatalf("expected user/assistant/user, got %d messages: %s", len(msgs), got)
	}
	for _, want := range []string{
		`{"signature":"sig-1","thinking":"reasoning","type":"thinking"}`,
		`{"data":"opaque","type":"redacted_thinking"}`,
		`{"id":"toolu_a","input":{"directory":"."},"name":"list_files","type":"tool_use"}`,
		`"input":{},"name":"grep"`,
		`"tool_use_id":"toolu_a"`,
		`"is_error":true`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	if strings.Contains(got, "unsigned thought") {
		t.Error("unsigned thinking must be dropped")
	}
	if n := len(msgs[2].Content); n != 2 {
		t.Errorf("parallel tool results should share one user message, got %d blocks", n)
	}

	// Negative: a conversation must begin with the user.
	if _, err := convertContents([]*genai.Content{{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "hi"}}}}); err == nil {
		t.Error("expected error for assistant-first conversation")
	}
}

func TestConvertToolsSchemas(t *testing.T) {
	tools := []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{
		{Name: "json_schema", Description: "raw schema", ParametersJsonSchema: map[string]any{
			"type": "object", "$schema": "https://json-schema.org/draft/2020-12/schema",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []string{"path"}, "additionalProperties": false,
		}},
		{Name: "genai_schema", Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{
			"n":    {Type: genai.TypeInteger, Description: "count"},
			"tags": {Type: genai.TypeArray, Items: &genai.Schema{Type: genai.TypeString}},
		}, Required: []string{"n"}}},
		{Name: "no_params"},
	}}}
	out, err := convertTools(tools)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	got := string(raw)
	for _, want := range []string{
		`"name":"json_schema"`, `"description":"raw schema"`, `"required":["path"]`, `"additionalProperties":false`,
		`"n":{"description":"count","type":"integer"}`, `"items":{"type":"string"}`, `"type":"array"`,
		`"name":"no_params"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	if strings.Contains(got, "INTEGER") || strings.Contains(got, "$schema") {
		t.Errorf("schema not normalised: %s", got)
	}
}

func TestAnthropicRequestShape(t *testing.T) {
	temp := float32(0.2)
	req := func(name string) *model.LLMRequest {
		return &model.LLMRequest{
			Model:    name,
			Contents: []*genai.Content{userText("hi")},
			Config: &genai.GenerateContentConfig{
				SystemInstruction: genai.NewContentFromText("You are a puppy.", genai.RoleUser),
				Temperature:       &temp,
				MaxOutputTokens:   4096,
			},
		}
	}
	f, opts := newFake(t, jsonReply(message("end_turn", `{"type":"text","text":"a"}`)), jsonReply(message("end_turn", `{"type":"text","text":"b"}`)), jsonReply(message("end_turn", `{"type":"text","text":"c"}`)))
	m := newAnthropicModel(config.AnthropicConfig{APIKey: "sk-ant-test"}, "claude-opus-5", opts...)

	if _, err := collectResponses(t, m, req("claude-opus-5"), false); err != nil {
		t.Fatal(err)
	}
	r := f.requests[0]
	if r["model"] != "claude-opus-5" || r["max_tokens"] != float64(4096) {
		t.Errorf("model/max_tokens: %v", r)
	}
	if _, ok := r["temperature"]; ok {
		t.Error("temperature must not be sent to claude-opus-5 (400 on current models)")
	}
	sys, _ := json.Marshal(r["system"])
	if !strings.Contains(string(sys), `"cache_control":{"type":"ephemeral"}`) || !strings.Contains(string(sys), "You are a puppy.") {
		t.Errorf("system not cached: %s", sys)
	}
	if r["fallbacks"] != "default" || !strings.Contains(f.headers[0].Get("Anthropic-Beta"), "server-side-fallback-2026-07-01") {
		t.Errorf("default fallbacks not enabled: %v / %q", r["fallbacks"], f.headers[0].Get("Anthropic-Beta"))
	}
	if f.headers[0].Get("X-Api-Key") != "sk-ant-test" {
		t.Error("api key header missing")
	}

	// Older model: temperature allowed, no fallbacks.
	collectResponses(t, m, req("claude-haiku-4-5"), false)
	if f.requests[1]["temperature"] == nil || f.requests[1]["fallbacks"] != nil {
		t.Errorf("haiku request: %v", f.requests[1])
	}

	// Fallbacks off.
	off := newAnthropicModel(config.AnthropicConfig{Fallbacks: "off"}, "claude-opus-5", opts...)
	collectResponses(t, off, req("claude-opus-5"), false)
	if f.requests[2]["fallbacks"] != nil {
		t.Error("fallbacks should be omitted when off")
	}
}

func TestAnthropicResponseConversion(t *testing.T) {
	_, opts := newFake(t,
		jsonReply(message("tool_use", `{"type":"thinking","thinking":"hmm","signature":"sig-9"},{"type":"text","text":"Checking."},{"type":"tool_use","id":"toolu_1","name":"list_files","input":{"recursive":true}}`)),
		jsonReply(message("refusal", `{"type":"text","text":"I can't"}`)),
		jsonReply(message("max_tokens", `{"type":"text","text":"cut"}`)),
	)
	m := newAnthropicModel(config.AnthropicConfig{}, "claude-opus-5", opts...)
	req := &model.LLMRequest{Contents: []*genai.Content{userText("go")}}

	out, err := collectResponses(t, m, req, false)
	if err != nil || len(out) != 1 {
		t.Fatalf("%v %v", out, err)
	}
	r := out[0]
	parts := r.Content.Parts
	if len(parts) != 3 || !parts[0].Thought || string(parts[0].ThoughtSignature) != "sig-9" || parts[1].Text != "Checking." {
		t.Fatalf("parts %+v", parts)
	}
	fc := parts[2].FunctionCall
	if fc == nil || fc.ID != "toolu_1" || fc.Name != "list_files" || fc.Args["recursive"] != true {
		t.Errorf("function call %+v", fc)
	}
	u := r.UsageMetadata
	if u.PromptTokenCount != 550 || u.CachedContentTokenCount != 400 || u.CandidatesTokenCount != 20 || r.FinishReason != genai.FinishReasonStop {
		t.Errorf("usage/finish %+v %v", u, r.FinishReason)
	}

	out, _ = collectResponses(t, m, req, false)
	if out[0].FinishReason != genai.FinishReasonSafety || !strings.Contains(out[0].Content.Parts[len(out[0].Content.Parts)-1].Text, "declined") {
		t.Errorf("refusal not surfaced: %+v", out[0])
	}
	out, _ = collectResponses(t, m, req, false)
	if out[0].FinishReason != genai.FinishReasonMaxTokens {
		t.Errorf("max_tokens finish: %v", out[0].FinishReason)
	}
}

func sseReply(events ...string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			var m map[string]any
			json.Unmarshal([]byte(e), &m)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m["type"], e)
		}
	}
}

func TestAnthropicStreaming(t *testing.T) {
	_, opts := newFake(t, sseReply(
		`{"type":"message_start","message":{"id":"msg_s","type":"message","role":"assistant","model":"claude-opus-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_s","name":"grep","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"TODO\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":42}}`,
		`{"type":"message_stop"}`,
	))
	m := newAnthropicModel(config.AnthropicConfig{}, "claude-opus-5", opts...)
	out, err := collectResponses(t, m, &model.LLMRequest{Contents: []*genai.Content{userText("go")}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || !out[0].Partial || !out[1].Partial || out[2].Partial {
		t.Fatalf("expected 2 partials + final, got %d: %+v", len(out), out)
	}
	if out[0].Content.Parts[0].Text+out[1].Content.Parts[0].Text != "Hello" {
		t.Error("partial text wrong")
	}
	final := out[2]
	if final.Content.Parts[0].Text != "Hello" {
		t.Errorf("final text %q", final.Content.Parts[0].Text)
	}
	fc := final.Content.Parts[1].FunctionCall
	if fc == nil || fc.ID != "toolu_s" || fc.Args["query"] != "TODO" {
		t.Errorf("streamed tool call %+v", fc)
	}
	if final.UsageMetadata.CandidatesTokenCount != 42 || final.UsageMetadata.PromptTokenCount != 10 {
		t.Errorf("streamed usage %+v", final.UsageMetadata)
	}
}

func TestAnthropicErrors(t *testing.T) {
	status := func(code int) func(http.ResponseWriter) {
		return func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			io.WriteString(w, `{"type":"error","error":{"type":"x","message":"nope"}}`)
		}
	}
	_, opts := newFake(t, status(401), status(429), status(529))
	m := newAnthropicModel(config.AnthropicConfig{}, "claude-opus-5", opts...)
	req := &model.LLMRequest{Contents: []*genai.Content{userText("x")}}
	for _, want := range []string{"authentication failed", "rate limited", "API error (529)"} {
		_, err := collectResponses(t, m, req, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
	// Empty requests are rejected before any network call.
	if _, err := collectResponses(t, m, &model.LLMRequest{}, false); err == nil {
		t.Error("expected error for empty request")
	}
}

func TestModelNameResolution(t *testing.T) {
	cfg := config.DefaultConfig()
	for provider, want := range map[string]string{"gemini": "gemini-2.5-flash", "anthropic": "claude-opus-5", "openai": "gpt-4o", "ollama": "gpt-4o"} {
		cfg.LLM.Provider = provider
		if got := cfg.ModelName(); got != want {
			t.Errorf("%s: ModelName = %q, want %q", provider, got, want)
		}
	}
	cfg.CodePuppy.DefaultModel = "claude-sonnet-5"
	if cfg.ModelName() != "claude-sonnet-5" {
		t.Error("default_model should override the provider model")
	}

	cfg = config.DefaultConfig()
	cfg.LLM.Provider = "anthropic"
	m, err := NewModel(context.Background(), cfg, "")
	if err != nil || m.Name() != "claude-opus-5" {
		t.Fatalf("NewModel(anthropic) = %v, %v", m, err)
	}
	if m2, _ := NewModel(context.Background(), cfg, "claude-haiku-4-5"); m2.Name() != "claude-haiku-4-5" {
		t.Error("override ignored")
	}
}

// TestAnthropicEngineToolLoop drives a full tool round trip through the ADK
// engine: the second request must carry the assistant's thinking block with
// its signature and the tool_result for the same tool_use ID.
func TestAnthropicEngineToolLoop(t *testing.T) {
	f, opts := newFake(t,
		jsonReply(message("tool_use", `{"type":"thinking","thinking":"need files","signature":"sig-loop"},{"type":"tool_use","id":"toolu_loop","name":"list_files","input":{}}`)),
		jsonReply(message("end_turn", `{"type":"text","text":"Found them."}`)),
	)
	cfg := config.DefaultConfig()
	cfg.LLM.Provider = "anthropic"
	cfg.Tools.WorkspaceDir = t.TempDir()
	cfg.Tools.ApprovalsFile = filepath.Join(t.TempDir(), "a.json")
	agentReg, _ := agents.NewRegistry()
	skillProv, _ := skills.NewProvider()
	reg, err := tools.NewRegistry(cfg, agentReg, skillProv)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	llm := newAnthropicModel(cfg.LLM.Anthropic, "claude-opus-5", opts...)
	eng, err := NewEngine(context.Background(), cfg, agentReg, skillProv, reg, llm)
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	err = eng.Execute(context.Background(), "s", "list files", func(ev *session.Event) error {
		if ev.Content != nil && !ev.Partial {
			for _, p := range ev.Content.Parts {
				if !p.Thought {
					text.WriteString(p.Text)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "Found them.") {
		t.Errorf("final text %q", text.String())
	}
	if len(f.requests) != 2 {
		t.Fatalf("expected 2 API calls, got %d", len(f.requests))
	}
	second, _ := json.Marshal(f.requests[1]["messages"])
	for _, want := range []string{`"signature":"sig-loop"`, `"id":"toolu_loop"`, `"tool_use_id":"toolu_loop"`} {
		if !strings.Contains(string(second), want) {
			t.Errorf("second request missing %s:\n%s", want, second)
		}
	}
	tools, _ := json.Marshal(f.requests[0]["tools"])
	if !strings.Contains(string(tools), `"name":"list_files"`) || !strings.Contains(string(tools), `"input_schema"`) {
		t.Errorf("tools not sent: %s", tools)
	}
	if u := eng.Usage("s"); u.Calls != 2 || u.Cached != 800 || u.CacheWrite != 100 || !u.Priced {
		t.Errorf("usage not recorded/priced: %+v", u)
	}
}
