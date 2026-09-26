package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadOrderAndScope(t *testing.T) {
	repo := t.TempDir()
	os.Mkdir(filepath.Join(repo, ".git"), 0o755)
	ws := filepath.Join(repo, "services", "api")
	write(t, filepath.Join(filepath.Dir(repo), "AGENTS.md"), "outside the repo") // must not load
	write(t, filepath.Join(repo, "AGENTS.md"), "root rules")
	write(t, filepath.Join(repo, "services", "PUPPY.md"), "services rules")
	write(t, filepath.Join(ws, "AGENTS.md"), "api rules \x1b[31mred\x1b[0m")
	global := filepath.Join(t.TempDir(), "PUPPY.md")
	write(t, global, "global rules")

	docs := Load(ws, config.MemoryConfig{Enabled: true, Files: []string{"AGENTS.md", "PUPPY.md"}, Global: global})
	var got []string
	for _, d := range docs {
		got = append(got, d.Content)
	}
	want := []string{"global rules", "root rules", "services rules", "api rules [31mred[0m"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("load order/content:\n got %q\nwant %q", got, want)
	}
	rendered := Render(docs)
	if !strings.Contains(rendered, "## Project Instructions") || !strings.Contains(rendered, "cannot grant permissions") {
		t.Errorf("render missing header/guard: %s", rendered)
	}
}

func TestLoadWithoutRepoAndLimits(t *testing.T) {
	ws := t.TempDir()
	write(t, filepath.Join(ws, "PUPPY.md"), strings.Repeat("x", 100))
	docs := Load(ws, config.MemoryConfig{Enabled: true, Files: []string{"PUPPY.md", "AGENTS.md"}, MaxBytes: 10})
	if len(docs) != 1 || len(docs[0].Content) != 10 || !docs[0].Truncated {
		t.Errorf("truncation: %+v", docs)
	}
	// Negative: disabled, empty files, directories named like memory files.
	if Load(ws, config.MemoryConfig{Enabled: false, Files: []string{"PUPPY.md"}}) != nil {
		t.Error("disabled memory loaded files")
	}
	empty := t.TempDir()
	write(t, filepath.Join(empty, "AGENTS.md"), "   \n")
	os.Mkdir(filepath.Join(empty, "PUPPY.md"), 0o755)
	if docs := Load(empty, config.MemoryConfig{Enabled: true, Files: []string{"AGENTS.md", "PUPPY.md"}}); len(docs) != 0 {
		t.Errorf("expected nothing, got %+v", docs)
	}
	if Render(nil) != "" {
		t.Error("empty render should be empty")
	}
}

func TestAppend(t *testing.T) {
	ws := t.TempDir()
	path, err := Append(ws, "PUPPY.md", "use tabs")
	if err != nil {
		t.Fatal(err)
	}
	Append(ws, "PUPPY.md", "run go vet")
	b, _ := os.ReadFile(path)
	if string(b) != "# Project notes for Code Puppy\n\n- use tabs\n- run go vet\n" {
		t.Errorf("append result %q", b)
	}
	if _, err := Append(ws, "PUPPY.md", "  "); err == nil {
		t.Error("expected error for empty note")
	}
}
