package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/tools"
	"github.com/retail-cortex/code_puppy/internal/workers"
	"google.golang.org/genai"
)

func addWorker(t *testing.T, w *Workspace, name, content string) {
	t.Helper()
	dir := filepath.Join(w.Dir(), "workers", name)
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, workers.FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWorkersLifecycle(t *testing.T) {
	w, _ := openTestWith(t, func(c *config.Config) { c.Workers.Policy.Allow = []string{"shell", "write"} })
	addWorker(t, w, "deps", "---\nschedule: Daily at 6 AM\npermissions: [\"shell:go list -m -u all\", \"web:proxy.golang.org\"]\n---\nReport outdated modules.\n")
	addWorker(t, w, "broken", "---\nschedule: whenever\n---\ndo it\n")

	list, err := w.ListWorkers()
	if err != nil || len(list) != 2 {
		t.Fatalf("list %v %v", list, err)
	}
	broken, deps := list[0], list[1]
	if broken.State != workers.StateInvalid || len(broken.Problems) == 0 {
		t.Errorf("broken %+v", broken)
	}
	if deps.State != workers.StateNew || deps.Cron != "0 6 * * *" || !deps.Next.IsZero() ||
		len(deps.Permissions) != 1 || deps.Limits.MaxTurns == 0 || len(deps.Problems) != 1 || !strings.Contains(deps.Problems[0], "web") {
		t.Errorf("deps %+v", deps)
	}

	if _, err := w.EnableWorker("deps", "sha256:stale"); !errors.Is(err, workers.ErrHashMismatch) {
		t.Errorf("stale hash: %v", err)
	}
	if _, err := w.EnableWorker("broken", broken.Hash); err == nil {
		t.Error("an invalid worker was enabled")
	}
	if _, err := w.EnableWorker("nope", "x"); !errors.Is(err, ErrUnknownWorker) {
		t.Errorf("unknown: %v", err)
	}
	on, err := w.EnableWorker("deps", deps.Hash)
	if err != nil || on.State != workers.StateEnabled || on.Next.IsZero() {
		t.Fatalf("enable %+v %v", on, err)
	}

	// An edit suspends it.
	addWorker(t, w, "deps", "---\nschedule: Daily at 7 AM\n---\nReport outdated modules.\n")
	if list, _ := w.ListWorkers(); list[1].State != workers.StateChanged || !list[1].Next.IsZero() {
		t.Errorf("after an edit: %+v", list[1])
	}
	off, err := w.DisableWorker("deps")
	if err != nil || off.State != workers.StateDisabled {
		t.Errorf("disable %+v %v", off, err)
	}
}

func TestWorkersCanBeTurnedOff(t *testing.T) {
	w, _ := openTestWith(t, func(c *config.Config) { c.Workers.Enabled = false })
	if _, err := w.ListWorkers(); !errors.Is(err, ErrWorkersDisabled) {
		t.Errorf("%v", err)
	}
}

func enable(t *testing.T, w *Workspace, name string) {
	t.Helper()
	list, err := w.ListWorkers()
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range list {
		if info.Name == name {
			if _, err := w.EnableWorker(name, info.Hash); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no worker %s", name)
}

func TestRunWorkerEnforcesPermissions(t *testing.T) {
	create := func(path string) *genai.Content {
		return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "create_file", Args: map[string]any{"path": path, "content": "x\n"}}}}}
	}
	ask := &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "ask_user_question", Args: map[string]any{"question": "ok?"}}}}}
	// auto_approve is on for people; it must not widen what a worker may do.
	w, llm := openTestWith(t, func(c *config.Config) { c.CodePuppy.AutoApprove = true },
		create("reports/deps.md"), create("main.go"), ask, text("Wrote the report."))
	addWorker(t, w, "deps", "---\nschedule: daily at 6 AM\npermissions: [\"write:reports/\"]\n---\nWrite reports/deps.md.\n")
	user := newSession(t, w)

	if _, err := w.RunWorker(context.Background(), "deps", RunOptions{Manual: true}); !errors.Is(err, ErrWorkerNotEnabled) {
		t.Fatalf("not enabled: %v", err)
	}
	enable(t, w, "deps")
	var results []string
	var started workers.Run
	run, err := w.RunWorker(context.Background(), "deps", RunOptions{Manual: true, OnStart: func(r workers.Run) { started = r }, OnEvent: func(e Event) {
		if e.ToolResult != nil {
			results = append(results, fmt.Sprint(e.ToolResult.Result))
		}
	}})
	if started.ID != run.ID || started.Status != workers.RunRunning {
		t.Errorf("started %+v", started)
	}
	if err != nil || run.Status != workers.RunSucceeded || !run.Manual || run.SessionID == "" || run.SessionID == user.ID {
		t.Fatalf("run %+v %v", run, err)
	}
	if _, err := os.Stat(filepath.Join(w.Dir(), "reports", "deps.md")); err != nil {
		t.Errorf("the permitted write didn't happen: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.Dir(), "main.go")); !os.IsNotExist(err) {
		t.Error("the refused write happened")
	}
	if len(run.Refusals) != 1 || run.Refusals[0].Kind != tools.ActionWrite {
		t.Errorf("refusals %+v", run.Refusals)
	}
	if len(results) != 3 || !strings.Contains(results[2], "unattended") {
		t.Errorf("results %q", results)
	}
	first := llm.Requests[0].Contents
	if got := first[len(first)-1].Parts[0].Text; !strings.Contains(got, "running unattended") || !strings.Contains(got, "Write reports/deps.md.") {
		t.Error("the agent wasn't told it runs unattended")
	}
	// The workspace's own session was left alone.
	if active, _ := w.ActiveSession(); active.ID != user.ID || len(active.Messages) != 0 {
		t.Errorf("active session %s with %d messages", active.ID, len(active.Messages))
	}
	runs, err := w.WorkerRuns("deps", 0)
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID {
		t.Errorf("recorded runs %+v %v", runs, err)
	}
}

func TestRunWorkerStopsAtItsTurnLimit(t *testing.T) {
	list := &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "list_files", Args: map[string]any{}}}}}
	w, _ := openTestWith(t, nil, list, list, list, text("done"))
	addWorker(t, w, "loop", "---\nschedule: hourly\nlimits: {max_turns: 2}\n---\nLook around.\n")
	enable(t, w, "loop")
	run, err := w.RunWorker(context.Background(), "loop", RunOptions{})
	if err != nil || run.Status != workers.RunLimited || run.Error == "" {
		t.Errorf("run %+v %v", run, err)
	}
}
