package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestSaveUILocale(t *testing.T) {
	cases := []struct {
		name, before string
		want         []string // substrings of the result
	}{
		{"missing file", "", []string{"[ui]\nlocale = \"es\"\n"}},
		{"no ui table", "# top comment\n[llm]\nprovider = \"gemini\"\n",
			[]string{"# top comment\n[llm]\nprovider = \"gemini\"\n\n[ui]\nlocale = \"es\"\n"}},
		{"replace keeps comment", "[ui]\nmarkdown = true\nlocale    = \"en-US\"   # interface language\n\n[memory]\nenabled = true\n",
			[]string{"locale = \"es\"   # interface language\n", "markdown = true", "[memory]\nenabled = true"}},
		{"insert at end of table", "[ui]\nmarkdown = true\n# spinner = false\n\n[memory]\nenabled = true\n",
			[]string{"# spinner = false\nlocale = \"es\"\n\n[memory]"}},
		{"ignores other tables' locale", "[other]\nlocale = \"x\"\n[ui]\nspinner = true\n",
			[]string{"[other]\nlocale = \"x\"\n[ui]\nspinner = true\nlocale = \"es\""}},
		{"ignores sub-tables", "[ui.theme]\nlocale = \"x\"\n",
			[]string{"[ui.theme]\nlocale = \"x\"\n\n[ui]\nlocale = \"es\"\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".env.toml")
			if tc.before != "" {
				os.WriteFile(path, []byte(tc.before), 0o640)
			}
			got, err := SaveUILocale(dir, "es")
			if err != nil || got != path {
				t.Fatalf("SaveUILocale = %q, %v", got, err)
			}
			data, _ := os.ReadFile(path)
			for _, w := range tc.want {
				if !strings.Contains(string(data), w) {
					t.Errorf("result lacks %q:\n%s", w, data)
				}
			}
			var cfg struct {
				UI struct{ Locale string } `toml:"ui"`
			}
			if _, err := toml.Decode(string(data), &cfg); err != nil || cfg.UI.Locale != "es" {
				t.Errorf("decoded locale %q, %v", cfg.UI.Locale, err)
			}
			info, _ := os.Stat(path)
			wantPerm := os.FileMode(0o600)
			if tc.before != "" {
				wantPerm = 0o640 // existing permissions are kept
			}
			if info.Mode().Perm() != wantPerm {
				t.Errorf("mode = %v, want %v", info.Mode().Perm(), wantPerm)
			}
		})
	}
}

func TestSaveUILocaleRefusesToBreakConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env.toml")
	bad := "[ui\nlocale = \n"
	os.WriteFile(path, []byte(bad), 0o600)
	if _, err := SaveUILocale(dir, "es"); err == nil {
		t.Fatal("expected an error for an unparseable file")
	}
	if data, _ := os.ReadFile(path); string(data) != bad {
		t.Error("file must be left untouched on failure")
	}
	if _, err := SaveUILocale("", "es"); err == nil {
		t.Error("empty dir should fail")
	}
}

func TestDefaultLocale(t *testing.T) {
	if c := DefaultConfig(); c.UI.Locale != "en-US" || !strings.HasSuffix(c.UI.LocalesDir, "locales") {
		t.Errorf("defaults: %q %q", c.UI.Locale, c.UI.LocalesDir)
	}
}
