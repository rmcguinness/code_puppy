package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/adk/v2/model"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func appendText(t *testing.T, svc adksession.Service, s adksession.Session, author, role, text string, partial bool) {
	t.Helper()
	ev := adksession.NewEvent(context.Background(), "inv-1")
	ev.Author = author
	ev.LLMResponse = model.LLMResponse{Content: genai.NewContentFromText(text, genai.Role(role)), Partial: partial}
	if err := svc.AppendEvent(context.Background(), s, ev); err != nil {
		t.Fatal(err)
	}
}

func eventTexts(t *testing.T, svc adksession.Service, id string) []string {
	t.Helper()
	resp, err := svc.Get(context.Background(), &adksession.GetRequest{AppName: "app", UserID: "u", SessionID: id})
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	var out []string
	for ev := range resp.Session.Events().All() {
		if ev.Content != nil {
			for _, p := range ev.Content.Parts {
				if p.Text != "" {
					out = append(out, p.Text)
				}
				if p.FunctionCall != nil {
					out = append(out, "call:"+p.FunctionCall.Name)
				}
			}
		}
	}
	return out
}

func TestPersistentServiceResume(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	svc, err := NewPersistentService(dir)
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.Create(ctx, &adksession.CreateRequest{AppName: "app", UserID: "u", SessionID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	s := created.Session
	appendText(t, svc, s, "user", "user", "remember pineapple", false)
	appendText(t, svc, s, "agent", "model", "streaming chunk", true) // partial: not persisted
	call := adksession.NewEvent(context.Background(), "inv-1")
	call.Author = "agent"
	call.LLMResponse = model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "read_file", Args: map[string]any{"path": "x"}}}}}}
	if err := svc.AppendEvent(ctx, s, call); err != nil {
		t.Fatal(err)
	}
	appendText(t, svc, s, "agent", "model", "noted", false)

	info, err := os.Stat(filepath.Join(dir, "sess-1"+eventsSuffix))
	if err != nil || info.Mode().Perm() != filePerm {
		t.Fatalf("event log missing or wrong mode: %v %v", info, err)
	}

	// A new process (new service, same dir) sees the full history.
	svc2, _ := NewPersistentService(dir)
	got := eventTexts(t, svc2, "sess-1")
	want := []string{"remember pineapple", "call:read_file", "noted"}
	if len(got) != len(want) {
		t.Fatalf("replayed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, got[i], want[i])
		}
	}

	// Create with a previously used ID also replays (runner auto-create path).
	svc3, _ := NewPersistentService(dir)
	c3, err := svc3.Create(ctx, &adksession.CreateRequest{AppName: "app", UserID: "u", SessionID: "sess-1"})
	if err != nil || c3.Session.Events().Len() != 3 {
		t.Errorf("create-replay: %v len=%d", err, c3.Session.Events().Len())
	}

	// Delete removes the stored log.
	if err := svc2.Delete(ctx, &adksession.DeleteRequest{AppName: "app", UserID: "u", SessionID: "sess-1"}); err != nil {
		t.Fatal(err)
	}
	if svc2.HasEvents("sess-1") {
		t.Error("event log not deleted")
	}
}

func TestPersistentServiceEdgeCases(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	svc, _ := NewPersistentService(dir)

	// Unknown sessions are still not found.
	if _, err := svc.Get(ctx, &adksession.GetRequest{AppName: "app", UserID: "u", SessionID: "missing"}); err == nil {
		t.Error("expected not found")
	}
	// Unsafe IDs are never used as file names.
	if svc.HasEvents("../escape") {
		t.Error("HasEvents accepted traversal id")
	}
	// A torn trailing line is skipped.
	os.WriteFile(filepath.Join(dir, "torn"+eventsSuffix), []byte(`{"id":"e1","author":"user","content":{"role":"user","parts":[{"text":"ok"}]}}`+"\n"+`{"id":"e2","auth`), filePerm)
	if got := eventTexts(t, svc, "torn"); len(got) != 1 || got[0] != "ok" {
		t.Errorf("torn log replay: %v", got)
	}
}
