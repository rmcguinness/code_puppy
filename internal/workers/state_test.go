package workers

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/internal/config"
)

func TestStoreStatesAndPersistence(t *testing.T) {
	root := t.TempDir()
	dir := writeWorker(t, root, "deps", valid)
	w, _ := Load(dir)
	path := filepath.Join(t.TempDir(), "workers.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ws := "/work"
	if got := s.State(ws, w, nil); got != StateNew {
		t.Errorf("new: %s", got)
	}
	if err := s.Enable(ws, w, "sha256:reviewed-something-else"); !errors.Is(err, ErrHashMismatch) {
		t.Errorf("stale hash: %v", err)
	}
	if err := s.Enable(ws, w, w.Hash); err != nil {
		t.Fatal(err)
	}
	if got := s.State(ws, w, nil); got != StateEnabled || len(s.Workspaces()) != 1 {
		t.Errorf("enabled: %s %v", got, s.Workspaces())
	}

	// Another process reads the same state.
	again, _ := OpenStore(path)
	if got := again.State(ws, w, nil); got != StateEnabled {
		t.Errorf("reloaded: %s", got)
	}

	// Editing the worker suspends it until re-enabled.
	os.WriteFile(w.Path, []byte(valid+"\nAlso check tools.\n"), 0o644)
	edited, _ := Load(dir)
	if got := s.State(ws, edited, nil); got != StateChanged {
		t.Errorf("edited: %s", got)
	}
	if err := s.Disable(ws, "deps"); err != nil {
		t.Fatal(err)
	}
	if got := s.State(ws, edited, nil); got != StateDisabled || len(s.Workspaces()) != 0 {
		t.Errorf("disabled: %s %v", got, s.Workspaces())
	}
	if got := s.State(ws, edited, errors.New("broken")); got != StateInvalid {
		t.Errorf("invalid: %s", got)
	}
}

func TestApplyPolicy(t *testing.T) {
	w, err := Load(writeWorker(t, t.TempDir(), "deps", `---
schedule: hourly
permissions: ["shell:go list -m -u all", "web:proxy.golang.org"]
limits: { max_turns: 500, timeout: 5h }
---
do it
`))
	if err != nil {
		t.Fatal(err)
	}
	p := config.DefaultConfig().Workers.Policy
	p.Allow = []string{"shell", "write"}
	eff := Apply(w, p)
	if len(eff.Permissions) != 1 || eff.Permissions[0].Kind != "shell" {
		t.Errorf("permissions %v", eff.Permissions)
	}
	if eff.Limits.MaxTurns != p.MaxTurns || eff.Limits.MaxCostUSD != p.DefaultMaxCostUSD || eff.Limits.Timeout != 2*time.Hour {
		t.Errorf("limits %+v", eff.Limits)
	}
	if len(eff.Notes) != 3 {
		t.Errorf("notes %q", eff.Notes)
	}
}
