package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The agent can write .git/config; showing the diff must not run what it
// names there.
func TestGitDiffRunsNothingFromRepoConfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	w := openTest(t)
	dir := w.Dir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("f.txt", "before\n")
	git("add", "f.txt")
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init")

	write(".gitattributes", "*.txt filter=evil diff=evil\n")
	for key, cmd := range map[string]string{
		"core.fsmonitor":      "touch pwned-fsmonitor",
		"diff.external":       "touch pwned-external",
		"diff.evil.textconv":  "touch pwned-textconv; cat",
		"filter.evil.clean":   "touch pwned-clean; cat",
		"filter.evil.process": "touch pwned-process",
	} {
		git("config", key, cmd)
	}
	git("config", "filter.evil.required", "true")
	write("f.txt", "after\n")

	out, err := w.GitDiff(context.Background(), false)
	if err != nil || !strings.Contains(out, "+after") {
		t.Fatalf("diff %v:\n%s", err, out)
	}
	if got, _ := filepath.Glob(filepath.Join(dir, "pwned-*")); len(got) > 0 {
		t.Errorf("git diff ran commands from the repository's config: %v", got)
	}
}
