package app

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/retail-cortex/code_puppy/internal/runtime"
	"github.com/retail-cortex/code_puppy/internal/skills"
	"github.com/retail-cortex/code_puppy/internal/tools"
)

// Skills and their scripts' environments, MCP servers, and the tools an
// agent can use.

// SkillInfo describes a skill and what the skills policy lets it do.
type SkillInfo struct {
	Name          string
	Description   string
	Version       string
	License       string
	Category      string
	Compatibility string
	Tags          []string
	// Hash identifies the skill's content, for pinning it in the policy
	// ("" if it couldn't be computed).
	Hash string
	// Tools are the tools the skill says it needs, and why.
	Tools []SkillTool
	// Scripts are the skill's scripts and whether each may run.
	Scripts []SkillScript
	// Tier is the approval tier the scripts run at ("" without scripts);
	// Bypass means the skill may skip approvals (tier 0) and the policy
	// allows it.
	Tier   string
	Bypass bool
	// Network: the scripts may use the network. NeedsNetwork: the skill
	// asks for it (granted or not).
	Network      bool
	NeedsNetwork bool
	// Env are the host environment variables passed to the scripts;
	// Withheld are the ones the skill asked for that the policy keeps back.
	Env, Withheld []string
	// Blocked are reasons none of the scripts may run.
	Blocked []string
}

// SkillTool is a tool a skill needs.
type SkillTool struct {
	Name   string
	Scopes []string
	Why    string
}

// SkillScript is one of a skill's scripts and the policy's verdict on it.
type SkillScript struct {
	Name     string
	Language string
	Source   string // its path in the skill, or "inline"
	Timeout  time.Duration
	Deps     []string
	Allowed  bool
	Reasons  []string // why it may not run
}

// Runnable reports whether any of the skill's scripts may run.
func (s SkillInfo) Runnable() bool {
	for _, sc := range s.Scripts {
		if sc.Allowed {
			return true
		}
	}
	return false
}

func (w *Workspace) skillInfo(s *skills.Skill) SkillInfo {
	info := SkillInfo{
		Name: s.Name, Description: s.Description, Version: s.Version, License: s.License, Category: s.Category,
		Compatibility: s.Compatibility, Tags: s.Tags, NeedsNetwork: s.ExecutionHints.NeedsNetwork(),
	}
	for _, t := range s.ToolRequirements {
		info.Tools = append(info.Tools, SkillTool{Name: t.Name, Scopes: t.Scopes, Why: t.Description})
	}
	ev := skills.Evaluate(s, w.cfg.Skills.Policy)
	info.Hash, info.Network, info.Env, info.Withheld, info.Blocked = ev.Hash, ev.Network, ev.Env, ev.Withheld, ev.Blocked
	if len(s.Scripts) > 0 {
		info.Tier, info.Bypass = ev.Tier.String(), ev.Bypass
	}
	for i, v := range ev.Scripts {
		src := s.Scripts[i].RelativePath
		if src == "" {
			src = "inline"
		}
		info.Scripts = append(info.Scripts, SkillScript{
			Name: v.Name, Language: string(s.Scripts[i].Language), Source: src, Timeout: time.Duration(v.TimeoutSeconds) * time.Second,
			Deps: v.Dependencies, Allowed: v.Allowed, Reasons: v.Reasons,
		})
	}
	return info
}

func (w *Workspace) skillInfos(list []*skills.Skill) []SkillInfo {
	out := make([]SkillInfo, len(list))
	for i, s := range list {
		out[i] = w.skillInfo(s)
	}
	return out
}

// ListSkills returns every skill found.
func (w *Workspace) ListSkills() []SkillInfo { return w.skillInfos(w.skills.List()) }

// SearchSkills returns the skills matching query.
func (w *Workspace) SearchSkills(query string) []SkillInfo {
	return w.skillInfos(w.skills.Search(query))
}

// Skill returns the named skill.
func (w *Workspace) Skill(name string) (SkillInfo, bool) {
	s, ok := w.skills.Get(name)
	if !ok {
		return SkillInfo{}, false
	}
	return w.skillInfo(s), true
}

// ErrScriptsDisabled reports that skill scripts (and so their
// environments) are turned off.
var ErrScriptsDisabled = errors.New("skill scripts are disabled")

// Env is an isolated Python environment built for skill scripts.
type Env struct {
	Key      string
	Deps     []string
	Skills   []string // the skills that used it
	Size     int64    // bytes on disk
	LastUsed time.Time
	Ready    bool // built completely
}

func (w *Workspace) pyEnvs() (*tools.PyEnvs, error) {
	if w.tools.SkillScripts() == nil {
		return nil, ErrScriptsDisabled
	}
	return w.tools.SkillScripts().Envs(), nil
}

// ListEnvs returns the script environments.
func (w *Workspace) ListEnvs() ([]Env, error) {
	envs, err := w.pyEnvs()
	if err != nil {
		return nil, err
	}
	var out []Env
	for _, e := range envs.List() {
		out = append(out, Env{Key: e.Key, Deps: e.Deps, Skills: e.Skills, Size: e.Size, LastUsed: e.LastUsed, Ready: e.Ready})
	}
	return out, nil
}

// RemoveEnv deletes one environment.
func (w *Workspace) RemoveEnv(key string) error {
	envs, err := w.pyEnvs()
	if err != nil {
		return err
	}
	return envs.Remove(key)
}

// EnvError is an environment that couldn't be removed.
type EnvError struct {
	Key string
	Err error
}

// PruneResult is what PruneEnvs removed.
type PruneResult struct {
	Removed int
	Freed   int64 // bytes
	Failed  []EnvError
}

// PruneEnvs removes environments no script the policy lets run needs, and
// incomplete ones.
func (w *Workspace) PruneEnvs() (PruneResult, error) {
	envs, err := w.pyEnvs()
	if err != nil {
		return PruneResult{}, err
	}
	needed := w.neededEnvs(envs)
	var res PruneResult
	for _, e := range envs.List() {
		if e.Ready && needed[e.Key] {
			continue
		}
		if err := envs.Remove(e.Key); err != nil {
			res.Failed = append(res.Failed, EnvError{e.Key, err})
			continue
		}
		res.Removed++
		res.Freed += e.Size
	}
	return res, nil
}

// neededEnvs are the environments of the scripts the skills policy lets run.
func (w *Workspace) neededEnvs(envs *tools.PyEnvs) map[string]bool {
	needed := map[string]bool{}
	python, err := tools.SystemPython()
	if err != nil {
		return needed
	}
	for _, s := range w.skills.List() {
		ev := skills.Evaluate(s, w.cfg.Skills.Policy)
		for i, sc := range s.Scripts {
			if ev.Scripts[i].Allowed && len(sc.Dependencies) > 0 {
				needed[envs.Key(python, sc.Dependencies)] = true
			}
		}
	}
	return needed
}

// MCPServer is a configured MCP server.
type MCPServer struct {
	Name string
	// Target is its URL, or the command that starts it.
	Target      string
	AutoApprove bool
}

// ListMCPServers returns the configured MCP servers, or none when none
// could be started.
func (w *Workspace) ListMCPServers() []MCPServer {
	if len(w.tools.MCP().Servers()) == 0 {
		return nil
	}
	var out []MCPServer
	for _, s := range w.cfg.MCP.Servers {
		target := s.URL
		if target == "" {
			target = strings.Join(append([]string{s.Command}, s.Args...), " ")
		}
		out = append(out, MCPServer{Name: s.Name, Target: target, AutoApprove: s.AutoApprove})
	}
	return out
}

// ToolInfo is a tool an agent can call.
type ToolInfo struct {
	Name        string
	Description string
	// PlanAllowed: the tool stays available in plan mode, which refuses
	// tools that change anything.
	PlanAllowed bool
}

// MCPOffer is an MCP server offered to an agent.
type MCPOffer struct {
	Server string
	Tools  []string // the tools offered; empty means all of them
	Prefix string   // prepended to tool names ("" for none)
}

// AgentTools is what an agent can use.
type AgentTools struct {
	Agent string
	Tools []ToolInfo // by name
	MCP   []MCPOffer
}

// ActiveAgentTools returns what the active agent can use: its built-in
// tools and the MCP servers offered to it.
func (w *Workspace) ActiveAgentTools() AgentTools {
	active := w.engine.ActiveAgent()
	out := AgentTools{Agent: active}
	spec, ok := w.agents.Get(active)
	if !ok {
		return out
	}
	for _, t := range w.tools.GetToolsForAgent(spec.Tools) {
		out.Tools = append(out.Tools, ToolInfo{Name: t.Name(), Description: t.Description(), PlanAllowed: runtime.PlanAllows(t.Name())})
	}
	sort.Slice(out.Tools, func(i, j int) bool { return out.Tools[i].Name < out.Tools[j].Name })
	for _, s := range w.cfg.MCP.Servers {
		if mcpOfferedTo(s.Agents, active) {
			out.MCP = append(out.MCP, MCPOffer{Server: s.Name, Tools: s.Tools, Prefix: s.Prefix})
		}
	}
	return out
}

// mcpOfferedTo mirrors the MCP manager's rule for the primary agent: no
// agents list means the primary agent only; "*" means every agent.
func mcpOfferedTo(agents []string, active string) bool {
	if len(agents) == 0 {
		return true
	}
	for _, a := range agents {
		if a == "*" || a == active {
			return true
		}
	}
	return false
}
