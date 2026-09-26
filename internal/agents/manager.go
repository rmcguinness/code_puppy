package agents

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/retail-cortex/code_puppy/internal/agents/builtin"
	"github.com/retail-cortex/code_puppy/internal/config"
)

// Registry manages discovered and built-in agents.
type Registry struct {
	mu      sync.RWMutex
	agents  map[string]*AgentSpec
	builtin map[string]bool
}

// NewRegistry creates a new Registry and loads built-in embedded agent specs.
func NewRegistry() (*Registry, error) {
	r := &Registry{
		agents:  make(map[string]*AgentSpec),
		builtin: make(map[string]bool),
	}

	if err := r.loadEmbeddedAgents(); err != nil {
		return nil, fmt.Errorf("failed to load embedded agents: %w", err)
	}

	return r, nil
}

func (r *Registry) loadEmbeddedAgents() error {
	entries, err := builtin.FS.ReadDir(".")
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		data, err := builtin.FS.ReadFile(entry.Name())
		if err != nil {
			return fmt.Errorf("failed to read embedded agent %s: %w", entry.Name(), err)
		}

		spec, err := ParseMarkdownSpec(data)
		if err != nil {
			return fmt.Errorf("failed to parse embedded agent %s: %w", entry.Name(), err)
		}

		r.agents[spec.Name] = spec
		r.builtin[spec.Name] = true
	}

	return nil
}

// LoadExternalAgents scans directories for user-defined .md agent specifications.
// A leading "~" is expanded. External specs may add new agents but may not
// replace built-in ones, since that would let a directory silently swap the
// system prompt and tool list of a trusted persona. Rejected or unparsable
// specs are reported in the returned (joined) error; valid ones are still loaded.
func (r *Registry) LoadExternalAgents(dirs ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var errs []error
	for _, dir := range dirs {
		dir = config.ExpandHome(dir)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}

		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
				return nil
			}

			data, err := os.ReadFile(path)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", path, err))
				return nil
			}

			spec, err := ParseMarkdownSpec(data)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", path, err))
				return nil
			}
			if r.builtin[spec.Name] {
				errs = append(errs, fmt.Errorf("%s: agent name %q is reserved by a built-in agent", path, spec.Name))
				return nil
			}
			r.agents[spec.Name] = spec
			return nil
		})
		if err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// Get retrieves an agent spec by name.
func (r *Registry) Get(name string) (*AgentSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	spec, ok := r.agents[name]
	return spec, ok
}

// List returns all registered agents sorted by name.
func (r *Registry) List() []*AgentSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()

	list := make([]*AgentSpec, 0, len(r.agents))
	for _, spec := range r.agents {
		list = append(list, spec)
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})

	return list
}
