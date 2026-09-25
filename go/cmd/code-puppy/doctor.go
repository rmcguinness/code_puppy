package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/memory"
	"github.com/retail-cortex/code_puppy/pkg/observability"
	"github.com/retail-cortex/code_puppy/pkg/runtime"
	"github.com/retail-cortex/code_puppy/pkg/tools"
	"github.com/retail-cortex/code_puppy/pkg/tui"
	"github.com/spf13/cobra"
	"google.golang.org/adk/v2/model"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type checkStatus int

const (
	statusOK checkStatus = iota
	statusWarn
	statusFail
)

type check struct {
	name   string
	status checkStatus
	detail string
}

func newDoctorCommand(g *globalFlags) *cobra.Command {
	var online bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check configuration, credentials, sandbox and integrations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			checks := runDoctor(cmd.Context(), g, online)
			failed := printChecks(cmd.OutOrStdout(), checks)
			if failed > 0 {
				return withCode(exitFailure, fmt.Errorf("%d check(s) failed", failed))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&online, "online", false, "Also send a tiny request to the model and connect to MCP servers")
	return cmd
}

func runDoctor(ctx context.Context, g *globalFlags, online bool) []check {
	if ctx == nil {
		ctx = context.Background()
	}
	var checks []check
	add := func(name string, st checkStatus, format string, a ...any) {
		checks = append(checks, check{name, st, fmt.Sprintf(format, a...)})
	}

	cfgDir := config.ConfigDir(g.config)
	cfgFile := filepath.Join(cfgDir, ".env.toml")
	if info, err := os.Stat(cfgFile); err != nil {
		add("config file", statusWarn, "%s not found (defaults in use; run 'code-puppy config init')", cfgFile)
	} else if info.Mode().Perm()&0o077 != 0 {
		add("config file", statusWarn, "%s is readable by others (mode %v); run chmod 600", cfgFile, info.Mode().Perm())
	} else {
		add("config file", statusOK, "%s", cfgFile)
	}

	cfg, err := loadConfig(g)
	if err != nil {
		add("config parse", statusFail, "%v", err)
		return checks
	}
	add("config parse", statusOK, "provider %s, model %s, agent %s", cfg.LLM.Provider, cfg.ModelName(), cfg.CodePuppy.DefaultAgent)

	switch key := apiKeyFor(cfg); {
	case cfg.LLM.Provider == "anthropic" && key == "":
		if os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
			add("credentials", statusOK, "ANTHROPIC_AUTH_TOKEN is set")
		} else {
			add("credentials", statusWarn, "no api_key; relying on an `ant auth login` profile or workload identity (use --online to confirm)")
		}
	case cfg.LLM.Provider == "ollama":
		add("credentials", statusOK, "ollama needs no API key (%s)", cfg.LLM.OpenAI.BaseURL)
	case key == "":
		add("credentials", statusFail, "no API key for provider %q", cfg.LLM.Provider)
	default:
		add("credentials", statusOK, "API key for %s: %s", cfg.LLM.Provider, maskSecret(key))
	}

	if !runtime.NewUsageTracker(cfg.Pricing).HasPrice(cfg.ModelName()) {
		add("pricing", statusWarn, "no price for %q; /cost will show tokens only (add [pricing.%q])", cfg.ModelName(), cfg.ModelName())
	} else {
		add("pricing", statusOK, "estimates use [pricing] for %s", cfg.ModelName())
	}

	switch _, on, err := observability.ParseLevel(cfg.Log.Level); {
	case err != nil:
		add("log", statusWarn, "%v", err)
	case !on:
		add("log", statusOK, "off")
	default:
		add("log", statusOK, "%s level to %s", cfg.Log.Level, config.ExpandHome(cfg.Log.Dir))
	}
	switch endpoint := cfg.Telemetry.Endpoint; {
	case !cfg.Telemetry.Enabled:
		add("telemetry", statusOK, "off")
	case endpoint == "" && os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "":
		add("telemetry", statusWarn, "on, but no endpoint set; exporting to the OTLP default %s", observability.DefaultEndpoint)
	default:
		if endpoint == "" {
			endpoint = "OTEL_EXPORTER_OTLP_* endpoint"
		}
		content := "names, timings and token counts only"
		if cfg.Telemetry.CaptureContent {
			content = "including prompts and tool content (secrets masked)"
		}
		add("telemetry", statusOK, "OTLP/HTTP to %s; %s", endpoint, content)
	}

	llm, err := runtime.NewModel(ctx, cfg, "")
	if err != nil {
		add("model", statusFail, "%s", modelErrorSummary(err, cfg))
	} else {
		add("model", statusOK, "%s initialised", llm.Name())
		if online {
			octx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := pingModel(octx, llm)
			cancel()
			if err != nil {
				add("model request", statusFail, "%s", modelErrorSummary(err, cfg))
			} else {
				add("model request", statusOK, "model responded")
			}
		}
	}

	for _, bin := range []string{"bash", "git"} {
		if p, err := exec.LookPath(bin); err != nil {
			st := statusWarn
			if bin == "bash" {
				st = statusFail
			}
			add(bin, st, "not found on PATH")
		} else {
			add(bin, statusOK, "%s", p)
		}
	}

	reg, err := tools.NewRegistry(cfg, nil, nil)
	if err != nil {
		add("sandbox", statusFail, "%v", err)
	} else {
		defer reg.Close()
		add("workspace", statusOK, "%s", reg.Workspace().Dir())
		st := statusOK
		if !reg.ShellSandbox().Active() {
			st = statusWarn
		}
		add("shell sandbox", st, "%s", reg.ShellSandbox().Status())

		for _, s := range cfg.MCP.Servers {
			switch {
			case s.Command != "":
				if _, err := exec.LookPath(s.Command); err != nil {
					add("mcp "+s.Name, statusFail, "command %q not found", s.Command)
					continue
				}
			}
			if !online {
				add("mcp "+s.Name, statusOK, "configured (use --online to connect)")
				continue
			}
			n, err := countMCPTools(ctx, reg, s.Name)
			if err != nil {
				add("mcp "+s.Name, statusFail, "%v", err)
			} else {
				add("mcp "+s.Name, statusOK, "%d tools", n)
			}
		}
	}

	for _, h := range append(append(cfg.Hooks.PreTool, cfg.Hooks.PostTool...), cfg.Hooks.PromptSubmit...) {
		first := strings.Fields(h.Command)
		if len(first) > 0 && !strings.ContainsAny(first[0], "$;|&") {
			if _, err := exec.LookPath(first[0]); err != nil && !fileExists(first[0]) {
				add("hook", statusWarn, "%q: %s not found", h.Command, first[0])
				continue
			}
		}
		add("hook", statusOK, "%s", h.Command)
	}

	sessDir := config.ExpandHome(cfg.Session.StorageDir)
	if info, err := os.Stat(sessDir); err == nil && info.Mode().Perm()&0o077 != 0 {
		add("sessions", statusWarn, "%s is accessible by others (mode %v)", sessDir, info.Mode().Perm())
	} else {
		add("sessions", statusOK, "%s", sessDir)
	}
	if cfg.Audit.Enabled {
		add("audit log", statusOK, "%s", config.ExpandHome(cfg.Audit.Dir))
	} else {
		add("audit log", statusWarn, "disabled")
	}

	wd, _ := os.Getwd()
	if docs := memory.Load(wd, cfg.Memory); len(docs) > 0 {
		var paths []string
		for _, d := range docs {
			paths = append(paths, d.Path)
		}
		add("project memory", statusOK, "%s", strings.Join(paths, ", "))
	} else {
		add("project memory", statusOK, "none found (%s)", strings.Join(cfg.Memory.Files, ", "))
	}
	return checks
}

func printChecks(w io.Writer, checks []check) (failed int) {
	icons := map[checkStatus]string{statusOK: tui.Green + "✓" + tui.Reset, statusWarn: tui.Yellow + "!" + tui.Reset, statusFail: tui.Red + "✗" + tui.Reset}
	for _, c := range checks {
		fmt.Fprintf(w, " %s %-16s %s\n", icons[c.status], c.name, c.detail)
		if c.status == statusFail {
			failed++
		}
	}
	return failed
}

func apiKeyFor(cfg *config.Config) string {
	switch cfg.LLM.Provider {
	case "openai", "ollama":
		return cfg.LLM.OpenAI.APIKey
	case "anthropic":
		return cfg.LLM.Anthropic.APIKey
	default:
		return cfg.LLM.Gemini.APIKey
	}
}

// maskSecret shows only enough of a secret to recognise it.
func maskSecret(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:3] + "…" + s[len(s)-4:]
}

// pingModel sends a minimal request to verify credentials and connectivity.
func pingModel(ctx context.Context, llm model.LLM) error {
	req := &model.LLMRequest{
		Model:    llm.Name(),
		Contents: []*genai.Content{genai.NewContentFromText("Reply with the single word: ok", genai.RoleUser)},
		Config:   &genai.GenerateContentConfig{MaxOutputTokens: 16},
	}
	for resp, err := range llm.GenerateContent(ctx, req, false) {
		if err != nil {
			return err
		}
		if resp != nil {
			return nil
		}
	}
	return errors.New("no response")
}

// standaloneContext satisfies agent.ReadonlyContext outside an agent run,
// e.g. to list MCP tools from the doctor command.
type standaloneContext struct{ context.Context }

func (standaloneContext) UserContent() *genai.Content             { return nil }
func (standaloneContext) InvocationID() string                    { return "doctor" }
func (standaloneContext) AgentName() string                       { return "doctor" }
func (standaloneContext) ReadonlyState() adksession.ReadonlyState { return nil }
func (standaloneContext) UserID() string                          { return "user" }
func (standaloneContext) AppName() string                         { return "code-puppy" }
func (standaloneContext) SessionID() string                       { return "doctor" }
func (standaloneContext) Branch() string                          { return "" }

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// countMCPTools connects to one server and lists its tools.
func countMCPTools(ctx context.Context, reg *tools.Registry, name string) (int, error) {
	for _, ts := range reg.MCP().Toolsets() {
		if ts.Name() != "mcp:"+name {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		list, err := ts.Tools(standaloneContext{cctx})
		if err != nil {
			return 0, err
		}
		return len(list), nil
	}
	return 0, fmt.Errorf("not found")
}
