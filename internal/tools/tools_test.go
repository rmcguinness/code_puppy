package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/internal/agents"
	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/skills"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
)

type runnerTool interface {
	Run(ctx agent.Context, args any) (map[string]any, error)
}

type mockIC struct {
	agent.InvocationContext
	ctx context.Context
}

func (m *mockIC) Deadline() (deadline time.Time, ok bool) { return m.ctx.Deadline() }
func (m *mockIC) Done() <-chan struct{}                   { return m.ctx.Done() }
func (m *mockIC) Err() error                              { return m.ctx.Err() }
func (m *mockIC) Value(key any) any                       { return m.ctx.Value(key) }
func (m *mockIC) Artifacts() agent.Artifacts              { return nil }

func createTestToolContext() agent.Context {
	return createTestToolContextWith(context.Background())
}

func createTestToolContextWith(ctx context.Context) agent.Context {
	return agent.NewToolContext(&mockIC{ctx: ctx}, "test_call", &session.EventActions{}, nil)
}

// --- shared helpers ---

func newTestWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	dir := t.TempDir()
	ws, err := NewWorkspace(dir, 0)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	t.Cleanup(func() { ws.Close() })
	return ws, dir
}

// toolOf adapts a (tool.Tool, error) constructor result: toolOf(t)(NewX(...)).
func toolOf(t *testing.T) func(tool.Tool, error) runnerTool {
	return func(tl tool.Tool, err error) runnerTool {
		t.Helper()
		if err != nil {
			t.Fatalf("failed to create tool: %v", err)
		}
		rt, ok := tl.(runnerTool)
		if !ok {
			t.Fatalf("%T does not implement runnerTool", tl)
		}
		return rt
	}
}

func runTool(t *testing.T, rt runnerTool, args map[string]any) map[string]any {
	t.Helper()
	out, err := rt.Run(createTestToolContext(), args)
	if err != nil {
		t.Fatalf("tool run returned error: %v", err)
	}
	return out
}

func errOf(out map[string]any) string {
	s, _ := out["error"].(string)
	return s
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// allowAll auto-approves every action.
func allowAll() *Hooks { return NewHooks(Policy{AutoApproveAll: true}) }

// approverHooks records requests and approves once (true) or denies (false).
func approverHooks(approve bool) (*Hooks, *[]ApprovalRequest) {
	d := DecisionDeny
	if approve {
		d = DecisionOnce
	}
	return decisionHooks(d)
}

// decisionHooks records requests and answers with d.
func decisionHooks(d Decision) (*Hooks, *[]ApprovalRequest) {
	var reqs []ApprovalRequest
	h := NewHooks(Policy{})
	h.SetApprover(func(ctx context.Context, req ApprovalRequest) (Decision, error) {
		reqs = append(reqs, req)
		return d, nil
	})
	return h, &reqs
}

// --- registry ---

func TestToolsSuite(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = tmpDir
	cfg.Tools.UCToolsDir = filepath.Join(tmpDir, "uc")
	cfg.Tools.AutoApproveCommands = true
	cfg.CodePuppy.AutoApprove = true

	agentReg, err := agents.NewRegistry()
	if err != nil {
		t.Fatalf("failed to create agent registry: %v", err)
	}
	skillProv, err := skills.NewProvider()
	if err != nil {
		t.Fatalf("failed to create skill provider: %v", err)
	}

	toolReg, err := NewRegistry(cfg, agentReg, skillProv)
	if err != nil {
		t.Fatalf("failed to create tool registry: %v", err)
	}
	defer toolReg.Close()

	all := toolReg.GetAllTools()
	if len(all) < 10 {
		t.Errorf("expected at least 10 tools, got %d", len(all))
	}
	seen := map[string]bool{}
	for _, tl := range all {
		if tl.Name() == "" {
			t.Errorf("tool missing name: %T", tl)
		}
		if tl.Description() == "" {
			t.Errorf("tool %s missing description", tl.Name())
		}
		if seen[tl.Name()] {
			t.Errorf("GetAllTools returned duplicate %s", tl.Name())
		}
		seen[tl.Name()] = true
	}
	if !seen["manage_background_process"] {
		t.Errorf("expected manage_background_process to be registered")
	}

	// UC tools dir is created lazily, not at registry construction.
	if _, err := os.Stat(cfg.Tools.UCToolsDir); !os.IsNotExist(err) {
		t.Errorf("expected uc tools dir to not be created eagerly, stat err=%v", err)
	}

	writeFile(t, filepath.Join(tmpDir, "test.txt"), "line 1\nline 2 target\nline 3\n")
	ws := toolReg.Workspace()
	hooks := toolReg.Hooks()

	readOut := runTool(t, toolOf(t)(NewReadFileTool(ws)), map[string]any{"path": "test.txt"})
	if content, _ := readOut["content"].(string); !strings.Contains(content, "line 2 target") {
		t.Errorf("expected read_file to contain 'line 2 target', got: %v", content)
	}

	runTool(t, toolOf(t)(NewReplaceInFileTool(ws, hooks)), map[string]any{
		"path":                "test.txt",
		"target_content":      "line 2 target",
		"replacement_content": "line 2 replaced",
	})
	if b, _ := os.ReadFile(filepath.Join(tmpDir, "test.txt")); !strings.Contains(string(b), "line 2 replaced") {
		t.Errorf("expected updated content, got %s", b)
	}

	grepOut := runTool(t, toolOf(t)(NewGrepTool(ws)), map[string]any{"query": "line 2 replaced"})
	if matches, _ := grepOut["matches"].([]any); len(matches) == 0 {
		t.Errorf("expected grep to find matches, got 0")
	}

	shellOut := runTool(t, toolOf(t)(NewRunShellCommandTool(ShellConfig{Workspace: ws, Hooks: hooks})),
		map[string]any{"command": "echo 'puppy power'"})
	if output, _ := shellOut["output"].(string); !strings.Contains(output, "puppy power") {
		t.Errorf("expected shell output 'puppy power', got: %s", output)
	}

	if got := toolReg.GetToolsForAgent([]string{"read_file", "list_files", "run_shell_command"}); len(got) != 3 {
		t.Errorf("expected 3 tools for agent, got %d", len(got))
	}
	// Aliases resolve to the same tool and are de-duplicated.
	if got := toolReg.GetToolsForAgent([]string{"edit", "replace_in_file", "missing_tool"}); len(got) != 1 {
		t.Errorf("expected alias de-duplication and unknown names skipped, got %d tools", len(got))
	}
}

func TestRegistryRejectsMissingWorkspace(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := NewRegistry(cfg, nil, nil); err == nil {
		t.Fatal("expected error for missing workspace directory")
	}
}

func TestRegistryCloseKillsBackgroundProcesses(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = t.TempDir()
	reg, err := NewRegistry(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	bp, err := reg.Processes().Start("sleep 30", reg.Workspace().Dir())
	if err != nil {
		t.Fatal(err)
	}
	reg.Close()
	select {
	case <-bp.done:
	case <-time.After(5 * time.Second):
		t.Fatal("background process survived registry Close")
	}
}
