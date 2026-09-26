package config

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

var bareKeyRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// tomlKey returns key as written in TOML: bare when possible, else quoted.
func tomlKey(key string) string {
	if bareKeyRE.MatchString(key) {
		return key
	}
	return strconv.Quote(key)
}

// SaveAgentModel pins agent to model ref ("provider/model") under
// [agent_models] in dir/.env.toml, or removes the pin when ref is "".
// Only that line changes; comments and other settings are kept.
func SaveAgentModel(dir, agent, ref string) (string, error) {
	if agent == "" {
		return "", errors.New("no agent name")
	}
	key := tomlKey(agent)
	return editConfigFile(dir,
		func(doc string) string {
			if ref == "" {
				return removeTOMLKey(doc, "agent_models", key)
			}
			return setTOMLKey(doc, "agent_models", key, strconv.Quote(ref))
		},
		func(check map[string]any) error {
			pins, _ := check["agent_models"].(map[string]any)
			got, _ := pins[agent].(string)
			if got != ref {
				return errors.New("could not update [agent_models]")
			}
			return nil
		})
}

// removeTOMLKey deletes key's assignment from [table], if present.
func removeTOMLKey(doc, table, key string) string {
	lines := strings.Split(doc, "\n")
	keyRE := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
	in := false
	for i, l := range lines {
		if m := tableRE.FindStringSubmatch(l); m != nil {
			in = m[1] == table && !strings.HasPrefix(strings.TrimSpace(l), "[[")
			continue
		}
		if in && keyRE.MatchString(l) {
			return strings.Join(append(lines[:i], lines[i+1:]...), "\n")
		}
	}
	return doc
}
