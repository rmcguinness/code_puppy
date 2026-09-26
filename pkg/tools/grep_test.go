package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func grepMatches(t *testing.T, ws *Workspace, input GrepInput) []GrepMatch {
	t.Helper()
	m, err := grepWorkspace(context.Background(), ws, input)
	if err != nil {
		t.Fatalf("grep %+v: %v", input, err)
	}
	return m
}

func TestGrepCaseInsensitiveLiteral(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "Hello World\nhello.world\nhelloXworld\n")

	// Positive (regression): case_insensitive + literal used to escape "(?i)".
	m := grepMatches(t, ws, GrepInput{Query: "HELLO", CaseInsensitive: true})
	if len(m) != 3 {
		t.Errorf("expected 3 case-insensitive matches, got %d: %v", len(m), m)
	}
	// Literal metacharacters stay literal: "hello.world" must not match "helloXworld".
	m = grepMatches(t, ws, GrepInput{Query: "HELLO.WORLD", CaseInsensitive: true})
	if len(m) != 1 || m[0].LineNumber != 2 {
		t.Errorf("expected only the literal dotted line, got %v", m)
	}
	// Negative: case-sensitive literal doesn't match other cases.
	if m := grepMatches(t, ws, GrepInput{Query: "HELLO"}); len(m) != 0 {
		t.Errorf("expected no case-sensitive matches, got %v", m)
	}
}

func TestGrepRegex(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	writeFile(t, filepath.Join(dir, "a.go"), "func Foo() {}\nvar x = 1\nfunc bar() {}\n")

	m := grepMatches(t, ws, GrepInput{Query: `^func [A-Z]`, IsRegex: true})
	if len(m) != 1 || m[0].LineNumber != 1 {
		t.Errorf("expected anchored regex to match line 1 only, got %v", m)
	}
	m = grepMatches(t, ws, GrepInput{Query: `^FUNC`, IsRegex: true, CaseInsensitive: true})
	if len(m) != 2 {
		t.Errorf("expected 2 case-insensitive regex matches, got %v", m)
	}
	// Negative: invalid regex and empty query.
	if _, err := grepWorkspace(context.Background(), ws, GrepInput{Query: "(", IsRegex: true}); err == nil || !strings.Contains(err.Error(), "invalid regex") {
		t.Errorf("expected invalid regex error, got %v", err)
	}
	if _, err := grepWorkspace(context.Background(), ws, GrepInput{}); err == nil {
		t.Error("expected error for empty query")
	}
}

func TestGrepLongLinesAndBinaries(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	// A 200KB line exceeds bufio.Scanner's default 64KB token and used to
	// silently abort scanning that file.
	long := strings.Repeat("a", 200*1024) + "NEEDLE"
	writeFile(t, filepath.Join(dir, "min.js"), long+"\nNEEDLE on line two\n")
	writeFile(t, filepath.Join(dir, "blob.bin"), "NEEDLE\x00\x01\x02")

	m := grepMatches(t, ws, GrepInput{Query: "NEEDLE"})
	if len(m) != 2 {
		t.Fatalf("expected 2 matches from min.js, got %v", len(m))
	}
	for _, match := range m {
		if match.File == "blob.bin" {
			t.Error("binary file should be skipped")
		}
		if len(match.Content) > grepMaxLineContent {
			t.Errorf("match content not truncated: %d bytes", len(match.Content))
		}
	}
	if m[0].LineNumber != 1 || m[1].LineNumber != 2 {
		t.Errorf("unexpected line numbers %v", m)
	}
}

func TestGrepDefaultWorkspaceDot(t *testing.T) {
	// Regression: with workspace ".", the walk root is named "." and the
	// hidden-directory rule used to skip the entire tree.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "x.txt"), "findme\n")
	t.Chdir(dir)
	ws, err := NewWorkspace(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if m := grepMatches(t, ws, GrepInput{Query: "findme"}); len(m) != 1 {
		t.Errorf("expected 1 match with workspace '.', got %v", m)
	}
}

func TestGrepSkipsVendoredAndHiddenDirs(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	for _, d := range []string{".git", "node_modules", "vendor", "target", "__pycache__"} {
		writeFile(t, filepath.Join(dir, d, "f.txt"), "token\n")
	}
	writeFile(t, filepath.Join(dir, "src", "f.txt"), "token\n")
	m := grepMatches(t, ws, GrepInput{Query: "token"})
	if len(m) != 1 || m[0].File != "src/f.txt" {
		t.Errorf("expected only src/f.txt, got %v", m)
	}
	// Positive: explicitly searching inside a skipped dir still works.
	if m := grepMatches(t, ws, GrepInput{Query: "token", Path: "vendor"}); len(m) != 1 {
		t.Errorf("expected explicit path search to work, got %v", m)
	}
	// Single-file path.
	if m := grepMatches(t, ws, GrepInput{Query: "token", Path: "src/f.txt"}); len(m) != 1 {
		t.Errorf("expected single-file search to work, got %v", m)
	}
}

func TestGrepMaxMatchesDeterministic(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	for i := 0; i < 50; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("f%02d.txt", i)), "hit\nhit\nhit\n")
	}
	first := grepMatches(t, ws, GrepInput{Query: "hit", MaxMatches: 10})
	if len(first) != 10 {
		t.Fatalf("expected 10 matches, got %d", len(first))
	}
	if first[0].File != "f00.txt" || first[9].File != "f03.txt" {
		t.Errorf("expected matches in walk order, got first=%s last=%s", first[0].File, first[9].File)
	}
	for i := 0; i < 5; i++ {
		if again := grepMatches(t, ws, GrepInput{Query: "hit", MaxMatches: 10}); !reflect.DeepEqual(first, again) {
			t.Fatalf("parallel grep returned non-deterministic results")
		}
	}
}

func TestGrepOutsideWorkspaceAndCancellation(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "x\n")
	if _, err := grepWorkspace(context.Background(), ws, GrepInput{Query: "root", Path: "/etc"}); err == nil {
		t.Error("expected error searching outside workspace")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := grepWorkspace(ctx, ws, GrepInput{Query: "x"}); err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestGrepTool(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha\n")
	rt := toolOf(t)(NewGrepTool(ws))
	out := runTool(t, rt, map[string]any{"query": "ALPHA", "case_insensitive": true})
	if out["total_matches"].(float64) != 1 {
		t.Errorf("expected 1 match, got %v", out)
	}
	if out := runTool(t, rt, map[string]any{"query": "(", "is_regex": true}); errOf(out) == "" {
		t.Error("expected tool-level error for invalid regex")
	}
}
