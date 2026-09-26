package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ApprovalRule is a persisted "always allow" decision.
type ApprovalRule struct {
	Key   string    `json:"key"`
	Label string    `json:"label,omitempty"`
	Added time.Time `json:"added"`
}

// ApprovalStore persists "always allow" rules in a JSON file (owner-only).
// Deny rules and the sandbox are always checked first, so a saved rule can
// never unlock something policy forbids.
type ApprovalStore struct {
	mu    sync.RWMutex
	path  string
	rules map[string]ApprovalRule
}

type approvalFile struct {
	Version int            `json:"version"`
	Rules   []ApprovalRule `json:"rules"`
}

// OpenApprovalStore loads rules from path; a missing file is an empty store.
func OpenApprovalStore(path string) (*ApprovalStore, error) {
	s := &ApprovalStore{path: path, rules: map[string]ApprovalRule{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f approvalFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("corrupt approvals file %s: %w", path, err)
	}
	for _, r := range f.Rules {
		s.rules[r.Key] = r
	}
	return s, nil
}

// Has reports whether key is allowed. A nil store has no rules.
func (s *ApprovalStore) Has(key string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.rules[key]
	return ok
}

// Rules returns all rules sorted by key.
func (s *ApprovalStore) Rules() []ApprovalRule {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ApprovalRule, 0, len(s.rules))
	for _, r := range s.rules {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Add persists a rule.
func (s *ApprovalStore) Add(key, label string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules[key] = ApprovalRule{Key: key, Label: label, Added: time.Now().UTC()}
	return s.saveLocked()
}

// Remove deletes a rule; it reports whether it existed.
func (s *ApprovalStore) Remove(key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rules[key]; !ok {
		return false, nil
	}
	delete(s.rules, key)
	return true, s.saveLocked()
}

func (s *ApprovalStore) saveLocked() error {
	f := approvalFile{Version: 1}
	for _, r := range s.rules {
		f.Rules = append(f.Rules, r)
	}
	sort.Slice(f.Rules, func(i, j int) bool { return f.Rules[i].Key < f.Rules[j].Key })
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".approvals-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
