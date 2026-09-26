package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/retail-cortex/code_puppy/internal/i18n"
	"github.com/retail-cortex/code_puppy/internal/skills"
	"github.com/retail-cortex/code_puppy/internal/tools"
)

// cmdEnvs lists skill scripts' Python environments, prunes those no loaded
// skill needs, or removes one:
//
//	/envs                 list
//	/envs prune           remove environments no allowed script uses
//	/envs remove <key>    remove one
func cmdEnvs(args []string, app *App) {
	if app.Tools == nil || app.Tools.SkillScripts() == nil {
		fmt.Println(i18n.T("skills.disabled"))
		return
	}
	envs := app.Tools.SkillScripts().Envs()
	sub := ""
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	switch {
	case sub == "":
		list := envs.List()
		if len(list) == 0 {
			fmt.Println(i18n.T("envs.none"))
			return
		}
		fmt.Printf("\n%s🐍 %s%s\n", Bold, i18n.T("envs.title", "count", len(list)), Reset)
		for _, e := range list {
			state := ""
			if !e.Ready {
				state = " " + Yellow + i18n.T("envs.incomplete") + Reset
			}
			fmt.Printf("  %s%s%s %s%s · %s%s%s\n", Bold, e.Key, Reset, Dim, sizeLabel(e.Size), usedLabel(e.LastUsed), Reset, state)
			if len(e.Deps) > 0 {
				fmt.Printf("      %s\n", safe(strings.Join(e.Deps, ", ")))
			}
			if len(e.Skills) > 0 {
				fmt.Printf("      %s%s%s\n", Dim, i18n.T("envs.used_by", "skills", safe(strings.Join(e.Skills, ", "))), Reset)
			}
		}
		fmt.Println()
	case sub == "prune":
		needed := neededEnvs(app, envs)
		removed, freed := 0, int64(0)
		for _, e := range envs.List() {
			if e.Ready && needed[e.Key] {
				continue
			}
			if err := envs.Remove(e.Key); err != nil {
				fmt.Printf("%s❌ %s%s\n", Red, i18n.T("envs.remove_failed", "key", e.Key, "error", safe(err.Error())), Reset)
				continue
			}
			removed++
			freed += e.Size
		}
		fmt.Printf("%s✅ %s%s\n", Green, i18n.T("envs.pruned", "count", removed, "size", sizeLabel(freed)), Reset)
	case sub == "remove" && len(args) == 2:
		if err := envs.Remove(args[1]); err != nil {
			fmt.Printf("%s❌ %s%s\n", Red, i18n.T("envs.remove_failed", "key", safe(args[1]), "error", safe(err.Error())), Reset)
			return
		}
		fmt.Printf("%s✅ %s%s\n", Green, i18n.T("envs.removed", "key", safe(args[1])), Reset)
	default:
		fmt.Printf("%s%s%s\n", Yellow, i18n.T("envs.usage"), Reset)
	}
}

// neededEnvs are the environments of the scripts the skills policy lets run.
func neededEnvs(app *App, envs *tools.PyEnvs) map[string]bool {
	needed := map[string]bool{}
	python, err := tools.SystemPython()
	if err != nil || app.Skills == nil {
		return needed
	}
	for _, s := range app.Skills.List() {
		ev := skills.Evaluate(s, skillPolicy(app))
		for i, sc := range s.Scripts {
			if ev.Scripts[i].Allowed && len(sc.Dependencies) > 0 {
				needed[envs.Key(python, sc.Dependencies)] = true
			}
		}
	}
	return needed
}

func sizeLabel(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d KB", n>>10)
	}
}

func usedLabel(t time.Time) string {
	if t.IsZero() {
		return i18n.T("envs.never_used")
	}
	return i18n.T("envs.last_used", "when", t.Format("2006-01-02 15:04"))
}
