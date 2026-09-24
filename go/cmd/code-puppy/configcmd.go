package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/spf13/cobra"
)

func newConfigCommand(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Create, locate, or inspect configuration",
	}
	var force bool
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Write a commented config template to ~/.code_puppy/.env.toml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := filepath.Join(config.ConfigDir(g.config), ".env.toml")
			if err := writeTemplate(path, force); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s (mode 600). Edit it, then run 'code-puppy doctor'.\n", path)
			return nil
		},
	}
	initCmd.Flags().BoolVar(&force, "force", false, "Overwrite an existing file")

	pathCmd := &cobra.Command{
		Use:   "path",
		Short: "Print the config file location",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), filepath.Join(config.ConfigDir(g.config), ".env.toml"))
		},
	}
	showCmd := &cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration (secrets masked)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(g)
			if err != nil {
				return err
			}
			masked := maskConfig(cfg)
			return toml.NewEncoder(cmd.OutOrStdout()).Encode(masked)
		},
	}
	cmd.AddCommand(initCmd, pathCmd, showCmd)
	return cmd
}

// maskConfig returns a copy of cfg with credentials masked.
func maskConfig(cfg *config.Config) config.Config {
	c := *cfg
	c.LLM.Gemini.APIKey = maskSecret(c.LLM.Gemini.APIKey)
	c.LLM.OpenAI.APIKey = maskSecret(c.LLM.OpenAI.APIKey)
	c.LLM.Anthropic.APIKey = maskSecret(c.LLM.Anthropic.APIKey)
	servers := make([]config.MCPServerConfig, len(c.MCP.Servers))
	for i, s := range c.MCP.Servers {
		env := make(map[string]string, len(s.Env))
		for k, v := range s.Env {
			env[k] = maskSecret(v)
		}
		s.Env = env
		servers[i] = s
	}
	c.MCP.Servers = servers
	return c
}

func writeTemplate(path string, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return withCode(exitUsage, fmt.Errorf("%s already exists (use --force to overwrite)", path))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Validate the template parses before writing it.
	var probe map[string]any
	if _, err := toml.Decode(configTemplate, &probe); err != nil {
		return fmt.Errorf("internal error: template does not parse: %w", err)
	}
	return os.WriteFile(path, []byte(strings.TrimLeft(configTemplate, "\n")), 0o600)
}

const configTemplate = `
# Code Puppy configuration. Loaded from ~/.code_puppy/.env.toml (or --config DIR).
# A .env.toml inside a project is ignored unless you pass --config explicitly.

[code_puppy]
puppy_name    = "Code Puppy"
owner_name    = "Developer"
default_agent = "code-puppy"
default_model = "gemini-2.5-flash"
agency_level  = "high"      # low | medium | high | extreme
temperature   = 0.2
max_tokens    = 8192
auto_approve  = false       # true skips ALL approval prompts (deny rules still apply)
trust_workspace = false     # load ./agents and ./skills from the project

[llm]
provider = "gemini"         # gemini | openai | ollama

[llm.gemini]
# api_key = "..."           # or export GEMINI_API_KEY

[llm.openai]
# api_key  = "..."          # or export OPENAI_API_KEY
# base_url = "https://api.openai.com/v1"
# model    = "gpt-4o"

[tools]
shell_timeout_seconds = 120
auto_approve_commands = false

[sandbox]
# allowed_paths   = ["~/src/shared"]     # extra read-write roots for file tools
# read_only_paths = ["~/docs"]           # extra read-only roots
# blocked_paths   = [".env", "*.pem", "~/.ssh"]   # replaces the defaults
# shell_writable_paths = ["~/.npm", "~/.cargo"]   # caches the shell may write
shell         = "auto"      # auto | required | off  (OS sandbox for shell commands)
allow_network = true

[sandbox.commands]
# allow        = ["git *", "go *", "make *"]   # if set, ONLY these may run
# deny         = ["git push *"]                # replaces the default deny list
# auto_approve = ["git status", "go test *"]   # run without prompting

[ui]
markdown  = true
spinner   = true
diff_lines = 120

[memory]
enabled = true
files   = ["AGENTS.md", "PUPPY.md"]

[context]
compaction      = true
token_threshold = 120000
retain_events   = 20

[audit]
enabled = true

[checkpoints]
enabled = true

[web]
enabled = true
# allow_domains = ["*.go.dev", "docs.python.org"]   # fetched without approval
# deny_domains  = ["*.internal.example.com"]

# [pricing."gemini-2.5-flash"]
# input_per_mtok = 0.30
# output_per_mtok = 2.50
# cached_input_per_mtok = 0.075

# [[mcp.servers]]
# name    = "github"
# command = "npx"
# args    = ["-y", "@modelcontextprotocol/server-github"]
# env     = { GITHUB_PERSONAL_ACCESS_TOKEN = "..." }
# auto_approve = false

# [[hooks.pre_tool]]
# match   = "run_shell_command"
# command = "~/.code_puppy/hooks/check-command.sh"   # exit 2 to block; stderr is the reason
`
