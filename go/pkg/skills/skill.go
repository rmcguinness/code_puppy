package skills

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillMetadata defines metadata stored in SKILL.md frontmatter.
type SkillMetadata struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tags        []string `yaml:"tags"`
	Version     string   `yaml:"version,omitempty"`
	Author      string   `yaml:"author,omitempty"`
}

// Skill represents an Agent Skill specification.
type Skill struct {
	SkillMetadata
	Content   string   `json:"content"`
	Path      string   `json:"path"`
	Resources []string `json:"resources"`
}

// ParseSkillMD parses a SKILL.md document with YAML frontmatter.
func ParseSkillMD(content []byte, path string) (*Skill, error) {
	trimmed := bytes.TrimSpace(content)
	if !bytes.HasPrefix(trimmed, []byte("---")) {
		return nil, fmt.Errorf("SKILL.md missing starting frontmatter delimiter '---'")
	}

	rest := trimmed[3:]
	idx := bytes.Index(rest, []byte("---"))
	if idx == -1 {
		return nil, fmt.Errorf("SKILL.md missing closing frontmatter delimiter '---'")
	}

	frontmatterBytes := rest[:idx]
	instructionBytes := bytes.TrimSpace(rest[idx+3:])

	var meta SkillMetadata
	if err := yaml.Unmarshal(frontmatterBytes, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse SKILL.md frontmatter: %w", err)
	}

	if meta.Name == "" {
		return nil, fmt.Errorf("SKILL.md must specify a name")
	}

	return &Skill{
		SkillMetadata: meta,
		Content:       string(instructionBytes),
		Path:          path,
		Resources:     []string{},
	}, nil
}

// Matches returns true if the query matches the skill's name, description, or tags.
func (s *Skill) Matches(query string) bool {
	q := strings.ToLower(query)
	if strings.Contains(strings.ToLower(s.Name), q) {
		return true
	}
	if strings.Contains(strings.ToLower(s.Description), q) {
		return true
	}
	for _, tag := range s.Tags {
		if strings.Contains(strings.ToLower(tag), q) {
			return true
		}
	}
	return false
}
