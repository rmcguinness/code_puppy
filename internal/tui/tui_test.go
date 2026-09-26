package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	core "github.com/retail-cortex/code_puppy/internal/app"
	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"github.com/retail-cortex/code_puppy/internal/tools"
	"google.golang.org/adk/v2/model"
)

func TestLineReaderSharedSequentialReads(t *testing.T) {
	var out bytes.Buffer
	lr := NewLineReader(strings.NewReader("first\r\nsecond\nlast-no-newline"), &out)

	for _, want := range []string{"first", "second", "last-no-newline"} {
		got, err := lr.ReadLine("> ")
		if err != nil || got != want {
			t.Fatalf("ReadLine = %q, %v; want %q", got, err, want)
		}
	}
	if _, err := lr.ReadLine("> "); !errors.Is(err, io.EOF) {
		t.Errorf("expected EOF, got %v", err)
	}
	if strings.Count(out.String(), "> ") != 4 {
		t.Errorf("prompt not echoed each time: %q", out.String())
	}
}

func TestApprover(t *testing.T) {
	keyed := tools.ApprovalRequest{Tool: "run_shell_command", Kind: tools.ActionCommand, Detail: "rm -rf build\x1b]52;c;ZXZpbA==\x07",
		Key: "cmd:rm -rf build", KeyLabel: "this exact command"}
	cases := map[string]tools.Decision{
		"y\n": tools.DecisionOnce, "YES\n": tools.DecisionOnce, " y \n": tools.DecisionOnce,
		"s\n": tools.DecisionSession, "a\n": tools.DecisionAlways, "always\n": tools.DecisionAlways,
		"\n": tools.DecisionDeny, "n\n": tools.DecisionDeny, "maybe\n": tools.DecisionDeny,
	}
	for input, want := range cases {
		var out bytes.Buffer
		approve := NewApprover(NewLineReader(strings.NewReader(input), &out), 0)
		got, err := approve(context.Background(), keyed)
		if err != nil || got != want {
			t.Errorf("input %q: got %v, %v; want %v", input, got, err, want)
		}
		if strings.Contains(out.String(), "\x1b]52") || strings.Contains(out.String(), "\x07") {
			t.Errorf("approval prompt echoed raw escape sequence: %q", out.String())
		}
		if !strings.Contains(out.String(), "rm -rf build") || !strings.Contains(out.String(), "this exact command") {
			t.Errorf("approval prompt missing detail or scope: %q", out.String())
		}
	}

	// Without a key, session/always are unavailable and deny.
	unkeyed := tools.ApprovalRequest{Tool: "x", Kind: tools.ActionWrite, Detail: "d"}
	for _, in := range []string{"s\n", "a\n"} {
		var out bytes.Buffer
		if got, _ := NewApprover(NewLineReader(strings.NewReader(in), &out), 0)(context.Background(), unkeyed); got != tools.DecisionDeny {
			t.Errorf("%q without key should deny, got %v", in, got)
		}
		if strings.Contains(out.String(), "[s]") {
			t.Error("session option offered without a key")
		}
	}

	// Negative: EOF and cancelled context deny with an error.
	var out bytes.Buffer
	approve := NewApprover(NewLineReader(strings.NewReader(""), &out), 0)
	if d, err := approve(context.Background(), keyed); d != tools.DecisionDeny || err == nil {
		t.Errorf("expected EOF to deny with error, got %v %v", d, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if d, err := approve(ctx, keyed); d != tools.DecisionDeny || err == nil {
		t.Errorf("expected cancelled ctx to deny, got %v %v", d, err)
	}
}

func TestApproverShowsDiff(t *testing.T) {
	diff := "--- a/f.go\n+++ b/f.go\n@@ -1,3 +1,3 @@\n ctx\n-old\n+new\n" + strings.Repeat(" more\n", 50)
	req := tools.ApprovalRequest{Tool: "replace_in_file", Kind: tools.ActionWrite, Detail: "Edit f.go", Diff: diff}

	// Truncated diff offers [d]; choosing it prints the full diff and re-asks.
	var out bytes.Buffer
	d, err := NewApprover(NewLineReader(strings.NewReader("d\ny\n"), &out), 10)(context.Background(), req)
	if err != nil || d != tools.DecisionOnce {
		t.Fatalf("got %v %v", d, err)
	}
	o := out.String()
	if !strings.Contains(o, Red+"-old"+Reset) || !strings.Contains(o, Green+"+new"+Reset) {
		t.Errorf("diff not colourised: %q", o)
	}
	if !strings.Contains(o, "diff truncated") || !strings.Contains(o, "[d] show full diff") {
		t.Errorf("expected truncation notice and [d] option")
	}
	if strings.Count(o, " more") < 50 {
		t.Errorf("full diff not shown after [d]")
	}

	// Short diffs aren't truncated and don't offer [d].
	out.Reset()
	short := tools.ApprovalRequest{Tool: "t", Kind: tools.ActionWrite, Diff: "+x\n"}
	NewApprover(NewLineReader(strings.NewReader("y\n"), &out), 10)(context.Background(), short)
	if strings.Contains(out.String(), "[d]") {
		t.Error("[d] offered for an untruncated diff")
	}
}

func TestReadMultiline(t *testing.T) {
	cases := map[string]string{
		"single\n":                           "single",
		"first \\\nsecond\\\nthird\n":        "first \nsecond\nthird",
		"\"\"\"\nline 1\n\nline 3\n\"\"\"\n": "line 1\n\nline 3",
	}
	for in, want := range cases {
		got, err := NewLineReader(strings.NewReader(in), io.Discard).ReadInput(context.Background(), "> ")
		if err != nil || got != want {
			t.Errorf("ReadInput(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// Negative: EOF inside a block is an error, not a silent partial entry.
	if _, err := NewLineReader(strings.NewReader("\"\"\"\nunterminated\n"), io.Discard).ReadInput(context.Background(), "> "); err == nil {
		t.Error("expected error for unterminated block")
	}
}

func TestUserPrompter(t *testing.T) {
	var out bytes.Buffer
	lr := NewLineReader(strings.NewReader("2\nfree text\n9\n"), &out)
	ask := NewUserPrompter(lr)
	opts := []string{"alpha", "beta"}

	if got, _ := ask(context.Background(), "pick", opts); got != "beta" {
		t.Errorf("numeric choice = %q, want beta", got)
	}
	if got, _ := ask(context.Background(), "pick", opts); got != "free text" {
		t.Errorf("free text = %q", got)
	}
	// Out-of-range number is returned verbatim rather than panicking.
	if got, _ := ask(context.Background(), "pick", opts); got != "9" {
		t.Errorf("out-of-range = %q", got)
	}
	if _, err := ask(context.Background(), "pick", opts); err == nil {
		t.Error("expected EOF error")
	}
}

func TestFormatToolCallSanitizesAndTruncates(t *testing.T) {
	evil := "\x1b[2J\x1b]0;pwned\x07ls"
	s := FormatToolCall("run_shell_command", map[string]any{"command": evil})
	if strings.Contains(s, "\x1b[2J") || strings.Contains(s, "\x1b]0") || strings.Contains(s, "\x07") {
		t.Errorf("unsanitized output %q", s)
	}
	if !strings.Contains(s, "ls") {
		t.Errorf("command text lost: %q", s)
	}

	long := strings.Repeat("🐶", 40) // multi-byte; old code sliced mid-rune
	s = FormatToolCall("run_shell_command", map[string]any{"command": long})
	if !utf8.ValidString(s) {
		t.Errorf("truncation produced invalid UTF-8: %q", s)
	}
	if !strings.Contains(s, "...") {
		t.Errorf("expected ellipsis for long command")
	}
}

func TestFormatToolResult(t *testing.T) {
	s := FormatToolResult("grep", true, "line1\n\x1b[31mFAKE ✅ [other] done\x1b[0m\nline3")
	if strings.Count(s, "\n") != 1 {
		t.Errorf("summary should be collapsed to one line: %q", s)
	}
	if strings.Contains(s, "\x1b[31m") {
		t.Errorf("escape sequence leaked: %q", s)
	}
	if !strings.Contains(FormatToolResult("x", false, ""), "done") {
		t.Error("empty summary should render done badge")
	}
}

func TestSummarizeToolResponse(t *testing.T) {
	if s, ok := SummarizeToolResponse(map[string]any{"error": "boom"}); ok || s != "boom" {
		t.Errorf("error summary = %q %v", s, ok)
	}
	if s, ok := SummarizeToolResponse(map[string]any{"result": "fine"}); !ok || s != "fine" {
		t.Errorf("result summary = %q %v", s, ok)
	}
	if s, _ := SummarizeToolResponse(map[string]any{"content": "abcd"}); s != "4 bytes read" {
		t.Errorf("content summary = %q", s)
	}
	if s, ok := SummarizeToolResponse(nil); !ok || s != "" {
		t.Errorf("nil summary = %q %v", s, ok)
	}
}

// isolateHome points HOME at a temporary directory, so opening a workspace
// never reads or writes the real ~/.code_puppy. Call it before
// config.DefaultConfig, which resolves paths under HOME.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MODENV_PREFIX", "")
}

// modelFactory builds models by name, for tests of /model and pins.
type modelFactory = func(ctx context.Context, cfg *config.Config, name string) (model.LLM, error)

// openApp opens a workspace for cfg around llm and builds the App from it,
// as main does.
func openApp(t *testing.T, cfg *config.Config, llm model.LLM) *App {
	return openAppWith(t, cfg, core.Options{Model: llm})
}

func openAppWith(t *testing.T, cfg *config.Config, o core.Options) *App {
	t.Helper()
	cfg.Session.StorageDir = t.TempDir()
	w, err := core.Open(context.Background(), cfg, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return &App{Workspace: w, Printer: PrinterOptions{Out: io.Discard}}
}

// savedConfig reads back the config file that commands save to.
func savedConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join(os.Getenv("HOME"), ".code_puppy"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func newTestApp(t *testing.T, factory modelFactory) *App {
	t.Helper()
	isolateHome(t)
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = t.TempDir()
	cfg.Images.Dir = t.TempDir()
	app := openAppWith(t, cfg, core.Options{Model: runtime.NewMockLLM("mock-a"), NewModel: factory})
	app.Printer = PrinterOptions{}
	return app
}

func TestHandleCommandModel(t *testing.T) {
	ctx := context.Background()
	app := newTestApp(t, func(ctx context.Context, cfg *config.Config, name string) (model.LLM, error) {
		if name == "bad-model" {
			return nil, errors.New("unknown model")
		}
		return runtime.NewMockLLM(name), nil
	})

	// Positive: /model rebuilds the engine with the new LLM.
	if handled, err := HandleCommand(ctx, "/model mock-b", app); !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if app.Workspace.Engine().ModelName() != "mock-b" || app.Workspace.Config().CodePuppy.DefaultModel != "mock-b" {
		t.Errorf("model not switched: engine=%q cfg=%q", app.Workspace.Engine().ModelName(), app.Workspace.Config().CodePuppy.DefaultModel)
	}
	// Negative: factory failure leaves the current model in place.
	HandleCommand(ctx, "/model bad-model", app)
	if app.Workspace.Engine().ModelName() != "mock-b" || app.Workspace.Config().CodePuppy.DefaultModel != "mock-b" {
		t.Errorf("failed switch changed model to %q", app.Workspace.Engine().ModelName())
	}
}

func TestHandleCommandSetAndSession(t *testing.T) {
	ctx := context.Background()
	app := newTestApp(t, nil)

	HandleCommand(ctx, "/set agency=low", app)
	if app.Workspace.Config().CodePuppy.AgencyLevel != "low" {
		t.Errorf("agency not updated: %q", app.Workspace.Config().CodePuppy.AgencyLevel)
	}
	// Negative: invalid agency rejected.
	HandleCommand(ctx, "/set agency=reckless", app)
	if app.Workspace.Config().CodePuppy.AgencyLevel != "low" {
		t.Errorf("invalid agency accepted: %q", app.Workspace.Config().CodePuppy.AgencyLevel)
	}
	// Values with spaces are kept whole.
	HandleCommand(ctx, "/set owner_name=Ada Lovelace", app)
	if app.Workspace.Config().CodePuppy.OwnerName != "Ada Lovelace" {
		t.Errorf("owner_name = %q", app.Workspace.Config().CodePuppy.OwnerName)
	}

	// /session new records the active agent and becomes active.
	HandleCommand(ctx, "/agent helios", app)
	HandleCommand(ctx, "/session new", app)
	if a := app.Workspace.Storage().Active(); a == nil || a.Agent != "helios" {
		t.Errorf("new session should use active agent, got %+v", a)
	}
	// Negative: unknown agent keeps current one.
	HandleCommand(ctx, "/agent ghost", app)
	if app.Workspace.Engine().ActiveAgent() != "helios" {
		t.Errorf("unknown agent changed active agent")
	}
}

func TestHandleCommandRouting(t *testing.T) {
	app := newTestApp(t, nil)
	ctx := context.Background()
	if handled, _ := HandleCommand(ctx, "hello puppy", app); handled {
		t.Error("plain text should not be handled as a command")
	}
	if _, err := HandleCommand(ctx, "/quit", app); !errors.Is(err, ErrExit) {
		t.Errorf("expected ErrExit, got %v", err)
	}
	if handled, err := HandleCommand(ctx, "/nonsense", app); !handled || err != nil {
		t.Errorf("unknown command: handled=%v err=%v", handled, err)
	}
	// /model without a factory is reported, not a panic.
	if handled, _ := HandleCommand(ctx, "/model x", app); !handled {
		t.Error("expected /model to be handled")
	}
}

func TestRunREPLUsesCurrentSession(t *testing.T) {
	app := newTestApp(t, nil)
	var out bytes.Buffer
	app.Input = NewLineReader(strings.NewReader("hello\n/session new\nsecond\n/exit\n"), &out)
	if err := RunREPL(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	// The second prompt must be recorded in the session created by /session new.
	active := app.Workspace.Storage().Active()
	if active == nil || len(active.Messages) == 0 || active.Messages[0].Content != "second" {
		t.Errorf("expected 'second' in the new active session, got %+v", active)
	}

	// EOF ends the REPL cleanly.
	app.Input = NewLineReader(strings.NewReader(""), &out)
	if err := RunREPL(context.Background(), app); err != nil {
		t.Errorf("EOF should end REPL without error, got %v", err)
	}
}

func TestLineReaderCancelDoesNotLoseInput(t *testing.T) {
	pr, pw := io.Pipe()
	var out bytes.Buffer
	lr := NewLineReader(pr, &out)

	// Negative: a cancelled read returns promptly with ctx error.
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { _, err := lr.Ask(ctx, "> "); errCh <- err }()
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled Ask did not return")
	}

	// Positive: the line typed afterwards goes to the next reader, not the abandoned one.
	go pw.Write([]byte("kept\n"))
	got, err := lr.ReadLine("> ")
	if err != nil || got != "kept" {
		t.Fatalf("expected 'kept', got %q %v", got, err)
	}
	pw.Close()
}

func TestLineReaderSerializesConcurrentAsks(t *testing.T) {
	var out syncBuffer
	lr := NewLineReader(strings.NewReader("a\nb\n"), &out)
	var wg sync.WaitGroup
	answers := make(chan string, 2)
	for _, q := range []string{"Q1:\n", "Q2:\n"} {
		wg.Add(1)
		go func(q string) {
			defer wg.Done()
			ans, _ := lr.Ask(context.Background(), q)
			answers <- q + ans
		}(q)
	}
	wg.Wait()
	close(answers)
	got := map[string]bool{}
	for a := range answers {
		got[a] = true
	}
	// Each prompt must be paired with exactly one full answer.
	if !(got["Q1:\na"] && got["Q2:\nb"]) && !(got["Q1:\nb"] && got["Q2:\na"]) {
		t.Errorf("answers mixed up: %v", got)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// blockingLLM blocks until the request context is cancelled.
type blockingLLM struct{ started chan struct{} }

func (b *blockingLLM) Name() string { return "blocking" }
func (b *blockingLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		select {
		case b.started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		yield(nil, ctx.Err())
	}
}

func TestREPLInterruptAtPromptExits(t *testing.T) {
	app := newTestApp(t, nil)
	pr, pw := io.Pipe()
	defer pw.Close()
	app.Input = NewLineReader(pr, io.Discard)
	sigs := make(chan os.Signal, 1)
	app.Interrupts = sigs

	done := make(chan error, 1)
	go func() { done <- RunREPL(context.Background(), app) }()
	time.Sleep(100 * time.Millisecond)
	sigs <- os.Interrupt
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected clean exit, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("REPL did not exit on interrupt while idle")
	}
}

func TestREPLInterruptCancelsTurnOnly(t *testing.T) {
	app := newTestApp(t, nil)
	llm := &blockingLLM{started: make(chan struct{}, 1)}
	if err := app.Workspace.Engine().SetModel(context.Background(), llm); err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	app.Input = NewLineReader(pr, io.Discard)
	sigs := make(chan os.Signal, 1)
	app.Interrupts = sigs

	done := make(chan error, 1)
	go func() { done <- RunREPL(context.Background(), app) }()

	go pw.Write([]byte("long task\n"))
	select {
	case <-llm.started:
	case <-time.After(3 * time.Second):
		t.Fatal("turn never started")
	}
	sigs <- os.Interrupt

	// The REPL must survive the interrupt and keep accepting input.
	go pw.Write([]byte("/exit\n"))
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("unexpected error %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("REPL did not return to the prompt after interrupting a turn")
	}
	pw.Close()
}
