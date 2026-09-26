package tui

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	core "github.com/retail-cortex/code_puppy/internal/app"
	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/i18n"
)

// cmdModelSettings shows or changes per-model generation settings:
//
//	/model_settings                          every model with settings
//	/model_settings <model>                  one model, with inherited values
//	/model_settings <model> key=value ...    set (key= clears one)
//	/model_settings <model> reset            clear all of them
//
// Changes apply from the next model call and are saved to the config file.
func cmdModelSettings(args []string, app *App) {
	if len(args) == 0 {
		all := app.Workspace.AllModelSettings()
		if len(all) == 0 {
			fmt.Println(i18n.T("msettings.none"))
			fmt.Println()
			return
		}
		fmt.Printf("\n%s🎛️  %s%s\n", Bold, i18n.T("msettings.title"), Reset)
		for _, name := range slices.Sorted(maps.Keys(all)) {
			fmt.Printf("  %s%-24s%s %s\n", Bold, safe(name), Reset, safe(summarizeSettings(all[name])))
		}
		fmt.Println()
		return
	}

	if len(args) == 1 {
		info, err := app.Workspace.ModelSettings(args[0])
		if err != nil {
			fmt.Printf("%s%s%s\n", Yellow, i18n.T("msettings.usage"), Reset)
			return
		}
		showModelSettings(info)
		return
	}

	reset := len(args) == 2 && args[1] == "reset"
	var changes []core.Setting
	if !reset {
		for _, pair := range args[1:] {
			key, value, ok := strings.Cut(pair, "=")
			if !ok {
				fmt.Printf("%s%s%s\n", Yellow, i18n.T("msettings.bad_pair", "arg", safe(pair)), Reset)
				return
			}
			changes = append(changes, core.Setting{Key: key, Value: value})
		}
	}
	res, err := app.Workspace.UpdateModelSettings(args[0], reset, changes)
	var invalid *core.InvalidSettingError
	switch {
	case errors.Is(err, core.ErrBadModelRef):
		fmt.Printf("%s%s%s\n", Yellow, i18n.T("msettings.usage"), Reset)
		return
	case errors.As(err, &invalid):
		fmt.Printf("%s❌ %s%s\n", Red, i18n.T("msettings.invalid", "error", safe(invalid.Error())), Reset)
		return
	case err != nil:
		fmt.Printf("%s❌ %s%s\n", Red, safe(err.Error()), Reset)
		return
	}
	if res.Settings.IsZero() {
		fmt.Printf("%s✅ %s%s\n", Green, i18n.T("msettings.cleared", "model", safe(res.Model)), Reset)
	} else {
		fmt.Printf("%s✅ %s%s\n", Green, i18n.T("msettings.updated", "model", safe(res.Model), "settings", safe(summarizeSettings(res.Settings))), Reset)
	}
	if len(res.Unsupported) > 0 {
		fmt.Printf("%s⚠️  %s%s\n", Yellow, i18n.T("msettings.unsupported",
			"model", safe(res.Model), "provider", res.Provider, "keys", strings.Join(res.Unsupported, ", ")), Reset)
	}
	printSaved(res.Saved)
}

// showModelSettings lists every setting for one model: its own value, or
// where the value comes from when it has none.
func showModelSettings(info core.ModelSettingsInfo) {
	fmt.Printf("\n%s🎛️  %s%s\n", Bold, safe(info.Model), Reset)
	for _, key := range config.ModelSettingKeys {
		if v, ok := info.Settings.Get(key); ok {
			fmt.Printf("  %-12s %s\n", key, v)
			continue
		}
		inherited := i18n.T("msettings.provider_default")
		switch {
		case key == "temperature" && info.GlobalTemperature > 0:
			inherited = i18n.T("msettings.global", "value", strconv.FormatFloat(info.GlobalTemperature, 'f', -1, 64))
		case key == "max_tokens" && info.GlobalMaxTokens > 0:
			inherited = i18n.T("msettings.global", "value", strconv.Itoa(info.GlobalMaxTokens))
		}
		fmt.Printf("  %-12s %s%s%s\n", key, Dim, inherited, Reset)
	}
	fmt.Println()
}

// summarizeSettings renders the settings that are set: "temperature=0.3 seed=7".
func summarizeSettings(s config.ModelSettings) string {
	var parts []string
	for _, key := range config.ModelSettingKeys {
		if v, ok := s.Get(key); ok {
			parts = append(parts, key+"="+v)
		}
	}
	return strings.Join(parts, " ")
}
