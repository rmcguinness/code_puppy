package skills

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const castorSkill = `---
name: gh-issues
description: Triage GitHub issues
license: Apache-2.0
compatibility: Requires Python 3.11+
allowed-tools: Bash(gh:*) Read
metadata:
  owner: platform
authors:
  - name: Ada
    email: ada@example.com
category: devops
tags: [github, triage]
trigger_phrases: ["triage issues"]
tool_requirements:
  - name: Bash
    scopes: ["gh:*"]
    description: read issues
execution_hints:
  preferred_model: gemini-3.8-flash
  environment_variables: [GITHUB_TOKEN, HOME]
  timeout_seconds: 120
  custom_hints: {network: "true"}
  hitl_tier: HITL_POLICY_TIER_2_AUDITED_WRITE
compiled_reference:
  sha256_hash: abc123
scripts:
  - name: list
    language: SCRIPT_LANGUAGE_PYTHON
    relative_path: scripts/list.py
    entry_point: main
    dependencies: ["requests>=2.31.0", "rich==13.7.1"]
    timeout_seconds: 60
    environment_variables: {PAGE_SIZE: "50"}
skill_id: sk-1
uri: skm://skills/sk-1
source_uri: github://example/skills@main
---
Instructions here.
`

func TestParseCastorFrontmatter(t *testing.T) {
	s, err := ParseSkillMD([]byte(castorSkill), "x/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Problems) != 0 {
		t.Fatalf("problems: %v", s.Problems)
	}
	if s.License != "Apache-2.0" || s.Compatibility == "" || s.Metadata["owner"] != "platform" || s.Category != "devops" ||
		len(s.Authors) != 1 || s.Authors[0].Email != "ada@example.com" || s.SkillID != "sk-1" || s.SourceURI == "" {
		t.Errorf("metadata: %+v", s.SkillMetadata)
	}
	if got := s.AllowedToolsList(); !slices.Equal(got, []string{"Bash(gh:*)", "Read"}) {
		t.Errorf("allowed-tools: %q", got)
	}
	h := s.ExecutionHints
	if h == nil || h.HITLTier != Tier2AuditedWrite || !h.NeedsNetwork() || h.TimeoutSeconds != 120 || len(h.EnvironmentVariables) != 2 {
		t.Fatalf("hints: %+v", h)
	}
	sc := s.Scripts[0]
	if sc.Language != LanguagePython || sc.RelativePath != "scripts/list.py" || len(sc.Dependencies) != 2 || sc.EnvironmentVariables["PAGE_SIZE"] != "50" {
		t.Errorf("script: %+v", sc)
	}
	if s.CompiledReference.SHA256Hash != "abc123" || s.ToolRequirements[0].Scopes[0] != "gh:*" {
		t.Errorf("reference/tools: %+v %+v", s.CompiledReference, s.ToolRequirements)
	}
	if s.Content != "Instructions here." {
		t.Errorf("content %q", s.Content)
	}
}

func TestHITLTierNames(t *testing.T) {
	for in, want := range map[string]HITLTier{
		"HITL_POLICY_TIER_0_BYPASS_ALL": Tier0BypassAll,
		"TIER_1_AUTO_READ":              Tier1AutoRead,
		"tier_2":                        Tier2AuditedWrite,
		"TIER_3_MANDATORY_APPROVAL":     Tier3MandatoryApproval,
		"":                              TierUnspecified,
	} {
		if got, err := ParseHITLTier(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := ParseHITLTier("TIER_9"); err == nil {
		t.Error("unknown tier accepted")
	}
	// A number is ambiguous (the proto numbers tier 2 as 3), so it's refused.
	_, err := ParseSkillMD([]byte("---\nname: x\nexecution_hints:\n  hitl_tier: 2\n---\n"), "")
	if err == nil || !strings.Contains(err.Error(), "not a number") {
		t.Fatalf("numeric tier: %v", err)
	}
	// Omitted means unspecified, never a bypass.
	s, _ := ParseSkillMD([]byte("---\nname: x\n---\n"), "")
	if s.DeclaredTier() != TierUnspecified {
		t.Fatalf("omitted tier = %v", s.DeclaredTier())
	}
}

func TestValidateScripts(t *testing.T) {
	doc := `---
name: bad
scripts:
  - name: a
    language: python
    relative_path: ../escape.py
  - name: a
    language: python
    inline_code: "print(1)"
    relative_path: x.py
  - name: "no spaces"
    language: python
    storage_uri: gs://bucket/x.py
  - name: c
    inline_code: "x"
    timeout_seconds: -1
    environment_variables: {"BAD-NAME": "1"}
execution_hints:
  environment_variables: ["1BAD"]
tool_requirements:
  - scopes: ["x"]
---
`
	s, err := ParseSkillMD([]byte(doc), "")
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(s.Problems, "\n")
	for _, want := range []string{
		`script "a": relative_path must stay inside`,
		`script "a": name is used twice`,
		`needs exactly one of inline_code, storage_uri or relative_path`,
		`may only use letters`,
		`storage_uri isn't supported yet`,
		`script "c": needs a language`,
		`timeout_seconds can't be negative`,
		`invalid environment variable name "BAD-NAME"`,
		`execution_hints: invalid environment variable name "1BAD"`,
		`tool_requirements[0]: needs a name`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing problem %q in:\n%s", want, all)
		}
	}
	if _, err := ParseSkillMD([]byte("---\nname: x\nscripts:\n  - name: s\n    language: cobol\n---\n"), ""); err == nil {
		t.Error("unknown language accepted")
	}
}

func TestInBundle(t *testing.T) {
	for p, want := range map[string]bool{
		"scripts/a.py": true, "a.py": true, "./a.py": true, "x/../a.py": true,
		"../a.py": false, "/etc/passwd": false, "x/../../a.py": false, `scripts\a.py`: false, ".": false,
	} {
		if inBundle(p) != want {
			t.Errorf("inBundle(%q) = %v", p, !want)
		}
	}
}

func TestContentHashCoversEveryFileAndSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "scripts"), 0o755)
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: h\n---\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "scripts", "a.py"), []byte("print(1)"), 0o644)
	p, _ := NewProvider()
	if err := p.DiscoverExternal([]string{dir}); err != nil {
		t.Fatal(err)
	}
	s, _ := p.Get("h")
	h1, err := s.ContentHash()
	if err != nil || !strings.HasPrefix(h1, "sha256:") {
		t.Fatalf("%q %v", h1, err)
	}
	if h, _ := s.ContentHash(); h != h1 {
		t.Fatal("hash isn't stable")
	}
	os.Symlink("/etc/hosts", filepath.Join(dir, "scripts", "link"))
	if h, _ := s.ContentHash(); h != h1 {
		t.Fatal("a symbolic link changed the hash")
	}
	os.WriteFile(filepath.Join(dir, "scripts", "a.py"), []byte("print(2)"), 0o644)
	if h, _ := s.ContentHash(); h == h1 {
		t.Fatal("changing a script didn't change the hash")
	}
	// Built-in skills hash too.
	b, _ := p.Get("code-review")
	if h, err := b.ContentHash(); err != nil || h == "" {
		t.Fatalf("builtin: %q %v", h, err)
	}
}

func TestDiscoveryReportsUnparseableSkills(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "broken"), 0o755)
	os.WriteFile(filepath.Join(dir, "broken", "SKILL.md"), []byte("---\nname: broken\nexecution_hints:\n  hitl_tier: TIER_7\n---\n"), 0o644)
	p, _ := NewProvider()
	err := p.DiscoverExternal([]string{dir})
	if err == nil || !strings.Contains(err.Error(), "broken/SKILL.md") || !strings.Contains(err.Error(), "TIER_7") {
		t.Fatalf("error: %v", err)
	}
	if _, ok := p.Get("broken"); ok {
		t.Fatal("an unparseable skill was loaded")
	}
}
