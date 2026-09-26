package workers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// State is whether a worker may run.
type State string

const (
	// StateNew: found but never enabled.
	StateNew State = "new"
	// StateEnabled: enabled at its current hash; it runs on schedule.
	StateEnabled State = "enabled"
	// StateDisabled: turned off.
	StateDisabled State = "disabled"
	// StateChanged: enabled at an older hash; the files changed since, so
	// it doesn't run until re-enabled.
	StateChanged State = "changed"
	// StateInvalid: WORKER.md can't be used.
	StateInvalid State = "invalid"
)

// ErrHashMismatch reports enabling a worker at a hash other than its
// current one: the files changed after they were reviewed.
var ErrHashMismatch = errors.New("the worker changed since it was reviewed")

// entry is what the store keeps for one worker.
type entry struct {
	Hash    string    `json:"hash"`
	Enabled bool      `json:"enabled"`
	Changed time.Time `json:"changed"`
}

// Store keeps which workers are enabled, at which hash, in one file for
// the user (~/.code_puppy/workers.json). The service is its only writer.
type Store struct {
	path string

	mu         sync.Mutex
	workspaces map[string]map[string]entry // by workspace directory, then name
}

// OpenStore reads the store at path; a missing file is an empty store.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, workspaces: map[string]map[string]entry{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		Workspaces map[string]map[string]entry `json:"workspaces"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if file.Workspaces != nil {
		s.workspaces = file.Workspaces
	}
	return s, nil
}

// State says whether the worker w, found in the workspace dir, may run.
// A worker Load reported invalid is StateInvalid.
func (s *Store) State(dir string, w *Worker, loadErr error) State {
	if loadErr != nil {
		return StateInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.workspaces[dir][w.Name]
	switch {
	case !ok:
		return StateNew
	case !e.Enabled:
		return StateDisabled
	case e.Hash != w.Hash:
		return StateChanged
	}
	return StateEnabled
}

// Enable enables w at hash, which must be its current hash.
func (s *Store) Enable(dir string, w *Worker, hash string) error {
	if hash != w.Hash {
		return fmt.Errorf("%w: reviewed %s, now %s", ErrHashMismatch, hash, w.Hash)
	}
	return s.set(dir, w.Name, entry{Hash: hash, Enabled: true, Changed: time.Now()})
}

// Disable turns the named worker off.
func (s *Store) Disable(dir, name string) error {
	s.mu.Lock()
	e := s.workspaces[dir][name]
	s.mu.Unlock()
	e.Enabled, e.Changed = false, time.Now()
	return s.set(dir, name, e)
}

// Workspaces are the directories with at least one enabled worker, which
// the service watches even when no client has them open.
func (s *Store) Workspaces() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for dir, workers := range s.workspaces {
		for _, e := range workers {
			if e.Enabled {
				out = append(out, dir)
				break
			}
		}
	}
	slices.Sort(out)
	return out
}

func (s *Store) set(dir, name string, e entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workspaces[dir] == nil {
		s.workspaces[dir] = map[string]entry{}
	}
	s.workspaces[dir][name] = e
	return s.saveLocked()
}

// saveLocked writes the store atomically; s.mu must be held.
func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(struct {
		Workspaces map[string]map[string]entry `json:"workspaces"`
	}{s.workspaces}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".workers-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
