package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goFile = `package main

import "fmt"

func main() {
	fmt.Println("hello")
}

func helper() int {
	return 1
}
`

func TestApplyHunksUnified(t *testing.T) {
	patches, err := parsePatch(`--- a/main.go
+++ b/main.go
@@ -5,3 +5,3 @@ import "fmt"
 func main() {
-	fmt.Println("hello")
+	fmt.Println("goodbye")
 }
@@ -9,3 +9,4 @@ func main() {
 func helper() int {
-	return 1
+	x := 2
+	return x
 }
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 1 || len(patches[0].hunks) != 2 || patches[0].path != "main.go" {
		t.Fatalf("parse: %+v", patches)
	}
	got, err := applyHunks(goFile, patches[0].hunks)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(strings.Replace(goFile, `"hello"`, `"goodbye"`, 1), "\treturn 1\n", "\tx := 2\n\treturn x\n", 1)
	if got != want {
		t.Errorf("result:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyHunksTolerance(t *testing.T) {
	// Wrong line numbers and trailing-whitespace differences still apply.
	hunks := []patchHunk{{hint: 40, old: []string{"\tfmt.Println(\"hello\")   "}, new: []string{"\tfmt.Println(\"hi\")"}}}
	got, err := applyHunks(goFile, hunks)
	if err != nil || !strings.Contains(got, `"hi"`) {
		t.Errorf("tolerant apply failed: %v", err)
	}
	// Negative: context that doesn't exist.
	if _, err := applyHunks(goFile, []patchHunk{{hint: -1, old: []string{"nope"}, new: []string{"x"}}}); err == nil || !strings.Contains(err.Error(), "could not find") {
		t.Errorf("expected not-found error, got %v", err)
	}
	// Ambiguous context resolves to the occurrence nearest the hint.
	src := "a\nx\nb\nx\nc\n"
	got, _ = applyHunks(src, []patchHunk{{hint: 3, old: []string{"x"}, new: []string{"Y"}}})
	if got != "a\nx\nb\nY\nc\n" {
		t.Errorf("nearest-hint match: %q", got)
	}
	// Pure insertion at EOF, no trailing newline preserved.
	got, _ = applyHunks("a\nb", []patchHunk{{hint: -1, new: []string{"c"}}})
	if got != "a\nb\nc" {
		t.Errorf("insertion: %q", got)
	}
}

func TestParseBeginPatch(t *testing.T) {
	patches, err := parsePatch(`*** Begin Patch
*** Add File: docs/new.md
+# Title
+body
*** Update File: main.go
@@ func helper() int {
-	return 1
+	return 2
*** Delete File: old.txt
*** End Patch`)
	if err != nil {
		t.Fatal(err)
	}
	if len(patches) != 3 {
		t.Fatalf("expected 3 files, got %d", len(patches))
	}
	if patches[0].op != opAdd || patches[0].content != "# Title\nbody\n" {
		t.Errorf("add: %+v", patches[0])
	}
	if patches[1].op != opUpdate || patches[1].hunks[0].anchor != "func helper() int {" {
		t.Errorf("update: %+v", patches[1])
	}
	got, err := applyHunks(goFile, patches[1].hunks)
	if err != nil || !strings.Contains(got, "return 2") || strings.Contains(got, "return 1") {
		t.Errorf("anchored apply: %v\n%s", err, got)
	}
	if patches[2].op != opDelete {
		t.Errorf("delete: %+v", patches[2])
	}

	for name, bad := range map[string]string{
		"no end":        "*** Begin Patch\n*** Add File: x\n+a\n",
		"empty":         "*** Begin Patch\n*** End Patch",
		"stray line":    "*** Begin Patch\nhello\n*** End Patch",
		"bad add line":  "*** Begin Patch\n*** Add File: x\nno plus\n*** End Patch",
		"empty update":  "*** Begin Patch\n*** Update File: x\n*** End Patch",
		"not a patch":   "just some text",
		"hunk w/o file": "@@ -1 +1 @@\n-a\n+b\n",
	} {
		if _, err := parsePatch(bad); err == nil {
			t.Errorf("%s: expected parse error", name)
		}
	}
}

func TestApplyPatchTool(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	writeFile(t, filepath.Join(dir, "main.go"), goFile)
	writeFile(t, filepath.Join(dir, "old.txt"), "bye\n")
	writeFile(t, filepath.Join(dir, "move.txt"), "m\n")
	h, reqs := approverHooks(true)
	rt := toolOf(t)(NewApplyPatchTool(ws, h))

	out := runTool(t, rt, map[string]any{"patch": `*** Begin Patch
*** Add File: docs/new.md
+hello
*** Update File: main.go
@@
-	return 1
+	return 2
*** Update File: move.txt
*** Move to: moved/move.txt
@@
-m
+M
*** Delete File: old.txt
*** End Patch`})
	if out["success"] != true {
		t.Fatalf("apply failed: %v", out)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "docs", "new.md")); string(b) != "hello\n" {
		t.Errorf("add: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "main.go")); !strings.Contains(string(b), "return 2") {
		t.Error("update not applied")
	}
	if _, err := os.Stat(filepath.Join(dir, "old.txt")); !os.IsNotExist(err) {
		t.Error("delete not applied")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "moved", "move.txt")); string(b) != "M\n" {
		t.Errorf("move: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "move.txt")); !os.IsNotExist(err) {
		t.Error("moved source still exists")
	}
	if len(*reqs) != 1 || !strings.Contains((*reqs)[0].Diff, "+hello") || !strings.Contains((*reqs)[0].Diff, "-\treturn 1") {
		t.Errorf("single approval with combined diff expected: %+v", *reqs)
	}
	files := out["files"].([]any)
	if len(files) != 4 {
		t.Errorf("summary: %v", files)
	}
}

func TestApplyPatchAllOrNothing(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
	writeFile(t, filepath.Join(dir, "b.txt"), "b\n")
	h, reqs := approverHooks(true)
	rt := toolOf(t)(NewApplyPatchTool(ws, h))

	// Second file's hunk doesn't match: nothing is written, nobody is asked.
	out := runTool(t, rt, map[string]any{"patch": "--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-a\n+A\n--- a/b.txt\n+++ b/b.txt\n@@ -1 +1 @@\n-zzz\n+B\n"})
	if out["success"] == true || !strings.Contains(errOf(out), "b.txt") {
		t.Errorf("expected failure naming b.txt: %v", out)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "a\n" {
		t.Error("partial patch was applied")
	}
	if len(*reqs) != 0 {
		t.Error("user asked to approve an invalid patch")
	}

	// Negative: denial, sandbox escapes, duplicate files, add over existing.
	denied, _ := approverHooks(false)
	if out := runTool(t, toolOf(t)(NewApplyPatchTool(ws, denied)), map[string]any{"patch": "--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-a\n+A\n"}); !strings.Contains(errOf(out), "not approved") {
		t.Errorf("expected denial: %v", out)
	}
	for name, p := range map[string]string{
		"escape":    "*** Begin Patch\n*** Add File: ../evil.txt\n+x\n*** End Patch",
		"duplicate": "*** Begin Patch\n*** Update File: a.txt\n-a\n+1\n*** Update File: a.txt\n-a\n+2\n*** End Patch",
		"add exist": "*** Begin Patch\n*** Add File: a.txt\n+x\n*** End Patch",
		"del miss":  "*** Begin Patch\n*** Delete File: nope.txt\n*** End Patch",
	} {
		if out := runTool(t, rt, map[string]any{"patch": p}); out["success"] == true {
			t.Errorf("%s: expected failure", name)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.txt")); err == nil {
		t.Error("patch escaped the workspace")
	}
}

func TestApplyPatchIsUndoable(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	cp := NewCheckpoints(ws, 0)
	writeFile(t, filepath.Join(dir, "a.txt"), "a\n")
	cp.Begin("patch")
	out := runTool(t, toolOf(t)(NewApplyPatchTool(ws, allowAll())), map[string]any{"patch": "*** Begin Patch\n*** Update File: a.txt\n-a\n+A\n*** Add File: n.txt\n+n\n*** End Patch"})
	if out["success"] != true {
		t.Fatal(out)
	}
	if _, err := cp.Undo(false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "a\n" {
		t.Error("undo did not revert patch")
	}
	if _, err := os.Stat(filepath.Join(dir, "n.txt")); !os.IsNotExist(err) {
		t.Error("undo did not remove added file")
	}
}

func TestApplyHunksCRLF(t *testing.T) {
	src := "line1\r\nold\r\nline3\r\n"
	got, err := applyHunks(src, []patchHunk{{hint: -1, old: []string{"line1", "old"}, new: []string{"line1", "new"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "line1\r\nnew\r\nline3\r\n" {
		t.Errorf("CRLF not preserved: %q", got)
	}
}
