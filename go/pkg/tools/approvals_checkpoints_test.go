package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/pkg/config"
)

func TestApprovalRemembering(t *testing.T) {
	ctx := context.Background()
	store, err := OpenApprovalStore(filepath.Join(t.TempDir(), "approvals.json"))
	if err != nil {
		t.Fatal(err)
	}
	h, reqs := decisionHooks(DecisionSession)
	h.SetStore(store)
	req := ApprovalRequest{Tool: "run_shell_command", Kind: ActionCommand, Key: "cmd:ls", KeyLabel: "this exact command"}

	// Session: asked once, then remembered for the same key only.
	for i := 0; i < 3; i++ {
		if err := h.Approve(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	if len(*reqs) != 1 {
		t.Errorf("expected 1 prompt for repeated session-approved action, got %d", len(*reqs))
	}
	other := req
	other.Key = "cmd:ls -la"
	h.Approve(ctx, other)
	if len(*reqs) != 2 {
		t.Error("session approval leaked to a different key")
	}
	if store.Has("cmd:ls") {
		t.Error("session decision must not be persisted")
	}
	if !h.RevokeSession("cmd:ls") || h.RevokeSession("cmd:ls") {
		t.Error("RevokeSession should remove the rule once")
	}

	// Always: persisted and honoured by a fresh Hooks with the same store file.
	always, _ := decisionHooks(DecisionAlways)
	always.SetStore(store)
	if err := always.Approve(ctx, ApprovalRequest{Tool: "t", Kind: ActionWrite, Key: "write:/ws"}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := OpenApprovalStore(store.path)
	if err != nil || !reloaded.Has("write:/ws") {
		t.Fatalf("always rule not persisted: %v", err)
	}
	fresh, freshReqs := approverHooks(false)
	fresh.SetStore(reloaded)
	if err := fresh.Approve(ctx, ApprovalRequest{Tool: "t", Kind: ActionWrite, Key: "write:/ws"}); err != nil || len(*freshReqs) != 0 {
		t.Errorf("saved rule not applied without prompting: %v prompts=%d", err, len(*freshReqs))
	}
	if info, _ := os.Stat(store.path); info.Mode().Perm() != 0o600 {
		t.Errorf("approvals file mode %v", info.Mode().Perm())
	}
	if ok, err := reloaded.Remove("write:/ws"); !ok || err != nil || reloaded.Has("write:/ws") {
		t.Error("Remove failed")
	}

	// Negative: unkeyed requests can't be remembered; each one prompts.
	empty, _ := OpenApprovalStore(filepath.Join(t.TempDir(), "empty.json"))
	h2, reqs2 := decisionHooks(DecisionAlways)
	h2.SetStore(empty)
	for i := 0; i < 2; i++ {
		h2.Approve(ctx, ApprovalRequest{Tool: "t", Kind: ActionWrite})
	}
	if len(*reqs2) != 2 || len(empty.Rules()) != 0 {
		t.Errorf("unkeyed approvals should not be remembered: prompts=%d rules=%v", len(*reqs2), empty.Rules())
	}

	// Negative: corrupt store file.
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte("{nope"), 0o600)
	if _, err := OpenApprovalStore(bad); err == nil {
		t.Error("expected error for corrupt approvals file")
	}
}

func TestSavedCommandRuleCannotBypassDeny(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	store, _ := OpenApprovalStore(filepath.Join(t.TempDir(), "a.json"))
	store.Add("cmd:"+ws.Dir()+"\x00touch x", "")
	h := NewHooks(Policy{})
	h.SetStore(store)
	policy := mustPolicy(t, CommandPolicyConfig{Deny: []string{"touch *"}})
	out := runShellCommand(context.Background(), ShellConfig{Workspace: ws, Hooks: h, Policy: policy}, RunShellCommandInput{Command: "touch x"})
	if !strings.Contains(out.Error, "blocked") {
		t.Errorf("saved rule bypassed deny: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "x")); err == nil {
		t.Error("denied command ran")
	}
}

func TestWriteToolsSendDiffAndKey(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "one\ntwo\n")
	h, reqs := approverHooks(true)
	runTool(t, toolOf(t)(NewReplaceInFileTool(ws, h)), map[string]any{"path": "a.txt", "target_content": "two", "replacement_content": "TWO"})
	runTool(t, toolOf(t)(NewCreateFileTool(ws, h)), map[string]any{"path": "b.txt", "content": "new\n"})
	runTool(t, toolOf(t)(NewDeleteFileTool(ws, h)), map[string]any{"path": "b.txt"})
	if len(*reqs) != 3 {
		t.Fatalf("expected 3 approvals, got %d", len(*reqs))
	}
	r := *reqs
	if !strings.Contains(r[0].Diff, "-two") || !strings.Contains(r[0].Diff, "+TWO") || r[0].Key != "write:"+ws.Dir() {
		t.Errorf("replace approval: diff=%q key=%q", r[0].Diff, r[0].Key)
	}
	if !strings.Contains(r[1].Diff, "/dev/null") || !strings.Contains(r[1].Diff, "+new") {
		t.Errorf("create approval diff: %q", r[1].Diff)
	}
	if !strings.Contains(r[2].Diff, "-new") || r[2].Key != "delete:"+ws.Dir() {
		t.Errorf("delete approval: diff=%q key=%q", r[2].Diff, r[2].Key)
	}
}

func TestCheckpointUndo(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	cp := NewCheckpoints(ws, 0)
	path := filepath.Join(dir, "f.txt")
	writeFile(t, path, "v1\n")
	os.Chmod(path, 0o755)

	cp.Begin("turn 1")
	ws.WriteFileAtomic("f.txt", []byte("v2\n"))
	ws.WriteFileAtomic("f.txt", []byte("v3\n")) // same turn: original snapshot kept
	ws.CreateExclusive("new.txt", []byte("n\n"))
	cp.Begin("turn 2")
	ws.RemoveFile("new.txt")

	if l := cp.List(); len(l) != 2 || l[0].Label != "turn 2" || len(l[1].Files) != 2 {
		t.Fatalf("unexpected checkpoints %+v", l)
	}
	if d := cp.SessionDiff(); !strings.Contains(d, "-v1") || !strings.Contains(d, "+v3") {
		t.Errorf("session diff missing change:\n%s", d)
	}

	// Undo turn 2: deleted file comes back.
	if _, err := cp.Undo(false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "new.txt")); string(b) != "n\n" {
		t.Errorf("deleted file not restored: %q", b)
	}
	// Undo turn 1: original content and mode restored, created file removed.
	res, err := cp.Undo(false)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "v1\n" {
		t.Errorf("content not restored: %q", b)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o755 {
		t.Errorf("mode not restored: %v", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Error("created file not removed by undo")
	}
	if len(res.Restored) != 2 {
		t.Errorf("restored %v", res.Restored)
	}
	// Negative: nothing left.
	if _, err := cp.Undo(false); err == nil {
		t.Error("expected nothing to undo")
	}
}

func TestCheckpointUndoConflict(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	cp := NewCheckpoints(ws, 0)
	path := filepath.Join(dir, "f.txt")
	writeFile(t, path, "orig\n")
	cp.Begin("edit")
	ws.WriteFileAtomic("f.txt", []byte("tool edit\n"))
	os.WriteFile(path, []byte("user edit after\n"), 0o644) // changed outside the tools

	if _, err := cp.Undo(false); !errors.Is(err, ErrUndoConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "user edit after\n" {
		t.Error("conflicting undo modified the file")
	}
	if _, err := cp.Undo(true); err != nil {
		t.Fatalf("forced undo: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "orig\n" {
		t.Errorf("forced undo did not restore: %q", b)
	}
}

func TestCheckpointFailedWriteNotRecorded(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	cp := NewCheckpoints(ws, 0)
	os.Mkdir(filepath.Join(dir, "adir"), 0o755)
	cp.Begin("t")
	if err := ws.WriteFileAtomic("adir", []byte("x")); err == nil {
		t.Fatal("expected write over directory to fail")
	}
	if err := ws.CreateExclusive("adir", []byte("x")); err == nil {
		t.Fatal("expected create over directory to fail")
	}
	if l := cp.List(); len(l) != 0 {
		t.Errorf("failed writes were recorded: %+v", l)
	}
	if _, err := cp.Undo(false); err == nil {
		t.Error("undo should have nothing to do")
	}
	if _, err := os.Stat(filepath.Join(dir, "adir")); err != nil {
		t.Error("directory was removed")
	}
}

func TestCheckpointMemoryBudget(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	cp := NewCheckpoints(ws, 100)
	for i, name := range []string{"a", "b", "c"} {
		writeFile(t, filepath.Join(dir, name), strings.Repeat("x", 60))
		cp.Begin(name)
		ws.WriteFileAtomic(name, []byte{byte('0' + i)})
	}
	l := cp.List()
	if len(l) == 0 || len(l) == 3 {
		t.Errorf("expected oldest turns dropped to fit budget, have %d", len(l))
	}
	if l[0].Label != "c" {
		t.Error("most recent turn must be kept")
	}
}

func TestCommandApprovalScopedToWorkspace(t *testing.T) {
	wsA, _ := newTestWorkspace(t)
	wsB, _ := newTestWorkspace(t)
	store, _ := OpenApprovalStore(filepath.Join(t.TempDir(), "a.json"))
	always, _ := decisionHooks(DecisionAlways)
	always.SetStore(store)
	runShellCommand(context.Background(), ShellConfig{Workspace: wsA, Hooks: always}, RunShellCommandInput{Command: "true"})

	// Same command, other workspace: must prompt again.
	h, reqs := approverHooks(false)
	h.SetStore(store)
	out := runShellCommand(context.Background(), ShellConfig{Workspace: wsB, Hooks: h}, RunShellCommandInput{Command: "true"})
	if len(*reqs) != 1 || !strings.Contains(out.Error, "not approved") {
		t.Errorf("saved rule leaked across workspaces: prompts=%d out=%+v", len(*reqs), out)
	}
	// Same workspace: remembered.
	h2, reqs2 := approverHooks(false)
	h2.SetStore(store)
	if out := runShellCommand(context.Background(), ShellConfig{Workspace: wsA, Hooks: h2}, RunShellCommandInput{Command: "true"}); out.Error != "" || len(*reqs2) != 0 {
		t.Errorf("saved rule not applied in its workspace: %+v", out)
	}
}

func TestHooksRunOutsideSandbox(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = t.TempDir()
	cfg.Tools.ApprovalsFile = ""
	outside := filepath.Join(t.TempDir(), "hook-log.txt") // not writable by sandboxed commands
	cfg.Hooks.PostTool = []config.HookConfig{{Command: "echo logged >> " + outside}}
	reg, err := NewRegistry(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	reg.ScriptHooks().PostTool(context.Background(), "s", "grep", nil, nil, nil)
	if b, _ := os.ReadFile(outside); !strings.Contains(string(b), "logged") {
		t.Error("hook could not write outside the workspace; hooks should not be sandboxed")
	}
}
