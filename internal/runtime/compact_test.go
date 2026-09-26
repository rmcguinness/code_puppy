package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
	cpsession "github.com/retail-cortex/code_puppy/internal/session"
	"google.golang.org/genai"
)

// lastRequestText joins every text, call and response in the latest model request.
func lastRequestText(m *MockLLM) string {
	req := m.Requests[len(m.Requests)-1]
	var sb strings.Builder
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			sb.WriteString(p.Text)
			if p.FunctionCall != nil {
				sb.WriteString(" CALL:" + p.FunctionCall.ID)
			}
			if p.FunctionResponse != nil {
				sb.WriteString(" RESP:" + p.FunctionResponse.ID)
			}
			sb.WriteString("|")
		}
	}
	return sb.String()
}

func runTurns(t *testing.T, eng *Engine, sid string, prompts ...string) {
	t.Helper()
	for _, p := range prompts {
		if _, err := collect(t, eng, sid, p); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCompactReplacesOldTurns(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{},
		textContent("reply-one"), textContent("reply-two"), textContent("reply-three"),
		textContent("SUMMARY-XYZ: user wants a CLI"), // summarizer call
		textContent("reply-four"))
	runTurns(t, f.eng, "s", "turn-one", "turn-two", "turn-three")

	res, err := f.eng.Compact(context.Background(), "s", "the CLI flags", 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.EventsCompacted != 4 || res.SummaryChars == 0 {
		t.Errorf("result %+v", res)
	}
	summarizerPrompt := lastRequestText(f.llm)
	if !strings.Contains(summarizerPrompt, "the CLI flags") || !strings.Contains(summarizerPrompt, "turn-one") {
		t.Errorf("summarizer prompt missing focus or history: %s", summarizerPrompt)
	}

	runTurns(t, f.eng, "s", "turn-four")
	got := lastRequestText(f.llm)
	if !strings.Contains(got, "SUMMARY-XYZ") {
		t.Errorf("summary not in next prompt: %s", got)
	}
	for _, gone := range []string{"turn-one", "reply-one", "turn-two", "reply-two"} {
		if strings.Contains(got, gone) {
			t.Errorf("compacted %q still sent: %s", gone, got)
		}
	}
	for _, kept := range []string{"turn-three", "reply-three", "turn-four"} {
		if !strings.Contains(got, kept) {
			t.Errorf("kept %q missing: %s", kept, got)
		}
	}
}

func TestCompactNothingToDoAndEmptySummary(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{}, textContent("only"), textContent("two"), textContent(""), textContent("three"))
	if _, err := f.eng.Compact(context.Background(), "missing", "", 1); !errors.Is(err, ErrNothingToCompact) {
		t.Errorf("unknown session: %v", err)
	}
	runTurns(t, f.eng, "s", "first")
	if _, err := f.eng.Compact(context.Background(), "s", "", 1); !errors.Is(err, ErrNothingToCompact) {
		t.Errorf("single turn: %v", err)
	}
	runTurns(t, f.eng, "s", "second")
	// The summarizer returns nothing: history must stay intact.
	if _, err := f.eng.Compact(context.Background(), "s", "", 1); err == nil {
		t.Fatal("an empty summary must be rejected")
	}
	runTurns(t, f.eng, "s", "third")
	if got := lastRequestText(f.llm); !strings.Contains(got, "first") {
		t.Errorf("failed compaction dropped history: %s", got)
	}
}

func TestCompactKeepsToolCallWithResult(t *testing.T) {
	call := toolCall("list_files", map[string]any{})
	call.Parts[0].FunctionCall.ID = "call-keep"
	f := newEngineWith(t, fixtureOpts{},
		textContent("r1"), call, textContent("listed"), textContent("SUMMARY"), textContent("after"))
	runTurns(t, f.eng, "s", "hello", "list please")
	if _, err := f.eng.Compact(context.Background(), "s", "", 1); err != nil {
		t.Fatal(err)
	}
	runTurns(t, f.eng, "s", "next")
	got := lastRequestText(f.llm)
	if !strings.Contains(got, "CALL:call-keep") || !strings.Contains(got, "RESP:call-keep") {
		t.Errorf("tool call and result must stay together in the kept turn: %s", got)
	}
	if strings.Contains(got, "hello") {
		t.Errorf("first turn should be summarized: %s", got)
	}
}

func TestCompactPersistsAndWorksWithAutoCompactionOff(t *testing.T) {
	dir := t.TempDir()
	off := func(c *config.Config) { c.Context.Compaction = false }
	svc, _ := cpsession.NewPersistentService(dir)
	f := newEngineWith(t, fixtureOpts{cfg: off, opts: []Option{WithSessionService(svc)}},
		textContent("a1"), textContent("a2"), textContent("PERSISTED-SUMMARY"))
	runTurns(t, f.eng, "s", "q1", "q2")
	if _, err := f.eng.Compact(context.Background(), "s", "", 1); err != nil {
		t.Fatal(err)
	}

	svc2, _ := cpsession.NewPersistentService(dir)
	g := newEngineWith(t, fixtureOpts{cfg: off, opts: []Option{WithSessionService(svc2)}}, textContent("resumed"))
	runTurns(t, g.eng, "s", "q3")
	got := lastRequestText(g.llm)
	if !strings.Contains(got, "PERSISTED-SUMMARY") || strings.Contains(got, "q1") || !strings.Contains(got, "q2") {
		t.Errorf("resumed session should use the stored summary: %s", got)
	}
}

func TestCompactRollsEarlierSummary(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{},
		textContent("r1"), textContent("r2"), textContent("FIRST-SUMMARY"),
		textContent("r3"), textContent("SECOND-SUMMARY"), textContent("r4"))
	runTurns(t, f.eng, "s", "t1", "t2")
	if _, err := f.eng.Compact(context.Background(), "s", "", 1); err != nil {
		t.Fatal(err)
	}
	runTurns(t, f.eng, "s", "t3")
	if _, err := f.eng.Compact(context.Background(), "s", "", 1); err != nil {
		t.Fatal(err)
	}
	second := lastRequestText(f.llm)
	if !strings.Contains(second, "FIRST-SUMMARY") {
		t.Errorf("second summarization should see the first summary: %s", second)
	}
	if strings.Contains(second, "t1") {
		t.Errorf("events covered by the first summary should not be re-summarized raw: %s", second)
	}
	runTurns(t, f.eng, "s", "t4")
	got := lastRequestText(f.llm)
	if !strings.Contains(got, "SECOND-SUMMARY") || strings.Contains(got, "FIRST-SUMMARY") || strings.Contains(got, "t2") {
		t.Errorf("rolled summary should replace the first: %s", got)
	}
	if !strings.Contains(got, "t3") || !strings.Contains(got, "t4") {
		t.Errorf("recent turns missing: %s", got)
	}
	_ = genai.RoleUser
}
