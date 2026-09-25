package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestSaveAgentModelPinsAndUnpins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env.toml")
	orig := "# my settings\n[llm]\nprovider = \"gemini\"  # keep me\n"
	os.WriteFile(path, []byte(orig), 0o600)

	steps := []struct{ agent, ref string }{
		{"qa-kitten", "anthropic/claude-haiku-4-5"},
		{"helios", "openai/gpt-5"},
		{"qa-kitten", "gemini-3.8-flash"}, // replace
		{"my agent", "ollama/qwen2.5-coder:7b"},
		{"helios", ""}, // unpin
	}
	for _, s := range steps {
		if _, err := SaveAgentModel(dir, s.agent, s.ref); err != nil {
			t.Fatalf("%v: %v", s, err)
		}
	}
	b, _ := os.ReadFile(path)
	var got struct {
		LLM         map[string]any    `toml:"llm"`
		AgentModels map[string]string `toml:"agent_models"`
	}
	if _, err := toml.Decode(string(b), &got); err != nil {
		t.Fatalf("invalid TOML:\n%s\n%v", b, err)
	}
	want := map[string]string{"qa-kitten": "gemini-3.8-flash", "my agent": "ollama/qwen2.5-coder:7b"}
	if len(got.AgentModels) != len(want) || got.AgentModels["qa-kitten"] != want["qa-kitten"] || got.AgentModels["my agent"] != want["my agent"] {
		t.Fatalf("agent_models = %v\n%s", got.AgentModels, b)
	}
	if !strings.Contains(string(b), "# my settings") || !strings.Contains(string(b), "# keep me") || got.LLM["provider"] != "gemini" {
		t.Fatalf("other settings or comments lost:\n%s", b)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	if _, err := SaveAgentModel(dir, "nobody", ""); err != nil {
		t.Fatalf("unpinning an unpinned agent: %v", err)
	}
}
