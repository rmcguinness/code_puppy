package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

var tableRE = regexp.MustCompile(`^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*(#.*)?$`)

// SaveUILocale records locale as [ui] locale in dir/.env.toml, editing only
// that line so comments, ordering and encrypted values are kept. The file is
// created (mode 600) if missing. It returns the file written.
func SaveUILocale(dir, locale string) (string, error) {
	return editConfigFile(dir,
		func(doc string) string { return setTOMLKey(doc, "ui", "locale", strconv.Quote(locale)) },
		func(check map[string]any) error {
			if ui, _ := check["ui"].(map[string]any); ui == nil || ui["locale"] != locale {
				return errors.New("could not set [ui] locale")
			}
			return nil
		})
}

// editConfigFile applies edit to dir/.env.toml (created mode 600 if
// missing), refuses results that aren't valid TOML or fail verify, and
// replaces the file atomically with its permissions kept.
func editConfigFile(dir string, edit func(doc string) string, verify func(map[string]any) error) (string, error) {
	if dir == "" {
		return "", errors.New("no configuration directory")
	}
	path := filepath.Join(dir, ".env.toml")
	orig, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	perm := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}

	updated := edit(string(orig))
	var check map[string]any
	if _, err := toml.Decode(updated, &check); err != nil {
		return "", fmt.Errorf("%s would not be valid TOML after the change: %w", path, err)
	}
	if err := verify(check); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".env.toml.*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(updated); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return path, os.Rename(tmp.Name(), path)
}

// setTOMLKey sets key = value in [table], replacing an existing assignment,
// inserting one at the end of the table, or appending the table.
func setTOMLKey(doc, table, key, value string) string {
	lines := strings.Split(doc, "\n")
	line := key + " = " + value
	start, end := -1, len(lines)
	for i, l := range lines {
		m := tableRE.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if start >= 0 {
			end = i
			break
		}
		if m[1] == table && !strings.HasPrefix(strings.TrimSpace(l), "[[") {
			start = i
		}
	}
	if start < 0 {
		doc = strings.TrimRight(doc, "\n")
		if doc != "" {
			doc += "\n\n"
		}
		return doc + "[" + table + "]\n" + line + "\n"
	}
	keyRE := regexp.MustCompile(`^(\s*)` + regexp.QuoteMeta(key) + `\s*=`)
	for i := start + 1; i < end; i++ {
		if m := keyRE.FindStringSubmatch(lines[i]); m != nil {
			comment := ""
			if j := commentStart(lines[i]); j >= 0 {
				comment = "   " + lines[i][j:]
			}
			lines[i] = m[1] + line + comment
			return strings.Join(lines, "\n")
		}
	}
	// Insert after the table's last non-blank line.
	at := end
	for at > start+1 && strings.TrimSpace(lines[at-1]) == "" {
		at--
	}
	lines = append(lines[:at], append([]string{line}, lines[at:]...)...)
	return strings.Join(lines, "\n")
}

// commentStart returns the index of a trailing # comment outside strings, or -1.
func commentStart(line string) int {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0 && c == '\\' && quote == '"':
			i++
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
		case quote == 0 && c == '#':
			return i
		}
	}
	return -1
}
