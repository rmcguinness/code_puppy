package tools

import (
	"fmt"

	"github.com/retail-cortex/code_puppy/pkg/agents"
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
			list := registry.List()
			summaries := make([]AgentSummary, 0, len(list))
			for _, a := range list {
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

// InvokeAgentFunc is a callback invoked when an agent transfers or delegates a subtask.
type InvokeAgentFunc func(agentName, prompt string) (string, error)

var (
	subagentInvoker InvokeAgentFunc
)

// SetSubagentInvoker sets the delegator for subagent calls.
func SetSubagentInvoker(invoker InvokeAgentFunc) {
	subagentInvoker = invoker
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

// NewInvokeAgentTool creates an ADK tool for subagent delegation.
func NewInvokeAgentTool(registry *agents.Registry) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "invoke_agent",
			Description: "Delegate a task to another specialized agent persona and receive their response",
		},
		func(ctx agent.Context, input InvokeAgentInput) (InvokeAgentOutput, error) {
			if _, ok := registry.Get(input.AgentName); !ok {
				return InvokeAgentOutput{
					AgentName: input.AgentName,
					Error:     fmt.Sprintf("agent '%s' is not registered; use list_agents to inspect available agents", input.AgentName),
				}, nil
			}

			if subagentInvoker != nil {
				res, err := subagentInvoker(input.AgentName, input.Prompt)
				if err != nil {
					return InvokeAgentOutput{AgentName: input.AgentName, Error: fmt.Sprintf("subagent failed: %v", err)}, nil
				}
				return InvokeAgentOutput{AgentName: input.AgentName, Response: res}, nil
			}

			return InvokeAgentOutput{
				AgentName: input.AgentName,
				Response:  fmt.Sprintf("[Subagent %s]: completed task '%s'", input.AgentName, input.Prompt),
			}, nil
		},
	)
}
