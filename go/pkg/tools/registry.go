package tools

import (
	"fmt"
	"sync"

	"github.com/retail-cortex/code_puppy/pkg/agents"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/skills"
	"google.golang.org/adk/v2/tool"
)

// Registry manages initialized ADK tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]tool.Tool
}

// NewRegistry initializes all standard Code Puppy tools.
func NewRegistry(cfg *config.Config, agentReg *agents.Registry, skillProv *skills.Provider) (*Registry, error) {
	r := &Registry{
		tools: make(map[string]tool.Tool),
	}

	workspace := cfg.Tools.WorkspaceDir
	if workspace == "" {
		workspace = "."
	}

	// File ops
	readFileTool, err := NewReadFileTool(workspace)
	if err != nil {
		return nil, fmt.Errorf("failed to create read_file tool: %w", err)
	}
	r.tools["read_file"] = readFileTool

	listFilesTool, err := NewListFilesTool(workspace)
	if err != nil {
		return nil, fmt.Errorf("failed to create list_files tool: %w", err)
	}
	r.tools["list_files"] = listFilesTool

	createFileTool, err := NewCreateFileTool(workspace)
	if err != nil {
		return nil, fmt.Errorf("failed to create create_file tool: %w", err)
	}
	r.tools["create_file"] = createFileTool

	deleteFileTool, err := NewDeleteFileTool(workspace)
	if err != nil {
		return nil, fmt.Errorf("failed to create delete_file tool: %w", err)
	}
	r.tools["delete_file"] = deleteFileTool

	// File edit
	replaceTool, err := NewReplaceInFileTool(workspace)
	if err != nil {
		return nil, fmt.Errorf("failed to create replace_in_file tool: %w", err)
	}
	r.tools["replace_in_file"] = replaceTool
	r.tools["edit"] = replaceTool

	deleteSnippetTool, err := NewDeleteSnippetTool(workspace)
	if err != nil {
		return nil, fmt.Errorf("failed to create delete_snippet tool: %w", err)
	}
	r.tools["delete_snippet"] = deleteSnippetTool

	// Search
	grepTool, err := NewGrepTool(workspace)
	if err != nil {
		return nil, fmt.Errorf("failed to create grep tool: %w", err)
	}
	r.tools["grep"] = grepTool

	// Shell runner
	shellTool, err := NewRunShellCommandTool(workspace, cfg.Tools.ShellTimeoutSeconds)
	if err != nil {
		return nil, fmt.Errorf("failed to create run_shell_command tool: %w", err)
	}
	r.tools["run_shell_command"] = shellTool
	r.tools["agent_run_shell_command"] = shellTool

	// Skills
	if skillProv != nil {
		listSkillsTool, err := NewListSkillsTool(skillProv)
		if err == nil {
			r.tools["list_or_search_skills"] = listSkillsTool
		}
		activateSkillTool, err := NewActivateSkillTool(skillProv)
		if err == nil {
			r.tools["activate_skill"] = activateSkillTool
		}
	}

	// User interactions
	askUserTool, err := NewAskUserQuestionTool()
	if err == nil {
		r.tools["ask_user_question"] = askUserTool
	}

	// Universal Constructor (Helios)
	ucTool, err := NewUniversalConstructorTool("")
	if err == nil {
		r.tools["universal_constructor"] = ucTool
	}

	// Subagent tools
	if agentReg != nil {
		listAgentsTool, err := NewListAgentsTool(agentReg)
		if err == nil {
			r.tools["list_agents"] = listAgentsTool
		}
		invokeAgentTool, err := NewInvokeAgentTool(agentReg)
		if err == nil {
			r.tools["invoke_agent"] = invokeAgentTool
		}
	}

	return r, nil
}

// GetToolsForAgent returns the subset of tools configured for a given agent.
func (r *Registry) GetToolsForAgent(toolNames []string) []tool.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []tool.Tool
	seenNames := make(map[string]bool)

	for _, name := range toolNames {
		if t, ok := r.tools[name]; ok {
			if seenNames[t.Name()] {
				continue
			}
			result = append(result, t)
			seenNames[t.Name()] = true
		}
	}

	return result
}

// GetAllTools returns all registered tools.
func (r *Registry) GetAllTools() []tool.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var list []tool.Tool
	for _, t := range r.tools {
		list = append(list, t)
	}
	return list
}
