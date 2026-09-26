package agents

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentMetadata holds frontmatter metadata defined in the markdown header.
type AgentMetadata struct {
	Name         string   `yaml:"name"`
	DisplayName  string   `yaml:"display_name"`
	Description  string   `yaml:"description"`
	Tools        []string `yaml:"tools"`
	DefaultModel string   `yaml:"default_model,omitempty"`
	AgencyLevel  string   `yaml:"agency_level,omitempty"`
}

// AgentSpec represents a parsed agent specification with frontmatter and markdown body.
type AgentSpec struct {
	AgentMetadata
	SystemPrompt string
}

// ParseMarkdownSpec parses a markdown file containing YAML frontmatter.
func ParseMarkdownSpec(content []byte) (*AgentSpec, error) {
	trimmed := bytes.TrimSpace(content)
	if !bytes.HasPrefix(trimmed, []byte("---")) {
		return nil, fmt.Errorf("markdown spec missing starting frontmatter delimiter '---'")
	}

	// Find closing delimiter
	rest := trimmed[3:]
	idx := bytes.Index(rest, []byte("---"))
	if idx == -1 {
		return nil, fmt.Errorf("markdown spec missing closing frontmatter delimiter '---'")
	}

	frontmatterBytes := rest[:idx]
	promptBytes := bytes.TrimSpace(rest[idx+3:])

	var meta AgentMetadata
	if err := yaml.Unmarshal(frontmatterBytes, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse YAML frontmatter: %w", err)
	}

	if meta.Name == "" {
		return nil, fmt.Errorf("agent spec must declare a 'name' in frontmatter")
	}

	return &AgentSpec{
		AgentMetadata: meta,
		SystemPrompt:  string(promptBytes),
	}, nil
}

// InterpolatePrompt replaces variables like {puppy_name}, {owner_name}, and agency rules.
func (spec *AgentSpec) InterpolatePrompt(puppyName, ownerName, agencyLevel string) string {
	prompt := spec.SystemPrompt
	if puppyName == "" {
		puppyName = "Code Puppy"
	}
	if ownerName == "" {
		ownerName = "Developer"
	}
	if agencyLevel == "" {
		agencyLevel = "high"
	}

	prompt = strings.ReplaceAll(prompt, "{puppy_name}", puppyName)
	prompt = strings.ReplaceAll(prompt, "{owner_name}", ownerName)
	prompt = strings.ReplaceAll(prompt, "{{puppy_name}}", puppyName)
	prompt = strings.ReplaceAll(prompt, "{{owner_name}}", ownerName)

	agencyInstructions := getAgencyInstructions(agencyLevel)
	prompt = strings.ReplaceAll(prompt, "{agency_instructions}", agencyInstructions)
	prompt = strings.ReplaceAll(prompt, "{{agency_instructions}}", agencyInstructions)

	return prompt
}

func getAgencyInstructions(level string) string {
	switch strings.ToLower(level) {
	case "low":
		return "- You are at LOW agency: work one step at a time. After each meaningful unit of work, stop, summarize what you did, and ask before continuing.\n- Never take consequential or irreversible actions without explicit approval."
	case "medium":
		return "- You are at MEDIUM agency: complete routine, clearly-requested work without asking, but pause and check in at major milestones or before consequential changes."
	case "extreme":
		return "- You are at EXTREME agency: complete the requested task autonomously with maximal persistence.\n- If a background process gates completion, do not stop and force the user to reprompt you. Check progress when work remains. Be as agentic as possible."
	case "high":
		fallthrough
	default:
		return "- Complete the requested task autonomously. Do not ask for routine permission to continue work the user already requested. Ask only when blocked by missing requirements or irreversible actions requiring approval.\n- Continue autonomously unless user input is definitively required."
	}
}
