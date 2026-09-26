package config

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ModelSettings are generation settings for one model, from
// [model_settings."<model>"]. Unset (nil) fields fall back to the global
// code_puppy.temperature and max_tokens, or the provider's default.
type ModelSettings struct {
	Temperature *float64 `toml:"temperature"`
	MaxTokens   *int     `toml:"max_tokens"`
	TopP        *float64 `toml:"top_p"`
	Seed        *int     `toml:"seed"`
}

// ModelSettingKeys are the settings a model can have, in display order.
var ModelSettingKeys = []string{"temperature", "max_tokens", "top_p", "seed"}

// IsZero reports whether no setting is set.
func (s ModelSettings) IsZero() bool {
	return s.Temperature == nil && s.MaxTokens == nil && s.TopP == nil && s.Seed == nil
}

// Get returns key's value as written in TOML, and whether it is set.
func (s ModelSettings) Get(key string) (string, bool) {
	switch key {
	case "temperature":
		return formatFloat(s.Temperature)
	case "top_p":
		return formatFloat(s.TopP)
	case "max_tokens":
		return formatInt(s.MaxTokens)
	case "seed":
		return formatInt(s.Seed)
	}
	return "", false
}

// Set parses and validates text for key; "" clears the setting.
func (s *ModelSettings) Set(key, text string) error {
	text = strings.TrimSpace(text)
	switch key {
	case "temperature":
		return setFloat(&s.Temperature, key, text, 0, 2, true)
	case "top_p":
		return setFloat(&s.TopP, key, text, 0, 1, false)
	case "max_tokens":
		return setInt(&s.MaxTokens, key, text, 1)
	case "seed":
		return setInt(&s.Seed, key, text, math.MinInt32)
	}
	return fmt.Errorf("unknown setting %q (known: %s)", key, strings.Join(ModelSettingKeys, ", "))
}

func setFloat(dst **float64, key, text string, lo, hi float64, loInclusive bool) error {
	if text == "" {
		*dst = nil
		return nil
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(v) || v > hi || v < lo || (!loInclusive && v == lo) {
		open := "["
		if !loInclusive {
			open = "("
		}
		return fmt.Errorf("%s must be a number in %s%g, %g]", key, open, lo, hi)
	}
	*dst = &v
	return nil
}

func setInt(dst **int, key, text string, lo int64) error {
	if text == "" {
		*dst = nil
		return nil
	}
	v, err := strconv.ParseInt(text, 10, 32) // the APIs take 32-bit values
	if err != nil || v < lo {
		return fmt.Errorf("%s must be a whole number from %d to %d", key, lo, math.MaxInt32)
	}
	n := int(v)
	*dst = &n
	return nil
}

func formatFloat(v *float64) (string, bool) {
	if v == nil {
		return "", false
	}
	s := strconv.FormatFloat(*v, 'f', -1, 64)
	if !strings.ContainsAny(s, ".e") {
		s += ".0" // keep it a TOML float
	}
	return s, true
}

func formatInt(v *int) (string, bool) {
	if v == nil {
		return "", false
	}
	return strconv.Itoa(*v), true
}

// SaveModelSettings writes s as [model_settings."<model>"] in dir/.env.toml:
// set keys are written, unset ones removed, and the table is dropped when
// nothing is left. Other lines, and their comments, are kept.
func SaveModelSettings(dir, model string, s ModelSettings) (string, error) {
	if strings.TrimSpace(model) == "" {
		return "", errors.New("no model name")
	}
	table := "model_settings." + tomlKey(model)
	return editConfigFile(dir,
		func(doc string) string {
			for _, key := range ModelSettingKeys {
				if v, ok := s.Get(key); ok {
					doc = setTOMLKey(doc, table, key, v)
				} else {
					doc = removeTOMLKey(doc, table, key)
				}
			}
			return removeEmptyTable(doc, table)
		},
		func(check map[string]any) error {
			all, _ := check["model_settings"].(map[string]any)
			got, _ := all[model].(map[string]any)
			for _, key := range ModelSettingKeys {
				want, set := s.Get(key)
				v, present := got[key]
				if set != present || (set && !sameNumber(v, want)) {
					return fmt.Errorf("could not update [%s] %s", table, key)
				}
			}
			return nil
		})
}

// sameNumber reports whether a decoded TOML number equals text.
func sameNumber(v any, text string) bool {
	want, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return false
	}
	switch n := v.(type) {
	case int64:
		return float64(n) == want
	case float64:
		return n == want
	}
	return false
}

// removeEmptyTable deletes [table]'s header when only blank lines and
// comments follow it, up to the next table. Comments are kept.
func removeEmptyTable(doc, table string) string {
	lines := strings.Split(doc, "\n")
	for i, l := range lines {
		m := tableRE.FindStringSubmatch(l)
		if m == nil || m[1] != table || strings.HasPrefix(strings.TrimSpace(l), "[[") {
			continue
		}
		for _, next := range lines[i+1:] {
			if tableRE.MatchString(next) {
				break
			}
			if t := strings.TrimSpace(next); t != "" && !strings.HasPrefix(t, "#") {
				return doc
			}
		}
		lines = append(lines[:i], lines[i+1:]...)
		if i > 0 && i < len(lines) && strings.TrimSpace(lines[i-1]) == "" && strings.TrimSpace(lines[i]) == "" {
			lines = append(lines[:i], lines[i+1:]...) // don't leave a double blank line
		}
		return strings.Join(lines, "\n")
	}
	return doc
}
