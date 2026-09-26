package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathMatcher(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := t.TempDir()
	m, err := NewPathMatcher([]string{".env", "*.pem", "~/.ssh", "config/prod/*.yaml", "/opt/secret/**/token"}, []string{root})
	if err != nil {
		t.Fatal(err)
	}

	blocked := []string{
		filepath.Join(root, ".env"),
		filepath.Join(root, "deep", "nested", ".env"),
		filepath.Join(root, ".env", "inside-dir"), // a blocked directory blocks its contents
		filepath.Join(root, "certs", "server.pem"),
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(root, "config", "prod", "db.yaml"),
		"/opt/secret/token",
		"/opt/secret/a/b/token",
	}
	for _, p := range blocked {
		if _, ok := m.Match(p); !ok {
			t.Errorf("expected %s to be blocked", p)
		}
	}
	allowed := []string{
		filepath.Join(root, ".env.example"),
		filepath.Join(root, "env"),
		filepath.Join(root, "server.pem.txt"),
		filepath.Join(home, ".sshrc"),
		filepath.Join(root, "config", "dev", "db.yaml"),
		filepath.Join(root, "config", "prod", "sub", "db.yaml"), // "*" doesn't cross "/"
		"/opt/secret/tokens",
	}
	for _, p := range allowed {
		if pat, ok := m.Match(p); ok {
			t.Errorf("expected %s to be allowed, blocked by %q", p, pat)
		}
	}

	if caseInsensitiveFS {
		if _, ok := m.Match(filepath.Join(root, ".ENV")); !ok {
			t.Error("expected case-insensitive match on this platform")
		}
	}

	// Negative: patterns that could break out of the sandbox profile string.
	if _, err := NewPathMatcher([]string{`bad"pattern`}, nil); err == nil {
		t.Error("expected error for pattern containing a quote")
	}
	var nilMatcher *PathMatcher
	if _, ok := nilMatcher.Match("/x"); ok {
		t.Error("nil matcher should match nothing")
	}
}

type sandboxFixture struct {
	ws                          *Workspace
	work, shared, docs, outside string
}

func newSandboxFixture(t *testing.T) sandboxFixture {
	t.Helper()
	f := sandboxFixture{work: t.TempDir(), shared: t.TempDir(), docs: t.TempDir(), outside: t.TempDir()}
	writeFile(t, filepath.Join(f.work, "main.go"), "package main\n// secret-token here\n")
	writeFile(t, filepath.Join(f.work, ".env"), "API_KEY=secret-token\n")
	writeFile(t, filepath.Join(f.work, "certs", "tls.pem"), "secret-token")
	writeFile(t, filepath.Join(f.work, "vendor-ro", "lib.go"), "package lib\n")
	writeFile(t, filepath.Join(f.shared, "notes.md"), "shared notes\n")
	writeFile(t, filepath.Join(f.docs, "guide.md"), "read only guide\n")
	writeFile(t, filepath.Join(f.outside, "x.txt"), "outside\n")

	ws, err := OpenWorkspace(WorkspaceOptions{
		Dir:           f.work,
		AllowedPaths:  []string{f.shared},
		ReadOnlyPaths: []string{f.docs, filepath.Join(f.work, "vendor-ro")},
		BlockedPaths:  []string{".env", "*.pem"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	f.ws = ws
	return f
}

func TestWorkspaceMultiRootAccess(t *testing.T) {
	f := newSandboxFixture(t)
	ws := f.ws

	// Positive: read and write in the extra read-write root by absolute path.
	notes := filepath.Join(f.shared, "notes.md")
	if b, err := ws.ReadFile(notes); err != nil || string(b) != "shared notes\n" {
		t.Errorf("read allowed root: %q %v", b, err)
	}
	if err := ws.WriteFileAtomic(filepath.Join(f.shared, "new.md"), []byte("x")); err != nil {
		t.Errorf("write allowed root: %v", err)
	}
	// Display paths: workspace-relative inside, absolute outside.
	if got, _ := ws.Rel(filepath.Join(f.work, "main.go")); got != "main.go" {
		t.Errorf("Rel inside workspace = %q", got)
	}
	if got, _ := ws.Rel(notes); got != filepath.Join(ws.RootDirs()[1], "notes.md") {
		t.Errorf("Rel in allowed root = %q", got)
	}

	// Positive: read-only roots are readable...
	guide := filepath.Join(f.docs, "guide.md")
	if _, err := ws.ReadFile(guide); err != nil {
		t.Errorf("read read-only root: %v", err)
	}
	// ...negative: but not writable or deletable, including a read-only root
	// nested inside the writable workspace.
	for _, p := range []string{guide, filepath.Join(f.docs, "new.md"), "vendor-ro/lib.go"} {
		if err := ws.WriteFileAtomic(p, []byte("x")); !errors.Is(err, ErrReadOnlyPath) {
			t.Errorf("write %s: expected ErrReadOnlyPath, got %v", p, err)
		}
		if _, err := ws.WritablePath(p); !errors.Is(err, ErrReadOnlyPath) {
			t.Errorf("WritablePath %s: expected ErrReadOnlyPath, got %v", p, err)
		}
	}
	if err := ws.RemoveFile(guide); !errors.Is(err, ErrReadOnlyPath) {
		t.Errorf("delete in read-only root: %v", err)
	}

	// Negative: paths outside every root.
	if _, err := ws.ReadFile(filepath.Join(f.outside, "x.txt")); !errors.Is(err, ErrOutsideWorkspace) {
		t.Errorf("expected ErrOutsideWorkspace, got %v", err)
	}
}

func TestWorkspaceBlockedPaths(t *testing.T) {
	f := newSandboxFixture(t)
	ws := f.ws

	for _, p := range []string{".env", "certs/tls.pem", filepath.Join(f.work, ".env")} {
		if _, err := ws.ReadFile(p); !errors.Is(err, ErrBlockedPath) {
			t.Errorf("read %s: expected ErrBlockedPath, got %v", p, err)
		}
		if err := ws.WriteFileAtomic(p, []byte("x")); !errors.Is(err, ErrBlockedPath) {
			t.Errorf("write %s: expected ErrBlockedPath, got %v", p, err)
		}
	}
	// Creating a new blocked file is refused too.
	if err := ws.CreateExclusive("sub/.env", []byte("x")); !errors.Is(err, ErrBlockedPath) {
		t.Errorf("create blocked: %v", err)
	}

	// A symlink with an innocent name pointing at a blocked file is refused.
	if err := os.Symlink(".env", filepath.Join(f.work, "innocent.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.ReadFile("innocent.txt"); !errors.Is(err, ErrBlockedPath) {
		t.Errorf("symlink to blocked file: expected ErrBlockedPath, got %v", err)
	}

	// grep and list_files never surface blocked content.
	m, err := grepWorkspace(context.Background(), ws, GrepInput{Query: "secret-token"})
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 || m[0].File != "main.go" {
		t.Errorf("grep leaked blocked files: %v", m)
	}
	if _, err := grepWorkspace(context.Background(), ws, GrepInput{Query: "x", Path: ".env"}); err == nil {
		t.Error("expected grep on a blocked path to fail")
	}
	out := runTool(t, toolOf(t)(NewListFilesTool(ws)), map[string]any{"recursive": true})
	for _, e := range out["files"].([]any) {
		if p := e.(map[string]any)["path"].(string); strings.HasSuffix(p, ".pem") {
			t.Errorf("list_files showed blocked file %s", p)
		}
	}
}

func TestWorkspaceRejectsBadRoots(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenWorkspace(WorkspaceOptions{Dir: dir, AllowedPaths: []string{filepath.Join(dir, "missing")}}); err == nil {
		t.Error("expected error for missing allowed path")
	}
	file := filepath.Join(dir, "f")
	writeFile(t, file, "x")
	if _, err := OpenWorkspace(WorkspaceOptions{Dir: dir, ReadOnlyPaths: []string{file}}); err == nil {
		t.Error("expected error when a root is a file")
	}
}

func TestWriteToolsSkipApprovalWhenSandboxRefuses(t *testing.T) {
	f := newSandboxFixture(t)
	hooks, reqs := approverHooks(true)
	create := toolOf(t)(NewCreateFileTool(f.ws, hooks))
	out := runTool(t, create, map[string]any{"path": filepath.Join(f.docs, "x.md"), "content": "x"})
	if !strings.Contains(errOf(out), "read-only") {
		t.Errorf("expected read-only error, got %v", out)
	}
	out = runTool(t, toolOf(t)(NewDeleteFileTool(f.ws, hooks)), map[string]any{"path": ".env"})
	if !strings.Contains(errOf(out), "blocked") {
		t.Errorf("expected blocked error, got %v", out)
	}
	if len(*reqs) != 0 {
		t.Errorf("user was asked to approve %d writes the sandbox refuses", len(*reqs))
	}
}

func TestDefaultShellWritableDirs(t *testing.T) {
	dirs := DefaultShellWritableDirs()
	tmp, err := canonicalDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range dirs {
		if d == tmp || strings.HasPrefix(tmp, d+string(filepath.Separator)) {
			found = true
		}
		if !filepath.IsAbs(d) {
			t.Errorf("non-absolute writable dir %q", d)
		}
	}
	if !found {
		t.Errorf("temp dir %s not writable by default: %v", tmp, dirs)
	}
}

func TestExecEnvScrubsCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-leak")
	t.Setenv("MY_SERVICE_SECRET", "hunter2")
	t.Setenv("HARMLESS_VALUE", "visible")
	ws, _ := newTestWorkspace(t)
	cfg := ShellConfig{Workspace: ws, Hooks: allowAll(), Exec: &ExecEnv{ScrubEnv: []string{"*_api_key", "*_SECRET"}}}

	out := runShellCommand(context.Background(), cfg, RunShellCommandInput{Command: "env"})
	if strings.Contains(out.Output, "sk-leak") || strings.Contains(out.Output, "hunter2") {
		t.Errorf("credentials leaked to child process:\n%s", out.Output)
	}
	if !strings.Contains(out.Output, "HARMLESS_VALUE=visible") {
		t.Error("non-secret variables should pass through")
	}
	// Without scrubbing configured, the environment is inherited unchanged.
	cfg.Exec = &ExecEnv{}
	if out := runShellCommand(context.Background(), cfg, RunShellCommandInput{Command: "env"}); !strings.Contains(out.Output, "sk-leak") {
		t.Error("expected unscrubbed environment when ScrubEnv is empty")
	}
}
