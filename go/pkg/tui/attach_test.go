package tui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/pkg/agents"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/images"
	"github.com/retail-cortex/code_puppy/pkg/runtime"
	"github.com/retail-cortex/code_puppy/pkg/session"
	"github.com/retail-cortex/code_puppy/pkg/skills"
	"github.com/retail-cortex/code_puppy/pkg/tools"
	"google.golang.org/genai"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)))
	return buf.Bytes()
}

func newImageApp(t *testing.T, replies ...string) (*App, *runtime.MockLLM) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = t.TempDir()
	cfg.Images.Dir = t.TempDir()
	cfg.Audit.Enabled = false
	agentReg, _ := agents.NewRegistry()
	skillProv, _ := skills.NewProvider()
	reg, err := tools.NewRegistry(cfg, agentReg, skillProv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.Close() })
	var contents []*genai.Content
	for _, r := range replies {
		contents = append(contents, genai.NewContentFromText(r, genai.RoleModel))
	}
	llm := runtime.NewMockLLM("gemini-2.5-flash", contents...)
	eng, err := runtime.NewEngine(context.Background(), cfg, agentReg, skillProv, reg, llm)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := session.NewStorage(t.TempDir())
	st.CreateSession("", "t", "code-puppy")
	return &App{Cfg: cfg, Engine: eng, Agents: agentReg, Skills: skillProv, Storage: st, Tools: reg,
		Processes: reg.Processes(), Printer: PrinterOptions{Out: io.Discard}}, llm
}

func sentImages(llm *runtime.MockLLM) int {
	n := 0
	last := llm.Requests[len(llm.Requests)-1].Contents
	for _, p := range last[len(last)-1].Parts {
		if p.InlineData != nil {
			n++
		}
	}
	return n
}

func TestAttachCommandsAndMentions(t *testing.T) {
	app, llm := newImageApp(t, "one", "two", "three")
	ws := app.Tools.Workspace().Dir()
	os.WriteFile(filepath.Join(ws, "a.png"), pngOf(t, 40, 30), 0o644)
	os.WriteFile(filepath.Join(ws, "b.png"), pngOf(t, 50, 30), 0o644)
	ctx := context.Background()

	out := captureStdout(t, func() {
		HandleCommand(ctx, "/attach", app)        // usage
		HandleCommand(ctx, "/attach @a.png", app) // queued
		HandleCommand(ctx, "/attach a.png", app)  // duplicate
		HandleCommand(ctx, "/attach missing.png", app)
		HandleCommand(ctx, "/attach", app) // lists
	})
	for _, want := range []string{"Usage: /attach <image>", "a.png 40×30", "will be sent with your next message", "already queued", "Could not attach missing.png", "1 image waiting"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	// Queue plus an inline mention: both go with the prompt, then the
	// queue is empty.
	out = captureStdout(t, func() {
		runTurn(ctx, app, app.Storage.Active().ID, "compare these with @b.png", nil, false)
	})
	if n := sentImages(llm); n != 2 {
		t.Errorf("sent %d images, want 2", n)
	}
	if len(app.Attachments) != 0 || !strings.Contains(out, "📎 b.png 50×30") {
		t.Errorf("queue not emptied / not shown:\n%s", out)
	}
	msgs := app.Storage.Active().Messages
	if last := msgs[len(msgs)-2].Content; !strings.Contains(last, "[images: a.png, b.png]") {
		t.Errorf("transcript note missing: %q", last)
	}

	// A bad mention stops the turn and keeps the queue.
	HandleCommand(ctx, "/attach a.png", app)
	calls := len(llm.Requests)
	out = captureStdout(t, func() {
		runTurn(ctx, app, app.Storage.Active().ID, "what about @nope.png", nil, false)
	})
	if len(llm.Requests) != calls || len(app.Attachments) != 1 || !strings.Contains(out, "Nothing was sent") {
		t.Errorf("bad mention should not send (calls %d→%d, queue %d):\n%s", calls, len(llm.Requests), len(app.Attachments), out)
	}
	out = captureStdout(t, func() { HandleCommand(ctx, "/attach clear", app) })
	if len(app.Attachments) != 0 || !strings.Contains(out, "Removed 1 queued image.") {
		t.Errorf("clear: %s", out)
	}

	// Plain prompts send no images.
	runTurn(ctx, app, app.Storage.Active().ID, "just text, mail me@example.png", nil, false)
	if n := sentImages(llm); n != 0 {
		t.Errorf("plain prompt sent %d images", n)
	}
}

func TestAttachRespectsSandbox(t *testing.T) {
	app, _ := newImageApp(t)
	outside := filepath.Join(t.TempDir(), "secret.png")
	os.WriteFile(outside, pngOf(t, 4, 4), 0o644)
	out := captureStdout(t, func() { HandleCommand(context.Background(), "/attach "+outside, app) })
	if len(app.Attachments) != 0 || !strings.Contains(out, "Could not attach") {
		t.Errorf("outside file attached:\n%s", out)
	}
}

func TestPaste(t *testing.T) {
	defer func(f func(context.Context) ([]byte, error)) { images.ReadClipboard = f }(images.ReadClipboard)
	app, llm := newImageApp(t, "a screenshot")
	ctx := context.Background()

	images.ReadClipboard = func(context.Context) ([]byte, error) { return nil, images.ErrNoClipboardImage }
	out := captureStdout(t, func() { HandleCommand(ctx, "/paste", app) })
	if !strings.Contains(out, "clipboard has no image") || len(app.Attachments) != 0 {
		t.Errorf("empty clipboard: %s", out)
	}
	images.ReadClipboard = func(context.Context) ([]byte, error) { return []byte("plain text"), nil }
	out = captureStdout(t, func() { HandleCommand(ctx, "/paste", app) })
	if !strings.Contains(out, "not a PNG") {
		t.Errorf("non-image clipboard: %s", out)
	}
	images.ReadClipboard = func(context.Context) ([]byte, error) { return pngOf(t, 20, 10), nil }
	out = captureStdout(t, func() { HandleCommand(ctx, "/paste", app) })
	if len(app.Attachments) != 1 || !strings.HasPrefix(app.Attachments[0].Name, "clipboard-") {
		t.Fatalf("paste: %s", out)
	}
	runTurn(ctx, app, app.Storage.Active().ID, "what is this?", nil, false)
	if sentImages(llm) != 1 {
		t.Error("pasted image not sent")
	}

	images.ReadClipboard = func(context.Context) ([]byte, error) { return nil, errors.New("osascript exploded") }
	out = captureStdout(t, func() { HandleCommand(ctx, "/paste", app) })
	if !strings.Contains(out, "osascript exploded") {
		t.Errorf("unexpected errors should be shown: %s", out)
	}
}

func TestAttachDisabled(t *testing.T) {
	app := newTestApp(t, nil) // no Tools
	out := captureStdout(t, func() {
		HandleCommand(context.Background(), "/paste", app)
		HandleCommand(context.Background(), "/attach x.png", app)
	})
	if !strings.Contains(out, "Images are disabled") || !strings.Contains(out, "image support is disabled") {
		t.Errorf("disabled: %s", out)
	}
}
