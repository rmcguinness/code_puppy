package runtime

import (
	"maps"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/adk/v2/agent"
)

// SteerKey is the tool-result field that carries messages the user sent
// while the turn was running.
const SteerKey = "message_from_user"

// Steer queues a message for the turn running in sessionID. It is attached
// to the next tool result of that turn, so the model reads it before its
// next step without anything being interrupted, and it is kept in the
// session history in the place it arrived. Messages still queued when the
// turn ends (the model made no further tool calls) are returned by
// TakeSteers for the caller to send as the next prompt.
func (e *Engine) Steer(sessionID, text string) {
	if sessionID == "" {
		sessionID = "default"
	}
	e.steerMu.Lock()
	defer e.steerMu.Unlock()
	if e.steers == nil {
		e.steers = map[string][]string{}
	}
	e.steers[sessionID] = append(e.steers[sessionID], text)
}

// TakeSteers removes and returns the messages queued for sessionID that no
// tool result has carried yet.
func (e *Engine) TakeSteers(sessionID string) []string {
	e.steerMu.Lock()
	defer e.steerMu.Unlock()
	msgs := e.steers[sessionID]
	delete(e.steers, sessionID)
	return msgs
}

// attachSteers returns result with queued messages added, or nil when there
// are none. (Model callbacks can't add session events, so tool results are
// the channel that reaches the model and the history.) Tools run by a
// sub-agent, which has its own session, don't take the parent's messages.
func (e *Engine) attachSteers(ctx agent.Context, result map[string]any, toolErr error) map[string]any {
	st := stateFrom(ctx)
	if st == nil || ctx.SessionID() != st.sessionID {
		return nil
	}
	msgs := e.TakeSteers(st.sessionID)
	if len(msgs) == 0 {
		return nil
	}
	out := maps.Clone(result)
	if out == nil {
		out = map[string]any{}
	}
	if toolErr != nil { // returning a result replaces the error; keep it visible
		out["error"] = toolErr.Error()
	}
	if len(msgs) == 1 {
		out[SteerKey] = msgs[0]
	} else {
		out[SteerKey] = msgs
	}
	trace.SpanFromContext(ctx).AddEvent("steer", trace.WithAttributes(attribute.Int("messages", len(msgs))))
	return out
}
