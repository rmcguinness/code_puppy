package tui

import (
	"fmt"
	"strings"

	core "github.com/retail-cortex/code_puppy/internal/app"
	"github.com/retail-cortex/code_puppy/internal/i18n"
)

// scriptSummary is the one-line state of a skill's scripts for /skills
// list: how many, their approval tier, and whether they may run.
func scriptSummary(s core.SkillInfo) string {
	if len(s.Scripts) == 0 {
		return ""
	}
	n := i18n.N("skills.scripts", len(s.Scripts))
	if s.Runnable() {
		return i18n.T("skills.scripts_ok", "scripts", n, "tier", s.Tier)
	}
	reason := ""
	if len(s.Blocked) > 0 {
		reason = s.Blocked[0]
	} else if len(s.Scripts[0].Reasons) > 0 {
		reason = s.Scripts[0].Reasons[0]
	}
	return i18n.T("skills.scripts_blocked", "scripts", n, "reason", reason)
}

// showSkill prints everything about a skill that matters for running its
// scripts: what it declares and what the policy lets it do.
func showSkill(s core.SkillInfo) {
	var about []string
	for _, v := range []string{s.Version, s.License, s.Category} {
		if v != "" {
			about = append(about, v)
		}
	}
	fmt.Printf("\n%s📦 %s%s %s%s%s\n", Bold, safe(s.Name), Reset, Dim, safe(strings.Join(about, " · ")), Reset)
	fmt.Printf("   %s\n", safe(s.Description))
	if s.Compatibility != "" {
		fmt.Printf("   %s%s%s\n", Dim, safe(s.Compatibility), Reset)
	}
	if s.Hash != "" {
		fmt.Printf("   %s\n", i18n.T("skills.show_hash", "hash", s.Hash))
	}
	for _, t := range s.Tools {
		scopes := ""
		if len(t.Scopes) > 0 {
			scopes = " (" + strings.Join(t.Scopes, ", ") + ")"
		}
		fmt.Printf("   %s\n", i18n.T("skills.show_tool", "tool", safe(t.Name+scopes), "why", safe(t.Why)))
	}
	if len(s.Scripts) == 0 {
		fmt.Printf("   %s%s%s\n\n", Dim, i18n.T("skills.show_no_scripts"), Reset)
		return
	}
	tier := s.Tier
	if s.Bypass {
		tier += " (bypass)"
	}
	fmt.Printf("   %s\n", i18n.T("skills.show_tier", "tier", tier))
	switch {
	case s.Network:
		fmt.Printf("   %s\n", i18n.T("skills.show_network_on"))
	case !s.NeedsNetwork:
		fmt.Printf("   %s\n", i18n.T("skills.show_network_off"))
	}
	if len(s.Env) > 0 {
		fmt.Printf("   %s\n", i18n.T("skills.show_env", "names", strings.Join(s.Env, ", ")))
	}
	if len(s.Withheld) > 0 {
		fmt.Printf("   %s%s%s\n", Yellow, i18n.T("skills.show_withheld", "names", strings.Join(s.Withheld, ", ")), Reset)
	}
	for _, sc := range s.Scripts {
		mark, color := "✓", Green
		if !sc.Allowed {
			mark, color = "✗", Red
		}
		fmt.Printf("   %s%s %s%s %s(%s, %s, %ds)%s\n", color, mark, safe(sc.Name), Reset, Dim, sc.Language, safe(sc.Source), int(sc.Timeout.Seconds()), Reset)
		if len(sc.Deps) > 0 {
			fmt.Printf("       %s\n", i18n.T("skills.show_deps", "deps", safe(strings.Join(sc.Deps, ", "))))
		}
		for _, r := range sc.Reasons {
			fmt.Printf("       %s%s%s\n", Red, safe(r), Reset)
		}
	}
	fmt.Println()
}
