package runtime

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// hookedLLM runs onCall before each model call, e.g. to simulate the user
// typing while the model works.
type hookedLLM struct {
	*MockLLM
	onCall func(n int)
}

func (h *hookedLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if h.onCall != nil {
		h.onCall(h.Calls() + 1)
	}
	return h.MockLLM.GenerateContent(ctx, req, stream)
}

// steerIn returns the message_from_user values in contents' tool results.
func steerIn(cs []*genai.Content) []string {
	var out []string
	for _, c := range cs {
		for _, p := range c.Parts {
			if p.FunctionResponse != nil {
				if v, ok := p.FunctionResponse.Response[SteerKey]; ok {
					out = append(out, fmt.Sprint(v))
				}
			}
		}
	}
	return out
}

func steered(t *testing.T, f engineFixture, onCall func(n int)) {
	t.Helper()
	if err := f.eng.SetModel(context.Background(), &hookedLLM{MockLLM: f.llm, onCall: onCall}); err != nil {
		t.Fatal(err)
	}
}

func TestSteerRidesOnTheNextToolResult(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{},
		toolCall("list_files", map[string]any{}),
		textContent("done, using tabs"),
		textContent("second turn"))
	steered(t, f, func(n int) {
		if n == 1 { // the user types while the first call is in flight
			f.eng.Steer("s", "use tabs, not spaces")
		}
	})

	if _, err := collect(t, f.eng, "s", "reformat the file"); err != nil {
		t.Fatal(err)
	}
	reqs := f.llm.Requests
	if len(reqs) != 2 {
		t.Fatalf("want 2 model calls, got %d", len(reqs))
	}
	if got := steerIn(reqs[1].Contents); len(got) != 1 || got[0] != "use tabs, not spaces" {
		t.Fatalf("call 2 tool results carry %v", got)
	}
	if left := f.eng.TakeSteers("s"); len(left) != 0 {
		t.Fatalf("delivered steer still queued: %v", left)
	}

	// It is part of the history: the next turn sees it too.
	if _, err := collect(t, f.eng, "s", "anything else?"); err != nil {
		t.Fatal(err)
	}
	if got := steerIn(f.llm.Requests[2].Contents); len(got) != 1 {
		t.Fatalf("steer missing from history on the next turn: %v", got)
	}
	got, err := f.eng.sessions.Get(context.Background(), &session.GetRequest{AppName: appName, UserID: "user", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	var recorded []*genai.Content
	for ev := range got.Session.Events().All() {
		if ev.Content != nil {
			recorded = append(recorded, ev.Content)
		}
	}
	if len(steerIn(recorded)) != 1 {
		t.Fatal("steer not recorded in the session events")
	}
}

func TestSteerKeepsAFailedToolsError(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{},
		toolCall("read_file", map[string]any{"path": "missing.txt"}),
		textContent("ok"))
	steered(t, f, func(n int) {
		if n == 1 {
			f.eng.Steer("s", "try README instead")
		}
	})
	if _, err := collect(t, f.eng, "s", "read it"); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.llm.Requests[1].Contents {
		for _, p := range c.Parts {
			if r := p.FunctionResponse; r != nil {
				if r.Response[SteerKey] != "try README instead" || r.Response["error"] == nil || r.Response["error"] == "" {
					t.Fatalf("tool result = %v", r.Response)
				}
				return
			}
		}
	}
	t.Fatal("no tool result in call 2")
}

func TestSteerWithoutFurtherToolCallsIsLeftForTheCaller(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{}, textContent("all done"))
	steered(t, f, func(int) { f.eng.Steer("s", "one more thing") })
	if _, err := collect(t, f.eng, "s", "go"); err != nil {
		t.Fatal(err)
	}
	if left := f.eng.TakeSteers("s"); len(left) != 1 || left[0] != "one more thing" {
		t.Fatalf("leftover = %v", left)
	}
}

func TestSteerIsScopedToItsSession(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{}, toolCall("list_files", map[string]any{}), textContent("a"))
	f.eng.Steer("other", "not for you")
	if _, err := collect(t, f.eng, "s", "go"); err != nil {
		t.Fatal(err)
	}
	if got := steerIn(f.llm.Requests[1].Contents); len(got) != 0 {
		t.Fatalf("steer delivered to another session: %v", got)
	}
	if left := f.eng.TakeSteers("other"); len(left) != 1 {
		t.Fatalf("other session's steer lost: %v", left)
	}
}

func TestSubagentToolsDoNotTakeTheParentsSteer(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{})
	f.eng.Steer("s", "for the main agent")
	// Run a sub-agent inside a run of session "s"; its tool call must not
	// consume the message.
	sub := NewMockLLM("sub", toolCall("list_files", map[string]any{}), textContent("sub done"))
	if err := f.eng.SetModel(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), runStateKey{}, &runState{sessionID: "s"})
	if _, err := f.eng.InvokeSubagent(ctx, "qa-kitten", "check"); err != nil {
		t.Fatal(err)
	}
	if left := f.eng.TakeSteers("s"); len(left) != 1 {
		t.Fatalf("sub-agent took the parent's steer: left %v", left)
	}
	if strings.Contains(fmt.Sprint(steerIn(sub.Requests[len(sub.Requests)-1].Contents)), "main agent") {
		t.Fatal("steer reached the sub-agent")
	}
}
