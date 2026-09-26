package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/tools"
	"google.golang.org/genai"
)

func TestCheckpointsUndoAndDiff(t *testing.T) {
	create := &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
		Name: "create_file", Args: map[string]any{"path": "made.txt", "content": "by tool\n"}}}}}
	w, _ := openTestWith(t, func(c *config.Config) { c.CodePuppy.AutoApprove = true }, create, text("created"))
	sid := newSession(t, w).ID
	if w.SessionDiff() != "" || len(w.ListCheckpoints()) != 0 {
		t.Fatal("changes before any turn")
	}
	if _, err := w.Run(context.Background(), sid, Turn{Text: "make a file"}, ignore); err != nil {
		t.Fatal(err)
	}
	list := w.ListCheckpoints()
	if len(list) != 1 || list[0].Label != "make a file" || !slices.Equal(list[0].Files, []string{"made.txt"}) {
		t.Fatalf("checkpoints %+v", list)
	}
	if d := w.SessionDiff(); !strings.Contains(d, "+by tool") {
		t.Errorf("diff %q", d)
	}
	res, err := w.Undo(false)
	if err != nil || res.Label != "make a file" || !slices.Equal(res.Restored, []string{"made.txt"}) {
		t.Fatalf("undo %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(w.Dir(), "made.txt")); !os.IsNotExist(err) {
		t.Error("undo left the created file")
	}
}

func TestApprovalsListRevokeClear(t *testing.T) {
	w := openTest(t)
	hooks := w.Tools().Hooks()
	hooks.SetApprover(func(context.Context, tools.ApprovalRequest) (tools.Decision, error) {
		return tools.DecisionSession, nil
	})
	cmd := "cmd:" + w.Dir() + "\x00go test ./..."
	if err := hooks.Approve(context.Background(), tools.ApprovalRequest{Tool: "run_shell_command", Kind: tools.ActionCommand, Key: cmd}); err != nil {
		t.Fatal(err)
	}
	if err := hooks.Store().Add("web:example.com", "example.com"); err != nil {
		t.Fatal(err)
	}
	list := w.ListApprovals()
	if len(list) != 2 {
		t.Fatalf("approvals %+v", list)
	}
	if a := list[0]; a.Kind != "cmd" || a.Subject != "go test ./..." || a.Dir != w.Dir() || a.Always {
		t.Errorf("session approval %+v", a)
	}
	if a := list[1]; a.Kind != "web" || a.Subject != "example.com" || !a.Always || a.Added.IsZero() {
		t.Errorf("saved approval %+v", a)
	}
	if n := w.RevokeApprovals(list[1].Key); n != 1 || len(w.ListApprovals()) != 1 || len(hooks.Store().Rules()) != 0 {
		t.Errorf("revoke: %d, left %+v", n, w.ListApprovals())
	}
	if n := w.ClearApprovals(); n != 1 || len(w.ListApprovals()) != 0 {
		t.Errorf("clear: %d, left %+v", n, w.ListApprovals())
	}
}
