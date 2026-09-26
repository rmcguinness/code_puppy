package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/agents"
	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"google.golang.org/adk/v2/model"
)

type savedPin struct{ agent, ref string }

func pinApp(t *testing.T) (*App, *[]savedPin) {
	t.Helper()
	app, _ := newCommandApp(t, "")
	var saved []savedPin
	app.SaveAgentModel = func(agent, ref string) (string, error) {
		saved = append(saved, savedPin{agent, ref})
		return "/tmp/.env.toml", nil
	}
	app.NewModel = func(_ context.Context, _ *config.Config, name string) (model.LLM, error) {
		return runtime.NewMockLLM(strings.TrimPrefix(name, "anthropic/")), nil
	}
	return app, &saved
}

func TestPinModelAndUnpin(t *testing.T) {
	app, saved := pinApp(t)
	ctx := context.Background()
	run := func(cmd string) string {
		return captureStdout(t, func() { HandleCommand(ctx, cmd, app) })
	}

	if out := run("/pin_model"); !strings.Contains(out, "No agent is pinned") {
		t.Errorf("empty list:\n%s", out)
	}
	if out := run("/pin_model nobody anthropic/x"); !strings.Contains(out, "Unknown agent") || len(*saved) != 0 {
		t.Errorf("unknown agent:\n%s", out)
	}
	if out := run("/pin_model qa-kitten"); !strings.Contains(out, "Usage: /pin_model") {
		t.Errorf("usage:\n%s", out)
	}

	out := run("/pin_model qa-kitten anthropic/claude-haiku-4-5")
	if !strings.Contains(out, "qa-kitten now runs on claude-haiku-4-5") || !strings.Contains(out, "Saved in") {
		t.Errorf("pin:\n%s", out)
	}
	if m, pinned := app.Engine.AgentModel("qa-kitten"); !pinned || m != "claude-haiku-4-5" {
		t.Fatalf("engine pin: %s %v", m, pinned)
	}
	if len(*saved) != 1 || (*saved)[0] != (savedPin{"qa-kitten", "anthropic/claude-haiku-4-5"}) {
		t.Fatalf("saved = %v", *saved)
	}
	if out := run("/pin_model"); !strings.Contains(out, "qa-kitten") || !strings.Contains(out, "claude-haiku-4-5") {
		t.Errorf("list:\n%s", out)
	}
	if out := run("/agents"); !strings.Contains(out, "📌 claude-haiku-4-5") {
		t.Errorf("/agents doesn't show the pin:\n%s", out)
	}

	out = run("/unpin qa-kitten")
	if _, pinned := app.Engine.AgentModel("qa-kitten"); pinned || !strings.Contains(out, "qa-kitten now runs on gemini-3.8-flash") {
		t.Fatalf("unpin:\n%s", out)
	}
	if last := (*saved)[len(*saved)-1]; last != (savedPin{"qa-kitten", ""}) {
		t.Fatalf("unpin not saved: %v", *saved)
	}
}

func TestModelCommandNotesAPinnedActiveAgent(t *testing.T) {
	app, _ := pinApp(t)
	ctx := context.Background()
	captureStdout(t, func() { HandleCommand(ctx, "/pin_model code-puppy anthropic/claude-sonnet-5", app) })
	out := captureStdout(t, func() { HandleCommand(ctx, "/model gemini-3.5-flash-lite", app) })
	if !strings.Contains(out, "code-puppy is pinned to claude-sonnet-5") {
		t.Fatalf("no note that the active agent keeps its pin:\n%s", out)
	}
	if app.Engine.ModelName() != "claude-sonnet-5" {
		t.Fatalf("active agent's model = %s", app.Engine.ModelName())
	}
}

// An agent that declares default_model goes back to it on /unpin.
func TestUnpinRestoresTheAgentsOwnDefault(t *testing.T) {
	app, _ := pinApp(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "reviewer.md"), []byte("---\nname: reviewer\ndisplay_name: Reviewer\ndescription: reviews\ntools: [read_file]\ndefault_model: anthropic/claude-haiku-4-5\n---\nYou review code.\n"), 0o600)
	reg, _ := agents.NewRegistry()
	if err := reg.LoadExternalAgents(dir); err != nil {
		t.Fatal(err)
	}
	app.Agents = reg
	eng, err := runtime.NewEngine(context.Background(), app.Cfg, reg, app.Skills, app.Tools, runtime.NewMockLLM("gemini-3.8-flash"))
	if err != nil {
		t.Fatal(err)
	}
	app.Engine = eng
	ctx := context.Background()
	captureStdout(t, func() { HandleCommand(ctx, "/pin_model reviewer anthropic/claude-sonnet-5", app) })
	out := captureStdout(t, func() { HandleCommand(ctx, "/unpin reviewer", app) })
	if m, _ := eng.AgentModel("reviewer"); m != "claude-haiku-4-5" {
		t.Fatalf("after /unpin reviewer runs on %s, want its default_model:\n%s", m, out)
	}
}
