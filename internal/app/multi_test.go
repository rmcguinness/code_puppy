package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/i18n"
	"github.com/retail-cortex/code_puppy/internal/runtime"
)

// One process can hold several workspaces (the per-user service does):
// nothing may depend on the process's working directory, and per-workspace
// state stays separate.
func TestTwoWorkspacesInOneProcess(t *testing.T) {
	defer i18n.SetCurrent(nil)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MODENV_PREFIX", "")
	t.Chdir(t.TempDir()) // somewhere unrelated to either workspace

	open := func(agent string) *Workspace {
		t.Helper()
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, "agents"), 0o700)
		os.WriteFile(filepath.Join(dir, "agents", agent+".md"), []byte("---\nname: "+agent+"\ndisplay_name: "+agent+"\ndescription: d\ntools: []\n---\nprompt\n"), 0o600)
		cfg := config.DefaultConfig()
		cfg.Tools.WorkspaceDir = dir
		cfg.CodePuppy.TrustWorkspace = true // so ./agents is read
		cfg.Session.StorageDir = t.TempDir()
		w, err := Open(context.Background(), cfg, Options{Model: runtime.NewMockLLM("m"), NewModel: mockModels})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { w.Close() })
		return w
	}
	a, b := open("alpha"), open("beta")

	has := func(w *Workspace, name string) bool {
		return slices.ContainsFunc(w.ListAgents(), func(x AgentInfo) bool { return x.Name == name })
	}
	if !has(a, "alpha") || has(a, "beta") || !has(b, "beta") || has(b, "alpha") {
		t.Errorf("each workspace should read its own ./agents: a=%v b=%v", has(a, "alpha"), has(b, "beta"))
	}
	if a.Dir() == b.Dir() {
		t.Fatal("same directory")
	}

	// Reply languages are per workspace.
	if _, err := a.SetLocale(context.Background(), "es"); err != nil {
		t.Fatal(err)
	}
	if a.Settings().Locale != "es" || b.Settings().Locale != "en-US" {
		t.Errorf("locales a=%s b=%s", a.Settings().Locale, b.Settings().Locale)
	}

}

// Only one Workspace, in any process, owns a workspace at a time.
func TestAWorkspaceHasOneOwner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MODENV_PREFIX", "")
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	open := func(d string) (*Workspace, error) {
		cfg := config.DefaultConfig()
		cfg.Tools.WorkspaceDir = d
		cfg.Session.StorageDir = t.TempDir()
		return Open(context.Background(), cfg, Options{Model: runtime.NewMockLLM("m")})
	}
	first, err := open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := open(link); !errors.Is(err, ErrWorkspaceBusy) {
		t.Fatalf("second owner (through a symlink): %v", err)
	}
	first.Close()
	again, err := open(dir)
	if err != nil {
		t.Fatalf("after Close: %v", err)
	}
	again.Close()
}
