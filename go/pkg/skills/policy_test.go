package skills

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/pkg/config"
)

func defaultPolicy() config.SkillPolicy { return config.DefaultConfig().Skills.Policy }

// skillFrom loads a skill from a SKILL.md in its own directory, so it has
// files to hash.
func skillFrom(t *testing.T, doc string) *Skill {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	p, _ := NewProvider()
	if err := p.DiscoverExternal([]string{dir}); err != nil {
		t.Fatal(err)
	}
	for _, s := range p.List() {
		if s.Path == filepath.Join(dir, "SKILL.md") {
			return s
		}
	}
	t.Fatal("skill not loaded")
	return nil
}

func withScript(extra string) string {
	return "---\nname: s\n" + extra + "\nscripts:\n  - name: run\n    language: python\n    inline_code: \"print(1)\"\n---\n"
}

func TestTierIsTheStricterOfSkillAndPolicy(t *testing.T) {
	cases := []struct {
		hints       string
		minTier     int
		allowBypass bool
		want        HITLTier
		wantBypass  bool
	}{
		{"", 2, false, Tier2AuditedWrite, false}, // unspecified -> the policy's minimum
		{"execution_hints: {hitl_tier: TIER_1_AUTO_READ}", 2, false, Tier2AuditedWrite, false},
		{"execution_hints: {hitl_tier: TIER_3_MANDATORY_APPROVAL}", 2, false, Tier3MandatoryApproval, false},
		{"execution_hints: {hitl_tier: TIER_1_AUTO_READ}", 0, false, Tier1AutoRead, false}, // min 0 never means bypass
		{"execution_hints: {requires_human_approval: true, hitl_tier: TIER_1_AUTO_READ}", 1, false, Tier3MandatoryApproval, false},
		// Tier 0 needs both the skill's and the policy's consent.
		{"execution_hints: {hitl_tier: TIER_0_BYPASS_ALL}", 2, true, Tier3MandatoryApproval, false},
		{"execution_hints: {hitl_tier: TIER_0_BYPASS_ALL, allow_hitl_bypass: true}", 2, false, Tier3MandatoryApproval, false},
		{"execution_hints: {hitl_tier: TIER_0_BYPASS_ALL, allow_hitl_bypass: true}", 2, true, Tier0BypassAll, true},
		// The compiled reference's tier applies when the hints give none.
		{"compiled_reference: {hitl_tier: TIER_3_MANDATORY_APPROVAL}", 1, false, Tier3MandatoryApproval, false},
	}
	for _, c := range cases {
		p := defaultPolicy()
		p.MinHITLTier, p.AllowHITLBypass = c.minTier, c.allowBypass
		ev := Evaluate(skillFrom(t, withScript(c.hints)), p)
		if ev.Tier != c.want || ev.Bypass != c.wantBypass {
			t.Errorf("%q (min %d, bypass %v): got %v/%v, want %v/%v", c.hints, c.minTier, c.allowBypass, ev.Tier, ev.Bypass, c.want, c.wantBypass)
		}
	}
}

func TestNetworkEnvAndTimeout(t *testing.T) {
	s := skillFrom(t, withScript("execution_hints:\n  custom_hints: {network: required}\n  environment_variables: [GITHUB_TOKEN, AWS_SECRET_ACCESS_KEY]\n  timeout_seconds: 900"))
	p := defaultPolicy()
	ev := Evaluate(s, p)
	if ev.Network || ev.Runnable() || !strings.Contains(strings.Join(ev.Blocked, " "), "needs the network") {
		t.Fatalf("network denied by default: %+v", ev)
	}
	p.Network, p.NetworkAllow = "allowlist", []string{"s"}
	p.EnvPassthrough = []string{"GITHUB_*"}
	ev = Evaluate(s, p)
	if !ev.Network || !ev.Runnable() {
		t.Fatalf("allowlisted: %+v", ev)
	}
	if !slices.Equal(ev.Env, []string{"GITHUB_TOKEN"}) || !slices.Equal(ev.Withheld, []string{"AWS_SECRET_ACCESS_KEY"}) {
		t.Fatalf("env %v withheld %v", ev.Env, ev.Withheld)
	}
	if ev.Scripts[0].TimeoutSeconds != 300 {
		t.Fatalf("timeout %d, want the policy's 300", ev.Scripts[0].TimeoutSeconds)
	}
	p.MaxTimeoutSeconds = 0
	if got := Evaluate(s, p).Scripts[0].TimeoutSeconds; got != 900 {
		t.Fatalf("uncapped timeout %d", got)
	}
}

func TestLanguagesToolsAndHashes(t *testing.T) {
	ts := skillFrom(t, "---\nname: t\nscripts:\n  - name: run\n    language: typescript\n    inline_code: x\n---\n")
	if v := Evaluate(ts, defaultPolicy()).Scripts[0]; v.Allowed || !strings.Contains(v.Reasons[0], `language "typescript"`) {
		t.Fatalf("typescript: %+v", v)
	}

	tools := skillFrom(t, withScript("tool_requirements:\n  - name: Bash\n    scopes: [\"sudo:*\", \"git:*\"]"))
	p := defaultPolicy()
	p.DenyTools = []string{"bash:sudo*"}
	ev := Evaluate(tools, p)
	if ev.Runnable() || !strings.Contains(strings.Join(ev.Blocked, " "), "Bash:sudo:*") {
		t.Fatalf("deny_tools: %+v", ev.Blocked)
	}

	s := skillFrom(t, withScript(""))
	p = defaultPolicy()
	p.TrustedHashes = []string{"sha256:other"}
	if ev := Evaluate(s, p); ev.Runnable() || !strings.Contains(ev.Blocked[0], "trusted_hashes") {
		t.Fatalf("untrusted: %+v", ev.Blocked)
	}
	p.TrustedHashes = []string{Evaluate(s, defaultPolicy()).Hash}
	if !Evaluate(s, p).Runnable() {
		t.Fatal("trusted hash refused")
	}

	bad := skillFrom(t, "---\nname: b\nscripts:\n  - name: run\n    language: python\n    relative_path: ../x.py\n---\n")
	if ev := Evaluate(bad, defaultPolicy()); ev.Runnable() || !strings.Contains(ev.Blocked[0], "definition:") {
		t.Fatalf("definition problems: %+v", ev.Blocked)
	}
}

func TestDependencies(t *testing.T) {
	p := defaultPolicy().Packages
	for dep, want := range map[string]string{
		"requests":                         "",
		"requests>=2.31.0":                 "",
		"Rich[jupyter] ==13.7.1":           "",
		"numpy>=1.26,<3":                   "",
		"pywin32; sys_platform == 'win32'": "",
		"git+https://example.com/x.git":    "isn't a plain package requirement",
		"-e .":                             "isn't a plain package requirement",
		"--index-url=https://evil.example": "isn't a plain package requirement",
		"./local.whl":                      "isn't a plain package requirement",
		"pkg @ https://example.com/p.whl":  "isn't a plain package requirement",
	} {
		got := checkDependency(dep, p)
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%q: %q", dep, got)
		}
	}
	p.Deny = []string{"*-Nightly", "evil_pkg"}
	if r := checkDependency("torch-nightly==2.0", p); !strings.Contains(r, "denied") {
		t.Errorf("deny glob: %q", r)
	}
	if r := checkDependency("Evil.Pkg", p); !strings.Contains(r, "denied") {
		t.Errorf("deny by normalized name: %q", r)
	}
	p.Allow = []string{"requests", "rich"}
	if r := checkDependency("numpy", p); !strings.Contains(r, "isn't in skills.policy.packages.allow") {
		t.Errorf("allow list: %q", r)
	}
	p.RequireHashes = true
	for dep, ok := range map[string]bool{"requests==2.32.3": true, "requests>=2": false, "requests==2.*": false, "rich": false} {
		if (checkDependency(dep, p) == "") != ok {
			t.Errorf("require_hashes %q: %q", dep, checkDependency(dep, p))
		}
	}
}
