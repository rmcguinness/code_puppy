package tools

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceRel(t *testing.T) {
	ws, dir := newTestWorkspace(t)

	// Positive: relative paths, cleaned paths, and absolute paths inside the
	// workspace (both the raw TempDir spelling and the symlink-resolved one).
	positive := map[string]string{
		"":                               ".",
		"a/b.txt":                        filepath.Join("a", "b.txt"),
		"./a/../b.txt":                   "b.txt",
		filepath.Join(dir, "x", "y.go"):  filepath.Join("x", "y.go"),
		filepath.Join(ws.Dir(), "z.txt"): "z.txt",
		dir:                              ".",
	}
	for in, want := range positive {
		got, err := ws.Rel(in)
		if err != nil {
			t.Errorf("Rel(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("Rel(%q) = %q, want %q", in, got, want)
		}
	}

	// Negative: traversal and absolute paths outside the workspace.
	negative := []string{
		"..",
		"../secret",
		"a/../../secret",
		"/etc/passwd",
		filepath.Join(filepath.Dir(dir), "sibling", "f.txt"),
	}
	for _, in := range negative {
		if _, err := ws.Rel(in); !errors.Is(err, ErrOutsideWorkspace) {
			t.Errorf("Rel(%q) expected ErrOutsideWorkspace, got %v", in, err)
		}
	}
}

func TestWorkspaceSymlinkEscapeRejected(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), "top secret")
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	// Lexically "link/secret.txt" is inside the workspace; os.Root must refuse it.
	if _, err := ws.ReadFile(filepath.Join("link", "secret.txt")); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
	out := runTool(t, toolOf(t)(NewReadFileTool(ws)), map[string]any{"path": "link/secret.txt"})
	if errOf(out) == "" || strings.Contains(out["content"].(string), "top secret") {
		t.Fatalf("read_file followed a symlink out of the workspace: %v", out)
	}

	// Writes through the symlink must also fail and leave the target untouched.
	if err := ws.WriteFileAtomic(filepath.Join("link", "secret.txt"), []byte("pwned")); err == nil {
		t.Fatal("expected write through escaping symlink to fail")
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret.txt")); string(b) != "top secret" {
		t.Fatalf("outside file modified: %q", b)
	}

	// Positive: a relative symlink that stays inside the workspace works.
	writeFile(t, filepath.Join(dir, "real", "f.txt"), "inside")
	if err := os.Symlink("real", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if b, err := ws.ReadFile(filepath.Join("alias", "f.txt")); err != nil || string(b) != "inside" {
		t.Fatalf("expected internal symlink to resolve, got %q, %v", b, err)
	}

	// os.Root treats absolute symlink targets as escapes even when they point
	// back inside the root; document that behaviour.
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "absalias")); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.ReadFile(filepath.Join("absalias", "f.txt")); err == nil {
		t.Fatal("expected absolute symlink target to be rejected by os.Root")
	}
}

func TestWorkspaceReadFileSizeLimit(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	writeFile(t, filepath.Join(dir, "small.txt"), strings.Repeat("a", 100))
	writeFile(t, filepath.Join(dir, "big.txt"), strings.Repeat("a", 101))

	if b, err := ws.ReadFile("small.txt"); err != nil || len(b) != 100 {
		t.Errorf("expected small file to read, got %d bytes, %v", len(b), err)
	}
	if _, err := ws.ReadFile("big.txt"); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("expected size limit error, got %v", err)
	}
	if _, err := ws.ReadFile("."); err == nil {
		t.Errorf("expected error reading a directory")
	}
}

func TestWorkspaceWriteFileAtomic(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	script := filepath.Join(dir, "run.sh")
	writeFile(t, script, "#!/bin/sh\necho old\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}

	// Positive: content replaced, executable mode preserved, no temp files left.
	if err := ws.WriteFileAtomic("run.sh", []byte("#!/bin/sh\necho new\n")); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(script)
	if info.Mode().Perm() != 0o755 {
		t.Errorf("expected mode 0755 preserved, got %v", info.Mode().Perm())
	}
	if b, _ := os.ReadFile(script); !strings.Contains(string(b), "new") {
		t.Errorf("content not replaced: %q", b)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}

	// Positive: new nested file gets created with parents.
	if err := ws.WriteFileAtomic(filepath.Join("a", "b", "c.txt"), []byte("x")); err != nil {
		t.Fatal(err)
	}

	// Negative: refusing to replace a directory.
	if err := ws.WriteFileAtomic("a", []byte("x")); err == nil {
		t.Error("expected error writing over a directory")
	}
}

func TestWorkspaceCreateExclusiveAndRemove(t *testing.T) {
	ws, dir := newTestWorkspace(t)

	if err := ws.CreateExclusive("new.txt", []byte("hi")); err != nil {
		t.Fatalf("CreateExclusive: %v", err)
	}
	if err := ws.CreateExclusive("new.txt", []byte("again")); !errors.Is(err, fs.ErrExist) {
		t.Errorf("expected fs.ErrExist, got %v", err)
	}

	if err := ws.RemoveFile("new.txt"); err != nil {
		t.Errorf("RemoveFile: %v", err)
	}
	if err := ws.RemoveFile("new.txt"); err == nil {
		t.Error("expected error removing missing file")
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ws.RemoveFile("sub"); err == nil {
		t.Error("expected RemoveFile to refuse directories")
	}
	if err := ws.RemoveFile("."); err == nil {
		t.Error("expected RemoveFile to refuse the workspace root")
	}
}

func TestWorkspaceAbs(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := ws.Abs("sub"); err != nil || got != filepath.Join(ws.Dir(), "sub") {
		t.Errorf("Abs(sub) = %q, %v", got, err)
	}
	if _, err := ws.Abs("missing"); err == nil {
		t.Error("expected error for missing path")
	}
}

func TestWorkspaceWriteThroughSymlink(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	writeFile(t, filepath.Join(dir, "target.txt"), "old")
	if err := os.Symlink("target.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	rt := toolOf(t)(NewReplaceInFileTool(ws, allowAll()))
	out := runTool(t, rt, map[string]any{"path": "link.txt", "target_content": "old", "replacement_content": "new"})
	if errOf(out) != "" {
		t.Fatalf("edit through symlink failed: %v", out)
	}
	// Positive: target updated, link preserved.
	if b, _ := os.ReadFile(filepath.Join(dir, "target.txt")); string(b) != "new" {
		t.Errorf("target not updated: %q", b)
	}
	if info, _ := os.Lstat(filepath.Join(dir, "link.txt")); info.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink was replaced by a regular file")
	}
}
