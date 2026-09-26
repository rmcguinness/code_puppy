package app

import (
	adksession "google.golang.org/adk/v2/session"
)

// Event is something that happened during a turn. Exactly one of Text,
// ToolCall and ToolResult is set. Events arrive in order.
type Event struct {
	// Author is the agent that produced the event (a sub-agent's name
	// when one is working).
	Author     string
	Text       *Text
	ToolCall   *ToolCall
	ToolResult *ToolResult
}

// Text is model output. With streaming, partial chunks arrive first and a
// final event then repeats their text in full (Repeat); without streaming
// only final events arrive. To show text once, show partial chunks and
// final text that isn't a Repeat. The transcript keeps final text only.
type Text struct {
	Text    string
	Partial bool
	// Repeat marks final text whose partial chunks were already delivered.
	Repeat bool
	// Thought is the model's reasoning rather than its answer; front ends
	// may show it or not, and the transcript leaves it out.
	Thought bool
}

// ToolCall is the agent calling a tool.
type ToolCall struct {
	ID   string // matches the ToolResult
	Name string
	Args map[string]any
	// Partial: the call arrived in a streamed chunk, and the final event
	// repeats it.
	Partial bool
}

// ToolResult is what a tool returned.
type ToolResult struct {
	ID     string
	Name   string
	Result map[string]any
}

// events converts an ADK event: one Event per text part, tool call or tool
// result, in order.
func events(ev *adksession.Event) []Event {
	if ev == nil || ev.Content == nil {
		return nil
	}
	var out []Event
	for _, p := range ev.Content.Parts {
		switch {
		case p.Text != "":
			out = append(out, Event{Author: ev.Author, Text: &Text{Text: p.Text, Partial: ev.Partial, Thought: p.Thought}})
		case p.FunctionCall != nil:
			out = append(out, Event{Author: ev.Author, ToolCall: &ToolCall{ID: p.FunctionCall.ID, Name: p.FunctionCall.Name, Args: p.FunctionCall.Args, Partial: ev.Partial}})
		case p.FunctionResponse != nil:
			out = append(out, Event{Author: ev.Author, ToolResult: &ToolResult{ID: p.FunctionResponse.ID, Name: p.FunctionResponse.Name, Result: p.FunctionResponse.Response}})
		}
	}
	return out
}
