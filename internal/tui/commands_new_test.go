package tui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"google.golang.org/genai"
)

func newCommandApp(t *testing.T, input string, replies ...*genai.Content) (*App, *runtime.MockLLM) {
	t.Helper()
	isolateHome(t)
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = t.TempDir()
	cfg.Images.Dir = t.TempDir()
	cfg.Audit.Enabled = false
	cfg.CodePuppy.AutoApprove = true
	llm := runtime.NewMockLLM("gemini-3.8-flash", replies...)
	app := openApp(t, cfg, llm)
	app.Input = NewLineReader(strings.NewReader(input), io.Discard)
	return app, llm
}

func toolCallContent(name string, args map[string]any) *genai.Content {
	return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: name, Args: args}}}}
}

func TestPlanCommandRefusesEditsAndRecordsTheGoal(t *testing.T) {
	app, llm := newCommandApp(t, "/plan\n/plan add notes.txt\n/exit\n",
		toolCallContent("create_file", map[string]any{"path": "notes.txt", "content": "x"}),
		genai.NewContentFromText("1. Create notes.txt", genai.RoleModel))
	out := captureStdout(t, func() { RunREPL(context.Background(), app) })

	if !strings.Contains(out, "Usage: /plan <goal>") {
		t.Errorf("bare /plan should print usage:\n%s", out)
	}
	if llm.Calls() != 2 {
		t.Fatalf("want 2 model calls for the plan, got %d", llm.Calls())
	}
	if _, err := os.Stat(filepath.Join(app.Tools.Workspace().Dir(), "notes.txt")); err == nil {
		t.Fatal("/plan created a file")
	}
	first := llm.Requests[0].Contents
	if text := first[len(first)-1].Parts[0].Text; !strings.Contains(text, "plan-only mode") || !strings.Contains(text, "add notes.txt") {
		t.Fatalf("plan prompt = %q", text)
	}
	if got := userMessages(app.Storage); len(got) != 1 || got[0] != "/plan add notes.txt" {
		t.Fatalf("transcript = %v", got)
	}
}

func TestPlanGoalIsNotRunAsACommand(t *testing.T) {
	app, llm := newCommandApp(t, "/plan /clear everything\n/exit\n", genai.NewContentFromText("plan", genai.RoleModel))
	captureStdout(t, func() { RunREPL(context.Background(), app) })
	if llm.Calls() != 1 {
		t.Fatalf("goal starting with / was not sent to the agent (%d calls)", llm.Calls())
	}
}

func TestShellPassthroughRunsInWorkspaceWithoutTheAgent(t *testing.T) {
	app, llm := newCommandApp(t, "!echo hi > made.txt\n!exit 3\n!\n/exit\n")
	out := captureStdout(t, func() { RunREPL(context.Background(), app) })
	if llm.Calls() != 0 {
		t.Fatalf("! reached the agent (%d calls)", llm.Calls())
	}
	b, err := os.ReadFile(filepath.Join(app.Tools.Workspace().Dir(), "made.txt"))
	if err != nil || strings.TrimSpace(string(b)) != "hi" {
		t.Fatalf("command did not run in the workspace: %q %v", b, err)
	}
	for _, want := range []string{"$ echo hi > made.txt", "Done", "Exit code 3", "Usage: !<command>"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if len(userMessages(app.Storage)) != 0 {
		t.Fatal("! commands were recorded as prompts")
	}
}

// Ctrl+C during a ! command stops the command; the REPL's copy of the
// signal must not also trigger exit at the next prompt.
func TestCtrlCDuringShellPassthroughDoesNotExit(t *testing.T) {
	app, llm := newCommandApp(t, "", genai.NewContentFromText("hi", genai.RoleModel))
	// Like a person, the next line arrives after the command, so a stale
	// interrupt left in the channel would win the race at the prompt.
	app.Input = NewLineReader(&slowReader{chunks: []string{"!sleep 0.5\n", "hello\n/exit\n"}, delay: 300 * time.Millisecond}, io.Discard)
	sigs := make(chan os.Signal, 1)
	app.Interrupts = sigs
	go func() {
		time.Sleep(200 * time.Millisecond)
		sigs <- os.Interrupt
	}()
	captureStdout(t, func() { RunREPL(context.Background(), app) })
	if llm.Calls() != 1 {
		t.Fatalf("the prompt after the ! command was not sent (%d model calls); a stale Ctrl+C ended the session", llm.Calls())
	}
}

func TestToolsAndShowCommands(t *testing.T) {
	app, _ := newCommandApp(t, "")
	app.Cfg.MCP.Servers = []config.MCPServerConfig{{Name: "gh", Prefix: "gh"}, {Name: "qa-only", Agents: []string{"qa-kitten"}}}
	out := captureStdout(t, func() { HandleCommand(context.Background(), "/tools", app) })
	for _, want := range []string{"read_file", "run_shell_command", "mcp:gh", "gh__"} {
		if !strings.Contains(out, want) {
			t.Errorf("/tools missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "qa-only") {
		t.Error("/tools listed an MCP server not offered to the active agent")
	}
	// read_file is marked as available in /plan, run_shell_command is not.
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "read_file") && !strings.Contains(line, "●"):
			t.Errorf("read_file not marked for /plan: %q", line)
		case strings.Contains(line, "run_shell_command") && strings.Contains(line, "●"):
			t.Errorf("run_shell_command marked for /plan: %q", line)
		}
	}

	show := captureStdout(t, func() { HandleCommand(context.Background(), "/show", app) })
	set := captureStdout(t, func() { HandleCommand(context.Background(), "/set", app) })
	if show == "" || show != set {
		t.Fatalf("/show should match /set:\n%s\nvs\n%s", show, set)
	}
}

// slowReader returns its chunks one per Read, pausing before each after the first.
type slowReader struct {
	chunks []string
	delay  time.Duration
	n      int
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.n >= len(r.chunks) {
		return 0, io.EOF
	}
	if r.n > 0 {
		time.Sleep(r.delay)
	}
	c := r.chunks[r.n]
	r.n++
	return copy(p, c), nil
}
