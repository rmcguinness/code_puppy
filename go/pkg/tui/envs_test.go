package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/tools"
)

func TestEnvsListPruneRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	app, _ := newCommandApp(t, "")
	python, err := tools.SystemPython()
	if err != nil {
		t.Skip(err)
	}
	skillsDir := t.TempDir()
	os.MkdirAll(filepath.Join(skillsDir, "s"), 0o755)
	os.WriteFile(filepath.Join(skillsDir, "s", "SKILL.md"), []byte("---\nname: s\nscripts:\n  - name: r\n    language: python\n    inline_code: x\n    dependencies: [\"six==1.16.0\"]\n---\n"), 0o644)
	if err := app.Skills.DiscoverExternal([]string{skillsDir}); err != nil {
		t.Fatal(err)
	}
	envs := app.Tools.SkillScripts().Envs()
	dir := filepath.Join(home, ".code_puppy", "envs")
	mk := func(key string, marker bool, deps ...string) {
		os.MkdirAll(filepath.Join(dir, key, "lib"), 0o700)
		os.WriteFile(filepath.Join(dir, key, "lib", "pkg.py"), make([]byte, 4096), 0o600)
		if marker {
			b, _ := json.Marshal(map[string]any{"key": key, "deps": deps, "skills": []string{"s"}, "last_used": time.Now()})
			os.WriteFile(filepath.Join(dir, key, ".code-puppy-env.json"), b, 0o600)
		}
	}
	needed := envs.Key(python, []string{"six==1.16.0"})
	mk(needed, true, "six==1.16.0")
	mk("0123456789abcdef", true, "requests")
	mk("fedcba9876543210", false)
	run := func(cmd string) string { return captureStdout(t, func() { HandleCommand(context.Background(), cmd, app) }) }

	out := run("/envs")
	for _, want := range []string{"Script environments (3)", needed, "six==1.16.0", "used by s", "incomplete"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	if out := run("/envs prune"); !strings.Contains(out, "Removed 2 environment(s)") {
		t.Errorf("prune:\n%s", out)
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 1 || left[0].Name() != needed {
		t.Fatalf("after prune: %v", left)
	}
	if out := run("/envs remove " + needed); !strings.Contains(out, "Removed environment") {
		t.Errorf("remove:\n%s", out)
	}
	if out := run("/envs remove ../x"); !strings.Contains(out, "Could not remove") {
		t.Errorf("bad key:\n%s", out)
	}
	if out := run("/envs"); !strings.Contains(out, "No script environments") {
		t.Errorf("empty:\n%s", out)
	}
	if out := run("/envs bogus"); !strings.Contains(out, "Usage: /envs") {
		t.Errorf("usage:\n%s", out)
	}
}
