package app

import (
	"context"
	"log/slog"
	"strings"

	"github.com/retail-cortex/code_puppy/internal/audit"
	"github.com/retail-cortex/code_puppy/internal/i18n"
	"github.com/retail-cortex/code_puppy/internal/images"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"github.com/retail-cortex/code_puppy/internal/textutil"
	"github.com/retail-cortex/code_puppy/internal/tools"
	adksession "google.golang.org/adk/v2/session"
)

// Turn is one prompt for the agent.
type Turn struct {
	// Text is what the user typed. prompt_submit hooks see it and the
	// transcript records it.
	Text string
	// Prompt, if set, is sent to the agent instead of Text (e.g. /search
	// sends the fetched results, and the transcript records the command).
	Prompt string
	// Plan: Text is a goal to plan for. Tools that change anything are
	// refused for the whole turn (runtime.WithPlanOnly), and the transcript
	// records "/plan <Text>".
	Plan bool
	// ReadOnly names a mode that refuses the same tools as plan mode
	// (runtime.WithReadOnly).
	ReadOnly string
	// Aside: a /btw question, answered in a throwaway copy of the session
	// (runtime.Engine.Aside) and recorded nowhere. It takes no images, starts
	// no checkpoint and leaves queued steer messages alone.
	Aside bool
	// Accepted: Text already passed prompt_submit hooks and was recorded (a
	// steer message that arrived too late to be read mid-turn).
	Accepted bool
	// Images are sent with the prompt.
	Images []*images.Image
	// MaxTurns limits the model calls in the turn (0: unlimited).
	MaxTurns int
	// FetchGrants are URLs the agent may fetch in this turn without asking
	// (the pages a web search handed it).
	FetchGrants []string
	// OnAccepted, if set, runs once the prompt has passed prompt_submit
	// hooks, before anything is recorded or sent.
	OnAccepted func()
	// OnFinished, if set, runs when the agent has stopped, before unread
	// steer messages are collected: a front end still taking a steer message
	// finishes here, so the message ends up in TurnResult.Leftover.
	OnFinished func()
}

// TurnResult describes a finished turn.
type TurnResult struct {
	// Output is the model's final text (partial and thought text excluded).
	Output string
	// Before and After are the session's usage around the turn.
	Before, After runtime.Usage
	// Leftover are steer messages sent after the model's last tool call, so
	// never read. Front ends send them as the next turn (with Accepted set)
	// or, if the turn was interrupted, drop them.
	Leftover []string
}

// BlockedError reports a prompt refused by a prompt_submit hook. Nothing
// was recorded or sent.
type BlockedError struct{ Reason string }

func (e *BlockedError) Error() string { return "prompt blocked by hook: " + e.Reason }

// Run sends one prompt to the agent in the session and passes its events to
// on as they happen. It runs prompt_submit hooks, audits the prompt, starts
// a checkpoint for /undo and records both sides in the transcript. A
// refused prompt is a *BlockedError. A turn that fails part way still
// returns what it produced.
func (w *Workspace) Run(ctx context.Context, sessionID string, t Turn, on func(Event)) (TurnResult, error) {
	if !t.Accepted {
		if err := w.accept(ctx, sessionID, t.Text); err != nil {
			return TurnResult{}, err
		}
	}
	if t.OnAccepted != nil {
		t.OnAccepted()
	}

	prompt, recorded := t.Text, t.Text
	if t.Prompt != "" {
		prompt = t.Prompt
	}
	if t.Plan {
		prompt, recorded = runtime.PlanPrompt(t.Text), "/plan "+t.Text
	}
	if !t.Aside {
		w.tools.Checkpoints().Begin(textutil.Ellipsize(strings.Join(strings.Fields(recorded), " "), 60))
		if !t.Accepted {
			w.record("user", recorded+AttachmentNote(t.Images))
		}
	}

	r := &relay{on: on}
	handler := r.handle

	res := TurnResult{Before: w.engine.Usage(sessionID)}
	if len(t.FetchGrants) > 0 {
		ctx = tools.WithFetchGrants(ctx, t.FetchGrants)
	}
	var err error
	if t.Aside {
		err = w.engine.Aside(ctx, sessionID, prompt, handler)
	} else {
		var opts []runtime.ExecOption
		if t.MaxTurns > 0 {
			opts = append(opts, runtime.WithMaxTurns(t.MaxTurns))
		}
		for _, img := range t.Images {
			opts = append(opts, runtime.WithAttachments(images.Part(img)))
		}
		if t.Plan {
			opts = append(opts, runtime.WithPlanOnly())
		}
		if t.ReadOnly != "" {
			opts = append(opts, runtime.WithReadOnly(t.ReadOnly))
		}
		err = w.engine.Execute(ctx, sessionID, prompt, handler, opts...)
	}
	if t.OnFinished != nil {
		t.OnFinished()
	}
	res.After = w.engine.Usage(sessionID)
	res.Output = r.output.String()

	if !t.Aside {
		if res.Output != "" {
			w.record("model", res.Output)
		}
		res.Leftover = w.engine.TakeSteers(sessionID)
	}
	return res, err
}

// Steer queues a message for the agent while a turn runs in the session; the
// agent reads it with its next tool result. prompt_submit hooks apply, as to
// any prompt (a refusal is a *BlockedError), and the transcript records it.
func (w *Workspace) Steer(ctx context.Context, sessionID, text string) error {
	if err := w.accept(ctx, sessionID, text); err != nil {
		return err
	}
	w.record("user", text)
	w.engine.Steer(sessionID, text)
	return nil
}

// accept runs prompt_submit hooks and audits the prompt.
func (w *Workspace) accept(ctx context.Context, sessionID, text string) error {
	if reason := w.tools.ScriptHooks().PromptSubmit(ctx, sessionID, text); reason != "" {
		return &BlockedError{Reason: reason}
	}
	w.tools.Hooks().Audit().Log(audit.Entry{Kind: audit.KindPrompt, Session: sessionID, Detail: text})
	return nil
}

// record adds a message to the active session's transcript. A failure is
// reported, not returned: the conversation goes on without it.
func (w *Workspace) record(role, text string) {
	if err := w.storage.AddMessage(role, text); err != nil {
		slog.Warn("session save failed", "error", err)
		w.warn(i18n.T("session.save_failed", "error", err))
	}
}

// AttachmentNote is what the transcript records for images sent with a
// prompt: their names.
func AttachmentNote(imgs []*images.Image) string {
	if len(imgs) == 0 {
		return ""
	}
	names := make([]string, len(imgs))
	for i, img := range imgs {
		names[i] = img.Name
	}
	return "\n[images: " + strings.Join(names, ", ") + "]"
}

// relay passes a turn's ADK events on as Events, marking final text that
// repeats streamed chunks and collecting the transcript's model text.
type relay struct {
	on       func(Event)
	streamed bool // answer text arrived in partial chunks since the last final event
	output   strings.Builder
}

func (r *relay) handle(ev *adksession.Event) error {
	for _, e := range events(ev) {
		if t := e.Text; t != nil && !t.Thought {
			if t.Partial {
				r.streamed = true
			} else {
				t.Repeat = r.streamed
				r.output.WriteString(t.Text)
			}
		}
		r.on(e)
	}
	if !ev.Partial {
		r.streamed = false
	}
	return nil
}
