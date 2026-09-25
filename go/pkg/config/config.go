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
	Sandbox   SandboxConfig   `toml:"sandbox"`

	UI          UIConfig              `toml:"ui"`
	Memory      MemoryConfig          `toml:"memory"`
	Context     ContextConfig         `toml:"context"`
	Audit       AuditConfig           `toml:"audit"`
	Checkpoints CheckpointConfig      `toml:"checkpoints"`
	Images      ImagesConfig          `toml:"images"`
	Hooks       HooksConfig           `toml:"hooks"`
	MCP         MCPConfig             `toml:"mcp"`
	Web         WebConfig             `toml:"web"`
	Log         LogConfig             `toml:"log"`
	Telemetry   TelemetryConfig       `toml:"telemetry"`
	Pricing     map[string]ModelPrice `toml:"pricing"`
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
	// TrustWorkspace allows agents and skills found inside the current
	// workspace (./agents, ./skills, .agents/skills) to be loaded. Workspace
	// content can inject prompts, so it is off by default and can only be
	// enabled from trusted config or the --trust-workspace flag.
	TrustWorkspace bool `toml:"trust_workspace"`
}

// LLMConfig holds provider configurations for LLM backends.
type LLMConfig struct {
	Provider string `toml:"provider"` // gemini, openai, anthropic, ollama
	// MaxRetries is how often a failed model request is retried (rate
	// limits, overload, 5xx, connection errors), with exponential backoff
	// that honours Retry-After. 0 disables retries.
	MaxRetries int `toml:"max_retries"`
	// StallTimeoutSeconds fails a model request that sends nothing (no
	// headers, or no streamed data) for this long, so a hung connection
	// can't block a turn forever. It must exceed the longest non-streamed
	// generation.
	StallTimeoutSeconds int `toml:"stall_timeout_seconds"`

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
	// APIKey is optional: without it the SDK also accepts ANTHROPIC_AUTH_TOKEN
	// and `ant auth login` profiles.
	APIKey  string `toml:"api_key"`
	Model   string `toml:"model"`
	BaseURL string `toml:"base_url"` // for gateways/proxies; empty uses the API default
	// Fallbacks controls server-side refusal fallback on models that support
	// it (claude-opus-5, claude-fable-5*): "default" routes by refusal
	// category, a model ID pins one fallback model, "off" disables it.
	Fallbacks string `toml:"fallbacks"`
}

// ModelName returns the model to use: code_puppy.default_model when set,
// otherwise the active provider's model.
func (c *Config) ModelName() string {
	if c.CodePuppy.DefaultModel != "" {
		return c.CodePuppy.DefaultModel
	}
	switch strings.ToLower(c.LLM.Provider) {
	case "openai", "ollama":
		return c.LLM.OpenAI.Model
	case "anthropic":
		return c.LLM.Anthropic.Model
	default:
		return c.LLM.Gemini.Model
	}
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
	UCToolsDir          string `toml:"uc_tools_dir"`
	// ApprovalsFile stores "always allow" decisions.
	ApprovalsFile string `toml:"approvals_file"`
	// MaxParallel caps how many tool calls from one model response run at
	// once (0 = unlimited). Nested batches (sub-agents) get their own cap.
	MaxParallel int `toml:"max_parallel"`
}

// SandboxConfig bounds what tools may touch.
//
// File tools are confined to the workspace plus AllowedPaths (read-write) and
// ReadOnlyPaths, minus BlockedPaths. Shell commands are checked against the
// command policy and, when Shell is "auto" or "required", run under the OS
// sandbox (macOS Seatbelt): writes are limited to the writable roots,
// ShellWritablePaths and temp/cache dirs, blocked paths can't be read, and
// network access follows AllowNetwork.
type SandboxConfig struct {
	AllowedPaths       []string       `toml:"allowed_paths"`
	ReadOnlyPaths      []string       `toml:"read_only_paths"`
	BlockedPaths       []string       `toml:"blocked_paths"`
	ShellWritablePaths []string       `toml:"shell_writable_paths"`
	Shell              string         `toml:"shell"` // auto | required | off
	AllowNetwork       bool           `toml:"allow_network"`
	ScrubEnv           []string       `toml:"scrub_env"`
	Commands           CommandsConfig `toml:"commands"`
}

// CommandsConfig holds shell command patterns. Patterns match a whole simple
// command ("git status"); "*" matches anything and a trailing " *" also
// matches the bare command. Deny wins over Allow; if Allow is non-empty only
// matching commands may run; AutoApprove skips the approval prompt.
type CommandsConfig struct {
	Allow       []string `toml:"allow"`
	Deny        []string `toml:"deny"`
	AutoApprove []string `toml:"auto_approve"`
}

// DefaultBlockedPaths are secrets that tools never read or write.
var DefaultBlockedPaths = []string{
	".env", ".env.local", ".env.*.local", ".env.toml", ".env.*.toml",
	"*.pem", "*.key", "*.p12", "id_rsa*", "id_ecdsa*", "id_ed25519*",
	"~/.ssh", "~/.aws", "~/.gnupg", "~/.config/gcloud", "~/.azure", "~/.kube",
	"~/.docker/config.json", "~/.netrc", "~/.code_puppy/puppy.cfg",
}

// DefaultScrubEnv are environment variables withheld from commands the model
// runs, so they can't read Code Puppy's own credentials.
var DefaultScrubEnv = []string{
	"*_API_KEY", "*_API_TOKEN", "*_SECRET", "*_SECRET_KEY", "*_ACCESS_KEY",
	"AWS_SESSION_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "MODENV_*",
}

// DefaultDeniedCommands are refused regardless of approval.
var DefaultDeniedCommands = []string{
	"sudo *", "su *", "doas *",
	"shutdown *", "reboot *", "halt *", "mkfs *", "mkfs.*", "diskutil erase*",
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

	cfg := &Config{
		CodePuppy: CodePuppyConfig{
			PuppyName:    "Code Puppy",
			OwnerName:    "Developer",
			DefaultAgent: "code-puppy",
			DefaultModel: "", // empty: use llm.<provider>.model
			AgencyLevel:  string(AgencyHigh),
			Temperature:  0.2,
			MaxTokens:    8192,
			AutoApprove:  false,
		},
		LLM: LLMConfig{
			Provider:            "gemini",
			MaxRetries:          3,
			StallTimeoutSeconds: 600,
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
				APIKey:    os.Getenv("ANTHROPIC_API_KEY"),
				Model:     "claude-opus-5",
				Fallbacks: "default",
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
			MaxParallel:         8,
		},
		Session: SessionConfig{
			StorageDir: filepath.Join(homeDir, ".code_puppy", "sessions"),
			AutoSave:   true,
		},
		Sandbox: SandboxConfig{
			BlockedPaths:       append([]string(nil), DefaultBlockedPaths...),
			ShellWritablePaths: []string{"~/.cache", "~/go/pkg/mod", "~/.npm"},
			Shell:              "auto",
			AllowNetwork:       true,
			ScrubEnv:           append([]string(nil), DefaultScrubEnv...),
			Commands: CommandsConfig{
				Deny: append([]string(nil), DefaultDeniedCommands...),
			},
		},
	}
	applyFeatureDefaults(cfg)
	return cfg
}

// Load loads configuration using modenv from a trusted directory, falling back
// to DefaultConfig with environment variable overrides.
//
// The config directory is, in order: prefixDir (the --config flag), the
// MODENV_PREFIX environment variable, then ~/.code_puppy. The current working
// directory is deliberately NOT consulted: a cloned repository could otherwise
// ship a .env.toml that redirects llm.openai.base_url to an attacker's server
// and receive the user's API key, or enable auto-approval. Pass --config .
// to opt in to a workspace config explicitly.
func Load(prefixDir string) (*Config, error) {
	cfg := DefaultConfig()

	dir := ConfigDir(prefixDir)
	if dir == "" {
		applyEnvOverrides(cfg)
		return cfg, nil
	}
	if err := os.Setenv("MODENV_PREFIX", dir); err != nil {
		return nil, fmt.Errorf("failed to set MODENV_PREFIX: %w", err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".env.toml")); err == nil {
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

// ConfigDir returns the trusted directory to load .env.toml from, or "" if none.
func ConfigDir(prefixDir string) string {
	if prefixDir != "" {
		return ExpandHome(prefixDir)
	}
	if pfx := os.Getenv("MODENV_PREFIX"); pfx != "" {
		return ExpandHome(pfx)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".code_puppy")
	}
	return ""
}

// ExpandHome expands a leading "~" or "~/" to the user's home directory.
// Other forms (e.g. "~user") are returned unchanged.
func ExpandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, "~"+string(filepath.Separator)) {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}

// IsWorkspaceRelative reports whether a configured search path points into the
// current workspace (a relative path) rather than a user-level location.
func IsWorkspaceRelative(p string) bool {
	return !filepath.IsAbs(ExpandHome(p))
}

// SkillSearchPaths returns skill directories to scan, expanded, with
// workspace-relative entries removed unless the workspace is trusted.
func (c *Config) SkillSearchPaths() []string {
	return filterTrusted(c.Skills.Paths, c.CodePuppy.TrustWorkspace)
}

// AgentSearchPaths returns directories to scan for user-defined agents.
func (c *Config) AgentSearchPaths() []string {
	return filterTrusted([]string{"~/.code_puppy/agents", "./agents"}, c.CodePuppy.TrustWorkspace)
}

func filterTrusted(paths []string, trustWorkspace bool) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if IsWorkspaceRelative(p) && !trustWorkspace {
			continue
		}
		out = append(out, ExpandHome(p))
	}
	return out
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
	if level := os.Getenv("CODE_PUPPY_LOG_LEVEL"); level != "" {
		cfg.Log.Level = strings.ToLower(level)
	}
	switch strings.ToLower(os.Getenv("CODE_PUPPY_TELEMETRY")) {
	case "1", "true", "yes", "on":
		cfg.Telemetry.Enabled = true
	case "0", "false", "no", "off":
		cfg.Telemetry.Enabled = false
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
