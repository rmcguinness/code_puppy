package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"

	"github.com/retail-cortex/code_puppy/internal/agents"
	"github.com/retail-cortex/code_puppy/internal/audit"
	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/i18n"
	"github.com/retail-cortex/code_puppy/internal/images"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"github.com/retail-cortex/code_puppy/internal/session"
	"github.com/retail-cortex/code_puppy/internal/skills"
	"github.com/retail-cortex/code_puppy/internal/textutil"
	"github.com/retail-cortex/code_puppy/internal/tools"
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
	// SaveAgentModel records a model pin (ref "" removes it) in the config
	// file and returns the file written; nil means pins aren't saved.
	SaveAgentModel func(agent, ref string) (string, error)
	// SaveModelSettings records a model's settings (the zero value removes
	// them) in the config file; nil means they aren't saved.
	SaveModelSettings func(model string, s config.ModelSettings) (string, error)
	// SetLocale applies a new interface language to the model's reply
	// instructions and saves it to the config; it returns the file written.
	SetLocale func(ctx context.Context, l *i18n.Localizer) (string, error)
	// TerminalTitle shows the session's name in the terminal window title.
	TerminalTitle bool
	// Interrupts delivers Ctrl+C. If nil, RunREPL subscribes to os.Interrupt
	// itself. At the prompt an interrupt starts exit (confirming if background
	// processes run); during a turn it cancels only that turn.
	Interrupts <-chan os.Signal
}

// Printer renders agent events: model text (optionally as Markdown), tool
// activity, and a spinner while waiting.
type Printer struct {
	out        io.Writer
	pause      *pausableWriter
	respin     bool // Resume restarts the spinner (it was showing at Pause)
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
	pw := &pausableWriter{w: o.Out}
	p := &Printer{out: pw, pause: pw, transcript: o.Transcript, spin: NewSpinner(pw, o.Spinner)}
	if o.Markdown {
		if md, err := newMarkdownStream(pw, o.Theme, o.Width); err == nil {
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

// Pause holds back output (e.g. while the user types a steer message) until
// Resume, which prints what arrived meanwhile.
func (p *Printer) Pause() {
	p.respin = p.spin.Running()
	p.spin.Stop()
	p.pause.hold()
}

// Resume prints held-back output and continues normally. The spinner comes
// back only if it was showing: restarting it mid-sentence would erase the
// partly printed line.
func (p *Printer) Resume() {
	p.pause.release()
	if p.respin {
		p.spin.Start(i18n.T("spinner.working"))
	}
}

// pausableWriter buffers writes while held. Printer output arrives from the
// turn's goroutine while the steer prompt runs on another.
type pausableWriter struct {
	mu   sync.Mutex
	w    io.Writer
	held bool
	buf  bytes.Buffer
}

func (p *pausableWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held {
		return p.buf.Write(b)
	}
	return p.w.Write(b)
}

func (p *pausableWriter) hold() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held = true
}

func (p *pausableWriter) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held = false
	if p.buf.Len() > 0 {
		p.w.Write(p.buf.Bytes())
		p.buf.Reset()
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
		fmt.Printf("   %s%s%s\n", Dim, i18n.T("repl.hint"), Reset)
		if _, ok := app.Input.(steerInput); ok {
			fmt.Printf("   %s%s%s\n", Dim, i18n.T("steer.hint"), Reset)
		}
		fmt.Println()
	}

	if app.Storage.Active() == nil {
		if _, err := app.Storage.CreateSession("", "", app.Engine.ActiveAgent()); err != nil { // named after its first prompt
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

	var shownTitle string
	defer func() {
		if shownTitle != "" {
			fmt.Print("\033]0;\007") // give the terminal its own title back
		}
	}()
	goodbye := func() error {
		fmt.Printf("\n🐾 %s%s%s\n", Cyan, i18n.T("repl.goodbye"), Reset)
		if a := app.Storage.Active(); a != nil && a.MessageCount > 0 {
			fmt.Printf("%s%s%s\n", Dim, i18n.T("repl.resume_hint", "command", "code-puppy --resume="+a.ID), Reset)
		}
		return nil
	}
	exitPrompt := ExitPrompt{CanPrompt: true, AllowCancel: true}

	for {
		if app.TerminalTitle {
			if t := "🐶 " + sessionTitle(app.Storage.Active()); t != shownTitle {
				fmt.Printf("\033]0;%s\007", safe(t))
				shownTitle = t
			}
		}
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
		if cmd, ok := strings.CutPrefix(line, "!"); ok {
			runShellPassthrough(ctx, app, cmd, interrupts)
			continue
		}
		if q, ok := strings.CutPrefix(line, "/btw"); ok && (q == "" || q[0] == ' ') {
			active := app.Storage.Active()
			switch {
			case strings.TrimSpace(q) == "":
				fmt.Printf("%s%s%s\n", Yellow, i18n.T("btw.usage"), Reset)
			case active == nil:
				fmt.Printf("%s❌ %s%s\n", Red, i18n.T("session.none_active"), Reset)
			default:
				runTurn(ctx, app, active.ID, strings.TrimSpace(q), interrupts, turnOptions{aside: true})
			}
			continue
		}
		if rest, ok := strings.CutPrefix(line, "/search"); ok && (rest == "" || rest[0] == ' ') {
			active := app.Storage.Active()
			if active == nil {
				fmt.Printf("%s❌ %s%s\n", Red, i18n.T("session.none_active"), Reset)
				continue
			}
			if t, ok := prepareSearch(ctx, app, rest, interrupts); ok {
				runTurn(t.ctx, app, active.ID, t.recorded, interrupts, turnOptions{prompt: t.prompt, readOnly: "search"})
			}
			continue
		}
		plan := false
		if goal, ok := strings.CutPrefix(line, "/plan"); ok && (goal == "" || goal[0] == ' ') {
			if line = strings.TrimSpace(goal); line == "" {
				fmt.Printf("%s%s%s\n", Yellow, i18n.T("plan.usage"), Reset)
				continue
			}
			plan = true
		}

		if !plan { // a plan goal is text for the agent, even if it starts with "/"
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
		}

		// Re-read each turn: /session new and /session load switch sessions.
		active := app.Storage.Active()
		if active == nil {
			fmt.Printf("%s❌ %s%s\n", Red, i18n.T("session.none_active"), Reset)
			continue
		}
		runTurn(ctx, app, active.ID, line, interrupts, turnOptions{plan: plan})
	}
}

// turnOptions change how runTurn treats its prompt.
type turnOptions struct {
	// accepted: the prompt already passed prompt_submit hooks and was
	// recorded (a steer message that arrived too late to be read mid-turn).
	accepted bool
	// plan: the prompt is a goal to plan for; tools that change anything
	// are refused for the whole turn (see runtime.WithPlanOnly).
	plan bool
	// prompt, if set, is sent to the agent instead of the line, which is
	// what the transcript records (e.g. "/search web …").
	prompt string
	// readOnly names a mode that refuses the same tools as plan mode
	// (see runtime.WithReadOnly).
	readOnly string
	// aside: a /btw question, answered in a throwaway copy of the session
	// (runtime.Engine.Aside) and recorded nowhere.
	aside bool
}

// runTurn sends one prompt to the agent and renders the result.
func runTurn(ctx context.Context, app *App, sessionID, line string, interrupts <-chan os.Signal, o turnOptions) {
	if !o.accepted && !acceptPrompt(ctx, app, sessionID, line) {
		return
	}
	var attached []*images.Image
	if !o.aside { // attachments wait for the next real prompt
		var ok bool
		if attached, ok = takeAttachments(app, line); !ok {
			return
		}
	}
	prompt, recorded := line, line
	if o.prompt != "" {
		prompt = o.prompt
	}
	if o.plan {
		prompt, recorded = runtime.PlanPrompt(line), "/plan "+line
		fmt.Printf("%s📝 %s%s\n", Dim, i18n.T("plan.mode"), Reset)
	}
	if o.aside {
		fmt.Printf("%s💬 %s%s\n", Dim, i18n.T("btw.mode"), Reset)
	}
	if app.Tools != nil && !o.aside {
		app.Tools.Checkpoints().Begin(textutil.Ellipsize(strings.Join(strings.Fields(recorded), " "), 60))
	}
	if !o.accepted && !o.aside {
		warnOnErr(app.Storage.AddMessage("user", recorded+AttachmentNote(attached)))
	}

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
	if o.plan {
		execOpts = append(execOpts, runtime.WithPlanOnly())
	}
	if o.readOnly != "" {
		execOpts = append(execOpts, runtime.WithReadOnly(o.readOnly))
	}
	var streamErr error
	if o.aside {
		streamErr = app.Engine.Aside(turnCtx, sessionID, prompt, printer.Handle)
	} else {
		stopSteering := watchSteering(turnCtx, app, sessionID, printer)
		streamErr = app.Engine.Execute(turnCtx, sessionID, prompt, printer.Handle, execOpts...)
		stopSteering() // waits for a message being typed, so it isn't lost
	}
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

	if modelOutput.Len() > 0 && !o.aside {
		warnOnErr(app.Storage.AddMessage("model", modelOutput.String()))
	}
	fmt.Println()
	if o.aside {
		return
	}

	// Messages sent after the model's last tool call were never read.
	if left := app.Engine.TakeSteers(sessionID); len(left) > 0 {
		text := strings.Join(left, "\n\n")
		if turnInterrupted {
			fmt.Printf("%s%s%s\n\n", Yellow, i18n.T("steer.dropped", "text", safe(text)), Reset)
			return
		}
		fmt.Printf("%s%s%s\n", Cyan, i18n.T("steer.sending"), Reset)
		runTurn(ctx, app, sessionID, text, interrupts, turnOptions{accepted: true})
	}
}

// acceptPrompt runs prompt_submit hooks and audits the prompt.
func acceptPrompt(ctx context.Context, app *App, sessionID, text string) bool {
	if app.Tools == nil {
		return true
	}
	if reason := app.Tools.ScriptHooks().PromptSubmit(ctx, sessionID, text); reason != "" {
		fmt.Printf("%s⛔ %s%s\n", Red, i18n.T("repl.prompt_blocked", "reason", safe(reason)), Reset)
		return false
	}
	app.Tools.Hooks().Audit().Log(audit.Entry{Kind: audit.KindPrompt, Session: sessionID, Detail: text})
	return true
}

// steerInput is an Input that can watch the keyboard during a turn.
type steerInput interface {
	WatchKeys(onKey func(prefill string)) (stop func())
	AskSteer(ctx context.Context, prompt, prefill string) (string, error)
}

// watchSteering lets the user type a message while the turn runs. Output
// is held back while they type. An accepted message (prompt_submit hooks
// apply, as to any prompt) is queued for the agent, which reads it with its
// next tool result.
func watchSteering(ctx context.Context, app *App, sessionID string, printer *Printer) (stop func()) {
	in, ok := app.Input.(steerInput)
	if !ok {
		return func() {}
	}
	return in.WatchKeys(func(prefill string) {
		printer.Pause()
		defer printer.Resume()
		text, err := in.AskSteer(ctx, "\n"+Cyan+i18n.T("steer.prompt")+Reset, prefill)
		text = strings.TrimSpace(text)
		switch {
		case err != nil: // Ctrl+C: the turn is being cancelled
			return
		case text == "":
			fmt.Printf("%s%s%s\n", Dim, i18n.T("steer.cancelled"), Reset)
			return
		case !acceptPrompt(ctx, app, sessionID, text):
			return
		}
		warnOnErr(app.Storage.AddMessage("user", text))
		app.Engine.Steer(sessionID, text)
		fmt.Printf("%s%s%s\n", Dim, i18n.T("steer.queued"), Reset)
	})
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
		slog.Warn("session save failed", "error", err)
		fmt.Printf("%s⚠️  %s%s\n", Yellow, i18n.T("session.save_failed", "error", err), Reset)
	}
}
