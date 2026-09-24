package config

import (
	"os"
	"path/filepath"
)

// UIConfig controls terminal presentation.
type UIConfig struct {
	Markdown    bool   `toml:"markdown"`     // render model output as Markdown (TTY only)
	Spinner     bool   `toml:"spinner"`      // show progress while waiting (TTY only)
	HistoryFile string `toml:"history_file"` // REPL input history
	HistorySize int    `toml:"history_size"`
	DiffLines   int    `toml:"diff_lines"` // max diff lines shown in approval prompts
	Theme       string `toml:"theme"`      // glamour style: auto, dark, light, notty
}

// MemoryConfig controls project instruction files loaded into the prompt.
type MemoryConfig struct {
	Enabled  bool     `toml:"enabled"`
	Files    []string `toml:"files"`     // names searched from the workspace up to the repo root
	Global   string   `toml:"global"`    // user-wide instructions file
	MaxBytes int      `toml:"max_bytes"` // per file
}

// ContextConfig controls conversation compaction.
type ContextConfig struct {
	// Compaction summarises older history once a prompt reaches
	// TokenThreshold tokens, keeping the RetainEvents most recent events raw.
	Compaction     bool `toml:"compaction"`
	TokenThreshold int  `toml:"token_threshold"`
	RetainEvents   int  `toml:"retain_events"`
}

// ModelPrice is the cost per million tokens, in USD.
type ModelPrice struct {
	InputPerMTok       float64 `toml:"input_per_mtok"`
	OutputPerMTok      float64 `toml:"output_per_mtok"`
	CachedInputPerMTok float64 `toml:"cached_input_per_mtok"`
}

// AuditConfig controls the append-only audit log.
type AuditConfig struct {
	Enabled bool   `toml:"enabled"`
	Dir     string `toml:"dir"`
}

// CheckpointConfig controls file snapshots used by /undo.
type CheckpointConfig struct {
	Enabled  bool  `toml:"enabled"`
	MaxBytes int64 `toml:"max_bytes"` // total snapshot memory before the oldest turns are dropped
}

// HookConfig runs a command at a lifecycle point. The command receives a JSON
// event on stdin. Exit code 2 blocks the action (stderr is the reason); any
// other non-zero exit is reported and ignored unless FailClosed is set.
type HookConfig struct {
	Match          string `toml:"match"` // tool-name glob for tool hooks; empty matches all
	Command        string `toml:"command"`
	TimeoutSeconds int    `toml:"timeout_seconds"`
	FailClosed     bool   `toml:"fail_closed"`
}

// HooksConfig lists hooks per event.
type HooksConfig struct {
	PreTool      []HookConfig `toml:"pre_tool"`
	PostTool     []HookConfig `toml:"post_tool"`
	PromptSubmit []HookConfig `toml:"prompt_submit"`
}

// MCPServerConfig describes a Model Context Protocol server. Exactly one of
// Command (stdio) or URL (streamable HTTP) is set.
type MCPServerConfig struct {
	Name        string            `toml:"name"`
	Command     string            `toml:"command"`
	Args        []string          `toml:"args"`
	Env         map[string]string `toml:"env"`
	URL         string            `toml:"url"`
	Tools       []string          `toml:"tools"`        // optional allow-list of tool names
	AutoApprove bool              `toml:"auto_approve"` // skip approval for this server's tools
	Sandbox     *bool             `toml:"sandbox"`      // run stdio servers in the OS sandbox (default true)
}

// MCPConfig lists MCP servers.
type MCPConfig struct {
	Servers []MCPServerConfig `toml:"servers"`
}

// WebConfig controls the web_fetch tool.
type WebConfig struct {
	Enabled        bool     `toml:"enabled"`
	AllowDomains   []string `toml:"allow_domains"` // fetched without approval (globs like *.go.dev)
	DenyDomains    []string `toml:"deny_domains"`
	AllowPrivate   bool     `toml:"allow_private"` // permit localhost/private network targets
	MaxBytes       int64    `toml:"max_bytes"`
	TimeoutSeconds int      `toml:"timeout_seconds"`
}

// DefaultPricing holds estimated list prices for the default models. They
// change over time; override them under [pricing."model-name"].
var DefaultPricing = map[string]ModelPrice{
	"gemini-2.5-flash": {InputPerMTok: 0.30, OutputPerMTok: 2.50, CachedInputPerMTok: 0.075},
	"gemini-2.5-pro":   {InputPerMTok: 1.25, OutputPerMTok: 10.00, CachedInputPerMTok: 0.31},
	"gpt-4o":           {InputPerMTok: 2.50, OutputPerMTok: 10.00, CachedInputPerMTok: 1.25},
}

// Dir returns the Code Puppy home directory (~/.code_puppy).
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".code_puppy"
	}
	return filepath.Join(home, ".code_puppy")
}

func applyFeatureDefaults(c *Config) {
	dir := Dir()
	c.UI = UIConfig{
		Markdown:    true,
		Spinner:     true,
		HistoryFile: filepath.Join(dir, "history"),
		HistorySize: 1000,
		DiffLines:   120,
		Theme:       "auto",
	}
	c.Memory = MemoryConfig{
		Enabled:  true,
		Files:    []string{"AGENTS.md", "PUPPY.md"},
		Global:   filepath.Join(dir, "PUPPY.md"),
		MaxBytes: 32 * 1024,
	}
	c.Context = ContextConfig{Compaction: true, TokenThreshold: 120_000, RetainEvents: 20}
	c.Tools.ApprovalsFile = filepath.Join(dir, "approvals.json")
	c.Audit = AuditConfig{Enabled: true, Dir: filepath.Join(dir, "audit")}
	c.Checkpoints = CheckpointConfig{Enabled: true, MaxBytes: 64 * 1024 * 1024}
	c.Web = WebConfig{Enabled: true, MaxBytes: 2 * 1024 * 1024, TimeoutSeconds: 20}
	c.Pricing = make(map[string]ModelPrice, len(DefaultPricing))
	for k, v := range DefaultPricing {
		c.Pricing[k] = v
	}
}
