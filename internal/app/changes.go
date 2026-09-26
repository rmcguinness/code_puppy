package app

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/retail-cortex/code_puppy/internal/audit"
	"github.com/retail-cortex/code_puppy/internal/tools"
)

// Checkpoints of the agent's file changes, and the approvals it was given.

// Checkpoint is the files one turn changed, which Undo restores.
type Checkpoint struct {
	ID    int
	Label string // the prompt that started the turn, shortened
	Time  time.Time
	Files []string
}

// ListCheckpoints returns the turns that changed files, newest first.
func (w *Workspace) ListCheckpoints() []Checkpoint {
	var out []Checkpoint
	for _, c := range w.tools.Checkpoints().List() {
		out = append(out, Checkpoint{ID: c.ID, Label: c.Label, Time: c.Time, Files: c.Files})
	}
	return out
}

// ErrUndoConflict reports files changed since the agent's edit; Undo with
// force restores them anyway.
var ErrUndoConflict = tools.ErrUndoConflict

// UndoResult is what Undo restored.
type UndoResult struct {
	Label    string // the undone turn's checkpoint label
	Restored []string
}

// Undo restores the files the latest turn with changes modified, and
// audits it. A partial restore returns both what was restored and why the
// rest wasn't.
func (w *Workspace) Undo(force bool) (UndoResult, error) {
	res, err := w.tools.Checkpoints().Undo(force)
	out := UndoResult{Label: res.Turn.Label, Restored: res.Restored}
	if len(res.Restored) > 0 {
		w.tools.Hooks().Audit().Log(audit.Entry{Kind: audit.KindUndo, Detail: strings.Join(res.Restored, ", ")})
	}
	return out, err
}

// SessionDiff is a unified diff of every file the agent changed this
// session, against its state before the first change ("" when none).
func (w *Workspace) SessionDiff() string { return w.tools.Checkpoints().SessionDiff() }

// GitDiff runs git diff (stat and patch) in the workspace. color keeps
// git's terminal colours. On failure the output holds git's message.
func (w *Workspace) GitDiff(ctx context.Context, color bool) (string, error) {
	ui := "never"
	if color {
		ui = "always"
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "color.ui="+ui, "diff", "--stat", "--patch")
	cmd.Dir = w.Dir()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Approval is a standing permission: actions it covers run without asking.
type Approval struct {
	// Key identifies the approval, for RevokeApprovals.
	Key string
	// Kind is what it allows: "cmd" (a shell command), "write", "delete",
	// "web" (a host), "mcp" (an MCP tool), "uc-run" (a forged tool), or
	// another kind a newer version added.
	Kind string
	// Subject is the command, path, host or tool.
	Subject string
	// Dir is the workspace a command approval is limited to ("" if any).
	Dir string
	// Always: saved for future sessions (Added says when); otherwise it
	// lasts until this process exits.
	Always bool
	Added  time.Time
}

func parseApproval(key string) Approval {
	a := Approval{Key: key}
	kind, rest, ok := strings.Cut(key, ":")
	if !ok {
		return Approval{Key: key, Subject: key}
	}
	a.Kind, a.Subject = kind, rest
	switch kind {
	case "cmd":
		if dir, cmd, ok := strings.Cut(rest, "\x00"); ok {
			a.Dir, a.Subject = dir, cmd
		}
	case "uc-run":
		a.Subject = strings.ReplaceAll(rest, "\x00", " ")
	}
	return a
}

// ListApprovals returns this session's approvals, then saved ones.
func (w *Workspace) ListApprovals() []Approval {
	hooks := w.tools.Hooks()
	var out []Approval
	for _, k := range hooks.SessionRules() {
		out = append(out, parseApproval(k))
	}
	for _, r := range hooks.Store().Rules() {
		a := parseApproval(r.Key)
		a.Always, a.Added = true, r.Added
		out = append(out, a)
	}
	return out
}

// RevokeApprovals removes approvals by key, for this session and from the
// saved ones, and returns how many keys it was given.
func (w *Workspace) RevokeApprovals(keys ...string) int {
	hooks := w.tools.Hooks()
	store := hooks.Store()
	for _, k := range keys {
		hooks.RevokeSession(k)
		if store != nil {
			store.Remove(k)
		}
	}
	return len(keys)
}

// ClearApprovals revokes every approval and returns how many there were.
func (w *Workspace) ClearApprovals() int {
	var keys []string
	for _, a := range w.ListApprovals() {
		keys = append(keys, a.Key)
	}
	return w.RevokeApprovals(keys...)
}
