package tools

import (
	"fmt"
	"strings"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/skills"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// SkillSummary represents a skill in search results.
type SkillSummary struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Version     string   `json:"version"`
}

// ListSkillsInput defines arguments for listing or searching skills.
type ListSkillsInput struct {
	Query string `json:"query,omitempty" jsonschema:"Optional keyword query to filter skills by name, description, or tags"`
}

// ListSkillsOutput holds discovered skills.
type ListSkillsOutput struct {
	Skills []SkillSummary `json:"skills"`
	Count  int            `json:"count"`
	Error  string         `json:"error,omitempty"`
}

// NewListSkillsTool creates an ADK tool for searching skills.
func NewListSkillsTool(provider *skills.Provider) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "list_or_search_skills",
			Description: "List or search available Agent Skills by keyword",
		},
		func(ctx agent.Context, input ListSkillsInput) (ListSkillsOutput, error) {
			if provider == nil {
				return ListSkillsOutput{Error: "skills provider not initialized"}, nil
			}

			var matched []*skills.Skill
			if input.Query == "" {
				matched = provider.List()
			} else {
				matched = provider.Search(input.Query)
			}

			summaries := make([]SkillSummary, 0, len(matched))
			for _, s := range matched {
				summaries = append(summaries, SkillSummary{
					Name:        s.Name,
					Description: s.Description,
					Tags:        s.Tags,
					Version:     s.Version,
				})
			}

			return ListSkillsOutput{
				Skills: summaries,
				Count:  len(summaries),
			}, nil
		},
	)
}

// ActivateSkillInput defines arguments for activating a skill.
type ActivateSkillInput struct {
	SkillName string `json:"skill_name" jsonschema:"Name of the skill to activate"`
}

// ActivateSkillOutput holds skill instructions.
type ActivateSkillOutput struct {
	SkillName    string   `json:"skill_name"`
	Instructions string   `json:"instructions"`
	Resources    []string `json:"resources"`
	// Scripts can be run with run_skill_script, when allowed.
	Scripts []SkillScriptInfo `json:"scripts,omitempty"`
	Error   string            `json:"error,omitempty"`
}

// SkillScriptInfo describes one of a skill's scripts for the model.
type SkillScriptInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Allowed     bool   `json:"allowed"`
	// Why the host's skills policy doesn't allow it.
	Blocked string `json:"blocked,omitempty"`
}

// NewActivateSkillTool creates an ADK tool for activating a skill.
// policy (nil: the defaults) decides which of its scripts may run.
func NewActivateSkillTool(provider *skills.Provider, policy *config.SkillPolicy) (tool.Tool, error) {
	if policy == nil {
		p := config.DefaultConfig().Skills.Policy
		policy = &p
	}
	return functiontool.New(
		functiontool.Config{
			Name:        "activate_skill",
			Description: "Load and activate full SKILL.md instructions and resources for a skill",
		},
		func(ctx agent.Context, input ActivateSkillInput) (ActivateSkillOutput, error) {
			if provider == nil {
				return ActivateSkillOutput{Error: "skills provider not initialized"}, nil
			}

			skill, ok := provider.Get(input.SkillName)
			if !ok || skill == nil {
				return ActivateSkillOutput{
					SkillName: input.SkillName,
					Error:     fmt.Sprintf("skill '%s' not found; use list_or_search_skills to see available skills", input.SkillName),
				}, nil
			}

			out := ActivateSkillOutput{
				SkillName:    skill.Name,
				Instructions: skill.Content,
				Resources:    skill.Resources,
			}
			if len(skill.Scripts) > 0 {
				ev := skills.Evaluate(skill, *policy)
				for i, sc := range skill.Scripts {
					info := SkillScriptInfo{Name: sc.Name, Description: sc.Description, Allowed: ev.Scripts[i].Allowed}
					if !info.Allowed {
						info.Blocked = strings.Join(ev.Scripts[i].Reasons, "; ")
					}
					out.Scripts = append(out.Scripts, info)
				}
			}
			return out, nil
		},
	)
}
