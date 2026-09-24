package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/agents"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/skills"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
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
	ic := &mockIC{
		ctx: context.Background(),
	}
	return agent.NewToolContext(ic, "test_call", &session.EventActions{}, nil)
}

func TestToolsSuite(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = tmpDir

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

	all := toolReg.GetAllTools()
	if len(all) < 10 {
		t.Errorf("expected at least 10 tools, got %d", len(all))
	}

	// Verify tool names and descriptions
	for _, tl := range all {
		if tl.Name() == "" {
			t.Errorf("tool missing name: %T", tl)
		}
		if tl.Description() == "" {
			t.Errorf("tool %s missing description", tl.Name())
		}
	}

	// Test create_file directly
	testFilePath := filepath.Join(tmpDir, "test.txt")
	err = os.WriteFile(testFilePath, []byte("line 1\nline 2 target\nline 3\n"), 0644)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Test read_file tool
	readTool, err := NewReadFileTool(tmpDir)
	if err != nil {
		t.Fatalf("failed to create read tool: %v", err)
	}
	rt, ok := readTool.(runnerTool)
	if !ok {
		t.Fatalf("readTool does not implement runnerTool")
	}

	readOut, err := rt.Run(createTestToolContext(), map[string]any{"path": "test.txt"})
	if err != nil {
		t.Fatalf("read_file failed: %v", err)
	}
	content, _ := readOut["content"].(string)
	if !strings.Contains(content, "line 2 target") {
		t.Errorf("expected read_file to contain 'line 2 target', got: %v", content)
	}

	// Test replace_in_file tool
	replaceTool, err := NewReplaceInFileTool(tmpDir)
	if err != nil {
		t.Fatalf("failed to create replace tool: %v", err)
	}
	rept, ok := replaceTool.(runnerTool)
	if !ok {
		t.Fatalf("replaceTool does not implement runnerTool")
	}

	_, err = rept.Run(createTestToolContext(), map[string]any{
		"path":                "test.txt",
		"target_content":      "line 2 target",
		"replacement_content": "line 2 replaced",
	})
	if err != nil {
		t.Fatalf("replace_in_file failed: %v", err)
	}

	updatedBytes, _ := os.ReadFile(testFilePath)
	if !strings.Contains(string(updatedBytes), "line 2 replaced") {
		t.Errorf("expected updated content to have 'line 2 replaced', got %s", string(updatedBytes))
	}

	// Test grep tool
	grepTool, err := NewGrepTool(tmpDir)
	if err != nil {
		t.Fatalf("failed to create grep tool: %v", err)
	}
	gt, ok := grepTool.(runnerTool)
	if !ok {
		t.Fatalf("grepTool does not implement runnerTool")
	}

	grepOut, err := gt.Run(createTestToolContext(), map[string]any{
		"query": "line 2 replaced",
	})
	if err != nil {
		t.Fatalf("grep failed: %v", err)
	}
	matches, _ := grepOut["matches"].([]any)
	if len(matches) == 0 {
		t.Errorf("expected grep to find matches, got 0")
	}

	// Test shell runner
	shellTool, err := NewRunShellCommandTool(tmpDir, 10)
	if err != nil {
		t.Fatalf("failed to create shell tool: %v", err)
	}
	st, ok := shellTool.(runnerTool)
	if !ok {
		t.Fatalf("shellTool does not implement runnerTool")
	}

	shellOut, err := st.Run(createTestToolContext(), map[string]any{
		"command": "echo 'puppy power'",
	})
	if err != nil {
		t.Fatalf("shell runner failed: %v", err)
	}
	output, _ := shellOut["output"].(string)
	if !strings.Contains(output, "puppy power") {
		t.Errorf("expected shell output 'puppy power', got: %s", output)
	}

	// Test agent-specific tool filtering
	puppyTools := toolReg.GetToolsForAgent([]string{"read_file", "list_files", "run_shell_command"})
	if len(puppyTools) != 3 {
		t.Errorf("expected 3 tools for agent, got %d", len(puppyTools))
	}
}
