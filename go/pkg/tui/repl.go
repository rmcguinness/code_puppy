package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"

	"github.com/retail-cortex/code_puppy/pkg/agents"
	"github.com/retail-cortex/code_puppy/pkg/audit"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/i18n"
	"github.com/retail-cortex/code_puppy/pkg/images"
	"github.com/retail-cortex/code_puppy/pkg/runtime"
	"github.com/retail-cortex/code_puppy/pkg/session"
	"github.com/retail-cortex/code_puppy/pkg/skills"
	"github.com/retail-cortex/code_puppy/pkg/textutil"
	"github.com/retail-cortex/code_puppy/pkg/tools"
	"google.golang.org/adk/v2/model"
	sessionsdk "google.golang.org/adk/v2/session"
)

// ModelFactory builds an LLM for the given model name (used by /model).
type ModelFactory func(ctx context.Context, cfg *config.Config, modelName string) (model.LLM, error)

// App bundles the dependencies the REPL and slash commands operate on.
type App struct {
	Version  string
	Cfg      *config.Config
	Engine   *runtime.Engine
	Agents   *agents.Registry
	Skills   *skills.Provider
	Storage  *session.Storage
	Input    Input
	NewModel ModelFactory
	// Tools gives commands access to checkpoints, approvals, MCP and hooks.
	Tools *tools.Registry
	// Processes are the background processes to account for on exit.
	Processes *tools.ProcessManager
	// SandboxSummary describes the active sandbox (shown by /sandbox).
	SandboxSummary []string
	// Printer configures output rendering; Transcript is set per turn.
	Printer PrinterOptions
	// ReloadMemory re-reads project instruction files and returns their paths.
	ReloadMemory func(ctx context.Context) ([]string, error)
	// Attachments are images to send with the next prompt (/attach, /paste,
	// --image).
	Attachments []*images.Image
	// Locales are the loaded translation catalogs (nil: the built-in ones).
	Locales *i18n.Bundle
	// SetLocale applies a new interface language to the model's reply
	// instructions and saves it to the config; it returns the file written.
	SetLocale func(ctx context.Context, l *i18n.Localizer) (string, error)
	// Interrupts delivers Ctrl+C. If nil, RunREPL subscribes to os.Interrupt
	// itself. At the prompt an interrupt starts exit (confirming if background
	// processes run); during a turn it cancels only that turn.
	Interrupts <-chan os.Signal
}

// Printer renders agent events: model text (optionally as Markdown), tool
// activity, and a spinner while waiting.
type Printer struct {
	out        io.Writer
	md         *markdownStream
	spin       *Spinner
	transcript *strings.Builder
	streamed   bool // partial text was printed since the last final event
}

// PrinterOptions configure a Printer.
type PrinterOptions struct {
	Out        io.Writer
	Markdown   bool // render Markdown (requires a terminal)
	Theme      string
	Width      int
	Spinner    bool
	Transcript *strings.Builder // collects final model text
}

// NewPrinter creates a Printer; Markdown falls back to plain text on error.
func NewPrinter(o PrinterOptions) *Printer {
	if o.Out == nil {
		o.Out = os.Stdout
	}
	p := &Printer{out: o.Out, transcript: o.Transcript, spin: NewSpinner(o.Out, o.Spinner)}
	if o.Markdown {
		if md, err := newMarkdownStream(o.Out, o.Theme, o.Width); err == nil {
			p.md = md
		}
	}
	return p
}

// NewEventPrinter returns a plain-text event handler that accumulates model
// text into transcript when non-nil.
func NewEventPrinter(transcript *strings.Builder) runtime.EventHandler {
	return NewPrinter(PrinterOptions{Transcript: transcript}).Handle
}

// Begin marks the start of a turn.
func (p *Printer) Begin() { p.spin.Start(i18n.T("spinner.thinking")) }

// End flushes buffered output at the end of a turn.
func (p *Printer) End() {
	p.spin.Stop()
	if p.md != nil {
		p.md.Flush()
	}
}

func (p *Printer) text(s string) {
	p.spin.Stop()
	s = safe(s)
	if p.md != nil {
		p.md.Write(s)
		return
	}
	io.WriteString(p.out, s)
}

func (p *Printer) flushText() {
	if p.md != nil {
		p.md.Flush()
	}
}

// Handle is a runtime.EventHandler.
func (p *Printer) Handle(ev *sessionsdk.Event) error {
	if ev.Content == nil {
		return nil
	}
	for _, part := range ev.Content.Parts {
		if part.Text != "" && !part.Thought {
			switch {
			case ev.Partial:
				p.text(part.Text)
				p.streamed = true
			case p.streamed:
				// Final event repeating text already streamed.
			default:
				p.text(part.Text)
			}
			if !ev.Partial && p.transcript != nil {
				p.transcript.WriteString(part.Text)
			}
		}
		if part.FunctionCall != nil {
			p.spin.Stop()
			p.flushText()
			fmt.Fprint(p.out, FormatToolCall(part.FunctionCall.Name, part.FunctionCall.Args))
		}
		if part.FunctionResponse != nil {
			p.spin.Stop()
			summary, ok := SummarizeToolResponse(part.FunctionResponse.Response)
			fmt.Fprint(p.out, FormatToolResult(part.FunctionResponse.Name, ok, summary))
			p.spin.Start(i18n.T("spinner.working"))
		}
	}
	if !ev.Partial {
		p.streamed = false
	}
	return nil
}

// RunREPL runs the interactive REPL prompt loop until /exit, EOF, or ctx is cancelled.
func RunREPL(ctx context.Context, app *App) error {
	PrintBanner(app.Version, app.Engine.ActiveAgent(), app.Engine.ModelName())
	if len(app.SandboxSummary) > 0 {
		fmt.Printf("🛡️  %s%s%s\n", Dim, safe(app.SandboxSummary[0]), Reset)
		fmt.Printf("   %s%s%s\n\n", Dim, i18n.T("repl.hint"), Reset)
	}

	if app.Storage.Active() == nil {
		if _, err := app.Storage.CreateSession("", i18n.T("session.interactive_title"), app.Engine.ActiveAgent()); err != nil {
			return fmt.Errorf("failed to create session: %w", err)
		}
	}

	interrupts := app.Interrupts
	if interrupts == nil {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		defer signal.Stop(ch)
		interrupts = ch
	}

	goodbye := func() error {
		fmt.Printf("\n🐾 %s%s%s\n", Cyan, i18n.T("repl.goodbye"), Reset)
		return nil
	}
	exitPrompt := ExitPrompt{CanPrompt: true, AllowCancel: true}

	for {
		prompt := fmt.Sprintf("%s🐶 [%s]> %s", Bold+Green, app.Engine.ActiveAgent(), Reset)
		idleCtx, stopIdle := cancelOnSignal(ctx, interrupts)
		line, err := app.Input.ReadInput(idleCtx, prompt)
		stopIdle()
		switch {
		case err == nil:
		case ctx.Err() != nil:
			// SIGTERM: no prompt; deferred cleanup kills background processes.
			return goodbye()
		case errors.Is(err, io.EOF):
			ConfirmExit(ctx, app.Input, app.Processes, interrupts, ExitPrompt{})
			return goodbye()
		case errors.Is(err, context.Canceled): // Ctrl+C at the prompt
			if ConfirmExit(ctx, app.Input, app.Processes, interrupts, exitPrompt) {
				return goodbye()
			}
			continue
		default:
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		handled, err := HandleCommand(ctx, line, app)
		if errors.Is(err, ErrExit) {
			if ConfirmExit(ctx, app.Input, app.Processes, interrupts, exitPrompt) {
				return goodbye()
			}
			continue
		}
		if handled {
			continue
		}

		// Re-read each turn: /session new and /session load switch sessions.
		active := app.Storage.Active()
		if active == nil {
			fmt.Printf("%s❌ %s%s\n", Red, i18n.T("session.none_active"), Reset)
			continue
		}
		runTurn(ctx, app, active.ID, line, interrupts)
	}
}

// runTurn sends one prompt to the agent and renders the result.
func runTurn(ctx context.Context, app *App, sessionID, line string, interrupts <-chan os.Signal) {
	if app.Tools != nil {
		if reason := app.Tools.ScriptHooks().PromptSubmit(ctx, sessionID, line); reason != "" {
			fmt.Printf("%s⛔ %s%s\n", Red, i18n.T("repl.prompt_blocked", "reason", safe(reason)), Reset)
			return
		}
	}
	attached, ok := takeAttachments(app, line)
	if !ok {
		return
	}
	if app.Tools != nil {
		app.Tools.Checkpoints().Begin(textutil.Ellipsize(strings.Join(strings.Fields(line), " "), 60))
		app.Tools.Hooks().Audit().Log(audit.Entry{Kind: audit.KindPrompt, Session: sessionID, Detail: line})
	}
	warnOnErr(app.Storage.AddMessage("user", line+AttachmentNote(attached)))

	fmt.Println()
	var modelOutput strings.Builder
	opts := app.Printer
	opts.Transcript = &modelOutput
	printer := NewPrinter(opts)
	before := app.Engine.Usage(sessionID)

	turnCtx, stopTurn := cancelOnSignal(ctx, interrupts)
	if ih, ok := app.Input.(interface{ SetInterruptHandler(func()) }); ok {
		ih.SetInterruptHandler(stopTurn)
		defer ih.SetInterruptHandler(nil)
	}
	printer.Begin()
	var execOpts []runtime.ExecOption
	for _, img := range attached {
		execOpts = append(execOpts, runtime.WithAttachments(images.Part(img)))
	}
	streamErr := app.Engine.Execute(turnCtx, sessionID, line, printer.Handle, execOpts...)
	printer.End()
	turnInterrupted := turnCtx.Err() != nil
	stopTurn()
	switch {
	case streamErr != nil && turnInterrupted:
		fmt.Printf("\n%s⏹  %s%s\n", Yellow, i18n.T("repl.interrupted"), Reset)
	case streamErr != nil:
		fmt.Printf("\n%s❌ %s%s\n", Red, i18n.T("repl.error", "error", safe(streamErr.Error())), Reset)
	default:
		fmt.Println()
	}
	if line := UsageLine(before, app.Engine.Usage(sessionID)); line != "" {
		fmt.Printf("%s%s%s\n", Dim, line, Reset)
	}

	if modelOutput.Len() > 0 {
		warnOnErr(app.Storage.AddMessage("model", modelOutput.String()))
	}
	fmt.Println()
}

// UsageLine summarises a turn's usage: tokens in/out, context size and cost.
func UsageLine(before, after runtime.Usage) string {
	if after.Calls == before.Calls {
		return ""
	}
	s := "↳ " + i18n.T("usage.line", "input", humanTokens(after.Input-before.Input), "output", humanTokens(after.Output-before.Output), "context", humanTokens(after.LastPrompt))
	if after.Priced {
		s += " · " + i18n.T("usage.cost", "cost", fmt.Sprintf("$%.4f", after.CostUSD-before.CostUSD), "total", fmt.Sprintf("$%.4f", after.CostUSD))
	}
	return s
}

func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// cancelOnSignal returns a context cancelled when parent is done or a signal
// arrives on sigs. Call stop (safe to call more than once, e.g. from an
// interrupt handler and again at the end of a turn) to cancel the context
// and release the watcher goroutine.
func cancelOnSignal(parent context.Context, sigs <-chan os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigs:
			cancel()
		case <-done:
		case <-ctx.Done():
		}
	}()
	var once sync.Once
	return ctx, func() {
		once.Do(func() { close(done) })
		cancel()
	}
}

func warnOnErr(err error) {
	if err != nil {
		fmt.Printf("%s⚠️  %s%s\n", Yellow, i18n.T("session.save_failed", "error", err), Reset)
	}
}
