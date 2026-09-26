package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/runtime"
)

// Agents, models, pins, model settings and settings. Operations return data
// and typed errors, never text for the user: front ends word and localise
// the results.

// UnknownAgentError reports an agent name that isn't registered.
type UnknownAgentError struct{ Name string }

func (e *UnknownAgentError) Error() string { return fmt.Sprintf("unknown agent %q", e.Name) }

// Saved reports where a change was written in the config file. The change
// applies either way; Err is why it wasn't saved.
type Saved struct {
	Path string
	Err  error
}

// AgentInfo describes an agent.
type AgentInfo struct {
	Name        string
	DisplayName string
	Description string
	Active      bool
	// PinnedModel is the model the agent is pinned to ("" when it runs on
	// the configured model).
	PinnedModel string
}

// ListAgents returns every agent, in registry order.
func (w *Workspace) ListAgents() []AgentInfo {
	var out []AgentInfo
	for _, a := range w.agents.List() {
		out = append(out, w.agentInfo(a.Name))
	}
	return out
}

// ActiveAgent describes the agent that answers prompts.
func (w *Workspace) ActiveAgent() AgentInfo { return w.agentInfo(w.engine.ActiveAgent()) }

// SetAgent makes name the active agent.
func (w *Workspace) SetAgent(ctx context.Context, name string) (AgentInfo, error) {
	if err := w.engine.SetActiveAgent(ctx, name); err != nil {
		return AgentInfo{}, err
	}
	return w.agentInfo(name), nil
}

func (w *Workspace) agentInfo(name string) AgentInfo {
	info := AgentInfo{Name: name, Active: name == w.engine.ActiveAgent()}
	if spec, ok := w.agents.Get(name); ok {
		info.DisplayName, info.Description = spec.DisplayName, spec.Description
	}
	if m, pinned := w.engine.AgentModel(name); pinned {
		info.PinnedModel = m
	}
	return info
}

// ModelInfo names the model the active agent runs on.
type ModelInfo struct {
	Name     string
	Provider string // the configured provider
}

// Model returns the model the active agent runs on.
func (w *Workspace) Model() ModelInfo {
	return ModelInfo{Name: w.engine.ModelName(), Provider: w.cfg.LLM.Provider}
}

// SetModel switches every unpinned agent to ref (a name or "provider/model").
// It returns the active agent's pin when there is one, since that still
// decides what the active agent runs on.
func (w *Workspace) SetModel(ctx context.Context, ref string) (activePin string, err error) {
	llm, err := w.newModel(ctx, w.cfg, ref)
	if err == nil {
		err = w.engine.SetModel(ctx, llm)
	}
	if err != nil {
		return "", err
	}
	w.cfg.CodePuppy.DefaultModel = ref
	if m, pinned := w.engine.AgentModel(w.engine.ActiveAgent()); pinned {
		return m, nil
	}
	return "", nil
}

// PinResult describes an agent's model after PinModel or Unpin.
type PinResult struct {
	Agent string
	Model string // the model the agent now runs on
	Saved Saved
}

// PinModel runs agent on ref from now on and saves the pin in the config file.
func (w *Workspace) PinModel(ctx context.Context, agent, ref string) (PinResult, error) {
	if _, ok := w.agents.Get(agent); !ok {
		return PinResult{}, &UnknownAgentError{agent}
	}
	llm, err := w.newModel(ctx, w.cfg, ref)
	if err == nil {
		err = w.engine.PinModel(ctx, agent, llm)
	}
	if err != nil {
		return PinResult{}, err
	}
	return PinResult{Agent: agent, Model: llm.Name(), Saved: w.saveAgentModel(agent, ref)}, nil
}

// Unpin returns agent to the configured model, or to its own default_model
// if it declares one, and removes the pin from the config file.
func (w *Workspace) Unpin(ctx context.Context, agent string) (PinResult, error) {
	spec, ok := w.agents.Get(agent)
	if !ok {
		return PinResult{}, &UnknownAgentError{agent}
	}
	err := w.engine.Unpin(ctx, agent)
	if err == nil && spec.DefaultModel != "" {
		llm, merr := w.newModel(ctx, w.cfg, spec.DefaultModel)
		if err = merr; err == nil {
			err = w.engine.PinModel(ctx, agent, llm)
		}
	}
	if err != nil {
		return PinResult{}, err
	}
	m, _ := w.engine.AgentModel(agent)
	return PinResult{Agent: agent, Model: m, Saved: w.saveAgentModel(agent, "")}, nil
}

// saveAgentModel records (or with ref "" removes) a pin in the config file.
func (w *Workspace) saveAgentModel(agent, ref string) Saved {
	if ref == "" {
		delete(w.cfg.AgentModels, agent)
	} else {
		if w.cfg.AgentModels == nil {
			w.cfg.AgentModels = map[string]string{}
		}
		w.cfg.AgentModels[agent] = ref
	}
	path, err := config.SaveAgentModel(config.ConfigDir(""), agent, ref)
	return Saved{Path: path, Err: err}
}

// ErrBadModelRef reports a model reference with no model name.
var ErrBadModelRef = errors.New("not a model name")

// ModelSettingsInfo is one model's generation settings and what applies
// where it has none.
type ModelSettingsInfo struct {
	Model    string // the name settings are kept under, without a provider
	Provider string
	Settings config.ModelSettings
	// GlobalTemperature and GlobalMaxTokens apply when Settings leaves them
	// unset (0: the provider's default).
	GlobalTemperature float64
	GlobalMaxTokens   int
}

// AllModelSettings returns every model's settings, by model name.
func (w *Workspace) AllModelSettings() map[string]config.ModelSettings {
	return w.engine.AllModelSettings()
}

// ModelSettings returns ref's settings ("provider/" optional).
func (w *Workspace) ModelSettings(ref string) (ModelSettingsInfo, error) {
	provider, name := runtime.ParseModelRef(ref, w.cfg.LLM.Provider)
	if strings.Contains(ref, "=") || name == "" {
		return ModelSettingsInfo{}, ErrBadModelRef
	}
	return ModelSettingsInfo{
		Model: name, Provider: provider, Settings: w.engine.ModelSettings(name),
		GlobalTemperature: w.cfg.CodePuppy.Temperature, GlobalMaxTokens: w.cfg.CodePuppy.MaxTokens,
	}, nil
}

// Setting is one key=value change; an empty Value clears the key.
type Setting struct{ Key, Value string }

// ModelSettingsChange describes the result of UpdateModelSettings.
type ModelSettingsChange struct {
	ModelSettingsInfo
	// Unsupported are keys set that the provider or model ignores.
	Unsupported []string
	Saved       Saved
}

// InvalidSettingError reports a setting that can't be applied: an unknown
// key or a value out of range.
type InvalidSettingError struct{ Err error }

func (e *InvalidSettingError) Error() string { return e.Err.Error() }
func (e *InvalidSettingError) Unwrap() error { return e.Err }

// UpdateModelSettings applies changes to ref's settings (reset clears them
// all first). They apply from the next model call and are saved in the
// config file, under the key it already uses for the model if any.
func (w *Workspace) UpdateModelSettings(ref string, reset bool, changes []Setting) (ModelSettingsChange, error) {
	info, err := w.ModelSettings(ref)
	if err != nil {
		return ModelSettingsChange{}, err
	}
	s := info.Settings
	if reset {
		s = config.ModelSettings{}
	}
	for _, c := range changes {
		if err := s.Set(strings.TrimSpace(c.Key), c.Value); err != nil {
			return ModelSettingsChange{}, &InvalidSettingError{err}
		}
	}
	w.engine.SetModelSettings(info.Model, s)
	info.Settings = s
	out := ModelSettingsChange{ModelSettingsInfo: info}
	for _, key := range config.ModelSettingKeys {
		if _, set := s.Get(key); set && !runtime.SettingSupported(info.Provider, info.Model, key) {
			out.Unsupported = append(out.Unsupported, key)
		}
	}
	path, err := config.SaveModelSettings(config.ConfigDir(""), settingsKey(w.cfg, info.Model), s)
	out.Saved = Saved{Path: path, Err: err}
	return out, nil
}

// settingsKey returns the [model_settings] key the config file already uses
// for model name, e.g. a hand-written "openai/gpt-5", so a change edits that
// table instead of adding a second one; otherwise name itself.
func settingsKey(cfg *config.Config, name string) string {
	if _, ok := cfg.ModelSettings[name]; ok {
		return name
	}
	for _, key := range slices.Sorted(maps.Keys(cfg.ModelSettings)) {
		if _, n := runtime.ParseModelRef(key, ""); n == name {
			return key
		}
	}
	return name
}

// Settings are the values /set changes, plus the model, agent and locale.
type Settings struct {
	PuppyName string
	OwnerName string
	Agency    string
	Model     ModelInfo
	Agent     string
	Locale    string // the language the model replies in
}

// Settings returns the current settings.
func (w *Workspace) Settings() Settings {
	return Settings{
		PuppyName: w.cfg.CodePuppy.PuppyName, OwnerName: w.cfg.CodePuppy.OwnerName, Agency: w.cfg.CodePuppy.AgencyLevel,
		Model: w.Model(), Agent: w.engine.ActiveAgent(), Locale: w.reply.Tag().String(),
	}
}

// UnknownSettingError reports a key /set doesn't know.
type UnknownSettingError struct{ Key string }

func (e *UnknownSettingError) Error() string { return fmt.Sprintf("unknown setting %q", e.Key) }

// ErrInvalidAgency reports an agency level other than low, medium, high or extreme.
var ErrInvalidAgency = errors.New("agency must be low, medium, high or extreme")

// Set changes a setting for this session ("agency" or "agency_level",
// "puppy_name", "owner_name") and returns the key in canonical form. The
// agents' instructions embed these values, so they are rebuilt.
func (w *Workspace) Set(ctx context.Context, key, value string) (string, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	switch key {
	case "agency", "agency_level":
		switch strings.ToLower(value) {
		case "low", "medium", "high", "extreme":
		default:
			return key, ErrInvalidAgency
		}
		w.cfg.CodePuppy.AgencyLevel = strings.ToLower(value)
	case "puppy_name":
		w.cfg.CodePuppy.PuppyName = value
	case "owner_name":
		w.cfg.CodePuppy.OwnerName = value
	default:
		return key, &UnknownSettingError{key}
	}
	return key, w.engine.Rebuild(ctx)
}
