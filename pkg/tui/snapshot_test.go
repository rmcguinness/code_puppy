package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/pkg/runtime"
	"github.com/retail-cortex/code_puppy/pkg/session"
	adksession "google.golang.org/adk/v2/session"
)

// requestText joins the text the model was sent in its last request.
func requestText(llm *runtime.MockLLM) string { return requestTextAt(llm, len(llm.Requests)-1) }

// requestTextAt joins the text of the model's i-th request.
func requestTextAt(llm *runtime.MockLLM, i int) string {
	var b strings.Builder
	for _, c := range llm.Requests[i].Contents {
		for _, p := range c.Parts {
			b.WriteString(p.Text + "\n")
		}
	}
	return b.String()
}

// A snapshot restores what the model sees, not just the transcript, and
// later turns in the original session don't leak into it.
func TestSessionSaveAndLoadRestoreTheModelsContext(t *testing.T) {
	app, _ := newCommandApp(t, "")
	dir := t.TempDir()
	st, err := session.NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := session.NewPersistentService(dir)
	if err != nil {
		t.Fatal(err)
	}
	llm := runtime.NewMockLLM("gemini-3.8-flash")
	eng, err := runtime.NewEngine(context.Background(), app.Cfg, app.Agents, app.Skills, app.Tools, llm, runtime.WithSessionService(adksession.Service(svc)))
	if err != nil {
		t.Fatal(err)
	}
	app.Engine, app.Storage = eng, st
	ctx := context.Background()
	run := func(cmd string) string { return captureStdout(t, func() { HandleCommand(ctx, cmd, app) }) }
	turn := func(prompt string) {
		t.Helper()
		st.AddMessage("user", prompt)
		if err := eng.Execute(ctx, st.Active().ID, prompt, func(*adksession.Event) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}

	if out := run("/session save early"); !strings.Contains(out, "No active session") {
		t.Errorf("no session:\n%s", out)
	}
	orig, _ := st.CreateSession("", "fruit talk", "code-puppy")
	if out := run("/session save empty"); !strings.Contains(out, "nothing to save") {
		t.Errorf("empty session:\n%s", out)
	}
	turn("remember pineapple")

	if out := run("/session save"); !strings.Contains(out, "Usage: /session save") {
		t.Errorf("usage:\n%s", out)
	}
	if out := run("/session save fruit"); !strings.Contains(out, "Saved snapshot fruit (1 message)") {
		t.Fatalf("save:\n%s", out)
	}
	if out := run("/session save fruit"); !strings.Contains(out, "--force replaces it") {
		t.Errorf("taken:\n%s", out)
	}
	if out := run("/session save ../x"); !strings.Contains(out, "invalid snapshot name") {
		t.Errorf("bad name:\n%s", out)
	}
	if st.Active().ID != orig.ID {
		t.Fatal("saving switched sessions")
	}
	turn("actually, mango") // continues the original only

	out := run("/session load fruit")
	branch := st.Active()
	if !strings.Contains(out, "from snapshot fruit") || branch.ID == orig.ID || !strings.Contains(out, "remember pineapple") {
		t.Fatalf("load:\n%s", out)
	}
	turn("which fruit?")
	sent := requestText(llm)
	if !strings.Contains(sent, "remember pineapple") || strings.Contains(sent, "mango") {
		t.Fatalf("the model saw:\n%s", sent)
	}

	if out := run("/session list"); !strings.Contains(out, "📸 fruit") {
		t.Errorf("list:\n%s", out)
	}
	// /resume takes names too, and a second load branches again.
	if out := run("/resume fruit"); !strings.Contains(out, "from snapshot fruit") || st.Active().ID == branch.ID {
		t.Errorf("/resume <name>:\n%s", out)
	}
	// An ID still resumes in place, with everything said since.
	if out := run("/resume " + orig.ID); !strings.Contains(out, "Resumed session "+orig.ID) {
		t.Errorf("/resume <id>:\n%s", out)
	}
	turn("and now?")
	if sent := requestText(llm); !strings.Contains(sent, "mango") {
		t.Fatalf("the original lost its history:\n%s", sent)
	}
	if out := run("/session save fruit --force"); !strings.Contains(out, "Saved snapshot fruit (3 messages)") {
		t.Errorf("replace:\n%s", out)
	}
}
