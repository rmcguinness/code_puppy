package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestModelSettingsSetValidates(t *testing.T) {
	var s ModelSettings
	good := [][2]string{{"temperature", "0"}, {"temperature", "2"}, {"top_p", "1"}, {"top_p", "0.9"}, {"max_tokens", "1"}, {"seed", "-3"}, {"seed", "2147483647"}}
	for _, c := range good {
		if err := s.Set(c[0], c[1]); err != nil {
			t.Errorf("%s=%s: %v", c[0], c[1], err)
		}
	}
	bad := [][2]string{{"temperature", "2.1"}, {"temperature", "-1"}, {"temperature", "NaN"}, {"top_p", "0"}, {"top_p", "1.5"},
		{"max_tokens", "0"}, {"max_tokens", "1.5"}, {"max_tokens", "99999999999"}, {"seed", "x"}, {"top_k", "5"}}
	for _, c := range bad {
		before := s
		if err := s.Set(c[0], c[1]); err == nil {
			t.Errorf("%s=%s accepted", c[0], c[1])
		}
		if s != before {
			t.Errorf("%s=%s changed the settings on error", c[0], c[1])
		}
	}
	if err := s.Set("seed", ""); err != nil || s.Seed != nil {
		t.Fatalf("clearing: %v %v", err, s.Seed)
	}
	if v, ok := s.Get("temperature"); !ok || v != "2.0" {
		t.Fatalf("a whole temperature must stay a TOML float: %q", v)
	}
}

func TestSaveModelSettingsWritesAndRemoves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env.toml")
	orig := "# mine\n[code_puppy]\ntemperature = 0.2  # global\n\n[agent_models]\nqa-kitten = \"gpt-5\"\n"
	os.WriteFile(path, []byte(orig), 0o600)

	var gpt, local ModelSettings
	gpt.Set("temperature", "1")
	gpt.Set("top_p", "0.00001")
	gpt.Set("max_tokens", "4096")
	local.Set("seed", "7")
	for model, s := range map[string]ModelSettings{"gpt-4.1": gpt, "qwen2.5-coder:7b": local} {
		if _, err := SaveModelSettings(dir, model, s); err != nil {
			t.Fatalf("%s: %v", model, err)
		}
	}
	gpt.Set("max_tokens", "") // remove one key
	if _, err := SaveModelSettings(dir, "gpt-4.1", gpt); err != nil {
		t.Fatal(err)
	}

	b, _ := os.ReadFile(path)
	var cfg Config
	if _, err := toml.Decode(string(b), &cfg); err != nil {
		t.Fatalf("invalid TOML:\n%s\n%v", b, err)
	}
	g := cfg.ModelSettings["gpt-4.1"]
	if g.Temperature == nil || *g.Temperature != 1 || g.TopP == nil || *g.TopP != 0.00001 || g.MaxTokens != nil {
		t.Fatalf("gpt-4.1 = %+v\n%s", g, b)
	}
	if l := cfg.ModelSettings["qwen2.5-coder:7b"]; l.Seed == nil || *l.Seed != 7 {
		t.Fatalf("qwen = %+v\n%s", l, b)
	}
	for _, keep := range []string{"# mine", "# global", `qa-kitten = "gpt-5"`} {
		if !strings.Contains(string(b), keep) {
			t.Fatalf("lost %q:\n%s", keep, b)
		}
	}

	// Clearing everything removes the table.
	if _, err := SaveModelSettings(dir, "qwen2.5-coder:7b", ModelSettings{}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "qwen") || strings.Contains(string(b), "\n\n\n") {
		t.Fatalf("empty table left behind:\n%s", b)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
}

// Settings saved by SaveModelSettings load through modenv, as at startup.
func TestLoadReadsModelSettings(t *testing.T) {
	home := isolateConfigEnv(t)
	dir := filepath.Join(home, ".code_puppy")
	var s ModelSettings
	s.Set("temperature", "0.7")
	s.Set("seed", "11")
	if _, err := SaveModelSettings(dir, "claude-haiku-4-5", s); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.ModelSettings["claude-haiku-4-5"]
	if got.Temperature == nil || *got.Temperature != 0.7 || got.Seed == nil || *got.Seed != 11 || got.MaxTokens != nil {
		t.Fatalf("loaded %+v", got)
	}
}
