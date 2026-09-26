package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/pkg/agents"
)

func TestUniversalConstructorNameValidation(t *testing.T) {
	base := t.TempDir()
	ucDir := filepath.Join(base, "uc")
	rt := toolOf(t)(NewUniversalConstructorTool(ucDir, allowAll(), nil, nil))

	// Negative: traversal and separator names are rejected and nothing is written.
	for _, name := range []string{"../evil", "a/b", "..", ".hidden", "sp ace", strings.Repeat("x", 65)} {
		out := runTool(t, rt, map[string]any{"action": "create", "tool_name": name, "code": "echo hi"})
		if errOf(out) == "" {
			t.Errorf("expected rejection for tool_name %q", name)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "evil.sh")); !os.IsNotExist(err) {
		t.Error("traversal wrote outside the tools directory")
	}
	// Negative: unsupported language and missing code.
	if out := runTool(t, rt, map[string]any{"action": "create", "tool_name": "x", "code": "1", "language": "ruby"}); errOf(out) == "" {
		t.Error("expected unsupported language error")
	}
	if out := runTool(t, rt, map[string]any{"action": "create", "tool_name": "x"}); errOf(out) == "" {
		t.Error("expected missing code error")
	}
	if out := runTool(t, rt, map[string]any{"action": "bogus"}); errOf(out) == "" {
		t.Error("expected unknown action error")
	}
}

func TestUniversalConstructorCreateRun(t *testing.T) {
	ucDir := filepath.Join(t.TempDir(), "uc")
	rt := toolOf(t)(NewUniversalConstructorTool(ucDir, allowAll(), nil, nil))

	out := runTool(t, rt, map[string]any{
		"action": "create", "tool_name": "count_args", "language": "bash",
		"code": "echo \"$# [$1] [$2]\"", "description": "counts args",
	})
	if out["success"] != true {
		t.Fatalf("create failed: %v", out)
	}
	info, err := os.Stat(filepath.Join(ucDir, "count_args.sh"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("expected owner-only script, got %v %v", info, err)
	}

	// Positive: args are split into separate argv entries.
	out = runTool(t, rt, map[string]any{"action": "run", "tool_name": "count_args", "args": "one two"})
	if strings.TrimSpace(out["result"].(string)) != "2 [one] [two]" {
		t.Errorf("unexpected run output %v", out)
	}

	list := runTool(t, rt, map[string]any{"action": "list"})
	if tools, _ := list["tools"].([]any); len(tools) != 1 {
		t.Errorf("expected 1 listed tool, got %v", list)
	}

	// Negative: running an unknown tool, and a failing tool.
	if out := runTool(t, rt, map[string]any{"action": "run", "tool_name": "nope"}); errOf(out) == "" {
		t.Error("expected error for unknown tool")
	}
	runTool(t, rt, map[string]any{"action": "create", "tool_name": "fails", "code": "exit 4"})
	if out := runTool(t, rt, map[string]any{"action": "run", "tool_name": "fails"}); out["success"] == true {
		t.Error("expected failing tool to report failure")
	}
}

func TestUniversalConstructorApproval(t *testing.T) {
	ucDir := filepath.Join(t.TempDir(), "uc")

	denied, reqs := approverHooks(false)
	rt := toolOf(t)(NewUniversalConstructorTool(ucDir, denied, nil, nil))
	out := runTool(t, rt, map[string]any{"action": "create", "tool_name": "t1", "code": "echo hi"})
	if !strings.Contains(errOf(out), "not approved") {
		t.Errorf("expected denial, got %v", out)
	}
	if _, err := os.Stat(filepath.Join(ucDir, "t1.sh")); !os.IsNotExist(err) {
		t.Error("denied tool was written to disk")
	}
	if len(*reqs) != 1 || !strings.Contains((*reqs)[0].Diff, "+echo hi") {
		t.Errorf("approval request should show the code as a diff: %v", *reqs)
	}

	// Create approved, run denied: separate approvals for write and execution.
	h := NewHooks(Policy{})
	h.SetApprover(func(_ context.Context, r ApprovalRequest) (Decision, error) {
		if r.Kind == ActionWrite {
			return DecisionOnce, nil
		}
		return DecisionDeny, nil
	})
	rt = toolOf(t)(NewUniversalConstructorTool(ucDir, h, nil, nil))
	if out := runTool(t, rt, map[string]any{"action": "create", "tool_name": "t2", "code": "touch ran"}); out["success"] != true {
		t.Fatalf("approved create failed: %v", out)
	}
	if out := runTool(t, rt, map[string]any{"action": "run", "tool_name": "t2"}); !strings.Contains(errOf(out), "not approved") {
		t.Errorf("expected run to be denied, got %v", out)
	}
}

func TestInvokeAgentTool(t *testing.T) {
	reg, err := agents.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	hooks := NewHooks(Policy{})
	rt := toolOf(t)(NewInvokeAgentTool(reg, hooks))

	// Negative: no invoker wired must be an explicit error, not a fake success.
	out := runTool(t, rt, map[string]any{"agent_name": "helios", "prompt": "do it"})
	if !strings.Contains(errOf(out), "not available") || out["response"] != "" {
		t.Errorf("expected unavailable error, got %v", out)
	}
	// Negative: unknown agent and empty prompt.
	if out := runTool(t, rt, map[string]any{"agent_name": "ghost", "prompt": "x"}); errOf(out) == "" {
		t.Error("expected unknown agent error")
	}
	if out := runTool(t, rt, map[string]any{"agent_name": "helios", "prompt": "  "}); errOf(out) == "" {
		t.Error("expected empty prompt error")
	}

	// Positive: invoker result is returned; errors surface.
	var gotAgent, gotPrompt string
	hooks.SetSubagentInvoker(func(ctx context.Context, name, prompt string) (string, error) {
		gotAgent, gotPrompt = name, prompt
		if prompt == "fail" {
			return "", errors.New("boom")
		}
		return "done: " + prompt, nil
	})
	out = runTool(t, rt, map[string]any{"agent_name": "helios", "prompt": "build"})
	if out["response"] != "done: build" || gotAgent != "helios" || gotPrompt != "build" {
		t.Errorf("unexpected invoke result %v", out)
	}
	if out := runTool(t, rt, map[string]any{"agent_name": "helios", "prompt": "fail"}); !strings.Contains(errOf(out), "boom") {
		t.Errorf("expected invoker error, got %v", out)
	}
}

func TestListAgentsFilter(t *testing.T) {
	reg, err := agents.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	rt := toolOf(t)(NewListAgentsTool(reg))
	all, _ := runTool(t, rt, map[string]any{})["agents"].([]any)
	filtered, _ := runTool(t, rt, map[string]any{"filter": "HELI"})["agents"].([]any)
	none, _ := runTool(t, rt, map[string]any{"filter": "zzz"})["agents"].([]any)
	if len(all) < 7 || len(filtered) != 1 || len(none) != 0 {
		t.Errorf("filter not applied: all=%d filtered=%d none=%d", len(all), len(filtered), len(none))
	}
}

func TestAskUserQuestionTool(t *testing.T) {
	hooks := NewHooks(Policy{})
	rt := toolOf(t)(NewAskUserQuestionTool(hooks))

	// Negative: no prompter => explicit error instead of reading stdin directly.
	if out := runTool(t, rt, map[string]any{"question": "?"}); errOf(out) == "" {
		t.Error("expected error without prompter")
	}
	if out := runTool(t, rt, map[string]any{"question": ""}); errOf(out) == "" {
		t.Error("expected error for empty question")
	}

	hooks.SetUserPrompter(func(ctx context.Context, q string, opts []string) (string, error) {
		if q == "fail?" {
			return "", errors.New("eof")
		}
		return q + "->" + strings.Join(opts, "|"), nil
	})
	out := runTool(t, rt, map[string]any{"question": "pick", "options": []string{"a", "b"}})
	if out["answer"] != "pick->a|b" {
		t.Errorf("unexpected answer %v", out)
	}
	if out := runTool(t, rt, map[string]any{"question": "fail?"}); errOf(out) == "" {
		t.Error("expected prompter error to surface")
	}
}

func TestUniversalConstructorPersistence(t *testing.T) {
	ucDir := filepath.Join(t.TempDir(), "uc")
	first := toolOf(t)(NewUniversalConstructorTool(ucDir, allowAll(), nil, nil))
	runTool(t, first, map[string]any{"action": "create", "tool_name": "greet", "language": "python", "code": "import sys\nprint('hi', sys.argv[1])", "description": "says hi"})
	if info, err := os.Stat(filepath.Join(ucDir, "greet.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest missing or not owner-only: %v %v", info, err)
	}

	// A new process sees and can run the tool.
	second := toolOf(t)(NewUniversalConstructorTool(ucDir, allowAll(), nil, nil))
	list := runTool(t, second, map[string]any{"action": "list"})
	if tools, _ := list["tools"].([]any); len(tools) != 1 || !strings.Contains(tools[0].(string), "greet (python): says hi") {
		t.Fatalf("persisted tool not listed: %v", list)
	}
	if out := runTool(t, second, map[string]any{"action": "run", "tool_name": "greet", "args": "puppy"}); !strings.Contains(fmt.Sprint(out["result"]), "hi puppy") {
		t.Errorf("persisted tool did not run: %v", out)
	}

	// Delete removes script and manifest; a fresh load no longer sees it.
	if out := runTool(t, second, map[string]any{"action": "delete", "tool_name": "greet"}); out["success"] != true {
		t.Fatalf("delete: %v", out)
	}
	for _, f := range []string{"greet.py", "greet.json"} {
		if _, err := os.Stat(filepath.Join(ucDir, f)); !os.IsNotExist(err) {
			t.Errorf("%s not removed", f)
		}
	}
	third := toolOf(t)(NewUniversalConstructorTool(ucDir, allowAll(), nil, nil))
	if tools, _ := runTool(t, third, map[string]any{"action": "list"})["tools"].([]any); len(tools) != 0 {
		t.Errorf("deleted tool reloaded: %v", tools)
	}
	if out := runTool(t, third, map[string]any{"action": "delete", "tool_name": "greet"}); errOf(out) == "" {
		t.Error("deleting a missing tool should fail")
	}
	denied, _ := approverHooks(false)
	runTool(t, first, map[string]any{"action": "create", "tool_name": "keep", "code": "echo k"})
	guarded := toolOf(t)(NewUniversalConstructorTool(ucDir, denied, nil, nil))
	if out := runTool(t, guarded, map[string]any{"action": "delete", "tool_name": "keep"}); !strings.Contains(errOf(out), "not approved") {
		t.Errorf("delete should need approval: %v", out)
	}
	if _, err := os.Stat(filepath.Join(ucDir, "keep.sh")); err != nil {
		t.Error("denied delete removed the script")
	}
}

func TestUniversalConstructorRejectsTamperedManifests(t *testing.T) {
	ucDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "evil.sh")
	writeFile(t, outside, "echo pwned")
	write := func(name, body string) { writeFile(t, filepath.Join(ucDir, name), body) }

	write("traverse.json", `{"name":"../../evil","language":"bash"}`)
	write("badlang.json", `{"name":"badlang","language":"ruby"}`)
	write("badlang.rb", "puts 1")
	write("alias.json", `{"name":"alias","language":"sh"}`) // non-canonical language
	write("alias.sh", "echo a")
	write("mismatch.json", `{"name":"other","language":"bash"}`) // name differs from file
	write("other.sh", "echo o")
	write("noscript.json", `{"name":"noscript","language":"bash"}`)
	write("garbage.json", `{not json`)
	write("link.json", `{"name":"link","language":"bash"}`)
	if err := os.Symlink(outside, filepath.Join(ucDir, "link.sh")); err != nil {
		t.Fatal(err)
	}
	write("good.json", `{"name":"good","language":"bash","description":"fine"}`)
	write("good.sh", "echo good")

	rt := toolOf(t)(NewUniversalConstructorTool(ucDir, allowAll(), nil, nil))
	tools, _ := runTool(t, rt, map[string]any{"action": "list"})["tools"].([]any)
	if len(tools) != 1 || !strings.HasPrefix(tools[0].(string), "good ") {
		t.Errorf("only the valid manifest should load, got %v", tools)
	}
	if out := runTool(t, rt, map[string]any{"action": "run", "tool_name": "link"}); errOf(out) == "" {
		t.Error("symlinked script must not be runnable")
	}
}
