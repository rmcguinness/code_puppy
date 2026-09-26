package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/retail-cortex/code_puppy/internal/app"
	"github.com/retail-cortex/code_puppy/internal/images"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"github.com/retail-cortex/code_puppy/internal/tui"
	"golang.org/x/term"
	adksession "google.golang.org/adk/v2/session"
)

// Output formats for non-interactive runs.
const (
	formatText       = "text"
	formatJSON       = "json"        // one result object at the end
	formatStreamJSON = "stream-json" // one JSON object per event, then the result
)

type usageJSON struct {
	InputTokens  int64 `json:"input_tokens"`
	CachedTokens int64 `json:"cached_input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	ModelCalls   int   `json:"model_calls"`
}

type toolCallJSON struct {
	Name   string         `json:"name"`
	Args   map[string]any `json:"args,omitempty"`
	Result map[string]any `json:"result,omitempty"`
}

// runResult is the final JSON object for json and stream-json output.
type runResult struct {
	Type       string         `json:"type"`
	SessionID  string         `json:"session_id"`
	Result     string         `json:"result"`
	IsError    bool           `json:"is_error"`
	Error      string         `json:"error,omitempty"`
	ExitCode   int            `json:"exit_code"`
	DurationMs int64          `json:"duration_ms"`
	Usage      usageJSON      `json:"usage"`
	CostUSD    *float64       `json:"cost_usd,omitempty"`
	ToolCalls  []toolCallJSON `json:"tool_calls,omitempty"`
}

type oneShotOptions struct {
	prompt     string
	sessionID  string
	format     string
	maxTurns   int
	plan       bool // --plan: read-only tools, answer with a plan
	input      tui.Input
	stdinTTY   bool
	stdout     io.Writer
	markdown   bool
	spinner    bool
	width      int
	usageLines bool
	images     []*images.Image
}

// runOneShot executes a single prompt and exits. Background processes are
// never left behind: the user is asked (text mode on a terminal) or they are
// killed.
func runOneShot(ctx context.Context, w *app.Workspace, o oneShotOptions) error {
	start := time.Now()
	out := o.stdout
	sid := o.sessionID

	var handler runtime.EventHandler
	var printer *tui.Printer
	var calls []toolCallJSON
	var enc *json.Encoder
	switch o.format {
	case formatText:
		printer = tui.NewPrinter(tui.PrinterOptions{Out: out, Markdown: o.markdown, Theme: w.Config().UI.Theme, Width: o.width, Spinner: o.spinner})
		handler = printer.Handle
	case formatStreamJSON:
		enc = json.NewEncoder(out)
		handler = streamJSONHandler(enc)
	default:
		handler = collectHandler(&calls)
	}
	turn, runErr := w.Run(ctx, sid, app.Turn{
		Text: o.prompt, Plan: o.plan, Images: o.images, MaxTurns: o.maxTurns,
		OnAccepted: func() {
			if printer != nil {
				printer.Begin()
			}
			if enc != nil {
				_ = enc.Encode(map[string]any{"type": "session", "session_id": sid, "model": w.Engine().ModelName(), "agent": w.Engine().ActiveAgent()})
			}
		},
	}, handler)
	var blocked *app.BlockedError
	if errors.As(runErr, &blocked) {
		runErr = withCode(exitBlocked, runErr)
	} else if printer != nil {
		printer.End()
		fmt.Fprintln(out)
	}
	if ctx.Err() != nil && runErr != nil && !errors.Is(runErr, runtime.ErrMaxTurns) {
		runErr = withCode(exitInterrupted, runErr)
	}

	usage := w.Engine().Usage(sid)
	if o.format == formatText {
		if o.usageLines {
			if line := tui.UsageLine(runtime.Usage{}, usage); line != "" {
				fmt.Fprintf(os.Stderr, "%s%s%s\n", tui.Dim, line, tui.Reset)
			}
		}
	} else {
		res := runResult{
			Type: "result", SessionID: sid, Result: turn.Output,
			DurationMs: time.Since(start).Milliseconds(),
			Usage:      usageJSON{InputTokens: usage.Input, CachedTokens: usage.Cached, OutputTokens: usage.Output, ModelCalls: usage.Calls},
			ToolCalls:  calls,
		}
		if usage.Priced && usage.Calls > 0 {
			cost := usage.CostUSD
			res.CostUSD = &cost
		}
		if runErr != nil {
			res.IsError, res.Error = true, runErr.Error()
		}
		res.ExitCode = exitCodeFor(runErr)
		_ = json.NewEncoder(out).Encode(res)
	}

	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	tui.ConfirmExit(context.Background(), o.input, w.Tools().Processes(), interrupts, tui.ExitPrompt{
		CanPrompt: o.format == formatText && ctx.Err() == nil && o.stdinTTY && o.input != nil,
	})
	return runErr
}

// streamJSONHandler writes one JSON line per event.
func streamJSONHandler(enc *json.Encoder) runtime.EventHandler {
	return func(ev *adksession.Event) error {
		if ev.Content == nil {
			return nil
		}
		for _, p := range ev.Content.Parts {
			switch {
			case p.Text != "" && !p.Thought:
				_ = enc.Encode(map[string]any{"type": "text", "text": p.Text, "partial": ev.Partial, "author": ev.Author})
			case p.FunctionCall != nil:
				_ = enc.Encode(map[string]any{"type": "tool_call", "name": p.FunctionCall.Name, "args": p.FunctionCall.Args})
			case p.FunctionResponse != nil:
				_ = enc.Encode(map[string]any{"type": "tool_result", "name": p.FunctionResponse.Name, "result": p.FunctionResponse.Response})
			}
		}
		return nil
	}
}

// collectHandler gathers tool calls for json output.
func collectHandler(calls *[]toolCallJSON) runtime.EventHandler {
	pending := map[string]int{}
	return func(ev *adksession.Event) error {
		if ev.Content == nil || ev.Partial {
			return nil
		}
		for _, p := range ev.Content.Parts {
			switch {
			case p.FunctionCall != nil:
				pending[p.FunctionCall.ID+p.FunctionCall.Name] = len(*calls)
				*calls = append(*calls, toolCallJSON{Name: p.FunctionCall.Name, Args: p.FunctionCall.Args})
			case p.FunctionResponse != nil:
				if i, ok := pending[p.FunctionResponse.ID+p.FunctionResponse.Name]; ok {
					(*calls)[i].Result = p.FunctionResponse.Response
				}
			}
		}
		return nil
	}
}

func stdoutIsTerminal() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

func terminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		return w
	}
	return 100
}
