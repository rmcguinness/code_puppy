package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/agents"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/runtime"
	"github.com/retail-cortex/code_puppy/pkg/session"
	"github.com/retail-cortex/code_puppy/pkg/skills"
	"github.com/retail-cortex/code_puppy/pkg/tools"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

func TestSteerTrigger(t *testing.T) {
	cases := []struct {
		in      string
		prefill string
		trigger bool
	}{
		{"fix", "fix", true},
		{"é", "é", true},
		{"\x14", "", true},      // Ctrl+T
		{"\x14ab", "ab", true},  // Ctrl+T then typing
		{"\r", "", false},       // Enter
		{"\x1b[A", "", false},   // arrow key
		{"\x03\x04", "", false}, // other control keys
		{"a\rb", "ab", true},    // control bytes dropped
	}
	for _, c := range cases {
		p, trig := steerTrigger([]byte(c.in))
		if p != c.prefill || trig != c.trigger {
			t.Errorf("%q: got (%q, %v), want (%q, %v)", c.in, p, trig, c.prefill, c.trigger)
		}
	}
}

// chanKeys is a keyTerm fed through a channel.
type chanKeys struct {
	in            chan []byte
	pending       []byte
	entered, left atomic.Int32
	inMode        atomic.Bool
}

func newChanKeys() *chanKeys { return &chanKeys{in: make(chan []byte, 8)} }

func (c *chanKeys) enter() error { c.entered.Add(1); c.inMode.Store(true); return nil }
func (c *chanKeys) leave() error { c.left.Add(1); c.inMode.Store(false); return nil }
func (c *chanKeys) ready(d time.Duration) (bool, error) {
	if c.pending != nil {
		return true, nil
	}
	select {
	case b := <-c.in:
		c.pending = b
		return true, nil
	case <-time.After(d):
		return false, nil
	}
}
func (c *chanKeys) read(p []byte) (int, error) {
	n := copy(p, c.pending)
	c.pending = nil
	return n, nil
}

func TestKeyWatcherTriggersPausesAndRestores(t *testing.T) {
	keys := newChanKeys()
	got := make(chan string, 4)
	w := startKeyWatcher(keys, func(prefill string) {
		if keys.inMode.Load() {
			t.Error("onKey ran with the terminal still in key mode")
		}
		got <- prefill
	})

	keys.in <- []byte("\r") // ignored
	keys.in <- []byte("fix")
	select {
	case p := <-got:
		if p != "fix" {
			t.Fatalf("prefill %q", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("typing did not open the steer prompt")
	}

	resume, err := w.pause(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if keys.inMode.Load() {
		t.Fatal("terminal still in key mode while paused")
	}
	keys.in <- []byte("x")
	select {
	case p := <-got:
		t.Fatalf("key handled while paused: %q", p)
	case <-time.After(200 * time.Millisecond):
	}
	resume()
	select {
	case p := <-got:
		if p != "x" {
			t.Fatalf("after resume: %q", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("key typed during the pause was lost")
	}

	w.close()
	if keys.inMode.Load() || keys.entered.Load() != keys.left.Load() {
		t.Fatalf("terminal not restored: entered %d, left %d", keys.entered.Load(), keys.left.Load())
	}
	if resume, err := w.pause(context.Background()); err != nil || resume == nil {
		t.Fatal("pause after close must be a no-op")
	}
}

// A prompt during the turn (an approval) waits while a steer message is
// being typed, and gives up if its context ends first.
func TestKeyWatcherPauseWaitsForOpenSteerPrompt(t *testing.T) {
	keys := newChanKeys()
	release := make(chan struct{})
	w := startKeyWatcher(keys, func(string) { <-release })
	defer w.close()
	keys.in <- []byte("a")
	time.Sleep(100 * time.Millisecond) // steer prompt now "open"

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := w.pause(ctx); err == nil {
		t.Fatal("pause succeeded while a steer prompt was open")
	}
	close(release)
	resume, err := w.pause(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resume()
}

func TestPrinterPauseHoldsOutputAndOnlyRespinsIfSpinning(t *testing.T) {
	var out bytes.Buffer
	p := NewPrinter(PrinterOptions{Out: &out, Spinner: true})
	p.text("before ")
	p.Pause()
	p.text("during ")
	if strings.Contains(out.String(), "during") {
		t.Fatal("output written while paused")
	}
	p.Resume()
	if !strings.Contains(out.String(), "before during ") {
		t.Fatalf("held output not flushed in order: %q", out.String())
	}
	if p.spin.Running() {
		t.Fatal("spinner restarted mid-text")
	}

	p.spin.Start("working")
	p.Pause()
	p.Resume()
	if !p.spin.Running() {
		t.Fatal("spinner not restored after pause")
	}
	p.End()
}

// fakeSteerInput types one message during the first turn it watches.
type fakeSteerInput struct {
	*LineReader
	message string
	once    sync.Once
	typed   chan struct{} // closed once the message has been handled
}

func (f *fakeSteerInput) WatchKeys(onKey func(string)) func() {
	started := false
	f.once.Do(func() {
		started = true
		go func() {
			onKey(f.message[:1])
			close(f.typed)
		}()
	})
	if !started {
		return func() {}
	}
	return func() { <-f.typed }
}

func (f *fakeSteerInput) AskSteer(_ context.Context, _ string, prefill string) (string, error) {
	return prefill + f.message[1:], nil
}

// gatedLLM holds its first call until the user's message is handled, so
// the test doesn't depend on timing.
type gatedLLM struct {
	*runtime.MockLLM
	gate <-chan struct{}
}

func (g *gatedLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if g.Calls() == 0 {
		select {
		case <-g.gate:
		case <-time.After(5 * time.Second):
		}
	}
	return g.MockLLM.GenerateContent(ctx, req, stream)
}

func newSteerApp(t *testing.T, message string, cfgFn func(*config.Config), replies ...*genai.Content) (*App, *runtime.MockLLM) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = t.TempDir()
	cfg.Images.Dir = t.TempDir()
	cfg.Audit.Enabled = false
	cfg.CodePuppy.AutoApprove = true
	if cfgFn != nil {
		cfgFn(cfg)
	}
	agentReg, _ := agents.NewRegistry()
	skillProv, _ := skills.NewProvider()
	reg, err := tools.NewRegistry(cfg, agentReg, skillProv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	in := &fakeSteerInput{LineReader: NewLineReader(strings.NewReader(""), io.Discard), message: message, typed: make(chan struct{})}
	llm := runtime.NewMockLLM("gemini-2.5-flash", replies...)
	eng, err := runtime.NewEngine(context.Background(), cfg, agentReg, skillProv, reg, &gatedLLM{MockLLM: llm, gate: in.typed})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := session.NewStorage(t.TempDir())
	st.CreateSession("", "t", "code-puppy")
	return &App{Cfg: cfg, Engine: eng, Agents: agentReg, Skills: skillProv, Storage: st, Tools: reg, Input: in,
		Processes: reg.Processes(), Printer: PrinterOptions{Out: io.Discard}}, llm
}

func listFilesCall() *genai.Content {
	return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "list_files", Args: map[string]any{}}}}}
}

func userMessages(st *session.Storage) []string {
	var out []string
	for _, m := range st.Active().Messages {
		if m.Role == "user" {
			out = append(out, m.Content)
		}
	}
	return out
}

func TestREPLSteerReachesTheAgentMidTurn(t *testing.T) {
	app, llm := newSteerApp(t, "use tabs", nil, listFilesCall(), genai.NewContentFromText("done", genai.RoleModel))
	runTurn(context.Background(), app, app.Storage.Active().ID, "reformat", nil, turnOptions{})

	if n := llm.Calls(); n != 2 {
		t.Fatalf("want 2 model calls, got %d", n)
	}
	found := false
	for _, c := range llm.Requests[1].Contents {
		for _, p := range c.Parts {
			if r := p.FunctionResponse; r != nil && r.Response[runtime.SteerKey] == "use tabs" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("steer message not in the tool result the model read")
	}
	if got := fmt.Sprint(userMessages(app.Storage)); got != "[reformat use tabs]" {
		t.Fatalf("transcript user messages = %s", got)
	}
}

func TestREPLLateSteerIsSentAsTheNextPrompt(t *testing.T) {
	app, llm := newSteerApp(t, "and add a test", nil,
		genai.NewContentFromText("done", genai.RoleModel),       // no tool call: the message can't ride along
		genai.NewContentFromText("test added", genai.RoleModel)) // the follow-up turn
	runTurn(context.Background(), app, app.Storage.Active().ID, "fix the bug", nil, turnOptions{})

	if n := llm.Calls(); n != 2 {
		t.Fatalf("want a follow-up turn (2 model calls), got %d", n)
	}
	last := llm.Requests[1].Contents[len(llm.Requests[1].Contents)-1]
	if last.Role != genai.RoleUser || !strings.Contains(last.Parts[0].Text, "and add a test") {
		t.Fatalf("follow-up prompt = %+v", last)
	}
	if got := fmt.Sprint(userMessages(app.Storage)); got != "[fix the bug and add a test]" {
		t.Fatalf("message recorded twice or not at all: %s", got)
	}
}

func TestREPLSteerGoesThroughPromptHooks(t *testing.T) {
	app, llm := newSteerApp(t, "my password is hunter2", func(c *config.Config) {
		c.Hooks.PromptSubmit = []config.HookConfig{{Command: `grep -q password && { echo "no secrets" >&2; exit 2; }; exit 0`}}
	}, listFilesCall(), genai.NewContentFromText("done", genai.RoleModel))
	runTurn(context.Background(), app, app.Storage.Active().ID, "reformat", nil, turnOptions{})

	for _, c := range llm.Requests[len(llm.Requests)-1].Contents {
		for _, p := range c.Parts {
			if r := p.FunctionResponse; r != nil && r.Response[runtime.SteerKey] != nil {
				t.Fatal("blocked message reached the agent")
			}
		}
	}
	if got := fmt.Sprint(userMessages(app.Storage)); strings.Contains(got, "hunter2") {
		t.Fatalf("blocked message recorded: %s", got)
	}
}
