package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cpsession "github.com/retail-cortex/code_puppy/internal/session"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func requestText(req *model.LLMRequest) string {
	var b strings.Builder
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			b.WriteString(p.Text + "\n")
			if p.FunctionResponse != nil {
				fmt.Fprintf(&b, "%s: %v\n", p.FunctionResponse.Name, p.FunctionResponse.Response)
			}
		}
	}
	return b.String()
}

// A side question sees the session's history, but leaves no trace in it:
// not in the next turn's request, not in the saved event log.
func TestAsideSeesHistoryAndLeavesNoTrace(t *testing.T) {
	dir := t.TempDir()
	svc, err := cpsession.NewPersistentService(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := newEngineWith(t, fixtureOpts{opts: []Option{WithSessionService(svc)}},
		textContent("noted: pineapple"),
		toolCall("create_file", map[string]any{"path": "x.txt", "content": "x"}),
		textContent("the word was pineapple"),
		textContent("next answer"))
	f.llm.Usage = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 2}
	ctx := context.Background()
	if _, err := collect(t, f.eng, "s", "remember pineapple"); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "s.events.jsonl")
	before, _ := os.ReadFile(logPath)
	usage := f.eng.Usage("s").Calls

	var answer strings.Builder
	err = f.eng.Aside(ctx, "s", "btw, what was the word?", func(ev *session.Event) error {
		if ev.Content != nil && !ev.Partial {
			for _, p := range ev.Content.Parts {
				answer.WriteString(p.Text)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(answer.String(), "the word was pineapple") {
		t.Fatalf("answer %q", answer.String())
	}
	f.llm.mu.Lock()
	asideFirst, asideSecond := requestText(f.llm.Requests[1]), requestText(f.llm.Requests[2])
	f.llm.mu.Unlock()
	if !strings.Contains(asideFirst, "remember pineapple") || !strings.Contains(asideFirst, "btw, what was the word?") {
		t.Fatalf("aside request lacks history or question:\n%s", asideFirst)
	}
	if !strings.Contains(asideSecond, "btw is read-only") {
		t.Fatalf("create_file wasn't refused as read-only:\n%s", asideSecond)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.Tools.WorkspaceDir, "x.txt")); err == nil {
		t.Fatal("the side question wrote a file")
	}
	if after, _ := os.ReadFile(logPath); string(after) != string(before) {
		t.Fatalf("the saved event log changed:\n%s", after)
	}
	if got := f.eng.Usage("s").Calls; got != usage+2 {
		t.Fatalf("usage calls %d, want %d", got, usage+2)
	}

	if _, err := collect(t, f.eng, "s", "carry on"); err != nil {
		t.Fatal(err)
	}
	f.llm.mu.Lock()
	next := requestText(f.llm.Requests[3])
	f.llm.mu.Unlock()
	if !strings.Contains(next, "remember pineapple") || strings.Contains(next, "btw") || strings.Contains(next, "the word was") {
		t.Fatalf("the next turn saw the side question:\n%s", next)
	}
}

// With no turns yet, a side question still works (on an empty history).
func TestAsideOnANewSession(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{}, textContent("hello"))
	if err := f.eng.Aside(context.Background(), "fresh", "hi?", func(*session.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if f.llm.Calls() != 1 {
		t.Fatalf("%d calls", f.llm.Calls())
	}
}
