package tui

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"google.golang.org/adk/v2/model"
	sessionsdk "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func TestCompleteBlocks(t *testing.T) {
	cases := map[string]int{
		"no newline yet":            0,
		"line one\n":                0,
		"para one\n\npara two":      len("para one\n\n"),
		"```go\nx := 1\n\ny := 2\n": 0, // blank line inside a fence doesn't split
		"```go\nx := 1\n```\nafter": len("```go\nx := 1\n```\n"),
		"a\n\nb\n\nc":               len("a\n\nb\n\n"),
		"~~~\ncode\n~~~\n":          len("~~~\ncode\n~~~\n"),
	}
	for in, want := range cases {
		if got := completeBlocks(in); got != want {
			t.Errorf("completeBlocks(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestMarkdownStreamRendersProgressively(t *testing.T) {
	var out bytes.Buffer
	md, err := newMarkdownStream(&out, "dark", 80)
	if err != nil {
		t.Fatal(err)
	}
	md.Write("# Title\n\nSome **bold**")
	first := out.String()
	if !strings.Contains(first, "Title") || strings.Contains(first, "bold") {
		t.Errorf("expected only the completed heading block, got %q", first)
	}
	md.Write(" text.\n")
	md.Flush()
	if !strings.Contains(out.String(), "bold") || strings.Contains(out.String(), "**") {
		t.Errorf("markdown not rendered: %q", out.String())
	}
}

func textEvent(text string, partial bool) *sessionsdk.Event {
	ev := &sessionsdk.Event{}
	ev.LLMResponse = model.LLMResponse{Content: genai.NewContentFromText(text, genai.RoleModel), Partial: partial}
	return ev
}

func TestPrinterStreamingDedup(t *testing.T) {
	var out bytes.Buffer
	p := NewPrinter(PrinterOptions{Out: &out})
	p.Handle(textEvent("Hel", true))
	p.Handle(textEvent("lo", true))
	p.Handle(textEvent("Hello", false)) // final aggregate repeats streamed text
	p.Handle(textEvent(" again", false))
	if out.String() != "Hello again" {
		t.Errorf("printed %q, want streamed text once", out.String())
	}
	// Control sequences in model text are neutralised.
	out.Reset()
	p.Handle(textEvent("\x1b]52;c;x\x07ok", false))
	if strings.ContainsRune(out.String(), '\x1b') {
		t.Errorf("escape leaked: %q", out.String())
	}
}

func TestSpinner(t *testing.T) {
	var out syncBuffer
	s := NewSpinner(&out, true)
	s.Start("thinking")
	s.Start("again") // no second spinner
	time.Sleep(150 * time.Millisecond)
	s.Stop()
	s.Stop() // idempotent
	o := out.b.String()
	if !strings.Contains(o, "thinking") || strings.Contains(o, "again") || !strings.HasSuffix(o, "\r\033[2K") {
		t.Errorf("spinner output %q", o)
	}
	var off bytes.Buffer
	d := NewSpinner(&off, false)
	d.Start("x")
	d.Stop()
	if off.Len() != 0 {
		t.Error("disabled spinner wrote output")
	}
	var nilSpinner *Spinner
	nilSpinner.Start("x")
	nilSpinner.Stop()
}

func TestCompleter(t *testing.T) {
	ws := t.TempDir()
	os.MkdirAll(filepath.Join(ws, "src", "pkg"), 0o755)
	os.WriteFile(filepath.Join(ws, "src", "main.go"), nil, 0o644)
	os.WriteFile(filepath.Join(ws, ".env"), nil, 0o644)
	c := NewCompleter(ws)
	c.Command("undo", "--force")
	c.Command("help")
	c.Dynamic("agent", func() []string { return []string{"helios", "code-puppy"} })

	do := func(line string) []string {
		cands, _ := c.Do([]rune(line), len([]rune(line)))
		var out []string
		for _, r := range cands {
			out = append(out, string(r))
		}
		return out
	}
	if got := do("/un"); len(got) != 1 || got[0] != "do " {
		t.Errorf("/un -> %q", got)
	}
	if got := do("/agent he"); len(got) != 1 || got[0] != "lios" {
		t.Errorf("/agent he -> %q", got)
	}
	if got := do("/undo "); len(got) != 1 || got[0] != "--force" {
		t.Errorf("/undo -> %q", got)
	}
	if got := strings.Join(do("look at @src/"), ","); got != "main.go,pkg/" {
		t.Errorf("@src/ -> %q", got)
	}
	// Negative: hidden files only when asked for, no traversal, nothing for plain words.
	if got := do("@"); strings.Contains(strings.Join(got, ","), ".env") {
		t.Errorf("hidden file offered: %q", got)
	}
	if got := do("@."); !strings.Contains(strings.Join(got, ","), "env") {
		t.Errorf("hidden file not offered for '@.': %q", got)
	}
	if got := do("@../"); len(got) != 0 {
		t.Errorf("traversal completed: %q", got)
	}
	if got := do("hello wor"); len(got) != 0 {
		t.Errorf("plain text completed: %q", got)
	}
}

func TestUsageLine(t *testing.T) {
	before := runtime.Usage{Calls: 1, Input: 1000, Output: 100, Priced: true, CostUSD: 0.01}
	after := runtime.Usage{Calls: 3, Input: 13_400, Output: 1_300, LastPrompt: 12_345, Priced: true, CostUSD: 0.0325}
	got := UsageLine(before, after)
	for _, want := range []string{"12.4k in", "1.2k out", "context 12.3k", "$0.0225", "session $0.0325"} {
		if !strings.Contains(got, want) {
			t.Errorf("usage line %q missing %q", got, want)
		}
	}
	after.Priced = false
	if strings.Contains(UsageLine(before, after), "$") {
		t.Error("unpriced usage should not show cost")
	}
	if UsageLine(before, before) != "" {
		t.Error("no calls -> no line")
	}
}

func newFullApp(t *testing.T) *App {
	t.Helper()
	isolateHome(t)
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = t.TempDir()
	cfg.Tools.ApprovalsFile = filepath.Join(t.TempDir(), "approvals.json")
	cfg.Images.Dir = t.TempDir()
	cfg.CodePuppy.AutoApprove = true
	llm := runtime.NewMockLLM("gemini-3.8-flash",
		&genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "create_file", Args: map[string]any{"path": "made.txt", "content": "by tool\n"}}}}},
		genai.NewContentFromText("created", genai.RoleModel))
	llm.Usage = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 500, CandidatesTokenCount: 50}
	app := openApp(t, cfg, llm)
	return app
}

// captureStdout runs f and returns what it printed.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	f()
	w.Close()
	os.Stdout = old
	return <-done
}

func TestREPLTurnUndoDiffCost(t *testing.T) {
	app := newFullApp(t)
	app.Input = NewLineReader(strings.NewReader("make a file\n/diff\n/cost\n/checkpoints\n/undo\n/exit\n"), io.Discard)
	out := captureStdout(t, func() {
		if err := RunREPL(context.Background(), app); err != nil {
			t.Error(err)
		}
	})
	made := filepath.Join(app.Tools.Workspace().Dir(), "made.txt")
	if _, err := os.Stat(made); !os.IsNotExist(err) {
		t.Error("/undo did not remove the file the turn created")
	}
	for _, want := range []string{"+by tool", "Model calls:   2", "make a file", "Undid", "context 500"} {
		if !strings.Contains(out, want) {
			t.Errorf("REPL output missing %q:\n%s", want, out)
		}
	}
}

func TestApprovalsAndMemoryCommands(t *testing.T) {
	app := newFullApp(t)
	ctx := context.Background()
	hooks := app.Tools.Hooks()
	hooks.Store().Add("cmd:ls -la", "")

	out := captureStdout(t, func() { HandleCommand(ctx, "/approvals", app) })
	if !strings.Contains(out, "command: ls -la") {
		t.Errorf("/approvals output: %s", out)
	}
	captureStdout(t, func() { HandleCommand(ctx, "/approvals revoke 1", app) })
	if hooks.Store().Has("cmd:ls -la") {
		t.Error("revoke did not remove the rule")
	}
	if out := captureStdout(t, func() { HandleCommand(ctx, "/approvals revoke 9", app) }); !strings.Contains(out, "Usage") {
		t.Errorf("bad revoke index: %s", out)
	}

	reloaded := 0
	app.ReloadMemory = func(context.Context) ([]string, error) { reloaded++; return []string{"PUPPY.md"}, nil }
	captureStdout(t, func() { HandleCommand(ctx, "/memory add always run go vet", app) })
	b, err := os.ReadFile(filepath.Join(app.Tools.Workspace().Dir(), "PUPPY.md"))
	if err != nil || !strings.Contains(string(b), "- always run go vet") || reloaded != 1 {
		t.Errorf("/memory add: %q %v reloaded=%d", b, err, reloaded)
	}
	if out := captureStdout(t, func() { HandleCommand(ctx, "/memory", app) }); !strings.Contains(out, "PUPPY.md") {
		t.Errorf("/memory: %s", out)
	}
	if out := captureStdout(t, func() { HandleCommand(ctx, "/mcp", app) }); !strings.Contains(out, "No MCP servers") {
		t.Errorf("/mcp: %s", out)
	}
	if out := captureStdout(t, func() { HandleCommand(ctx, "/undo", app) }); !strings.Contains(out, "nothing to undo") {
		t.Errorf("/undo with nothing: %s", out)
	}
}

func TestResumeCommand(t *testing.T) {
	app := newFullApp(t)
	rec, _ := app.Storage.CreateSession("", "earlier", "code-puppy")
	app.Storage.AddMessage("user", "remember the plan")
	app.Storage.CreateSession("", "later", "code-puppy")

	out := captureStdout(t, func() { HandleCommand(context.Background(), "/resume "+rec.ID, app) })
	if app.Storage.Active().ID != rec.ID || !strings.Contains(out, "remember the plan") {
		t.Errorf("resume: active=%s out=%s", app.Storage.Active().ID, out)
	}
	if out := captureStdout(t, func() { HandleCommand(context.Background(), "/resume ../../etc", app) }); !strings.Contains(out, "invalid session id") {
		t.Errorf("bad id: %s", out)
	}
}

func TestThemeFromEnv(t *testing.T) {
	for v, want := range map[string]string{"": "dark", "15;0": "dark", "0;15": "light", "0;7": "light", "garbage": "dark"} {
		t.Setenv("COLORFGBG", v)
		if got := themeFromEnv(); got != want {
			t.Errorf("COLORFGBG=%q -> %q, want %q", v, got, want)
		}
	}
}

// ctrlCInput scripts REPL lines and simulates Ctrl+C at any Ask prompt, the
// way the line editor reports it in raw mode (no SIGINT).
type ctrlCInput struct {
	lines   []string
	handler func()
	asked   int
}

func (c *ctrlCInput) SetInterruptHandler(f func()) { c.handler = f }
func (c *ctrlCInput) Ask(ctx context.Context, text string) (string, error) {
	c.asked++
	if c.handler != nil {
		c.handler()
	}
	return "", context.Canceled
}
func (c *ctrlCInput) ReadInput(ctx context.Context, prompt string) (string, error) {
	if len(c.lines) == 0 {
		return "", io.EOF
	}
	l := c.lines[0]
	c.lines = c.lines[1:]
	return l, nil
}

func TestCtrlCAtApprovalCancelsTurn(t *testing.T) {
	isolateHome(t)
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = t.TempDir()
	cfg.Tools.ApprovalsFile = filepath.Join(t.TempDir(), "approvals.json")
	cfg.Images.Dir = t.TempDir()
	cfg.CodePuppy.AutoApprove = false // approvals required
	llm := runtime.NewMockLLM("m",
		&genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "create_file", Args: map[string]any{"path": "x.txt", "content": "x"}}}}},
		genai.NewContentFromText("should never be reached", genai.RoleModel))
	app := openApp(t, cfg, llm)
	reg := app.Tools
	in := &ctrlCInput{lines: []string{"make x"}}
	app.Input = in
	reg.Hooks().SetApprover(NewApprover(in, 0))

	out := captureStdout(t, func() { RunREPL(context.Background(), app) })
	if in.asked == 0 {
		t.Fatal("approval was never requested")
	}
	if !strings.Contains(out, "Interrupted") {
		t.Errorf("turn was not cancelled by Ctrl+C at the approval prompt:\n%s", out)
	}
	if llm.Calls() > 1 {
		t.Errorf("model kept running after Ctrl+C (%d calls)", llm.Calls())
	}
	if in.handler != nil {
		t.Error("interrupt handler should be cleared after the turn")
	}
}

func TestSessionListScopedToWorkspace(t *testing.T) {
	app := newFullApp(t)
	app.Storage.SetWorkspace("/proj/other")
	app.Storage.CreateSession("", "elsewhere", "code-puppy")
	app.Storage.SetWorkspace("/proj/here")
	app.Storage.CreateSession("", "local", "code-puppy")

	out := captureStdout(t, func() { HandleCommand(context.Background(), "/session list", app) })
	if !strings.Contains(out, "local") || strings.Contains(out, "elsewhere") || !strings.Contains(out, "(1)") {
		t.Errorf("/session list should show only this workspace:\n%s", out)
	}
	out = captureStdout(t, func() { HandleCommand(context.Background(), "/session list --all", app) })
	if !strings.Contains(out, "elsewhere") || !strings.Contains(out, "/proj/other") {
		t.Errorf("/session list --all should show every workspace:\n%s", out)
	}
}

func TestCompactCommand(t *testing.T) {
	app := newFullApp(t) // mock: create_file call, "created", then default replies
	ctx := context.Background()
	if out := captureStdout(t, func() { HandleCommand(ctx, "/compact", app) }); !strings.Contains(out, "No active session") {
		t.Errorf("no session: %s", out)
	}
	app.Storage.CreateSession("", "t", "code-puppy")
	sid := app.Storage.Active().ID
	for _, p := range []string{"make a file", "second turn"} {
		if err := app.Engine.Execute(ctx, sid, p, nil); err != nil {
			t.Fatal(err)
		}
	}
	out := captureStdout(t, func() { HandleCommand(ctx, "/compact keep file names", app) })
	if !strings.Contains(out, "Replaced") {
		t.Errorf("/compact output: %s", out)
	}
	fresh := newFullApp(t)
	fresh.Storage.CreateSession("", "t", "code-puppy")
	if out := captureStdout(t, func() { HandleCommand(ctx, "/compact", fresh) }); !strings.Contains(out, "nothing to compact") {
		t.Errorf("empty session: %s", out)
	}
}
