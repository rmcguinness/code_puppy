package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileTool(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	writeFile(t, filepath.Join(dir, "f.txt"), "one\ntwo\nthree\nfour\n")
	rt := toolOf(t)(NewReadFileTool(ws))

	// Positive: slicing returns only the requested lines, numbered.
	out := runTool(t, rt, map[string]any{"path": "f.txt", "start_line": 2, "end_line": 3})
	content := out["content"].(string)
	if content != "   2: two\n   3: three\n" {
		t.Errorf("unexpected slice content %q", content)
	}
	if out["total_lines"].(float64) != 5 { // trailing newline yields an empty 5th line
		t.Errorf("expected 5 total lines, got %v", out["total_lines"])
	}

	// Positive: out-of-range start clamps to the last line; end < start clamps to start.
	out = runTool(t, rt, map[string]any{"path": "f.txt", "start_line": 99})
	if errOf(out) != "" || out["content"].(string) != "   5: \n" {
		t.Errorf("unexpected clamped output %v", out)
	}
	out = runTool(t, rt, map[string]any{"path": "f.txt", "start_line": 3, "end_line": 1})
	if out["content"].(string) != "   3: three\n" {
		t.Errorf("expected end<start to clamp to start, got %q", out["content"])
	}

	// Negative: outside the workspace, missing, and directories.
	for _, p := range []string{"../escape.txt", "/etc/hosts", "missing.txt", "."} {
		if out := runTool(t, rt, map[string]any{"path": p}); errOf(out) == "" {
			t.Errorf("expected error reading %q, got %v", p, out)
		}
	}
}

func TestReadFileToolLimits(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir, 2*maxReadOutputBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	rt := toolOf(t)(NewReadFileTool(ws))

	// Output larger than maxReadOutputBytes is truncated with a paging hint.
	line := strings.Repeat("x", 99) + "\n"
	writeFile(t, filepath.Join(dir, "large.txt"), strings.Repeat(line, (maxReadOutputBytes/100)+500))
	out := runTool(t, rt, map[string]any{"path": "large.txt"})
	if out["truncated"] != true {
		t.Errorf("expected truncated=true")
	}
	if len(out["content"].(string)) > maxReadOutputBytes+200 {
		t.Errorf("content exceeds cap: %d bytes", len(out["content"].(string)))
	}

	// Files above the workspace size limit are refused outright.
	writeFile(t, filepath.Join(dir, "huge.txt"), strings.Repeat("y", 2*maxReadOutputBytes+1))
	out = runTool(t, rt, map[string]any{"path": "huge.txt"})
	if !strings.Contains(errOf(out), "limit") {
		t.Errorf("expected size limit error, got %v", out)
	}
}

func TestListFilesTool(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "a")
	writeFile(t, filepath.Join(dir, "sub", "b.txt"), "b")
	writeFile(t, filepath.Join(dir, ".git", "config"), "x")
	rt := toolOf(t)(NewListFilesTool(ws))

	paths := func(out map[string]any) []string {
		var ps []string
		files, _ := out["files"].([]any)
		for _, f := range files {
			ps = append(ps, f.(map[string]any)["path"].(string))
		}
		return ps
	}

	// Positive: non-recursive lists top-level only and hides dot-dirs.
	got := strings.Join(paths(runTool(t, rt, map[string]any{})), ",")
	if got != "a.txt,sub" {
		t.Errorf("non-recursive listing = %q", got)
	}
	// Positive: recursive includes nested files.
	got = strings.Join(paths(runTool(t, rt, map[string]any{"recursive": true})), ",")
	if got != "a.txt,sub,sub/b.txt" {
		t.Errorf("recursive listing = %q", got)
	}
	// Positive: max_entries is honoured.
	if n := len(paths(runTool(t, rt, map[string]any{"recursive": true, "max_entries": 1}))); n != 1 {
		t.Errorf("expected 1 entry, got %d", n)
	}

	// Negative: outside workspace and missing directory.
	if out := runTool(t, rt, map[string]any{"directory": ".."}); errOf(out) == "" {
		t.Error("expected error listing outside workspace")
	}
	if out := runTool(t, rt, map[string]any{"directory": "nope"}); errOf(out) == "" {
		t.Error("expected error listing missing directory")
	}
}

func TestCreateFileTool(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	rt := toolOf(t)(NewCreateFileTool(ws, allowAll()))

	// Positive: creates nested file.
	out := runTool(t, rt, map[string]any{"path": "new/dir/f.txt", "content": "hello"})
	if out["success"] != true || out["bytes_written"].(float64) != 5 {
		t.Fatalf("create failed: %v", out)
	}

	// Negative: existing file without overwrite.
	out = runTool(t, rt, map[string]any{"path": "new/dir/f.txt", "content": "again"})
	if !strings.Contains(errOf(out), "already exists") {
		t.Errorf("expected already-exists error, got %v", out)
	}
	// Positive: overwrite=true replaces.
	out = runTool(t, rt, map[string]any{"path": "new/dir/f.txt", "content": "again", "overwrite": true})
	if b, _ := os.ReadFile(filepath.Join(dir, "new", "dir", "f.txt")); out["success"] != true || string(b) != "again" {
		t.Errorf("overwrite failed: %v, content %q", out, b)
	}

	// Negative: outside workspace, and the workspace root itself.
	outside := filepath.Join(t.TempDir(), "evil.txt")
	for _, p := range []string{"../evil.txt", outside, "."} {
		if out := runTool(t, rt, map[string]any{"path": p, "content": "x"}); errOf(out) == "" {
			t.Errorf("expected error creating %q", p)
		}
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Errorf("file created outside workspace")
	}
}

func TestDeleteFileTool(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	writeFile(t, filepath.Join(dir, "gone.txt"), "x")
	rt := toolOf(t)(NewDeleteFileTool(ws, allowAll()))

	if out := runTool(t, rt, map[string]any{"path": "gone.txt"}); out["success"] != true {
		t.Errorf("delete failed: %v", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.txt")); !os.IsNotExist(err) {
		t.Error("file still exists after delete")
	}

	outsideDir := t.TempDir()
	victim := filepath.Join(outsideDir, "victim.txt")
	writeFile(t, victim, "keep me")
	for _, p := range []string{victim, "../victim.txt", "missing.txt", "."} {
		if out := runTool(t, rt, map[string]any{"path": p}); errOf(out) == "" {
			t.Errorf("expected error deleting %q", p)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Error("file outside workspace was deleted")
	}
}

func TestReplaceInFileTool(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	path := filepath.Join(dir, "code.go")
	rt := toolOf(t)(NewReplaceInFileTool(ws, allowAll()))

	reset := func() { writeFile(t, path, "foo bar foo\n") }

	// Negative: empty target must be rejected (previously corrupted the file).
	reset()
	out := runTool(t, rt, map[string]any{"path": "code.go", "target_content": "", "replacement_content": "X", "allow_multiple": true})
	if errOf(out) == "" {
		t.Error("expected error for empty target_content")
	}
	if b, _ := os.ReadFile(path); string(b) != "foo bar foo\n" {
		t.Errorf("file modified on rejected edit: %q", b)
	}

	// Negative: ambiguous target without allow_multiple; missing target.
	out = runTool(t, rt, map[string]any{"path": "code.go", "target_content": "foo", "replacement_content": "baz"})
	if !strings.Contains(errOf(out), "matched 2 times") {
		t.Errorf("expected ambiguity error, got %v", out)
	}
	out = runTool(t, rt, map[string]any{"path": "code.go", "target_content": "absent", "replacement_content": "x"})
	if !strings.Contains(errOf(out), "not found") {
		t.Errorf("expected not-found error, got %v", out)
	}

	// Positive: allow_multiple replaces all.
	out = runTool(t, rt, map[string]any{"path": "code.go", "target_content": "foo", "replacement_content": "baz", "allow_multiple": true})
	if out["replacements_count"].(float64) != 2 {
		t.Errorf("expected 2 replacements, got %v", out)
	}
	if b, _ := os.ReadFile(path); string(b) != "baz bar baz\n" {
		t.Errorf("unexpected content %q", b)
	}

	// Negative: outside the workspace.
	if out := runTool(t, rt, map[string]any{"path": "../x.go", "target_content": "a", "replacement_content": "b"}); errOf(out) == "" {
		t.Error("expected error editing outside workspace")
	}
}

func TestDeleteSnippetTool(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	path := filepath.Join(dir, "s.txt")
	writeFile(t, path, "keep REMOVE keep")
	rt := toolOf(t)(NewDeleteSnippetTool(ws, allowAll()))

	if out := runTool(t, rt, map[string]any{"path": "s.txt", "snippet": ""}); errOf(out) == "" {
		t.Error("expected error for empty snippet")
	}
	if out := runTool(t, rt, map[string]any{"path": "s.txt", "snippet": "absent"}); errOf(out) == "" {
		t.Error("expected error for missing snippet")
	}
	if out := runTool(t, rt, map[string]any{"path": "s.txt", "snippet": "REMOVE "}); out["success"] != true {
		t.Errorf("delete_snippet failed: %v", out)
	}
	if b, _ := os.ReadFile(path); string(b) != "keep keep" {
		t.Errorf("unexpected content %q", b)
	}
}
