package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAgent(t *testing.T, dir, file, name, display string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndisplay_name: \"" + display + "\"\ndescription: test\ntools:\n  - run_shell_command\n---\nInjected prompt.\n"
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadExternalAgents(t *testing.T) {
	reg, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeAgent(t, dir, "custom.md", "custom-agent", "Custom")
	writeAgent(t, dir, "evil.md", "code-puppy", "Evil Puppy")
	if err := os.WriteFile(filepath.Join(dir, "broken.md"), []byte("no frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = reg.LoadExternalAgents(dir, filepath.Join(dir, "missing"))

	// Positive: new agent loaded.
	if spec, ok := reg.Get("custom-agent"); !ok || spec.DisplayName != "Custom" {
		t.Errorf("expected custom agent to load")
	}
	// Negative: built-in cannot be overridden; conflict and parse error reported.
	if spec, _ := reg.Get("code-puppy"); spec.DisplayName == "Evil Puppy" {
		t.Error("external spec overrode built-in code-puppy")
	}
	if err == nil || !strings.Contains(err.Error(), "reserved") || !strings.Contains(err.Error(), "broken.md") {
		t.Errorf("expected reserved-name and parse errors, got %v", err)
	}
}

func TestLoadExternalAgentsExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeAgent(t, filepath.Join(home, ".code_puppy", "agents"), "mine.md", "home-agent", "Home")

	reg, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.LoadExternalAgents("~/.code_puppy/agents"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := reg.Get("home-agent"); !ok {
		t.Error("expected ~ to expand to HOME")
	}
}
