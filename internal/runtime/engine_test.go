package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/agents"
	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/skills"
	"github.com/retail-cortex/code_puppy/internal/tools"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestEngineExecution(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultConfig()

	agentReg, err := agents.NewRegistry()
	if err != nil {
		t.Fatalf("failed to create agent registry: %v", err)
	}

	skillProv, err := skills.NewProvider()
	if err != nil {
		t.Fatalf("failed to create skill provider: %v", err)
	}

	toolReg, err := tools.NewRegistry(cfg, agentReg, skillProv)
	if err != nil {
		t.Fatalf("failed to create tool registry: %v", err)
	}

	mockModel := NewMockLLM("mock-model",
		genai.NewContentFromText("Woof! I wrote your code cleanly.", genai.RoleModel),
	)

	eng, err := NewEngine(ctx, cfg, agentReg, skillProv, toolReg, mockModel)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	if eng.ActiveAgent() != "code-puppy" {
		t.Errorf("expected active agent 'code-puppy', got '%s'", eng.ActiveAgent())
	}

	var observedTexts []string
	err = eng.Execute(ctx, "session-1", "Write a hello world program", func(ev *session.Event) error {
		if ev.Content != nil {
			for _, part := range ev.Content.Parts {
				if part.Text != "" {
					observedTexts = append(observedTexts, part.Text)
				}
			}
		}
		return nil
	})

	if err != nil {
		t.Fatalf("engine execution failed: %v", err)
	}

	joined := strings.Join(observedTexts, " ")
	if !strings.Contains(joined, "Woof!") {
		t.Errorf("expected output to contain 'Woof!', got: %s", joined)
	}

	// Test switching agent to helios
	err = eng.SetActiveAgent(ctx, "helios")
	if err != nil {
		t.Fatalf("failed to switch agent to helios: %v", err)
	}
	if eng.ActiveAgent() != "helios" {
		t.Errorf("expected active agent 'helios', got '%s'", eng.ActiveAgent())
	}
}
