package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/skills"
)

const scriptSkill = `---
name: demo
execution_hints:
  environment_variables: [CP_PASSED, CP_WITHHELD]
scripts:
  - name: work
    language: python
    relative_path: scripts/work.py
    environment_variables: {GREETING: hi}
  - name: entry
    language: python
    relative_path: scripts/work.py
    entry_point: main
  - name: inline
    language: python
    inline_code: "import sys; print('inline', sys.argv[1:])"
  - name: slow
    language: python
    inline_code: "import time; time.sleep(30)"
    timeout_seconds: 1
  - name: ts
    language: typescript
    inline_code: "console.log(1)"
  - name: needs-pkgs
    language: python
    inline_code: "import six"
    dependencies: ["six==1.16.0"]
---
`

const workPy = `import os, sys
print("args", sys.argv[1:])
print("readme", open("README.md").read().strip())
print("env", os.environ.get("GREETING"), os.environ.get("CP_PASSED"), os.environ.get("CP_WITHHELD"), os.environ.get("CP_SECRET"))
try:
    open("README.md", "w").write("overwritten")
    print("workspace write: ALLOWED")
except OSError as e:
    print("workspace write: refused")
out = os.environ.get("SKILL_OUTPUT")
if out:
    open(os.path.join(out, "result.txt"), "w").write("done")
print("output", bool(out))

def main():
    print("entry point")
    return 3

if __name__ == "__main__":
    pass
`

type scriptFixture struct {
	ws     string
	runner *SkillScripts
	reqs   *[]ApprovalRequest
}

func newScriptFixture(t *testing.T, tier string, approve bool, edit func(*config.SkillPolicy)) scriptFixture {
	t.Helper()
	skillsDir := t.TempDir()
	dir := filepath.Join(skillsDir, "demo")
	os.MkdirAll(filepath.Join(dir, "scripts"), 0o755)
	doc := scriptSkill
	if tier != "" {
		doc = strings.Replace(doc, "execution_hints:\n", "execution_hints:\n  hitl_tier: "+tier+"\n", 1)
	}
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(doc), 0o644)
	os.WriteFile(filepath.Join(dir, "scripts", "work.py"), []byte(workPy), 0o644)
	prov, _ := skills.NewProvider()
	if err := prov.DiscoverExternal([]string{skillsDir}); err != nil {
		t.Fatal(err)
	}

	wsDir, _ := filepath.EvalSymlinks(t.TempDir())
	os.WriteFile(filepath.Join(wsDir, "README.md"), []byte("original"), 0o644)
	ws, err := OpenWorkspace(WorkspaceOptions{Dir: wsDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	policy := config.DefaultConfig().Skills.Policy
	policy.EnvPassthrough = []string{"CP_PASSED"}
	if edit != nil {
		edit(&policy)
	}
	hooks, reqs := approverHooks(approve)
	r := NewSkillScripts(prov, policy, ws, hooks, NewPyEnvs(filepath.Join(t.TempDir(), "envs"), policy.Packages),
		ScriptBoxConfig{Mode: os.Getenv("CODE_PUPPY_PYENV_SANDBOX"), Blocked: ws.Blocked(), StateDir: t.TempDir()})
	if _, err := r.Box(); err != nil {
		t.Skipf("no script sandbox: %v", err)
	}
	if _, err := SystemPython(); err != nil {
		t.Skip(err)
	}
	return scriptFixture{ws: wsDir, runner: r, reqs: reqs}
}

func TestRunSkillScriptTier2(t *testing.T) {
	f := newScriptFixture(t, "", true, nil)
	t.Setenv("CP_PASSED", "passed")
	t.Setenv("CP_WITHHELD", "withheld")
	t.Setenv("CP_SECRET", "secret")
	out := f.runner.Run(context.Background(), RunSkillScriptInput{Skill: "demo", Script: "work", Args: []string{"a b", "c"}})
	if out.Error != "" || out.ExitCode != 0 {
		t.Fatalf("%+v", out)
	}
	for _, want := range []string{"args ['a b', 'c']", "readme original", "env hi passed None None", "workspace write: refused", "output True"} {
		if !strings.Contains(out.Stdout, want) {
			t.Errorf("stdout lacks %q:\n%s\n%s", want, out.Stdout, out.Stderr)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(f.ws, "README.md")); string(b) != "original" {
		t.Fatalf("the script changed the workspace: %q", b)
	}
	if out.Tier != "TIER_2_AUDITED_WRITE" || len(*f.reqs) != 0 {
		t.Errorf("tier 2 should run without asking: %s, %d prompts", out.Tier, len(*f.reqs))
	}
	if !strings.HasPrefix(out.OutputDir, SkillOutputDir+"/demo/work-") || len(out.Files) != 1 || !strings.HasSuffix(out.Files[0], "result.txt") {
		t.Fatalf("output: %q %v", out.OutputDir, out.Files)
	}
	if b, _ := os.ReadFile(filepath.Join(f.ws, out.Files[0])); string(b) != "done" {
		t.Fatalf("result file: %q", b)
	}
}

func TestRunSkillScriptEntryInlineAndTier1(t *testing.T) {
	f := newScriptFixture(t, "TIER_1_AUTO_READ", true, func(p *config.SkillPolicy) { p.MinHITLTier = 1 })
	out := f.runner.Run(context.Background(), RunSkillScriptInput{Skill: "demo", Script: "entry"})
	if out.ExitCode != 3 || !strings.Contains(out.Stdout, "entry point") || strings.Contains(out.Stdout, "args") && strings.Contains(out.Stdout, "__main__") {
		t.Fatalf("entry point: %+v", out)
	}
	if out.OutputDir != "" || !strings.Contains(out.Stdout, "output False") {
		t.Errorf("tier 1 got an output directory: %+v", out)
	}
	out = f.runner.Run(context.Background(), RunSkillScriptInput{Skill: "demo", Script: "inline", Args: []string{"x"}})
	if out.ExitCode != 0 || !strings.Contains(out.Stdout, "inline ['x']") {
		t.Fatalf("inline: %+v", out)
	}
}

func TestRunSkillScriptTier3AsksEveryTime(t *testing.T) {
	f := newScriptFixture(t, "TIER_3_MANDATORY_APPROVAL", false, nil)
	out := f.runner.Run(context.Background(), RunSkillScriptInput{Skill: "demo", Script: "inline"})
	if !strings.Contains(out.Error, "not approved") || len(*f.reqs) != 1 || (*f.reqs)[0].Key != "" {
		t.Fatalf("tier 3 denied: %+v %+v", out, *f.reqs)
	}
}

func TestRunSkillScriptRefusals(t *testing.T) {
	f := newScriptFixture(t, "", false, nil)
	for script, want := range map[string]string{
		"ts":      `language "typescript" isn't in skills.policy.languages`,
		"missing": `has no script "missing"`,
	} {
		if out := f.runner.Run(context.Background(), RunSkillScriptInput{Skill: "demo", Script: script}); !strings.Contains(out.Error, want) {
			t.Errorf("%s: %+v", script, out)
		}
	}
	if out := f.runner.Run(context.Background(), RunSkillScriptInput{Skill: "nope", Script: "x"}); !strings.Contains(out.Error, "no skill") {
		t.Errorf("unknown skill: %+v", out)
	}
	// Installing packages needs its own approval, rememberable per package list.
	out := f.runner.Run(context.Background(), RunSkillScriptInput{Skill: "demo", Script: "needs-pkgs"})
	if !strings.Contains(out.Error, "not approved") || len(*f.reqs) != 1 {
		t.Fatalf("install: %+v %+v", out, *f.reqs)
	}
	r := (*f.reqs)[0]
	if r.Kind != ActionNetwork || !strings.HasPrefix(r.Key, "pyenv:") || !strings.Contains(r.Detail, "six==1.16.0") || !strings.Contains(r.Detail, "--only-binary :all:") {
		t.Errorf("install approval: %+v", r)
	}
}

func TestRunSkillScriptTimeout(t *testing.T) {
	f := newScriptFixture(t, "", true, nil)
	out := f.runner.Run(context.Background(), RunSkillScriptInput{Skill: "demo", Script: "slow"})
	if !out.TimedOut || !strings.Contains(out.Error, "timeout") {
		t.Fatalf("%+v", out)
	}
}

func TestActivateSkillListsScripts(t *testing.T) {
	f := newScriptFixture(t, "", true, nil)
	p := config.DefaultConfig().Skills.Policy
	skill, _ := f.runner.provider.Get("demo")
	ev := skills.Evaluate(skill, p)
	allowed, blocked := 0, 0
	for _, v := range ev.Scripts {
		if v.Allowed {
			allowed++
		} else {
			blocked++
		}
	}
	if allowed != 5 || blocked != 1 {
		t.Fatalf("allowed %d, blocked %d", allowed, blocked)
	}
}

// The whole path with packages: approval, build, and a run that imports
// them. Needs the network: CODE_PUPPY_PYENV_TESTS=1.
func TestRunSkillScriptInstallsPackages(t *testing.T) {
	if os.Getenv("CODE_PUPPY_PYENV_TESTS") != "1" {
		t.Skip("set CODE_PUPPY_PYENV_TESTS=1 (needs the network)")
	}
	f := newScriptFixture(t, "", true, nil)
	out := f.runner.Run(context.Background(), RunSkillScriptInput{Skill: "demo", Script: "needs-pkgs"})
	if out.Error != "" || out.ExitCode != 0 || len(*f.reqs) != 1 {
		t.Fatalf("%+v (%d approvals)", out, len(*f.reqs))
	}
	// Built once: the next run doesn't ask or install again.
	if out := f.runner.Run(context.Background(), RunSkillScriptInput{Skill: "demo", Script: "needs-pkgs"}); out.ExitCode != 0 || len(*f.reqs) != 1 {
		t.Fatalf("second run: %+v (%d approvals)", out, len(*f.reqs))
	}
	envs := f.runner.Envs().List()
	if len(envs) != 1 || envs[0].Skills[0] != "demo" {
		t.Fatalf("envs: %+v", envs)
	}
	t.Logf("sandbox %s", out.Sandbox)
}
