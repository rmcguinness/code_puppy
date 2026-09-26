package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillsListAndShow(t *testing.T) {
	app, _ := newCommandApp(t, "")
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "gh", "scripts"), 0o755)
	os.WriteFile(filepath.Join(dir, "gh", "scripts", "list.py"), []byte("print(1)"), 0o644)
	os.WriteFile(filepath.Join(dir, "gh", "SKILL.md"), []byte(`---
name: gh-issues
description: Triage issues
version: "1.2"
license: MIT
tool_requirements:
  - {name: Bash, scopes: ["gh:*"], description: read issues}
execution_hints:
  environment_variables: [GITHUB_TOKEN, AWS_SECRET_ACCESS_KEY]
scripts:
  - name: list
    language: python
    relative_path: scripts/list.py
    dependencies: ["requests>=2.31", "git+https://x/y.git"]
  - name: fmt
    language: python
    inline_code: "print(2)"
    timeout_seconds: 30
---
`), 0o644)
	if err := app.Workspace.Skills().DiscoverExternal([]string{dir}); err != nil {
		t.Fatal(err)
	}
	app.Workspace.Config().Skills.Policy.EnvPassthrough = []string{"GITHUB_TOKEN"}
	run := func(cmd string) string {
		return captureStdout(t, func() { HandleCommand(context.Background(), cmd, app) })
	}

	if out := run("/skills list"); !strings.Contains(out, "2 scripts · TIER_2_AUDITED_WRITE · allowed by skills.policy") {
		t.Errorf("list:\n%s", out)
	}
	out := run("/skills show gh-issues")
	for _, want := range []string{
		"1.2 · MIT", "Content: sha256:", "Needs Bash (gh:*): read issues", "approval tier TIER_2_AUDITED_WRITE",
		"Network: not needed, off", "Receives: GITHUB_TOKEN", "won't receive (skills.policy.env_passthrough): AWS_SECRET_ACCESS_KEY",
		"✗ list", "isn't a plain package requirement", "✓ fmt", "inline, 30s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	if out := run("/skills show nope"); !strings.Contains(out, "No skill named nope") {
		t.Errorf("unknown:\n%s", out)
	}
	if out := run("/skills show"); !strings.Contains(out, "Usage: /skills show") {
		t.Errorf("usage:\n%s", out)
	}
	app.Workspace.Config().Skills.Policy.Languages = nil
	if out := run("/skills list"); !strings.Contains(out, `2 scripts · blocked: language "python"`) {
		t.Errorf("blocked list:\n%s", out)
	}
}
