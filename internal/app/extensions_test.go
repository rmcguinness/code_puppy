package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
)

func TestSkillsReportThePolicyVerdict(t *testing.T) {
	w, _ := openTestWith(t, func(c *config.Config) { c.Skills.Policy.Languages = []string{"typescript"} })
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "tidy"), 0o755)
	os.WriteFile(filepath.Join(dir, "tidy", "SKILL.md"), []byte("---\nname: tidy\ndescription: tidies imports\nscripts:\n  - name: run\n    language: python\n    inline_code: print(1)\n    dependencies: [\"six==1.16.0\"]\n---\n"), 0o644)
	if err := w.Skills().DiscoverExternal([]string{dir}); err != nil {
		t.Fatal(err)
	}
	s, ok := w.Skill("tidy")
	if !ok || s.Description != "tidies imports" || len(s.Scripts) != 1 {
		t.Fatalf("skill %+v %v", s, ok)
	}
	sc := s.Scripts[0]
	if sc.Language != "python" || sc.Source != "inline" || !slices.Equal(sc.Deps, []string{"six==1.16.0"}) || sc.Timeout <= 0 {
		t.Errorf("script %+v", sc)
	}
	// python isn't among the policy's languages.
	if sc.Allowed || len(sc.Reasons) == 0 || s.Runnable() || s.Tier == "" {
		t.Errorf("verdict %+v, tier %q", sc, s.Tier)
	}
	if _, ok := w.Skill("nope"); ok {
		t.Error("found a missing skill")
	}
	if i := slices.IndexFunc(w.ListSkills(), func(s SkillInfo) bool { return s.Name == "tidy" }); i < 0 {
		t.Error("tidy not listed")
	}
	if got := w.SearchSkills("imports"); len(got) != 1 || got[0].Name != "tidy" {
		t.Errorf("search %+v", got)
	}
}

func TestActiveAgentTools(t *testing.T) {
	w := openTest(t)
	at := w.ActiveAgentTools()
	find := func(name string) (ToolInfo, bool) {
		i := slices.IndexFunc(at.Tools, func(t ToolInfo) bool { return t.Name == name })
		if i < 0 {
			return ToolInfo{}, false
		}
		return at.Tools[i], true
	}
	if at.Agent != "code-puppy" || !slices.IsSortedFunc(at.Tools, func(a, b ToolInfo) int { return strings.Compare(a.Name, b.Name) }) {
		t.Errorf("agent %q, tools unsorted", at.Agent)
	}
	if r, ok := find("read_file"); !ok || !r.PlanAllowed || r.Description == "" {
		t.Errorf("read_file %+v %v", r, ok)
	}
	if c, ok := find("create_file"); !ok || c.PlanAllowed {
		t.Errorf("create_file %+v %v", c, ok)
	}
	if !mcpOfferedTo(nil, "a") || !mcpOfferedTo([]string{"*"}, "a") || mcpOfferedTo([]string{"b"}, "a") {
		t.Error("MCP offer rule")
	}
}
