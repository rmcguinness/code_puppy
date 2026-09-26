package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"google.golang.org/adk/v2/model"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func text(s string) *genai.Content { return genai.NewContentFromText(s, genai.RoleModel) }

func ignore(Event) {}

// transcript returns the active session's messages as "role: text".
func transcript(t *testing.T, w *Workspace) []string {
	t.Helper()
	s, ok := w.ActiveSession()
	if !ok {
		t.Fatal("no active session")
	}
	var out []string
	for _, m := range s.Messages {
		out = append(out, m.Role+": "+m.Text)
	}
	return out
}

func newSession(t *testing.T, w *Workspace) SessionInfo {
	t.Helper()
	s, _, err := w.OpenSession("", false)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRunRecordsBothSidesAndOmitsThoughts(t *testing.T) {
	reply := &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "pondering", Thought: true}, {Text: "hello there"}}}
	w, llm := openTestWith(t, nil, reply)
	sid := newSession(t, w).ID
	accepted := 0
	var seen int
	res, err := w.Run(context.Background(), sid, Turn{Text: "hi", OnAccepted: func() { accepted++ }},
		func(Event) { seen++ })
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "hello there" || accepted != 1 || seen == 0 || llm.Calls() != 1 {
		t.Errorf("output %q, accepted %d, events %d, calls %d", res.Output, accepted, seen, llm.Calls())
	}
	if got := transcript(t, w); !slices.Equal(got, []string{"user: hi", "model: hello there"}) {
		t.Errorf("transcript %q", got)
	}
}

func TestRunPlanPromptOverrideAndAside(t *testing.T) {
	w, llm := openTestWith(t, nil, text("a plan"), text("searched"), text("an aside"))
	sid := newSession(t, w).ID
	ctx := context.Background()
	if _, err := w.Run(ctx, sid, Turn{Text: "add a flag", Plan: true}, ignore); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(ctx, sid, Turn{Text: "/search web go", Prompt: "results: …", ReadOnly: "search"}, ignore); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastUserText(llm), "results: …") {
		t.Errorf("Prompt not sent: %q", lastUserText(llm))
	}
	if _, err := w.Run(ctx, sid, Turn{Text: "what's a flag?", Aside: true}, ignore); err != nil {
		t.Fatal(err)
	}
	want := []string{"user: /plan add a flag", "model: a plan", "user: /search web go", "model: searched"} // asides are recorded nowhere
	if got := transcript(t, w); !slices.Equal(got, want) {
		t.Errorf("transcript %q, want %q", got, want)
	}
}

func TestRunAcceptedSkipsHooksAndRecording(t *testing.T) {
	w, _ := openTestWith(t, func(c *config.Config) {
		c.Hooks.PromptSubmit = []config.HookConfig{{Command: `echo "nope" >&2; exit 2`}}
	}, text("ok"))
	sid := newSession(t, w).ID
	if _, err := w.Run(context.Background(), sid, Turn{Text: "late steer", Accepted: true}, ignore); err != nil {
		t.Fatal(err)
	}
	if got := transcript(t, w); !slices.Equal(got, []string{"model: ok"}) {
		t.Errorf("transcript %q", got)
	}
}

func TestRunAndSteerBlockedByHook(t *testing.T) {
	w, llm := openTestWith(t, func(c *config.Config) {
		c.Hooks.PromptSubmit = []config.HookConfig{{Command: `echo "no secrets" >&2; exit 2`}}
	}, text("should not run"))
	sid := newSession(t, w).ID
	accepted := false
	_, err := w.Run(context.Background(), sid, Turn{Text: "my password", OnAccepted: func() { accepted = true }}, ignore)
	var blocked *BlockedError
	if !errors.As(err, &blocked) || !strings.Contains(blocked.Reason, "no secrets") || accepted || llm.Calls() != 0 {
		t.Errorf("err %v, accepted %v, calls %d", err, accepted, llm.Calls())
	}
	if err := w.Steer(context.Background(), sid, "my password"); !errors.As(err, &blocked) {
		t.Errorf("steer: %v", err)
	}
	if got := transcript(t, w); len(got) != 0 {
		t.Errorf("a blocked prompt was recorded: %q", got)
	}
}

func TestSteerRecordsAndUnreadSteersAreLeftOver(t *testing.T) {
	w, _ := openTestWith(t, nil, text("done"))
	sid := newSession(t, w).ID
	ctx := context.Background()
	// A message sent as the agent stops (the front end was still taking it)
	// must still come back as unread.
	res, err := w.Run(ctx, sid, Turn{Text: "go", OnFinished: func() {
		if err := w.Steer(ctx, sid, "also do this"); err != nil {
			t.Error(err)
		}
	}}, ignore)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Leftover, []string{"also do this"}) {
		t.Errorf("leftover %q", res.Leftover)
	}
	if got := transcript(t, w); !slices.Equal(got, []string{"user: go", "user: also do this", "model: done"}) {
		t.Errorf("transcript %q", got)
	}
}

// lastUserText is the text of the last user message the model was sent.
func lastUserText(m *runtime.MockLLM) string {
	req := m.Requests[len(m.Requests)-1]
	for i := len(req.Contents) - 1; i >= 0; i-- {
		if c := req.Contents[i]; c.Role == genai.RoleUser {
			var sb strings.Builder
			for _, p := range c.Parts {
				sb.WriteString(p.Text)
			}
			return sb.String()
		}
	}
	return ""
}

func adkText(text string, partial, thought bool) *adksession.Event {
	ev := &adksession.Event{Author: "code-puppy"}
	ev.LLMResponse = model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: text, Thought: thought}}}, Partial: partial}
	return ev
}

// Streamed chunks are delivered as they come; the final event that repeats
// them is marked, and only final answer text reaches the transcript.
func TestRelayMarksRepeatedText(t *testing.T) {
	var got []Event
	r := &relay{on: func(e Event) { got = append(got, e) }}
	r.handle(adkText("thinking…", true, true))
	r.handle(adkText("Hel", true, false))
	r.handle(adkText("lo", true, false))
	r.handle(adkText("Hello", false, false))  // repeats the chunks
	r.handle(adkText(" again", false, false)) // not streamed
	r.handle(&adksession.Event{})             // no content
	if len(got) != 5 {
		t.Fatalf("got %d events", len(got))
	}
	var shown strings.Builder
	for _, e := range got {
		if e.Author != "code-puppy" || e.Text == nil {
			t.Fatalf("event %+v", e)
		}
		if !e.Text.Thought && (e.Text.Partial || !e.Text.Repeat) {
			shown.WriteString(e.Text.Text)
		}
	}
	if shown.String() != "Hello again" || !got[3].Text.Repeat || got[4].Text.Repeat {
		t.Errorf("shown %q, events %+v", shown.String(), got)
	}
	if r.output.String() != "Hello again" {
		t.Errorf("transcript text %q", r.output.String())
	}
}
