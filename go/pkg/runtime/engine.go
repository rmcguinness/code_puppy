package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/retail-cortex/code_puppy/pkg/agents"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/skills"
	"github.com/retail-cortex/code_puppy/pkg/tools"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// Engine orchestrates the Google ADK execution lifecycle for Code Puppy.
type Engine struct {
	cfg       *config.Config
	agentReg  *agents.Registry
	skillProv *skills.Provider
	toolReg   *tools.Registry
	llm       model.LLM
	runner    *runner.Runner
	active    string
}

// EventHandler receives events (text tokens, function calls, function responses) from the runner.
type EventHandler func(ev *session.Event) error

// NewEngine creates and wires the ADK runtime engine.
func NewEngine(
	ctx context.Context,
	cfg *config.Config,
	agentReg *agents.Registry,
	skillProv *skills.Provider,
	toolReg *tools.Registry,
	llm model.LLM,
) (*Engine, error) {
	e := &Engine{
		cfg:       cfg,
		agentReg:  agentReg,
		skillProv: skillProv,
		toolReg:   toolReg,
		llm:       llm,
		active:    cfg.CodePuppy.DefaultAgent,
	}

	if err := e.buildRunner(ctx); err != nil {
		return nil, fmt.Errorf("failed to initialize ADK runner: %w", err)
	}

	return e, nil
}

// SetActiveAgent switches the primary active agent persona and rebuilds the agent tree.
func (e *Engine) SetActiveAgent(ctx context.Context, agentName string) error {
	if _, ok := e.agentReg.Get(agentName); !ok {
		return fmt.Errorf("agent '%s' not found", agentName)
	}
	e.active = agentName
	return e.buildRunner(ctx)
}

// ActiveAgent returns the name of the currently active agent persona.
func (e *Engine) ActiveAgent() string {
	return e.active
}

func (e *Engine) buildRunner(ctx context.Context) error {
	rootSpec, ok := e.agentReg.Get(e.active)
	if !ok {
		// Fallback to code-puppy
		rootSpec, ok = e.agentReg.Get("code-puppy")
		if !ok {
			return fmt.Errorf("default agent 'code-puppy' not found in registry")
		}
		e.active = "code-puppy"
	}

	// Build sub-agents for all other personas
	var subAgents []agent.Agent
	for _, spec := range e.agentReg.List() {
		if spec.Name == e.active {
			continue
		}

		instruction := spec.InterpolatePrompt(
			e.cfg.CodePuppy.PuppyName,
			e.cfg.CodePuppy.OwnerName,
			spec.AgencyLevel,
		)

		subAgentTools := e.toolReg.GetToolsForAgent(spec.Tools)
		sub, err := llmagent.New(llmagent.Config{
			Name:        spec.Name,
			Description: spec.Description,
			Instruction: instruction,
			Model:       e.llm,
			Tools:       subAgentTools,
		})
		if err != nil {
			return fmt.Errorf("failed to build sub-agent %s: %w", spec.Name, err)
		}
		subAgents = append(subAgents, sub)
	}

	// Build root agent
	rootInstruction := rootSpec.InterpolatePrompt(
		e.cfg.CodePuppy.PuppyName,
		e.cfg.CodePuppy.OwnerName,
		e.cfg.CodePuppy.AgencyLevel,
	)

	// Append skills catalog awareness if skills are enabled
	if e.cfg.Skills.Enabled && e.skillProv != nil {
		allSkills := e.skillProv.List()
		if len(allSkills) > 0 {
			var sb strings.Builder
			sb.WriteString("\n\n## Available Agent Skills:\n")
			for _, s := range allSkills {
				sb.WriteString(fmt.Sprintf("- **%s**: %s\n", s.Name, s.Description))
			}
			sb.WriteString("\nUse `activate_skill` to load full skill instructions whenever relevant.\n")
			rootInstruction += sb.String()
		}
	}

	rootTools := e.toolReg.GetToolsForAgent(rootSpec.Tools)
	rootAgent, err := llmagent.New(llmagent.Config{
		Name:        rootSpec.Name,
		Description: rootSpec.Description,
		Instruction: rootInstruction,
		Model:       e.llm,
		Tools:       rootTools,
		SubAgents:   subAgents,
	})
	if err != nil {
		return fmt.Errorf("failed to build root agent: %w", err)
	}

	r, err := runner.NewInMemory("code-puppy", rootAgent)
	if err != nil {
		return fmt.Errorf("failed to instantiate ADK in-memory runner: %w", err)
	}
	e.runner = r

	return nil
}

// Execute runs a prompt within a session and streams ADK events to the handler.
func (e *Engine) Execute(ctx context.Context, sessionID, prompt string, handler EventHandler) error {
	if e.runner == nil {
		return fmt.Errorf("runner is not initialized")
	}

	if sessionID == "" {
		sessionID = "default"
	}

	userMsg := genai.NewContentFromText(prompt, genai.RoleUser)
	runCfg := agent.RunConfig{}

	events := e.runner.Run(ctx, "user", sessionID, userMsg, runCfg)
	for ev, err := range events {
		if err != nil {
			return fmt.Errorf("agent execution error: %w", err)
		}
		if handler != nil && ev != nil {
			if hErr := handler(ev); hErr != nil {
				return hErr
			}
		}
	}

	return nil
}
