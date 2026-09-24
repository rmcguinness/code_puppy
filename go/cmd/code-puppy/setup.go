package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/retail-cortex/code_puppy/pkg/agents"
	"github.com/retail-cortex/code_puppy/pkg/audit"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/memory"
	"github.com/retail-cortex/code_puppy/pkg/redact"
	"github.com/retail-cortex/code_puppy/pkg/runtime"
	"github.com/retail-cortex/code_puppy/pkg/session"
	"github.com/retail-cortex/code_puppy/pkg/skills"
	"github.com/retail-cortex/code_puppy/pkg/tools"
	"google.golang.org/adk/v2/model"
)

// globalFlags are shared by the root command and subcommands.
type globalFlags struct {
	config, dir, model, agent, agency string
	trustWorkspace                    bool
}

// loadConfig applies --dir (changing the working directory), loads trusted
// configuration and applies flag overrides.
func loadConfig(f *globalFlags) (*config.Config, error) {
	if f.dir != "" {
		if f.config != "" {
			abs, err := filepath.Abs(config.ExpandHome(f.config))
			if err != nil {
				return nil, withCode(exitUsage, fmt.Errorf("invalid --config: %w", err))
			}
			f.config = abs
		}
		if err := os.Chdir(config.ExpandHome(f.dir)); err != nil {
			return nil, withCode(exitUsage, fmt.Errorf("cannot use --dir: %w", err))
		}
	}
	cfg, err := config.Load(f.config)
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}
	if f.model != "" {
		cfg.CodePuppy.DefaultModel = f.model
	}
	if f.agent != "" {
		cfg.CodePuppy.DefaultAgent = f.agent
	}
	if f.agency != "" {
		cfg.CodePuppy.AgencyLevel = strings.ToLower(f.agency)
	}
	if f.trustWorkspace {
		cfg.CodePuppy.TrustWorkspace = true
	}
	if f.dir != "" {
		cfg.Tools.WorkspaceDir = "."
	}
	return cfg, nil
}

// env is everything a session needs.
type env struct {
	cfg      *config.Config
	agents   *agents.Registry
	skills   *skills.Provider
	tools    *tools.Registry
	engine   *runtime.Engine
	storage  *session.Storage
	audit    *audit.Logger
	memory   []memory.Doc
	modelErr error // set when the configured model failed to initialise
}

type envOptions struct {
	streaming bool
	warn      func(string)
}

// buildEnv wires registries, tools, the model and the engine.
func buildEnv(ctx context.Context, cfg *config.Config, o envOptions) (*env, error) {
	if o.warn == nil {
		o.warn = func(string) {}
	}
	e := &env{cfg: cfg}
	var err error

	if e.agents, err = agents.NewRegistry(); err != nil {
		return nil, fmt.Errorf("failed to load agent registry: %w", err)
	}
	if err := e.agents.LoadExternalAgents(cfg.AgentSearchPaths()...); err != nil {
		o.warn(err.Error())
	}
	if e.skills, err = skills.NewProvider(); err != nil {
		return nil, fmt.Errorf("failed to load skills provider: %w", err)
	}
	if cfg.Skills.Enabled {
		if err := e.skills.DiscoverExternal(cfg.SkillSearchPaths()); err != nil {
			o.warn(err.Error())
		}
	}

	if e.tools, err = tools.NewRegistry(cfg, e.agents, e.skills); err != nil {
		return nil, fmt.Errorf("failed to initialize tools: %w", err)
	}
	e.tools.SetWarn(o.warn)

	if cfg.Audit.Enabled {
		secrets := []string{cfg.LLM.Gemini.APIKey, cfg.LLM.OpenAI.APIKey, cfg.LLM.Anthropic.APIKey}
		for _, s := range cfg.MCP.Servers {
			for _, v := range s.Env {
				secrets = append(secrets, v)
			}
		}
		if e.audit, err = audit.Open(config.ExpandHome(cfg.Audit.Dir), redact.FromEnv(cfg.Sandbox.ScrubEnv, secrets...)); err != nil {
			o.warn("audit log disabled: " + err.Error())
		}
		e.tools.SetAudit(e.audit)
	}

	if e.storage, err = session.NewStorage(cfg.Session.StorageDir); err != nil {
		e.Close()
		return nil, fmt.Errorf("failed to initialize session storage: %w", err)
	}
	events, err := session.NewPersistentService(config.ExpandHome(cfg.Session.StorageDir))
	if err != nil {
		e.Close()
		return nil, err
	}

	llm, err := runtime.NewModel(ctx, cfg, "")
	if err != nil {
		e.modelErr = err
	}
	if llm == nil {
		llm = runtime.NewMockLLM("unconfigured-model")
	}

	e.memory = memory.Load(e.tools.Workspace().Dir(), cfg.Memory)
	e.engine, err = runtime.NewEngine(ctx, cfg, e.agents, e.skills, e.tools, llm,
		runtime.WithSessionService(events),
		runtime.WithInstructions(memory.Render(e.memory)),
		runtime.WithStreaming(o.streaming),
	)
	if err != nil {
		e.Close()
		return nil, fmt.Errorf("failed to initialize engine: %w", err)
	}
	return e, nil
}

// reloadMemory re-reads instruction files into the engine.
func (e *env) reloadMemory(ctx context.Context) ([]string, error) {
	e.memory = memory.Load(e.tools.Workspace().Dir(), e.cfg.Memory)
	if err := e.engine.SetInstructions(ctx, memory.Render(e.memory)); err != nil {
		return nil, err
	}
	paths := make([]string, len(e.memory))
	for i, d := range e.memory {
		paths[i] = d.Path
	}
	return paths, nil
}

func (e *env) newModel(ctx context.Context, cfg *config.Config, name string) (model.LLM, error) {
	return runtime.NewModel(ctx, cfg, name)
}

// Close kills background processes and MCP servers and flushes the audit log.
func (e *env) Close() error {
	var err error
	if e.tools != nil {
		err = e.tools.Close()
	}
	e.audit.Close()
	return err
}

// modelErrorSummary makes a model initialisation error safe and readable:
// some SDK errors embed their whole client config, including the API key.
func modelErrorSummary(err error, cfg *config.Config) string {
	msg := err.Error()
	if i := strings.Index(msg, "ClientConfig:"); i >= 0 {
		msg = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(msg[:i]), "."))
	}
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	r := redact.FromEnv(cfg.Sandbox.ScrubEnv, cfg.LLM.Gemini.APIKey, cfg.LLM.OpenAI.APIKey, cfg.LLM.Anthropic.APIKey)
	return r.String(msg)
}

// selectSession picks the session to use: an explicit --resume ID, the most
// recent one for --continue/--resume without an ID, or a new session.
func selectSession(st *session.Storage, resume string, cont bool, title, agent string) (*session.SessionRecord, bool, error) {
	if resume == "" && !cont {
		rec, err := st.CreateSession("", title, agent)
		return rec, false, err
	}
	if resume == "" || resume == "latest" {
		list, err := st.List()
		if err != nil {
			return nil, false, err
		}
		if len(list) == 0 {
			return nil, false, withCode(exitUsage, fmt.Errorf("no saved sessions to resume"))
		}
		resume = list[0].ID
	}
	rec, err := st.Load(resume)
	if err != nil {
		return nil, false, withCode(exitUsage, err)
	}
	return rec, true, nil
}
