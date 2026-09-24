package skills

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/retail-cortex/code_puppy/pkg/skills/builtin"
)

// Provider discovers, indexes, and activates skills across embedded and external paths.
type Provider struct {
	mu     sync.RWMutex
	skills map[string]*Skill
}

// NewProvider initializes the skills provider with embedded built-in skills.
func NewProvider() (*Provider, error) {
	p := &Provider{
		skills: make(map[string]*Skill),
	}

	if err := p.loadEmbeddedSkills(); err != nil {
		return nil, fmt.Errorf("failed to load embedded skills: %w", err)
	}

	return p, nil
}

func (p *Provider) loadEmbeddedSkills() error {
	err := fs.WalkDir(builtin.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}

		data, err := builtin.FS.ReadFile(path)
		if err != nil {
			return err
		}

		skill, err := ParseSkillMD(data, "builtin://"+path)
		if err == nil && skill.Name != "" {
			p.skills[skill.Name] = skill
		}
		return nil
	})

	return err
}

// DiscoverExternal scans search directories for SKILL.md files.
func (p *Provider) DiscoverExternal(dirs []string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	homeDir, _ := os.UserHomeDir()

	for _, dir := range dirs {
		expandedDir := dir
		if strings.HasPrefix(expandedDir, "~") {
			expandedDir = filepath.Join(homeDir, expandedDir[1:])
		}

		if _, err := os.Stat(expandedDir); os.IsNotExist(err) {
			continue
		}

		_ = filepath.WalkDir(expandedDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() != "SKILL.md" {
				return nil
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}

			skill, err := ParseSkillMD(data, path)
			if err == nil && skill.Name != "" {
				// Discover any neighboring resource files
				skillDir := filepath.Dir(path)
				resources := []string{}
				entries, _ := os.ReadDir(skillDir)
				for _, entry := range entries {
					if !entry.IsDir() && entry.Name() != "SKILL.md" {
						resources = append(resources, entry.Name())
					}
				}
				skill.Resources = resources
				p.skills[skill.Name] = skill
			}
			return nil
		})
	}

	return nil
}

// List returns all discovered skills sorted by name.
func (p *Provider) List() []*Skill {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]*Skill, 0, len(p.skills))
	for _, skill := range p.skills {
		result = append(result, skill)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})

	return result
}

// Search filters skills matching query in name, description, or tags.
func (p *Provider) Search(query string) []*Skill {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if query == "" {
		return p.List()
	}

	matches := []*Skill{}
	for _, skill := range p.skills {
		if skill.Matches(query) {
			matches = append(matches, skill)
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Name < matches[j].Name
	})

	return matches
}

// Get returns a skill by name.
func (p *Provider) Get(name string) (*Skill, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	s, ok := p.skills[name]
	return s, ok
}
