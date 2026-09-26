package tui

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/i18n"
	"github.com/retail-cortex/code_puppy/pkg/runtime"
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
	eng := app.Engine
	if len(args) == 0 {
		all := eng.AllModelSettings()
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

	ref := args[0]
	provider, name := runtime.ParseModelRef(ref, app.Cfg.LLM.Provider)
	if strings.Contains(ref, "=") || name == "" {
		fmt.Printf("%s%s%s\n", Yellow, i18n.T("msettings.usage"), Reset)
		return
	}
	if len(args) == 1 {
		showModelSettings(name, eng.ModelSettings(name), app.Cfg)
		return
	}

	s := eng.ModelSettings(name)
	if len(args) == 2 && args[1] == "reset" {
		s = config.ModelSettings{}
	} else {
		for _, pair := range args[1:] {
			key, value, ok := strings.Cut(pair, "=")
			if !ok {
				fmt.Printf("%s%s%s\n", Yellow, i18n.T("msettings.bad_pair", "arg", safe(pair)), Reset)
				return
			}
			if err := s.Set(strings.TrimSpace(key), value); err != nil {
				fmt.Printf("%s❌ %s%s\n", Red, i18n.T("msettings.invalid", "error", safe(err.Error())), Reset)
				return
			}
		}
	}

	eng.SetModelSettings(name, s)
	if s.IsZero() {
		fmt.Printf("%s✅ %s%s\n", Green, i18n.T("msettings.cleared", "model", safe(name)), Reset)
	} else {
		fmt.Printf("%s✅ %s%s\n", Green, i18n.T("msettings.updated", "model", safe(name), "settings", safe(summarizeSettings(s))), Reset)
	}
	var unsupported []string
	for _, key := range config.ModelSettingKeys {
		if _, set := s.Get(key); set && !runtime.SettingSupported(provider, name, key) {
			unsupported = append(unsupported, key)
		}
	}
	if len(unsupported) > 0 {
		fmt.Printf("%s⚠️  %s%s\n", Yellow, i18n.T("msettings.unsupported",
			"model", safe(name), "provider", provider, "keys", strings.Join(unsupported, ", ")), Reset)
	}
	if app.SaveModelSettings == nil {
		return
	}
	if path, err := app.SaveModelSettings(configKey(app.Cfg, name), s); err != nil {
		fmt.Printf("%s⚠️  %s%s\n", Yellow, i18n.T("pin.save_failed", "error", safe(err.Error())), Reset)
	} else {
		fmt.Printf("%s%s%s\n", Dim, i18n.T("pin.saved", "path", safe(path)), Reset)
	}
}

// configKey returns the [model_settings] key the config file already uses
// for model name, e.g. a hand-written "openai/gpt-5", so a change edits that
// table instead of adding a second one; otherwise name itself.
func configKey(cfg *config.Config, name string) string {
	if _, ok := cfg.ModelSettings[name]; ok {
		return name
	}
	for _, key := range slices.Sorted(maps.Keys(cfg.ModelSettings)) {
		if _, n := runtime.ParseModelRef(key, ""); n == name {
			return key
		}
	}
	return name
}

// showModelSettings lists every setting for one model: its own value, or
// where the value comes from when it has none.
func showModelSettings(name string, s config.ModelSettings, cfg *config.Config) {
	fmt.Printf("\n%s🎛️  %s%s\n", Bold, safe(name), Reset)
	for _, key := range config.ModelSettingKeys {
		if v, ok := s.Get(key); ok {
			fmt.Printf("  %-12s %s\n", key, v)
			continue
		}
		inherited := i18n.T("msettings.provider_default")
		switch {
		case key == "temperature" && cfg.CodePuppy.Temperature > 0:
			inherited = i18n.T("msettings.global", "value", strconv.FormatFloat(cfg.CodePuppy.Temperature, 'f', -1, 64))
		case key == "max_tokens" && cfg.CodePuppy.MaxTokens > 0:
			inherited = i18n.T("msettings.global", "value", strconv.Itoa(cfg.CodePuppy.MaxTokens))
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
