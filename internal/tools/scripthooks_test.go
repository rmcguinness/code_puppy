package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
)

func newHooks(t *testing.T, cfg config.HooksConfig) (*ScriptHooks, *[]string) {
	t.Helper()
	h, err := NewScriptHooks(cfg, nil, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var warnings []string
	h.Warn = func(s string) { warnings = append(warnings, s) }
	t.Cleanup(h.Close)
	return h, &warnings
}

func TestScriptHookBlocking(t *testing.T) {
	ctx := context.Background()
	h, _ := newHooks(t, config.HooksConfig{PreTool: []config.HookConfig{
		{Match: "run_shell_command", Command: `echo "no shell today" >&2; exit 2`},
		{Match: "delete_*", Command: `echo '{"decision":"block","reason":"deletes need review"}'`},
		{Match: "", Command: "exit 0"},
	}})
	if r := h.PreTool(ctx, "s", "run_shell_command", nil); r != "no shell today" {
		t.Errorf("exit 2 block reason = %q", r)
	}
	if r := h.PreTool(ctx, "s", "delete_file", nil); r != "deletes need review" {
		t.Errorf("JSON block reason = %q", r)
	}
	if r := h.PreTool(ctx, "s", "read_file", nil); r != "" {
		t.Errorf("unexpected block %q", r)
	}
}

func TestScriptHookReceivesEvent(t *testing.T) {
	out := filepath.Join(t.TempDir(), "event.json")
	h, _ := newHooks(t, config.HooksConfig{
		PostTool:     []config.HookConfig{{Command: "cat > " + out}},
		PromptSubmit: []config.HookConfig{{Command: "cat >> " + out}},
	})
	h.PostTool(context.Background(), "sess", "grep", map[string]any{"query": "x"}, map[string]any{"total_matches": 1}, nil)
	if err := h.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	for _, want := range []string{`"event":"post_tool"`, `"tool":"grep"`, `"session_id":"sess"`, `"query":"x"`, `"total_matches":1`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("hook stdin missing %s: %s", want, b)
		}
	}
	if r := h.PromptSubmit(context.Background(), "sess", "hello there"); r != "" {
		t.Errorf("prompt hook blocked: %q", r)
	}
	if b, _ := os.ReadFile(out); !strings.Contains(string(b), `"prompt":"hello there"`) {
		t.Errorf("prompt event not delivered: %s", b)
	}
}

func TestScriptHookFailures(t *testing.T) {
	ctx := context.Background()
	// Fail-open: error is warned about, action proceeds.
	h, warnings := newHooks(t, config.HooksConfig{PreTool: []config.HookConfig{{Command: "exit 1"}}})
	if r := h.PreTool(ctx, "s", "x", nil); r != "" || len(*warnings) != 1 {
		t.Errorf("fail-open: reason=%q warnings=%v", r, *warnings)
	}
	// Fail-closed: error blocks.
	h, _ = newHooks(t, config.HooksConfig{PreTool: []config.HookConfig{{Command: "exit 1", FailClosed: true}}})
	if r := h.PreTool(ctx, "s", "x", nil); !strings.Contains(r, "fail_closed") {
		t.Errorf("fail-closed reason = %q", r)
	}
	// Timeout counts as a failure.
	h, warnings = newHooks(t, config.HooksConfig{PreTool: []config.HookConfig{{Command: "sleep 10", TimeoutSeconds: 1}}})
	if r := h.PreTool(ctx, "s", "x", nil); r != "" || len(*warnings) != 1 || !strings.Contains((*warnings)[0], "timed out") {
		t.Errorf("timeout: reason=%q warnings=%v", r, *warnings)
	}
	// Invalid configs are rejected up front.
	for _, bad := range []config.HooksConfig{
		{PreTool: []config.HookConfig{{Command: " "}}},
		{PostTool: []config.HookConfig{{Match: "[", Command: "true"}}},
	} {
		if _, err := NewScriptHooks(bad, nil, ".", nil); err == nil {
			t.Errorf("expected error for %+v", bad)
		}
	}
	var nilHooks *ScriptHooks
	if nilHooks.PreTool(ctx, "", "x", nil) != "" || !nilHooks.Empty() {
		t.Error("nil hooks should be a no-op")
	}
}
