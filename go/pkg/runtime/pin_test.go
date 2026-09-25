package runtime

import (
	"context"
	"testing"

	"github.com/retail-cortex/code_puppy/pkg/config"
	"google.golang.org/genai"
)

func TestPinnedSubagentRunsAndIsPricedOnItsModel(t *testing.T) {
	haiku := NewMockLLM("claude-haiku-4-5", textContent("tests look fine"))
	haiku.Usage = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 1_000_000}
	f := newEngineWith(t, fixtureOpts{opts: []Option{WithAgentModel("qa-kitten", haiku)}},
		toolCall("invoke_agent", map[string]any{"agent_name": "qa-kitten", "prompt": "review"}),
		textContent("done"))

	if _, err := functionResponses(t, f.eng, "s", "get a review"); err != nil {
		t.Fatal(err)
	}
	if haiku.Calls() != 1 {
		t.Fatalf("pinned model got %d calls, want 1", haiku.Calls())
	}
	if f.llm.Calls() != 2 {
		t.Fatalf("main model got %d calls, want 2 (the sub-agent's call went elsewhere)", f.llm.Calls())
	}
	// The mock reports no model version: the sub-agent's tokens must be
	// priced as its own (pinned) model, not the active agent's.
	want := config.DefaultPricing["claude-haiku-4-5"].InputPerMTok
	if u := f.eng.Usage("s"); abs(u.CostUSD-want) > 1e-9 {
		t.Fatalf("cost $%v, want $%v (haiku input for 1M tokens)", u.CostUSD, want)
	}
	if name, pinned := f.eng.AgentModel("qa-kitten"); name != "claude-haiku-4-5" || !pinned {
		t.Fatalf("AgentModel = %s %v", name, pinned)
	}
	if name, pinned := f.eng.AgentModel("code-puppy"); name != "gemini-3.8-flash" || pinned {
		t.Fatalf("unpinned agent: %s %v", name, pinned)
	}
}

func TestPinAndUnpinTheActiveAgent(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{}, textContent("from main"))
	pinned := NewMockLLM("claude-sonnet-5", textContent("from pinned"))
	ctx := context.Background()
	if err := f.eng.PinModel(ctx, "nope", pinned); err == nil {
		t.Fatal("pinned an unknown agent")
	}
	if err := f.eng.PinModel(ctx, "code-puppy", pinned); err != nil {
		t.Fatal(err)
	}
	if f.eng.ModelName() != "claude-sonnet-5" {
		t.Fatalf("ModelName = %s", f.eng.ModelName())
	}
	if _, err := collect(t, f.eng, "s", "hi"); err != nil || pinned.Calls() != 1 || f.llm.Calls() != 0 {
		t.Fatalf("pinned %d, main %d, err %v", pinned.Calls(), f.llm.Calls(), err)
	}
	if err := f.eng.Unpin(ctx, "code-puppy"); err != nil {
		t.Fatal(err)
	}
	if _, err := collect(t, f.eng, "s", "hi"); err != nil || f.llm.Calls() != 1 || f.eng.ModelName() != "gemini-3.8-flash" {
		t.Fatalf("after unpin: main %d calls, model %s, err %v", f.llm.Calls(), f.eng.ModelName(), err)
	}
}

func TestNewModelAcceptsAProviderQualifiedName(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.LLM.Provider = "gemini"
	cfg.LLM.Anthropic.APIKey = "sk-ant-test"
	m, err := NewModel(context.Background(), cfg, "anthropic/claude-sonnet-5")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.(*anthropicModel); !ok || m.Name() != "claude-sonnet-5" {
		t.Fatalf("got %T %q", m, m.Name())
	}
	// The configured default model can name its provider too.
	cfg.CodePuppy.DefaultModel = "anthropic/claude-haiku-4-5"
	if m, err := NewModel(context.Background(), cfg, ""); err != nil || m.Name() != "claude-haiku-4-5" {
		t.Fatalf("default_model with provider: %v %v", m, err)
	}
}
