package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHooksApprovePolicy(t *testing.T) {
	ctx := context.Background()
	cmd := ApprovalRequest{Tool: "run_shell_command", Kind: ActionCommand, Detail: "ls"}
	write := ApprovalRequest{Tool: "create_file", Kind: ActionWrite, Detail: "f"}

	// Positive: AutoApproveAll covers everything.
	all := NewHooks(Policy{AutoApproveAll: true})
	if err := all.Approve(ctx, cmd); err != nil {
		t.Errorf("auto-approve-all denied command: %v", err)
	}
	if err := all.Approve(ctx, write); err != nil {
		t.Errorf("auto-approve-all denied write: %v", err)
	}

	// AutoApproveCommands covers commands only; writes still fail closed.
	cmds := NewHooks(Policy{AutoApproveCommands: true})
	if err := cmds.Approve(ctx, cmd); err != nil {
		t.Errorf("auto-approve-commands denied command: %v", err)
	}
	if err := cmds.Approve(ctx, write); !errors.Is(err, ErrNotApproved) {
		t.Errorf("expected write to need approval, got %v", err)
	}

	// Negative: no approver configured => denied (fail closed), incl. nil hooks.
	if err := NewHooks(Policy{}).Approve(ctx, cmd); !errors.Is(err, ErrNotApproved) {
		t.Errorf("expected fail-closed denial, got %v", err)
	}
	var nilHooks *Hooks
	if err := nilHooks.Approve(ctx, cmd); !errors.Is(err, ErrNotApproved) {
		t.Errorf("expected nil hooks to deny, got %v", err)
	}

	// Approver decisions and errors.
	yes, reqs := approverHooks(true)
	if err := yes.Approve(ctx, cmd); err != nil {
		t.Errorf("approver said yes but got %v", err)
	}
	if len(*reqs) != 1 || (*reqs)[0].Detail != "ls" {
		t.Errorf("approver did not receive request: %v", *reqs)
	}
	no, _ := approverHooks(false)
	if err := no.Approve(ctx, cmd); !errors.Is(err, ErrNotApproved) {
		t.Errorf("approver said no but got %v", err)
	}
	failing := NewHooks(Policy{})
	failing.SetApprover(func(context.Context, ApprovalRequest) (Decision, error) { return DecisionOnce, errors.New("tty gone") })
	if err := failing.Approve(ctx, cmd); !errors.Is(err, ErrNotApproved) {
		t.Errorf("approver error should deny, got %v", err)
	}
}

func TestMutatingToolsRespectApproval(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	writeFile(t, filepath.Join(dir, "existing.txt"), "original")

	type call struct {
		name  string
		build func(*Hooks) runnerTool
		args  map[string]any
		check func(t *testing.T, applied bool)
	}
	exists := func(p string) bool { _, err := os.Stat(filepath.Join(dir, p)); return err == nil }
	content := func(p string) string { b, _ := os.ReadFile(filepath.Join(dir, p)); return string(b) }

	calls := []call{
		{"create_file", func(h *Hooks) runnerTool { return toolOf(t)(NewCreateFileTool(ws, h)) },
			map[string]any{"path": "created.txt", "content": "x"},
			func(t *testing.T, applied bool) {
				if exists("created.txt") != applied {
					t.Errorf("create_file applied=%v but file exists=%v", applied, exists("created.txt"))
				}
			}},
		{"replace_in_file", func(h *Hooks) runnerTool { return toolOf(t)(NewReplaceInFileTool(ws, h)) },
			map[string]any{"path": "existing.txt", "target_content": "original", "replacement_content": "edited"},
			func(t *testing.T, applied bool) {
				if (content("existing.txt") == "edited") != applied {
					t.Errorf("replace_in_file applied=%v content=%q", applied, content("existing.txt"))
				}
			}},
		{"delete_snippet", func(h *Hooks) runnerTool { return toolOf(t)(NewDeleteSnippetTool(ws, h)) },
			map[string]any{"path": "existing.txt", "snippet": "ited"},
			func(t *testing.T, applied bool) {
				if (content("existing.txt") == "ed") != applied {
					t.Errorf("delete_snippet applied=%v content=%q", applied, content("existing.txt"))
				}
			}},
		{"run_shell_command", func(h *Hooks) runnerTool {
			return toolOf(t)(NewRunShellCommandTool(ShellConfig{Workspace: ws, Hooks: h}))
		},
			map[string]any{"command": "touch ran.marker"},
			func(t *testing.T, applied bool) {
				if exists("ran.marker") != applied {
					t.Errorf("shell applied=%v marker exists=%v", applied, exists("ran.marker"))
				}
			}},
		{"delete_file", func(h *Hooks) runnerTool { return toolOf(t)(NewDeleteFileTool(ws, h)) },
			map[string]any{"path": "existing.txt"},
			func(t *testing.T, applied bool) {
				if exists("existing.txt") == applied {
					t.Errorf("delete_file applied=%v but file exists=%v", applied, exists("existing.txt"))
				}
			}},
	}

	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			// Negative first: denial leaves the workspace untouched and reports why.
			denied, deniedReqs := approverHooks(false)
			out := runTool(t, c.build(denied), c.args)
			if !strings.Contains(errOf(out), "not approved") {
				t.Errorf("expected not-approved error, got %v", out)
			}
			if len(*deniedReqs) != 1 {
				t.Errorf("expected exactly one approval request, got %d", len(*deniedReqs))
			}
			c.check(t, false)

			// No approver at all: fail closed.
			out = runTool(t, c.build(NewHooks(Policy{})), c.args)
			if !strings.Contains(errOf(out), "not approved") {
				t.Errorf("expected fail-closed denial, got %v", out)
			}
			c.check(t, false)

			// Positive: approval applies the change.
			approved, reqs := approverHooks(true)
			out = runTool(t, c.build(approved), c.args)
			if errOf(out) != "" {
				t.Fatalf("approved call failed: %v", out)
			}
			if len(*reqs) != 1 || (*reqs)[0].Tool != c.name {
				t.Errorf("unexpected approval requests %v", *reqs)
			}
			c.check(t, true)
		})
	}
}
