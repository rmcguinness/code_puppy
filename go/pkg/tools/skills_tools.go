package tools

import (
	"fmt"

	"github.com/retail-cortex/code_puppy/pkg/skills"
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
	Error        string   `json:"error,omitempty"`
}

// NewActivateSkillTool creates an ADK tool for activating a skill.
func NewActivateSkillTool(provider *skills.Provider) (tool.Tool, error) {
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

			return ActivateSkillOutput{
				SkillName:    skill.Name,
				Instructions: skill.Content,
				Resources:    skill.Resources,
			}, nil
		},
	)
}
