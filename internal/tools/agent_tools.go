package tools

import (
	"fmt"
	"strings"

	"github.com/retail-cortex/code_puppy/internal/agents"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// AgentSummary provides summary info for list_agents.
type AgentSummary struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

// ListAgentsInput defines input for listing agents.
type ListAgentsInput struct {
	Filter string `json:"filter,omitempty" jsonschema:"Optional substring filter for agent names"`
}

// ListAgentsOutput holds registered agents.
type ListAgentsOutput struct {
	Agents []AgentSummary `json:"agents"`
}

// NewListAgentsTool creates an ADK tool for listing agents.
func NewListAgentsTool(registry *agents.Registry) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "list_agents",
			Description: "List all available agent personas and capabilities",
		},
		func(ctx agent.Context, input ListAgentsInput) (ListAgentsOutput, error) {
			filter := strings.ToLower(input.Filter)
			list := registry.List()
			summaries := make([]AgentSummary, 0, len(list))
			for _, a := range list {
				if filter != "" && !strings.Contains(strings.ToLower(a.Name), filter) {
					continue
				}
				summaries = append(summaries, AgentSummary{
					Name:        a.Name,
					DisplayName: a.DisplayName,
					Description: a.Description,
				})
			}
			return ListAgentsOutput{Agents: summaries}, nil
		},
	)
}

// InvokeAgentInput defines arguments for invoke_agent.
type InvokeAgentInput struct {
	AgentName string `json:"agent_name" jsonschema:"The name of the agent to invoke (e.g. qa-kitten, helios, planning-agent)"`
	Prompt    string `json:"prompt" jsonschema:"The specific task instruction for the delegated agent"`
}

// InvokeAgentOutput holds result of invoking an agent.
type InvokeAgentOutput struct {
	AgentName string `json:"agent_name"`
	Response  string `json:"response"`
	Error     string `json:"error,omitempty"`
}

// NewInvokeAgentTool creates an ADK tool for subagent delegation. The actual
// invocation is supplied at runtime through hooks.SetSubagentInvoker.
func NewInvokeAgentTool(registry *agents.Registry, hooks *Hooks) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "invoke_agent",
			Description: "Delegate a task to another specialized agent persona and receive their response",
		},
		func(ctx agent.Context, input InvokeAgentInput) (InvokeAgentOutput, error) {
			fail := func(msg string) (InvokeAgentOutput, error) {
				return InvokeAgentOutput{AgentName: input.AgentName, Error: msg}, nil
			}
			if _, ok := registry.Get(input.AgentName); !ok {
				return fail(fmt.Sprintf("agent '%s' is not registered; use list_agents to inspect available agents", input.AgentName))
			}
			if strings.TrimSpace(input.Prompt) == "" {
				return fail("prompt must not be empty")
			}
			invoker := hooks.subagentInvoker()
			if invoker == nil {
				return fail("subagent delegation is not available in this session")
			}
			res, err := invoker(ctx, input.AgentName, input.Prompt)
			if err != nil {
				return fail(fmt.Sprintf("subagent failed: %v", err))
			}
			return InvokeAgentOutput{AgentName: input.AgentName, Response: res}, nil
		},
	)
}
