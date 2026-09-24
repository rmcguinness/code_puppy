package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rrmcguinness/modenv/pkg/modenv"
)

// AgencyLevel determines how autonomously the agent operates.
type AgencyLevel string

const (
	AgencyLow     AgencyLevel = "low"     // Stop after each meaningful unit of work and ask
	AgencyMedium  AgencyLevel = "medium"  // Complete routine tasks; pause at major milestones
	AgencyHigh    AgencyLevel = "high"    // Complete tasks autonomously; ask only when blocked
	AgencyExtreme AgencyLevel = "extreme" // Maximally autonomous, polling background tasks
)

// Config represents the complete Code Puppy configuration.
type Config struct {
	CodePuppy CodePuppyConfig `toml:"code_puppy"`
	LLM       LLMConfig       `toml:"llm"`
	Skills    SkillsConfig    `toml:"skills"`
	Tools     ToolsConfig     `toml:"tools"`
	Session   SessionConfig   `toml:"session"`
}

// CodePuppyConfig controls the core persona and behavior settings.
type CodePuppyConfig struct {
	PuppyName    string  `toml:"puppy_name"`
	OwnerName    string  `toml:"owner_name"`
	DefaultAgent string  `toml:"default_agent"`
	DefaultModel string  `toml:"default_model"`
	AgencyLevel  string  `toml:"agency_level"`
	Temperature  float64 `toml:"temperature"`
	MaxTokens    int     `toml:"max_tokens"`
	AutoApprove  bool    `toml:"auto_approve"`
}

// LLMConfig holds provider configurations for LLM backends.
type LLMConfig struct {
	Provider  string          `toml:"provider"` // gemini, openai, anthropic, ollama
	Gemini    GeminiConfig    `toml:"gemini"`
	OpenAI    OpenAIConfig    `toml:"openai"`
	Anthropic AnthropicConfig `toml:"anthropic"`
}

// GeminiConfig holds settings for Google Gemini / Vertex AI.
type GeminiConfig struct {
	APIKey    string `toml:"api_key"`
	Model     string `toml:"model"`
	ProjectID string `toml:"project_id"`
	Location  string `toml:"location"`
}

// OpenAIConfig holds settings for OpenAI, OpenRouter, or Ollama.
type OpenAIConfig struct {
	APIKey  string `toml:"api_key"`
	BaseURL string `toml:"base_url"`
	Model   string `toml:"model"`
}

// AnthropicConfig holds settings for Anthropic Claude.
type AnthropicConfig struct {
	APIKey string `toml:"api_key"`
	Model  string `toml:"model"`
}

// SkillsConfig controls Agent Skills discovery and paths.
type SkillsConfig struct {
	Enabled bool     `toml:"enabled"`
	Paths   []string `toml:"paths"`
}

// ToolsConfig configures shell execution, file operations, and permissions.
type ToolsConfig struct {
	ShellTimeoutSeconds int    `toml:"shell_timeout_seconds"`
	MaxFileSizeBytes    int64  `toml:"max_file_size_bytes"`
	WorkspaceDir        string `toml:"workspace_dir"`
	AutoApproveCommands bool   `toml:"auto_approve_commands"`
}

// SessionConfig configures persistence and session storage.
type SessionConfig struct {
	StorageDir string `toml:"storage_dir"`
	AutoSave   bool   `toml:"auto_save"`
}

// DefaultConfig returns a fully initialized Config with sensible production defaults.
func DefaultConfig() *Config {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}

	return &Config{
		CodePuppy: CodePuppyConfig{
			PuppyName:    "Code Puppy",
			OwnerName:    "Developer",
			DefaultAgent: "code-puppy",
			DefaultModel: "gemini-2.5-flash",
			AgencyLevel:  string(AgencyHigh),
			Temperature:  0.2,
			MaxTokens:    8192,
			AutoApprove:  false,
		},
		LLM: LLMConfig{
			Provider: "gemini",
			Gemini: GeminiConfig{
				APIKey: os.Getenv("GEMINI_API_KEY"),
				Model:  "gemini-2.5-flash",
			},
			OpenAI: OpenAIConfig{
				APIKey:  os.Getenv("OPENAI_API_KEY"),
				BaseURL: "https://api.openai.com/v1",
				Model:   "gpt-4o",
			},
			Anthropic: AnthropicConfig{
				APIKey: os.Getenv("ANTHROPIC_API_KEY"),
				Model:  "claude-3-7-sonnet-20250219",
			},
		},
		Skills: SkillsConfig{
			Enabled: true,
			Paths: []string{
				filepath.Join(homeDir, ".code_puppy", "skills"),
				"./skills",
				".agents/skills",
			},
		},
		Tools: ToolsConfig{
			ShellTimeoutSeconds: 120,
			MaxFileSizeBytes:    10 * 1024 * 1024, // 10MB
			WorkspaceDir:        ".",
			AutoApproveCommands: false,
		},
		Session: SessionConfig{
			StorageDir: filepath.Join(homeDir, ".code_puppy", "sessions"),
			AutoSave:   true,
		},
	}
}

// Load loads configuration using retail-cortex/modenv if .env.toml exists,
// falling back to DefaultConfig with environment variable overrides.
func Load(prefixDir string) (*Config, error) {
	cfg := DefaultConfig()

	if prefixDir != "" {
		if err := os.Setenv("MODENV_PREFIX", prefixDir); err != nil {
			return nil, fmt.Errorf("failed to set MODENV_PREFIX: %w", err)
		}
	}

	// Check if .env.toml exists in prefixDir or current directory
	checkPath := ".env.toml"
	if pfx := os.Getenv("MODENV_PREFIX"); pfx != "" {
		checkPath = filepath.Join(pfx, ".env.toml")
	}

	if _, err := os.Stat(checkPath); err == nil {
		// Load hierarchical configuration and decrypt secrets via modenv
		_, loadErr := modenv.Load(cfg)
		if loadErr != nil {
			return nil, fmt.Errorf("failed to load configuration via modenv: %w", loadErr)
		}
	}

	// Apply direct environment variable fallbacks if not populated
	applyEnvOverrides(cfg)

	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if p := os.Getenv("LLM_PROVIDER"); p != "" {
		cfg.LLM.Provider = strings.ToLower(p)
	}
	if key := os.Getenv("GEMINI_API_KEY"); key != "" && cfg.LLM.Gemini.APIKey == "" {
		cfg.LLM.Gemini.APIKey = key
	}
	if key := os.Getenv("GOOGLE_API_KEY"); key != "" && cfg.LLM.Gemini.APIKey == "" {
		cfg.LLM.Gemini.APIKey = key
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" && cfg.LLM.OpenAI.APIKey == "" {
		cfg.LLM.OpenAI.APIKey = key
	}
	if b := os.Getenv("OPENAI_BASE_URL"); b != "" {
		cfg.LLM.OpenAI.BaseURL = b
	}
	if m := os.Getenv("OPENAI_MODEL"); m != "" {
		cfg.LLM.OpenAI.Model = m
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" && cfg.LLM.Anthropic.APIKey == "" {
		cfg.LLM.Anthropic.APIKey = key
	}
	if model := os.Getenv("CODE_PUPPY_MODEL"); model != "" {
		cfg.CodePuppy.DefaultModel = model
	}
	if agent := os.Getenv("CODE_PUPPY_AGENT"); agent != "" {
		cfg.CodePuppy.DefaultAgent = agent
	}
	if agency := os.Getenv("CODE_PUPPY_AGENCY"); agency != "" {
		cfg.CodePuppy.AgencyLevel = strings.ToLower(agency)
	}

	// Fallback to reading legacy ~/.code_puppy/puppy.cfg if keys still empty
	if cfg.LLM.Gemini.APIKey == "" || cfg.LLM.OpenAI.APIKey == "" || cfg.LLM.Anthropic.APIKey == "" {
		loadLegacyPuppyCfg(cfg)
	}
}

func loadLegacyPuppyCfg(cfg *Config) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	cfgPath := filepath.Join(home, ".code_puppy", "puppy.cfg")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "[") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k := strings.TrimSpace(strings.ToLower(parts[0]))
		v := strings.TrimSpace(parts[1])
		if v == "" {
			continue
		}

		switch k {
		case "gemini_api_key", "google_api_key", "google_generative_ai_api_key":
			if cfg.LLM.Gemini.APIKey == "" {
				cfg.LLM.Gemini.APIKey = v
			}
		case "openai_api_key":
			if cfg.LLM.OpenAI.APIKey == "" {
				cfg.LLM.OpenAI.APIKey = v
			}
		case "anthropic_api_key":
			if cfg.LLM.Anthropic.APIKey == "" {
				cfg.LLM.Anthropic.APIKey = v
			}
		}
	}
}
