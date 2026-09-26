package config

import (
	"os"
	"path/filepath"
)

// UIConfig controls terminal presentation.
type UIConfig struct {
	Markdown bool `toml:"markdown"` // render model output as Markdown (TTY only)
	Spinner  bool `toml:"spinner"`  // show progress while waiting (TTY only)
	// TerminalTitle shows the session's name in the terminal window title (TTY only).
	TerminalTitle bool   `toml:"terminal_title"`
	HistoryFile   string `toml:"history_file"` // REPL input history
	HistorySize   int    `toml:"history_size"`
	DiffLines     int    `toml:"diff_lines"`  // max diff lines shown in approval prompts
	Theme         string `toml:"theme"`       // glamour style: auto, dark, light, notty
	Locale        string `toml:"locale"`      // interface language, e.g. en-US, es, fr-CA
	LocalesDir    string `toml:"locales_dir"` // extra translation catalogs (*.json)
}

// ImagesConfig controls pictures sent to the model: @file.png mentions,
// /attach, /paste, --image and the view_image tool.
type ImagesConfig struct {
	Enabled      bool   `toml:"enabled"`
	Dir          string `toml:"dir"`           // where prepared images are kept (owner-only)
	MaxDimension int    `toml:"max_dimension"` // longest edge sent to the model, in pixels
	MaxInputMB   int    `toml:"max_input_mb"`  // largest file accepted
	RetainDays   int    `toml:"retain_days"`   // unused images are deleted after this (0 = keep)
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
	// CacheWritePerMTok prices tokens written to the prompt cache (Anthropic
	// bills these above the input rate); 0 means the input rate.
	CacheWritePerMTok float64 `toml:"cache_write_per_mtok"`
}

// AuditConfig controls the append-only audit log.
type AuditConfig struct {
	Enabled bool   `toml:"enabled"`
	Dir     string `toml:"dir"`
}

// LogConfig controls the diagnostic log (<dir>/code-puppy-YYYY-MM-DD.jsonl).
// Secrets are masked before writing, as in the audit log.
type LogConfig struct {
	Level      string `toml:"level"`       // debug, info, warn, error, or off
	Dir        string `toml:"dir"`         // owner-only
	RetainDays int    `toml:"retain_days"` // older log files are deleted (0 = keep)
}

// TelemetryConfig controls OpenTelemetry trace and log export over OTLP/HTTP.
// It is off by default; CODE_PUPPY_TELEMETRY=1 also turns it on. Standard
// OTEL_EXPORTER_OTLP_* variables (headers, timeouts) apply.
type TelemetryConfig struct {
	Enabled bool `toml:"enabled"`
	// Endpoint is the collector's base URL, e.g. http://localhost:4318.
	// Empty uses OTEL_EXPORTER_OTLP_ENDPOINT, then the OTLP default.
	Endpoint string `toml:"endpoint"`
	// CaptureContent exports prompts, model replies, tool arguments and tool
	// results (secrets masked). Off, spans carry only names, timings, token
	// counts and outcomes.
	CaptureContent bool `toml:"capture_content"`
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
	// Prefix namespaces the server's tools: prefix "gh" exposes create_issue
	// as gh__create_issue, avoiding collisions with other servers and built-ins.
	Prefix string `toml:"prefix"`
	// Agents lists the agents offered this server's tools: empty means the
	// active primary agent only, "*" means every agent (including ones run
	// through invoke_agent).
	Agents []string `toml:"agents"`
	// TimeoutSeconds bounds one tool call (default 300). A server that fails
	// or times out twice in a row is paused, with growing back-off.
	TimeoutSeconds int `toml:"timeout_seconds"`
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
	// SearchProvider enables web_search and /search web: "brave",
	// "tavily", "searxng" (self-hosted, needs SearchURL), or "google"
	// (Gemini's grounding with Google Search; uses the Gemini API key).
	// Empty disables it.
	SearchProvider string `toml:"search_provider"`
	SearchAPIKey   string `toml:"search_api_key"` // or BRAVE_API_KEY / TAVILY_API_KEY / GEMINI_API_KEY
	SearchURL      string `toml:"search_url"`
	// SearchModel is the Gemini model that runs "google" searches
	// (default: llm.gemini.model, else gemini-3.8-flash).
	SearchModel      string `toml:"search_model"`
	SearchMaxResults int    `toml:"search_max_results"`
}

// DefaultPricing holds estimated list prices for the default models. They
// change over time; override them under [pricing."model-name"].
var DefaultPricing = map[string]ModelPrice{
	// Gemini 3.8 Flash at its introductory price, valid through 2026-12-31;
	// from 2027-01-01 it is 1.50 / 7.50 / 0.15. Update this entry then.
	"gemini-3.8-flash": {InputPerMTok: 0.75, OutputPerMTok: 3.75, CachedInputPerMTok: 0.075},
	"gemini-2.5-pro":   {InputPerMTok: 1.25, OutputPerMTok: 10.00, CachedInputPerMTok: 0.31},
	"gpt-4o":           {InputPerMTok: 2.50, OutputPerMTok: 10.00, CachedInputPerMTok: 1.25},
	// Claude list prices; cache reads bill at 10% of input, 5-minute cache
	// writes at 125%.
	"claude-opus-5":    {InputPerMTok: 5.00, OutputPerMTok: 25.00, CachedInputPerMTok: 0.50, CacheWritePerMTok: 6.25},
	"claude-sonnet-5":  {InputPerMTok: 2.00, OutputPerMTok: 10.00, CachedInputPerMTok: 0.20, CacheWritePerMTok: 2.50},
	"claude-haiku-4-5": {InputPerMTok: 1.00, OutputPerMTok: 5.00, CachedInputPerMTok: 0.10, CacheWritePerMTok: 1.25},
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
		Markdown:      true,
		Spinner:       true,
		TerminalTitle: true,
		HistoryFile:   filepath.Join(dir, "history"),
		HistorySize:   1000,
		DiffLines:     120,
		Theme:         "auto",
		Locale:        "en-US",
		LocalesDir:    filepath.Join(dir, "locales"),
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
	c.Log = LogConfig{Level: "info", Dir: filepath.Join(dir, "logs"), RetainDays: 14}
	c.Checkpoints = CheckpointConfig{Enabled: true, MaxBytes: 64 * 1024 * 1024}
	c.Images = ImagesConfig{Enabled: true, Dir: filepath.Join(dir, "images"), MaxDimension: 1568, MaxInputMB: 20, RetainDays: 30}
	c.Web = WebConfig{Enabled: true, MaxBytes: 2 * 1024 * 1024, TimeoutSeconds: 20}
	c.Pricing = make(map[string]ModelPrice, len(DefaultPricing))
	for k, v := range DefaultPricing {
		c.Pricing[k] = v
	}
}
