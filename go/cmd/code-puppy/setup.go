package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/agents"
	"github.com/retail-cortex/code_puppy/pkg/audit"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/i18n"
	"github.com/retail-cortex/code_puppy/pkg/images"
	"github.com/retail-cortex/code_puppy/pkg/memory"
	"github.com/retail-cortex/code_puppy/pkg/observability"
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
	locales  *i18n.Bundle
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
	e := &env{cfg: cfg, locales: setupLocale(cfg, o.warn)}
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
	if st := e.tools.Images(); st != nil && cfg.Images.RetainDays > 0 {
		if _, err := st.Prune(time.Duration(cfg.Images.RetainDays) * 24 * time.Hour); err != nil {
			o.warn("image cleanup: " + err.Error())
		}
	}

	if cfg.Audit.Enabled {
		if e.audit, err = audit.Open(config.ExpandHome(cfg.Audit.Dir), secretRedactor(cfg)); err != nil {
			o.warn("audit log disabled: " + err.Error())
		}
		e.tools.SetAudit(e.audit)
	}

	if e.storage, err = session.NewStorage(cfg.Session.StorageDir); err != nil {
		e.Close()
		return nil, fmt.Errorf("failed to initialize session storage: %w", err)
	}
	e.storage.SetWorkspace(e.tools.Workspace().Dir())
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
	opts := []runtime.Option{runtime.WithSessionService(events)}
	for agent, ref := range agentModelRefs(cfg, e.agents, o.warn) {
		m, err := runtime.NewModel(ctx, cfg, ref)
		if err != nil {
			o.warn(i18n.T("pin.load_failed", "agent", agent, "model", ref, "error", modelErrorSummary(err, cfg)))
			continue
		}
		opts = append(opts, runtime.WithAgentModel(agent, m))
	}
	e.engine, err = runtime.NewEngine(ctx, cfg, e.agents, e.skills, e.tools, llm, append(opts,
		runtime.WithInstructions(e.instructions()),
		runtime.WithStreaming(o.streaming),
		runtime.WithTurnStore(e.storage),
		runtime.WithNotice(o.warn),
	)...)
	if err != nil {
		e.Close()
		return nil, fmt.Errorf("failed to initialize engine: %w", err)
	}
	return e, nil
}

// secretRedactor masks configured credentials and the values of scrubbed
// environment variables in audit entries, logs and telemetry.
func secretRedactor(cfg *config.Config) *redact.Redactor {
	secrets := []string{cfg.LLM.Gemini.APIKey, cfg.LLM.OpenAI.APIKey, cfg.LLM.Anthropic.APIKey, cfg.Web.SearchAPIKey}
	for _, s := range cfg.MCP.Servers {
		for _, v := range s.Env {
			secrets = append(secrets, v)
		}
	}
	return redact.FromEnv(cfg.Sandbox.ScrubEnv, secrets...)
}

// startObservability opens the diagnostic log and, when enabled, OpenTelemetry
// export, and makes the log the slog default. The returned func flushes both.
// Failures only disable the affected part: diagnostics must never stop a session.
func startObservability(ctx context.Context, cfg *config.Config, warn func(string)) func() {
	r := secretRedactor(cfg)
	tel, err := observability.StartTelemetry(ctx, cfg.Telemetry, version, r)
	if err != nil {
		warn("telemetry disabled: " + err.Error())
	}
	logger, logFile, err := observability.OpenLog(cfg.Log, r, tel.LogHandler())
	if err != nil {
		warn("diagnostic log disabled: " + err.Error())
		logger, logFile, _ = observability.OpenLog(config.LogConfig{Level: "off"}, r, tel.LogHandler())
	}
	slog.SetDefault(logger)
	slog.Info("start", "version", version, "provider", cfg.LLM.Provider, "model", cfg.ModelName(), "telemetry", tel != nil)
	return func() {
		if err := tel.Shutdown(context.WithoutCancel(ctx)); err != nil {
			slog.Debug("telemetry shutdown", "error", err)
		}
		logFile.Close()
	}
}

// agentModelRefs returns the model each agent should run on when it isn't
// the configured one: a pin in [agent_models] wins over the agent's own
// default_model. Pins naming unknown agents are reported and skipped.
func agentModelRefs(cfg *config.Config, reg *agents.Registry, warn func(string)) map[string]string {
	refs := map[string]string{}
	for _, spec := range reg.List() {
		if spec.DefaultModel != "" {
			refs[spec.Name] = spec.DefaultModel
		}
	}
	for agent, ref := range cfg.AgentModels {
		if _, ok := reg.Get(agent); !ok {
			warn(i18n.T("pin.unknown_agent", "agent", agent))
			continue
		}
		refs[agent] = ref
	}
	return refs
}

// saveAgentModel records (or with ref "" removes) a pin in the config file.
func (e *env) saveAgentModel(agent, ref string) (string, error) {
	if ref == "" {
		delete(e.cfg.AgentModels, agent)
	} else {
		if e.cfg.AgentModels == nil {
			e.cfg.AgentModels = map[string]string{}
		}
		e.cfg.AgentModels[agent] = ref
	}
	return config.SaveAgentModel(config.ConfigDir(""), agent, ref)
}

// saveModelSettings records a model's settings (zero: removed) in the config file.
func (e *env) saveModelSettings(model string, s config.ModelSettings) (string, error) {
	return config.SaveModelSettings(config.ConfigDir(""), model, s)
}

// reloadMemory re-reads instruction files into the engine.
func (e *env) reloadMemory(ctx context.Context) ([]string, error) {
	e.memory = memory.Load(e.tools.Workspace().Dir(), e.cfg.Memory)
	if err := e.engine.SetInstructions(ctx, e.instructions()); err != nil {
		return nil, err
	}
	paths := make([]string, len(e.memory))
	for i, d := range e.memory {
		paths[i] = d.Path
	}
	return paths, nil
}

// loadAttachments loads --image files (failures are errors) and @image
// mentions in prompt (failures are warnings: the prompt may be piped text
// that merely contains an @path) through the workspace sandbox.
func loadAttachments(e *env, paths []string, prompt string, warn func(string)) ([]*images.Image, error) {
	var out []*images.Image
	seen := map[string]bool{}
	add := func(img *images.Image) {
		if !seen[img.SHA256] {
			seen[img.SHA256] = true
			out = append(out, img)
		}
	}
	for _, p := range paths {
		img, err := e.tools.LoadImage(strings.TrimPrefix(p, "@"))
		if err != nil {
			return nil, fmt.Errorf("--image %s: %w", p, err)
		}
		add(img)
	}
	for _, p := range images.Mentions(prompt) {
		img, err := e.tools.LoadImage(p)
		if err != nil {
			warn(i18n.T("attach.failed", "path", p, "error", err.Error()))
			continue
		}
		add(img)
	}
	return out, nil
}

// instructions are the extra system instructions: project memory plus, for
// non-English locales, which language to reply in.
func (e *env) instructions() string {
	return memory.Render(e.memory) + i18n.ReplyInstruction(i18n.Current())
}

// setupLocale loads translation catalogs (built in, then ui.locales_dir) and
// activates ui.locale.
func setupLocale(cfg *config.Config, warn func(string)) *i18n.Bundle {
	b, errs := i18n.NewBundle(config.ExpandHome(cfg.UI.LocalesDir))
	for _, err := range errs {
		warn(err.Error())
	}
	locale := cfg.UI.Locale
	if locale == "" {
		locale = i18n.DefaultLocale
	}
	tag, err := b.Resolve(locale)
	if err != nil {
		warn(fmt.Sprintf("ui.locale %q is not a language code; using %s", cfg.UI.Locale, i18n.DefaultLocale))
		tag, _ = b.Resolve(i18n.DefaultLocale)
	}
	i18n.SetCurrent(b.Localizer(tag))
	return b
}

// setLocale applies the active locale to the model's instructions and saves
// it as ui.locale in the config file.
func (e *env) setLocale(ctx context.Context, l *i18n.Localizer) (string, error) {
	if err := e.engine.SetInstructions(ctx, e.instructions()); err != nil {
		return "", err
	}
	e.cfg.UI.Locale = l.Tag().String()
	return config.SaveUILocale(config.ConfigDir(""), e.cfg.UI.Locale)
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
// recent one in this workspace for --continue/--resume without an ID, or a
// new session.
func selectSession(st *session.Storage, resume string, cont bool, title, agent string) (*session.SessionRecord, bool, error) {
	if resume == "" && !cont {
		rec, err := st.CreateSession("", title, agent)
		return rec, false, err
	}
	if resume == "" || resume == "latest" {
		list, err := st.ListWorkspace(st.Workspace())
		if err != nil {
			return nil, false, err
		}
		if len(list) == 0 {
			return nil, false, withCode(exitUsage, fmt.Errorf("no saved sessions for %s (use --resume <id> for a session from another directory)", st.Workspace()))
		}
		resume = list[0].ID
	}
	rec, err := st.Load(resume)
	if err != nil {
		return nil, false, withCode(exitUsage, err)
	}
	return rec, true, nil
}
