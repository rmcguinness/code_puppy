// Package app is Code Puppy without a user interface: it opens a workspace
// (registries, tools, sessions, the model and the engine) and exposes what a
// front end needs. The terminal UI and, later, other front ends drive it;
// none of it prints.
package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/retail-cortex/code_puppy/internal/agents"
	"github.com/retail-cortex/code_puppy/internal/audit"
	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/i18n"
	"github.com/retail-cortex/code_puppy/internal/images"
	"github.com/retail-cortex/code_puppy/internal/memory"
	"github.com/retail-cortex/code_puppy/internal/redact"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"github.com/retail-cortex/code_puppy/internal/session"
	"github.com/retail-cortex/code_puppy/internal/skills"
	"github.com/retail-cortex/code_puppy/internal/tools"
	"google.golang.org/adk/v2/model"
)

// Options configure Open.
type Options struct {
	// Streaming asks the model for partial responses as they are generated.
	Streaming bool
	// Warn receives problems that don't stop the workspace from opening.
	Warn func(string)
	// Model replaces the model built from the configuration (tests use a
	// mock). Agents with their own model still get theirs.
	Model model.LLM
}

// Workspace is one open project: everything a session needs.
type Workspace struct {
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
	warn     func(string)
}

// Open wires registries, tools, the model and the engine for cfg. It also
// activates cfg's interface language, which is per process.
func Open(ctx context.Context, cfg *config.Config, o Options) (*Workspace, error) {
	if o.Warn == nil {
		o.Warn = func(string) {}
	}
	w := &Workspace{cfg: cfg, locales: SetupLocale(cfg, o.Warn), warn: o.Warn}
	var err error

	if w.agents, err = agents.NewRegistry(); err != nil {
		return nil, fmt.Errorf("failed to load agent registry: %w", err)
	}
	if err := w.agents.LoadExternalAgents(cfg.AgentSearchPaths()...); err != nil {
		o.Warn(err.Error())
	}
	if w.skills, err = skills.NewProvider(); err != nil {
		return nil, fmt.Errorf("failed to load skills provider: %w", err)
	}
	if cfg.Skills.Enabled {
		if err := w.skills.DiscoverExternal(cfg.SkillSearchPaths()); err != nil {
			o.Warn(err.Error())
		}
		for _, p := range cfg.Skills.Policy.Problems() {
			o.Warn(p)
		}
	}

	if w.tools, err = tools.NewRegistry(cfg, w.agents, w.skills); err != nil {
		return nil, fmt.Errorf("failed to initialize tools: %w", err)
	}
	w.tools.SetWarn(o.Warn)
	if st := w.tools.Images(); st != nil && cfg.Images.RetainDays > 0 {
		if _, err := st.Prune(time.Duration(cfg.Images.RetainDays) * 24 * time.Hour); err != nil {
			o.Warn("image cleanup: " + err.Error())
		}
	}

	if cfg.Audit.Enabled {
		if w.audit, err = audit.Open(config.ExpandHome(cfg.Audit.Dir), SecretRedactor(cfg)); err != nil {
			o.Warn("audit log disabled: " + err.Error())
		}
		w.tools.SetAudit(w.audit)
	}

	if w.storage, err = session.NewStorage(cfg.Session.StorageDir); err != nil {
		w.Close()
		return nil, fmt.Errorf("failed to initialize session storage: %w", err)
	}
	w.storage.SetWorkspace(w.tools.Workspace().Dir())
	events, err := session.NewPersistentService(config.ExpandHome(cfg.Session.StorageDir))
	if err != nil {
		w.Close()
		return nil, err
	}

	llm := o.Model
	if llm == nil {
		if llm, err = runtime.NewModel(ctx, cfg, ""); err != nil {
			w.modelErr = err
		}
		if llm == nil {
			llm = runtime.NewMockLLM("unconfigured-model")
		}
	}

	w.memory = memory.Load(w.tools.Workspace().Dir(), cfg.Memory)
	opts := []runtime.Option{runtime.WithSessionService(events)}
	for agent, ref := range agentModelRefs(cfg, w.agents, o.Warn) {
		m, err := runtime.NewModel(ctx, cfg, ref)
		if err != nil {
			o.Warn(i18n.T("pin.load_failed", "agent", agent, "model", ref, "error", ModelErrorSummary(err, cfg)))
			continue
		}
		opts = append(opts, runtime.WithAgentModel(agent, m))
	}
	w.engine, err = runtime.NewEngine(ctx, cfg, w.agents, w.skills, w.tools, llm, append(opts,
		runtime.WithInstructions(w.instructions()),
		runtime.WithStreaming(o.Streaming),
		runtime.WithTurnStore(w.storage),
		runtime.WithNotice(o.Warn),
	)...)
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("failed to initialize engine: %w", err)
	}
	return w, nil
}

// Config returns the workspace's configuration.
func (w *Workspace) Config() *config.Config { return w.cfg }

// Agents returns the agent registry.
func (w *Workspace) Agents() *agents.Registry { return w.agents }

// Skills returns the skills provider.
func (w *Workspace) Skills() *skills.Provider { return w.skills }

// Tools returns the tool registry: workspace, approvals, processes, MCP, hooks.
func (w *Workspace) Tools() *tools.Registry { return w.tools }

// Engine returns the agent engine.
func (w *Workspace) Engine() *runtime.Engine { return w.engine }

// Storage returns the saved-session store, scoped to this workspace.
func (w *Workspace) Storage() *session.Storage { return w.storage }

// Audit returns the audit log (nil when disabled; its methods accept nil).
func (w *Workspace) Audit() *audit.Logger { return w.audit }

// Locales returns the loaded translation catalogs.
func (w *Workspace) Locales() *i18n.Bundle { return w.locales }

// ModelErr is why the configured model failed to initialise (nil if it
// didn't). The workspace then runs on a placeholder model.
func (w *Workspace) ModelErr() error { return w.modelErr }

// Close kills background processes and MCP servers and flushes the audit log.
func (w *Workspace) Close() error {
	var err error
	if w.tools != nil {
		err = w.tools.Close()
	}
	w.audit.Close()
	return err
}

// ResumeError reports a session that can't be resumed: none is saved for
// this workspace, or no session has the given ID or snapshot name.
type ResumeError struct{ Err error }

func (e *ResumeError) Error() string { return e.Err.Error() }
func (e *ResumeError) Unwrap() error { return e.Err }

// OpenSession picks the session to use and points the audit log at it: the
// session resume names (an ID, or a snapshot to start a new session from),
// the most recent one in this workspace when cont is set or resume is
// "latest", or else a new session. It reports whether a session was resumed.
func (w *Workspace) OpenSession(resume string, cont bool) (*session.SessionRecord, bool, error) {
	// A new session is named after its first prompt.
	rec, resumed, err := selectSession(w.storage, resume, cont, "", w.engine.ActiveAgent())
	if err != nil {
		return nil, false, err
	}
	w.audit.SetContext(rec.ID, w.tools.Workspace().Dir())
	return rec, resumed, nil
}

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
		// Snapshots are saved copies, not conversations to carry on.
		list = slices.DeleteFunc(list, func(r *session.SessionRecord) bool { return r.Name != "" })
		if len(list) == 0 {
			return nil, false, &ResumeError{fmt.Errorf("no saved sessions for %s (use --resume <id> for a session from another directory)", st.Workspace())}
		}
		resume = list[0].ID
	}
	rec, _, err := st.Open(resume) // an ID, or a snapshot name to start from
	if err != nil {
		return nil, false, &ResumeError{err}
	}
	return rec, true, nil
}

// LoadAttachments loads image files (failures are errors naming the path)
// and @image mentions in prompt (failures are warnings: the prompt may be
// piped text that merely contains an @path) through the workspace sandbox.
// Identical images are attached once.
func (w *Workspace) LoadAttachments(paths []string, prompt string, warn func(string)) ([]*images.Image, error) {
	var out []*images.Image
	seen := map[string]bool{}
	add := func(img *images.Image) {
		if !seen[img.SHA256] {
			seen[img.SHA256] = true
			out = append(out, img)
		}
	}
	for _, p := range paths {
		img, err := w.tools.LoadImage(strings.TrimPrefix(p, "@"))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		add(img)
	}
	for _, p := range images.Mentions(prompt) {
		img, err := w.tools.LoadImage(p)
		if err != nil {
			warn(i18n.T("attach.failed", "path", p, "error", err.Error()))
			continue
		}
		add(img)
	}
	return out, nil
}

// NewModel builds the named model (a bare name or "provider/model") from cfg.
func (w *Workspace) NewModel(ctx context.Context, cfg *config.Config, name string) (model.LLM, error) {
	return runtime.NewModel(ctx, cfg, name)
}

// SaveAgentModel records (or with ref "" removes) a model pin in the config
// file and returns the file written.
func (w *Workspace) SaveAgentModel(agent, ref string) (string, error) {
	if ref == "" {
		delete(w.cfg.AgentModels, agent)
	} else {
		if w.cfg.AgentModels == nil {
			w.cfg.AgentModels = map[string]string{}
		}
		w.cfg.AgentModels[agent] = ref
	}
	return config.SaveAgentModel(config.ConfigDir(""), agent, ref)
}

// SaveModelSettings records a model's settings (zero: removed) in the config
// file and returns the file written.
func (w *Workspace) SaveModelSettings(model string, s config.ModelSettings) (string, error) {
	return config.SaveModelSettings(config.ConfigDir(""), model, s)
}

// ReloadMemory re-reads project instruction files into the engine and
// returns their paths.
func (w *Workspace) ReloadMemory(ctx context.Context) ([]string, error) {
	w.memory = memory.Load(w.tools.Workspace().Dir(), w.cfg.Memory)
	if err := w.engine.SetInstructions(ctx, w.instructions()); err != nil {
		return nil, err
	}
	paths := make([]string, len(w.memory))
	for i, d := range w.memory {
		paths[i] = d.Path
	}
	return paths, nil
}

// SetLocale applies the active locale to the model's instructions and saves
// it as ui.locale in the config file, which it returns.
func (w *Workspace) SetLocale(ctx context.Context, l *i18n.Localizer) (string, error) {
	if err := w.engine.SetInstructions(ctx, w.instructions()); err != nil {
		return "", err
	}
	w.cfg.UI.Locale = l.Tag().String()
	return config.SaveUILocale(config.ConfigDir(""), w.cfg.UI.Locale)
}

// instructions are the extra system instructions: project memory plus, for
// non-English locales, which language to reply in.
func (w *Workspace) instructions() string {
	return memory.Render(w.memory) + i18n.ReplyInstruction(i18n.Current())
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

// SetupLocale loads translation catalogs (built in, then ui.locales_dir) and
// activates ui.locale for the process.
func SetupLocale(cfg *config.Config, warn func(string)) *i18n.Bundle {
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

// SecretRedactor masks configured credentials and the values of scrubbed
// environment variables in audit entries, logs and telemetry.
func SecretRedactor(cfg *config.Config) *redact.Redactor {
	secrets := []string{cfg.LLM.Gemini.APIKey, cfg.LLM.OpenAI.APIKey, cfg.LLM.Anthropic.APIKey, cfg.Web.SearchAPIKey}
	for _, s := range cfg.MCP.Servers {
		for _, v := range s.Env {
			secrets = append(secrets, v)
		}
	}
	return redact.FromEnv(cfg.Sandbox.ScrubEnv, secrets...)
}

// ModelErrorSummary makes a model initialisation error safe and readable:
// some SDK errors embed their whole client config, including the API key.
func ModelErrorSummary(err error, cfg *config.Config) string {
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
