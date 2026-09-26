package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/workers"
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
