package tui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	core "github.com/retail-cortex/code_puppy/internal/app"
	"github.com/retail-cortex/code_puppy/internal/i18n"
)

// cmdEnvs lists skill scripts' Python environments, prunes those no loaded
// skill needs, or removes one:
//
//	/envs                 list
//	/envs prune           remove environments no allowed script uses
//	/envs remove <key>    remove one
func cmdEnvs(args []string, app *App) {
	sub := ""
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	switch {
	case sub == "":
		list, err := app.Workspace.ListEnvs()
		if err != nil {
			fmt.Println(i18n.T("skills.disabled"))
			return
		}
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
		res, err := app.Workspace.PruneEnvs()
		if err != nil {
			fmt.Println(i18n.T("skills.disabled"))
			return
		}
		for _, f := range res.Failed {
			fmt.Printf("%s❌ %s%s\n", Red, i18n.T("envs.remove_failed", "key", f.Key, "error", safe(f.Err.Error())), Reset)
		}
		fmt.Printf("%s✅ %s%s\n", Green, i18n.T("envs.pruned", "count", res.Removed, "size", sizeLabel(res.Freed)), Reset)
	case sub == "remove" && len(args) == 2:
		err := app.Workspace.RemoveEnv(args[1])
		switch {
		case errors.Is(err, core.ErrScriptsDisabled):
			fmt.Println(i18n.T("skills.disabled"))
		case err != nil:
			fmt.Printf("%s❌ %s%s\n", Red, i18n.T("envs.remove_failed", "key", safe(args[1]), "error", safe(err.Error())), Reset)
		default:
			fmt.Printf("%s✅ %s%s\n", Green, i18n.T("envs.removed", "key", safe(args[1])), Reset)
		}
	default:
		fmt.Printf("%s%s%s\n", Yellow, i18n.T("envs.usage"), Reset)
	}
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
