package tui

import (
	"fmt"
	"strings"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/i18n"
	"github.com/retail-cortex/code_puppy/internal/skills"
)

// skillPolicy is the configured skills policy, or the defaults.
func skillPolicy(app *App) config.SkillPolicy {
	if app != nil && app.Cfg != nil {
		return app.Cfg.Skills.Policy
	}
	return config.DefaultConfig().Skills.Policy
}

// scriptSummary is the one-line state of a skill's scripts for /skills
// list: how many, their approval tier, and whether they may run.
func scriptSummary(s *skills.Skill, p config.SkillPolicy) string {
	if len(s.Scripts) == 0 {
		return ""
	}
	ev := skills.Evaluate(s, p)
	n := i18n.N("skills.scripts", len(s.Scripts))
	if ev.Runnable() {
		return i18n.T("skills.scripts_ok", "scripts", n, "tier", ev.Tier.String())
	}
	reason := ""
	if len(ev.Blocked) > 0 {
		reason = ev.Blocked[0]
	} else if len(ev.Scripts) > 0 && len(ev.Scripts[0].Reasons) > 0 {
		reason = ev.Scripts[0].Reasons[0]
	}
	return i18n.T("skills.scripts_blocked", "scripts", n, "reason", reason)
}

// showSkill prints everything about a skill that matters for running its
// scripts: what it declares and what the policy lets it do.
func showSkill(name string, prov *skills.Provider, p config.SkillPolicy) {
	s, ok := prov.Get(name)
	if !ok {
		fmt.Printf("%s❌ %s%s\n", Red, i18n.T("skills.not_found", "name", safe(name)), Reset)
		return
	}
	ev := skills.Evaluate(s, p)
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
	if ev.Hash != "" {
		fmt.Printf("   %s\n", i18n.T("skills.show_hash", "hash", ev.Hash))
	}
	for _, t := range s.ToolRequirements {
		scopes := ""
		if len(t.Scopes) > 0 {
			scopes = " (" + strings.Join(t.Scopes, ", ") + ")"
		}
		fmt.Printf("   %s\n", i18n.T("skills.show_tool", "tool", safe(t.Name+scopes), "why", safe(t.Description)))
	}
	if len(s.Scripts) == 0 {
		fmt.Printf("   %s%s%s\n\n", Dim, i18n.T("skills.show_no_scripts"), Reset)
		return
	}
	tier := ev.Tier.String()
	if ev.Bypass {
		tier += " (bypass)"
	}
	fmt.Printf("   %s\n", i18n.T("skills.show_tier", "tier", tier))
	switch {
	case ev.Network:
		fmt.Printf("   %s\n", i18n.T("skills.show_network_on"))
	case !s.ExecutionHints.NeedsNetwork():
		fmt.Printf("   %s\n", i18n.T("skills.show_network_off"))
	}
	if len(ev.Env) > 0 {
		fmt.Printf("   %s\n", i18n.T("skills.show_env", "names", strings.Join(ev.Env, ", ")))
	}
	if len(ev.Withheld) > 0 {
		fmt.Printf("   %s%s%s\n", Yellow, i18n.T("skills.show_withheld", "names", strings.Join(ev.Withheld, ", ")), Reset)
	}
	for i, v := range ev.Scripts {
		sc := s.Scripts[i]
		src := sc.RelativePath
		if src == "" {
			src = "inline"
		}
		mark, color := "✓", Green
		if !v.Allowed {
			mark, color = "✗", Red
		}
		fmt.Printf("   %s%s %s%s %s(%s, %s, %ds)%s\n", color, mark, safe(v.Name), Reset, Dim, sc.Language, safe(src), v.TimeoutSeconds, Reset)
		if len(v.Dependencies) > 0 {
			fmt.Printf("       %s\n", i18n.T("skills.show_deps", "deps", safe(strings.Join(v.Dependencies, ", "))))
		}
		for _, r := range v.Reasons {
			fmt.Printf("       %s%s%s\n", Red, safe(r), Reset)
		}
	}
	fmt.Println()
}
