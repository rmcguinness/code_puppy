package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// barrierHooks approves every request, but holds the first approval until a
// second request arrives or wait elapses. Without per-file locking both edits
// read the original file during that window and the second write clobbers
// the first.
func barrierHooks(wait time.Duration) *Hooks {
	var mu sync.Mutex
	seen := 0
	second := make(chan struct{})
	h := NewHooks(Policy{})
	h.SetApprover(func(ctx context.Context, req ApprovalRequest) (Decision, error) {
		mu.Lock()
		seen++
		n := seen
		mu.Unlock()
		if n == 2 {
			close(second)
			return DecisionOnce, nil
		}
		if n == 1 {
			select {
			case <-second:
			case <-time.After(wait):
			}
		}
		return DecisionOnce, nil
	})
	return h
}

func runParallel(t *testing.T, calls ...func() map[string]any) []map[string]any {
	t.Helper()
	outs := make([]map[string]any, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Go(func() { outs[i] = call() })
	}
	wg.Wait()
	return outs
}

func TestParallelEditsToSameFileKeepBothChanges(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	path := filepath.Join(dir, "f.go")
	writeFile(t, path, "alpha\nbeta\n")
	edit := toolOf(t)(NewReplaceInFileTool(ws, barrierHooks(300*time.Millisecond)))

	outs := runParallel(t,
		func() map[string]any {
			return runTool(t, edit, map[string]any{"path": "f.go", "target_content": "alpha", "replacement_content": "ALPHA"})
		},
		func() map[string]any {
			return runTool(t, edit, map[string]any{"path": "f.go", "target_content": "beta", "replacement_content": "BETA"})
		},
	)
	for _, out := range outs {
		if e := errOf(out); e != "" {
			t.Fatalf("edit failed: %s", e)
		}
	}
	got, _ := os.ReadFile(path)
	if string(got) != "ALPHA\nBETA\n" {
		t.Fatalf("an edit was lost: %q", got)
	}
}

func TestParallelPatchAndEditKeepBothChanges(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "one\ntwo\nthree\nfour\nfive\n")
	hooks := barrierHooks(300 * time.Millisecond)
	patch := toolOf(t)(NewApplyPatchTool(ws, hooks))
	edit := toolOf(t)(NewReplaceInFileTool(ws, hooks))

	outs := runParallel(t,
		func() map[string]any {
			return runTool(t, patch, map[string]any{"patch": "*** Begin Patch\n*** Update File: a.txt\n@@\n-one\n+ONE\n two\n*** End Patch\n"})
		},
		func() map[string]any {
			return runTool(t, edit, map[string]any{"path": "a.txt", "target_content": "five", "replacement_content": "FIVE"})
		},
	)
	for _, out := range outs {
		if e := errOf(out); e != "" {
			t.Fatalf("call failed: %s", e)
		}
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "ONE\ntwo\nthree\nfour\nFIVE\n" {
		t.Fatalf("an edit was lost: %q", got)
	}
}

// A file changed outside the tools while the user was looking at the diff
// must not be overwritten with a version built from the stale content.
func TestEditRefusesWhenFileChangedDuringApproval(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	path := filepath.Join(dir, "f.txt")
	writeFile(t, path, "hello\n")

	h := NewHooks(Policy{})
	h.SetApprover(func(ctx context.Context, req ApprovalRequest) (Decision, error) {
		writeFile(t, path, "hello\nuser line\n")
		return DecisionOnce, nil
	})

	cases := map[string]func() map[string]any{
		"replace_in_file": func() map[string]any {
			return runTool(t, toolOf(t)(NewReplaceInFileTool(ws, h)), map[string]any{"path": "f.txt", "target_content": "hello", "replacement_content": "bye"})
		},
		"delete_snippet": func() map[string]any {
			return runTool(t, toolOf(t)(NewDeleteSnippetTool(ws, h)), map[string]any{"path": "f.txt", "snippet": "hello"})
		},
		"create_file": func() map[string]any {
			return runTool(t, toolOf(t)(NewCreateFileTool(ws, h)), map[string]any{"path": "f.txt", "content": "new", "overwrite": true})
		},
		"delete_file": func() map[string]any {
			return runTool(t, toolOf(t)(NewDeleteFileTool(ws, h)), map[string]any{"path": "f.txt"})
		},
		"apply_patch": func() map[string]any {
			return runTool(t, toolOf(t)(NewApplyPatchTool(ws, h)), map[string]any{"patch": "*** Begin Patch\n*** Update File: f.txt\n@@\n-hello\n+bye\n*** End Patch\n"})
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			writeFile(t, path, "hello\n")
			out := run()
			if e := errOf(out); !strings.Contains(e, "changed") {
				t.Fatalf("want a changed-file error, got %v", out)
			}
			if got, _ := os.ReadFile(path); string(got) != "hello\nuser line\n" {
				t.Fatalf("user's change was overwritten: %q", got)
			}
		})
	}
}

func TestPathLocksHonourContext(t *testing.T) {
	ws, _ := newTestWorkspace(t)
	unlock, err := ws.lockPaths(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := ws.lockPaths(ctx, "y", "x"); err == nil {
		t.Fatal("lock on a held path should fail when ctx expires")
	}
	// "y" must have been released after the failed attempt.
	u2, err := ws.lockPaths(context.Background(), "y")
	if err != nil {
		t.Fatal(err)
	}
	u2()
}
