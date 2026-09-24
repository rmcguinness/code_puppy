package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.CodePuppy.PuppyName != "Code Puppy" {
		t.Errorf("expected 'Code Puppy', got '%s'", cfg.CodePuppy.PuppyName)
	}
	if cfg.CodePuppy.DefaultAgent != "code-puppy" {
		t.Errorf("expected 'code-puppy', got '%s'", cfg.CodePuppy.DefaultAgent)
	}
	if cfg.CodePuppy.AgencyLevel != string(AgencyHigh) {
		t.Errorf("expected 'high', got '%s'", cfg.CodePuppy.AgencyLevel)
	}
	if !cfg.Skills.Enabled {
		t.Errorf("expected skills enabled by default")
	}
}

func TestModenvLoad(t *testing.T) {
	tmpDir := t.TempDir()
	tomlContent := `
[code_puppy]
puppy_name = "CustomPuppy"
owner_name = "Alice"
default_agent = "helios"
agency_level = "extreme"

[llm]
provider = "openai"

[llm.openai]
api_key = "test-openai-key"
model = "gpt-4o"
`
	err := os.WriteFile(filepath.Join(tmpDir, ".env.toml"), []byte(tomlContent), 0644)
	if err != nil {
		t.Fatalf("failed to write test .env.toml: %v", err)
	}

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	if cfg.CodePuppy.PuppyName != "CustomPuppy" {
		t.Errorf("expected 'CustomPuppy', got '%s'", cfg.CodePuppy.PuppyName)
	}
	if cfg.CodePuppy.OwnerName != "Alice" {
		t.Errorf("expected 'Alice', got '%s'", cfg.CodePuppy.OwnerName)
	}
	if cfg.CodePuppy.DefaultAgent != "helios" {
		t.Errorf("expected 'helios', got '%s'", cfg.CodePuppy.DefaultAgent)
	}
	if cfg.CodePuppy.AgencyLevel != "extreme" {
		t.Errorf("expected 'extreme', got '%s'", cfg.CodePuppy.AgencyLevel)
	}
	if cfg.LLM.OpenAI.APIKey != "test-openai-key" {
		t.Errorf("expected 'test-openai-key', got '%s'", cfg.LLM.OpenAI.APIKey)
	}
}
