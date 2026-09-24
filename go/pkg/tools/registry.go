package tools

import (
	"fmt"

	"github.com/retail-cortex/code_puppy/pkg/audit"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/agents"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/skills"
	"google.golang.org/adk/v2/tool"
)

// Registry manages initialized ADK tools and the resources they share.
// Tools are registered once at construction and never mutated afterwards.
type Registry struct {
	tools       map[string]tool.Tool
	workspace   *Workspace
	hooks       *Hooks
	processes   *ProcessManager
	policy      *CommandPolicy
	exec        *ExecEnv
	checkpoints *Checkpoints
	mcp         *MCPManager
	scripts     *ScriptHooks
}

// NewRegistry initializes all standard Code Puppy tools. Call Close when done
// to stop background processes and release the workspace handle.
func NewRegistry(cfg *config.Config, agentReg *agents.Registry, skillProv *skills.Provider) (*Registry, error) {
	sb := cfg.Sandbox
	ws, err := OpenWorkspace(WorkspaceOptions{
		Dir:           cfg.Tools.WorkspaceDir,
		AllowedPaths:  sb.AllowedPaths,
		ReadOnlyPaths: sb.ReadOnlyPaths,
		BlockedPaths:  sb.BlockedPaths,
		MaxFileSize:   cfg.Tools.MaxFileSizeBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open workspace: %w", err)
	}

	policy, err := NewCommandPolicy(CommandPolicyConfig{
		Allow:       sb.Commands.Allow,
		Deny:        sb.Commands.Deny,
		AutoApprove: sb.Commands.AutoApprove,
	})
	if err != nil {
		ws.Close()
		return nil, err
	}

	// Shell-only writable dirs are optional; missing ones are skipped.
	writable := append(ws.WritableDirs(), DefaultShellWritableDirs()...)
	for _, d := range sb.ShellWritablePaths {
		if real, err := canonicalDir(d); err == nil {
			writable = append(writable, real)
		}
	}
	osb, err := NewOSSandbox(OSSandboxSpec{
		Mode:         ShellSandboxMode(sb.Shell),
		WritableDirs: writable,
		ReadOnlyDirs: ws.ReadOnlyDirs(),
		Blocked:      ws.Blocked(),
		AllowNetwork: sb.AllowNetwork,
	})
	if err != nil {
		ws.Close()
		return nil, err
	}
	env := &ExecEnv{Sandbox: osb, ScrubEnv: sb.ScrubEnv}

	r := &Registry{
		tools:     make(map[string]tool.Tool),
		workspace: ws,
		hooks: NewHooks(Policy{
			AutoApproveAll:      cfg.CodePuppy.AutoApprove,
			AutoApproveCommands: cfg.Tools.AutoApproveCommands,
		}),
		processes: NewProcessManager(0, 0),
		policy:    policy,
		exec:      env,
	}
	r.processes.exec = env

	if cfg.Tools.ApprovalsFile != "" {
		store, err := OpenApprovalStore(cfg.Tools.ApprovalsFile)
		if err != nil {
			ws.Close()
			return nil, err
		}
		r.hooks.SetStore(store)
	}
	if cfg.Checkpoints.Enabled {
		r.checkpoints = NewCheckpoints(ws, cfg.Checkpoints.MaxBytes)
	}

	type entry struct {
		names []string
		build func() (tool.Tool, error)
	}
	entries := []entry{
		{[]string{"read_file"}, func() (tool.Tool, error) { return NewReadFileTool(ws) }},
		{[]string{"list_files"}, func() (tool.Tool, error) { return NewListFilesTool(ws) }},
		{[]string{"create_file"}, func() (tool.Tool, error) { return NewCreateFileTool(ws, r.hooks) }},
		{[]string{"delete_file"}, func() (tool.Tool, error) { return NewDeleteFileTool(ws, r.hooks) }},
		{[]string{"replace_in_file", "edit"}, func() (tool.Tool, error) { return NewReplaceInFileTool(ws, r.hooks) }},
		{[]string{"delete_snippet"}, func() (tool.Tool, error) { return NewDeleteSnippetTool(ws, r.hooks) }},
		{[]string{"apply_patch"}, func() (tool.Tool, error) { return NewApplyPatchTool(ws, r.hooks) }},
		{[]string{"grep"}, func() (tool.Tool, error) { return NewGrepTool(ws) }},
		{[]string{"run_shell_command", "agent_run_shell_command"}, func() (tool.Tool, error) {
			return NewRunShellCommandTool(ShellConfig{
				Workspace:      ws,
				Hooks:          r.hooks,
				Processes:      r.processes,
				Policy:         policy,
				Exec:           env,
				DefaultTimeout: time.Duration(cfg.Tools.ShellTimeoutSeconds) * time.Second,
			})
		}},
		{[]string{"manage_background_process"}, func() (tool.Tool, error) { return NewManageBackgroundTool(r.processes) }},
		{[]string{"ask_user_question"}, func() (tool.Tool, error) { return NewAskUserQuestionTool(r.hooks) }},
		{[]string{"universal_constructor"}, func() (tool.Tool, error) {
			return NewUniversalConstructorTool(cfg.Tools.UCToolsDir, r.hooks, env, policy)
		}},
	}
	if cfg.Web.Enabled {
		entries = append(entries, entry{[]string{"web_fetch"}, func() (tool.Tool, error) {
			return NewWebFetchTool(WebFetchConfig{
				AllowDomains: cfg.Web.AllowDomains,
				DenyDomains:  cfg.Web.DenyDomains,
				AllowPrivate: cfg.Web.AllowPrivate,
				AllowNetwork: sb.AllowNetwork,
				MaxBytes:     cfg.Web.MaxBytes,
				Timeout:      time.Duration(cfg.Web.TimeoutSeconds) * time.Second,
			}, r.hooks)
		}})
	}
	if skillProv != nil {
		entries = append(entries,
			entry{[]string{"list_or_search_skills"}, func() (tool.Tool, error) { return NewListSkillsTool(skillProv) }},
			entry{[]string{"activate_skill"}, func() (tool.Tool, error) { return NewActivateSkillTool(skillProv) }},
		)
	}
	if agentReg != nil {
		entries = append(entries,
			entry{[]string{"list_agents"}, func() (tool.Tool, error) { return NewListAgentsTool(agentReg) }},
			entry{[]string{"invoke_agent"}, func() (tool.Tool, error) { return NewInvokeAgentTool(agentReg, r.hooks) }},
		)
	}

	for _, e := range entries {
		t, err := e.build()
		if err != nil {
			r.Close()
			return nil, fmt.Errorf("failed to create %s tool: %w", e.names[0], err)
		}
		for _, name := range e.names {
			r.tools[name] = t
		}
	}

	reserved := make([]string, 0, len(r.tools))
	for name := range r.tools {
		reserved = append(reserved, name)
	}
	if r.mcp, err = NewMCPManager(cfg.MCP.Servers, env, reserved); err != nil {
		r.Close()
		return nil, err
	}
	// Hooks are the user's own trusted scripts from config: they keep the
	// process guard but run outside the OS sandbox with the full environment.
	if r.scripts, err = NewScriptHooks(cfg.Hooks, &ExecEnv{}, ws.Dir(), nil); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// SetAudit attaches the audit log to approvals and hooks.
func (r *Registry) SetAudit(l *audit.Logger) {
	r.hooks.SetAudit(l)
	r.scripts.audit = l
}

// SetWarn routes non-fatal warnings (MCP server failures, hook errors).
func (r *Registry) SetWarn(warn func(string)) {
	r.mcp.Warn = warn
	r.scripts.Warn = warn
}

// MCP returns the MCP server manager.
func (r *Registry) MCP() *MCPManager { return r.mcp }

// ScriptHooks returns the configured lifecycle hooks.
func (r *Registry) ScriptHooks() *ScriptHooks { return r.scripts }

// Hooks returns the callbacks the host uses to wire approvals, prompts and sub-agents.
func (r *Registry) Hooks() *Hooks { return r.hooks }

// Workspace returns the confined workspace the file tools operate in.
func (r *Registry) Workspace() *Workspace { return r.workspace }

// Checkpoints returns the file snapshot store (nil when disabled).
func (r *Registry) Checkpoints() *Checkpoints { return r.checkpoints }

// Processes returns the background process manager.
func (r *Registry) Processes() *ProcessManager { return r.processes }

// SandboxSummary describes the active file, command, and OS sandbox policy.
func (r *Registry) SandboxSummary() []string {
	lines := r.workspace.Describe()
	lines = append(lines, r.policy.Describe()...)
	lines = append(lines, "shell OS sandbox: "+r.exec.Sandbox.Status())
	return lines
}

// ShellSandbox returns the OS sandbox used for commands.
func (r *Registry) ShellSandbox() *OSSandbox { return r.exec.Sandbox }

// Close kills background processes and MCP servers and releases the workspace root.
func (r *Registry) Close() error {
	r.processes.Shutdown()
	r.mcp.Close()
	return r.workspace.Close()
}

// GetToolsForAgent returns the subset of tools configured for a given agent.
func (r *Registry) GetToolsForAgent(toolNames []string) []tool.Tool {
	var result []tool.Tool
	seen := make(map[string]bool)
	for _, name := range toolNames {
		if t, ok := r.tools[name]; ok && !seen[t.Name()] {
			result = append(result, t)
			seen[t.Name()] = true
		}
	}
	return result
}

// GetAllTools returns all registered tools (aliases de-duplicated).
func (r *Registry) GetAllTools() []tool.Tool {
	var list []tool.Tool
	seen := make(map[string]bool)
	for _, t := range r.tools {
		if !seen[t.Name()] {
			list = append(list, t)
			seen[t.Name()] = true
		}
	}
	return list
}
