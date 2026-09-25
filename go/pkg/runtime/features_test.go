package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/pkg/agents"
	"github.com/retail-cortex/code_puppy/pkg/audit"
	"github.com/retail-cortex/code_puppy/pkg/config"
	cpsession "github.com/retail-cortex/code_puppy/pkg/session"
	"github.com/retail-cortex/code_puppy/pkg/skills"
	"github.com/retail-cortex/code_puppy/pkg/tools"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestUsageEstimate(t *testing.T) {
	tr := NewUsageTracker(map[string]config.ModelPrice{"gemini-2.5-flash": {InputPerMTok: 1, OutputPerMTok: 10, CachedInputPerMTok: 0.1}})
	m := &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 1_000_000, CachedContentTokenCount: 500_000, CandidatesTokenCount: 100_000, ThoughtsTokenCount: 100_000}

	u := tr.Estimate("gemini-2.5-flash-001", m, 0) // prefix match
	want := 0.5*1 + 0.5*0.1 + 0.2*10
	if !u.Priced || abs(u.CostUSD-want) > 1e-9 || u.Output != 200_000 || u.LastPrompt != 1_000_000 {
		t.Errorf("estimate %+v, want cost %v", u, want)
	}
	if u := tr.Estimate("unknown-model", m, 0); u.Priced || u.CostUSD != 0 {
		t.Errorf("unknown model should be unpriced: %+v", u)
	}

	tr.Record("s", "gemini-2.5-flash", m)
	tr.Record("s", "gemini-2.5-flash", &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10})
	s := tr.Session("s")
	if s.Calls != 2 || s.LastPrompt != 10 || s.Input != 1_000_010 {
		t.Errorf("accumulated %+v", s)
	}
	tr.Record("s", "mystery", m)
	if tr.Session("s").Priced {
		t.Error("session with an unpriced call must be marked unpriced")
	}
	if z := tr.Session("none"); z.Calls != 0 || !z.Priced {
		t.Errorf("empty session %+v", z)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

type fixtureOpts struct {
	cfg  func(*config.Config)
	opts []Option
}

func newEngineWith(t *testing.T, fo fixtureOpts, responses ...*genai.Content) engineFixture {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = t.TempDir()
	cfg.Tools.UCToolsDir = t.TempDir()
	cfg.Images.Dir = t.TempDir()
	cfg.Tools.ApprovalsFile = filepath.Join(t.TempDir(), "approvals.json")
	cfg.CodePuppy.AutoApprove = true
	if fo.cfg != nil {
		fo.cfg(cfg)
	}
	agentReg, _ := agents.NewRegistry()
	skillProv, _ := skills.NewProvider()
	toolReg, err := tools.NewRegistry(cfg, agentReg, skillProv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { toolReg.Close() })
	llm := NewMockLLM("gemini-2.5-flash", responses...)
	eng, err := NewEngine(context.Background(), cfg, agentReg, skillProv, toolReg, llm, fo.opts...)
	if err != nil {
		t.Fatal(err)
	}
	return engineFixture{eng: eng, llm: llm, tools: toolReg, cfg: cfg}
}

func toolCall(name string, args map[string]any) *genai.Content {
	return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: name, Args: args}}}}
}

func functionResponses(t *testing.T, eng *Engine, sid, prompt string, opts ...ExecOption) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	err := eng.Execute(context.Background(), sid, prompt, func(ev *session.Event) error {
		if ev.Content != nil {
			for _, p := range ev.Content.Parts {
				if p.FunctionResponse != nil {
					out[p.FunctionResponse.Name] = p.FunctionResponse.Response
				}
			}
		}
		return nil
	}, opts...)
	return out, err
}

func TestEngineRecordsUsage(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{}, textContent("hi"))
	f.llm.Usage = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 1000, CandidatesTokenCount: 200}
	if _, err := collect(t, f.eng, "s1", "hello"); err != nil {
		t.Fatal(err)
	}
	u := f.eng.Usage("s1")
	if u.Calls != 1 || u.Input != 1000 || u.Output != 200 || !u.Priced || u.CostUSD <= 0 {
		t.Errorf("usage %+v", u)
	}
	if f.eng.Usage("other").Calls != 0 {
		t.Error("usage leaked across sessions")
	}
}

func TestEngineMaxTurns(t *testing.T) {
	loop := []*genai.Content{}
	for i := 0; i < 6; i++ {
		loop = append(loop, toolCall("list_files", map[string]any{}))
	}
	f := newEngineWith(t, fixtureOpts{}, loop...)
	_, err := functionResponses(t, f.eng, "s", "loop forever", WithMaxTurns(2))
	if !errors.Is(err, ErrMaxTurns) {
		t.Fatalf("expected ErrMaxTurns, got %v", err)
	}
	if f.llm.Calls() != 2 {
		t.Errorf("expected exactly 2 model calls, got %d", f.llm.Calls())
	}
	// Positive: without a limit the run finishes.
	g := newEngineWith(t, fixtureOpts{}, toolCall("list_files", map[string]any{}), textContent("done"))
	if _, err := functionResponses(t, g.eng, "s", "once", WithMaxTurns(5)); err != nil {
		t.Errorf("run within limit failed: %v", err)
	}
}

func TestEngineResumesPersistedSession(t *testing.T) {
	dir := t.TempDir()
	svc1, _ := cpsession.NewPersistentService(dir)
	f1 := newEngineWith(t, fixtureOpts{opts: []Option{WithSessionService(svc1)}}, textContent("noted"))
	if _, err := collect(t, f1.eng, "resume-me", "the secret word is pineapple"); err != nil {
		t.Fatal(err)
	}

	// A brand-new engine (new process) with the same store and session ID.
	svc2, _ := cpsession.NewPersistentService(dir)
	f2 := newEngineWith(t, fixtureOpts{opts: []Option{WithSessionService(svc2)}}, textContent("pineapple"))
	if _, err := collect(t, f2.eng, "resume-me", "what was the word?"); err != nil {
		t.Fatal(err)
	}
	var history strings.Builder
	for _, c := range f2.llm.Requests[0].Contents {
		for _, p := range c.Parts {
			history.WriteString(p.Text + "|")
		}
	}
	if !strings.Contains(history.String(), "pineapple") || !strings.Contains(history.String(), "noted") {
		t.Errorf("resumed session lost history: %s", history.String())
	}
}

func TestEngineInstructions(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{opts: []Option{WithInstructions("\nPROJECT RULE: use tabs")}}, textContent("ok"))
	collect(t, f.eng, "s", "hi")
	sys := systemText(f.llm)
	if !strings.Contains(sys, "PROJECT RULE: use tabs") {
		t.Errorf("instructions missing from system prompt")
	}
	f.eng.SetInstructions(context.Background(), "\nNEW RULE")
	collect(t, f.eng, "s2", "hi")
	if sys := systemText(f.llm); !strings.Contains(sys, "NEW RULE") || strings.Contains(sys, "PROJECT RULE") {
		t.Errorf("SetInstructions not applied")
	}
}

func systemText(m *MockLLM) string {
	req := m.Requests[len(m.Requests)-1]
	var sb strings.Builder
	if req.Config != nil && req.Config.SystemInstruction != nil {
		for _, p := range req.Config.SystemInstruction.Parts {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

func TestEngineAuditsToolCalls(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{}, toolCall("list_files", map[string]any{}), textContent("done"))
	dir := t.TempDir()
	log, _ := audit.Open(dir, nil)
	f.tools.SetAudit(log)
	if _, err := functionResponses(t, f.eng, "s", "list"); err != nil {
		t.Fatal(err)
	}
	log.Close()
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("audit files %v", files)
	}
	b, _ := os.ReadFile(files[0])
	for _, want := range []string{`"kind":"tool_call"`, `"kind":"tool_result"`, `"tool":"list_files"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("audit log missing %s:\n%s", want, b)
		}
	}
}

func TestEnginePreToolHookBlocks(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{cfg: func(c *config.Config) {
		c.Hooks.PreTool = []config.HookConfig{{Match: "list_*", Command: `echo "listing is forbidden" >&2; exit 2`}}
	}}, toolCall("list_files", map[string]any{}), toolCall("read_file", map[string]any{"path": "nope"}), textContent("done"))

	resps, err := functionResponses(t, f.eng, "s", "go")
	if err != nil {
		t.Fatal(err)
	}
	if msg, _ := resps["list_files"]["error"].(string); !strings.Contains(msg, "listing is forbidden") {
		t.Errorf("hook did not block list_files: %v", resps["list_files"])
	}
	// Non-matching tools run normally (read_file fails on its own, not via the hook).
	if msg, _ := resps["read_file"]["error"].(string); strings.Contains(msg, "hook") {
		t.Errorf("hook applied to non-matching tool: %v", msg)
	}
}

func TestUsageCacheWrites(t *testing.T) {
	tr := NewUsageTracker(map[string]config.ModelPrice{
		"claude-opus-5": {InputPerMTok: 5, OutputPerMTok: 25, CachedInputPerMTok: 0.5, CacheWritePerMTok: 6.25},
		"no-write-rate": {InputPerMTok: 1, OutputPerMTok: 2},
	})
	// 1M prompt tokens: 400k cache reads, 100k cache writes, 500k plain; 10k output.
	m := &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 1_000_000, CachedContentTokenCount: 400_000, CandidatesTokenCount: 10_000}
	u := tr.Estimate("claude-opus-5", m, 100_000)
	want := 0.5*5 + 0.4*0.5 + 0.1*6.25 + 0.01*25
	if abs(u.CostUSD-want) > 1e-9 || u.CacheWrite != 100_000 {
		t.Errorf("cost %v want %v (%+v)", u.CostUSD, want, u)
	}
	// Without a write rate, writes bill at the input rate.
	if u := tr.Estimate("no-write-rate", m, 100_000); abs(u.CostUSD-(0.6*1+0.01*2)) > 1e-9 {
		t.Errorf("fallback write rate: %v", u.CostUSD)
	}
	// Nonsense write counts are clamped to the uncached prompt.
	if u := tr.Estimate("claude-opus-5", m, 5_000_000); u.CacheWrite != 600_000 {
		t.Errorf("clamp: %d", u.CacheWrite)
	}
	if !tr.HasPrice("claude-opus-5-preview") || tr.HasPrice("gpt-9") {
		t.Error("HasPrice prefix logic")
	}
}

func TestEnginePricesServedModelAndCacheWrites(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{}, textContent("hi"))
	f.llm.Usage = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 1_000_000}
	// Configured model is gemini-2.5-flash, but a fallback model served it.
	f.llm.ServedBy = "claude-opus-5"
	f.llm.Metadata = map[string]any{CacheWriteTokensKey: int64(1_000_000)}
	collect(t, f.eng, "s", "go")
	u := f.eng.Usage("s")
	if abs(u.CostUSD-6.25) > 1e-9 || u.CacheWrite != 1_000_000 {
		t.Errorf("expected opus cache-write pricing ($6.25), got $%v %+v", u.CostUSD, u)
	}
	// An unknown served model falls back to the configured model's price.
	g := newEngineWith(t, fixtureOpts{}, textContent("hi"))
	g.llm.Usage = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 1_000_000}
	g.llm.ServedBy = "gemini-2.5-flash-exp-0927"
	collect(t, g.eng, "s", "go")
	if u := g.eng.Usage("s"); !u.Priced || abs(u.CostUSD-0.30) > 1e-9 {
		t.Errorf("prefix-priced served model: %+v", u)
	}
}
