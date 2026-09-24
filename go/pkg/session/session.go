package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SessionRecord holds persistent metadata and chat history for a session.
type SessionRecord struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Agent     string    `json:"agent"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Messages  []Message `json:"messages"`
}

// Message represents a single chat turn.
type Message struct {
	Role      string    `json:"role"` // "user", "model", "tool"
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Storage manages persistent sessions on disk.
type Storage struct {
	mu     sync.RWMutex
	dir    string
	active *SessionRecord
}

// NewStorage creates session storage in the specified directory.
func NewStorage(dir string) (*Storage, error) {
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".code_puppy", "sessions")
	}

	if strings.HasPrefix(dir, "~") {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, dir[1:])
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create session dir: %w", err)
	}

	return &Storage{
		dir: dir,
	}, nil
}

// CreateSession starts a new named session.
func (s *Storage) CreateSession(id, title, agent string) *SessionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id == "" {
		id = fmt.Sprintf("session-%d", time.Now().Unix())
	}
	if title == "" {
		title = "New Session"
	}

	rec := &SessionRecord{
		ID:        id,
		Title:     title,
		Agent:     agent,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Messages:  []Message{},
	}
	s.active = rec
	_ = s.saveRecord(rec)
	return rec
}

// AddMessage appends a message to the active session.
func (s *Storage) AddMessage(role, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.active == nil {
		return
	}

	s.active.Messages = append(s.active.Messages, Message{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	})
	s.active.UpdatedAt = time.Now()
	_ = s.saveRecord(s.active)
}

// Active returns the currently active session.
func (s *Storage) Active() *SessionRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

func (s *Storage) saveRecord(rec *SessionRecord) error {
	filePath := filepath.Join(s.dir, rec.ID+".json")
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filePath, data, 0644)
}

// Load loads a session by ID.
func (s *Storage) Load(id string) (*SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := filepath.Join(s.dir, id+".json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("session '%s' not found: %w", id, err)
	}

	var rec SessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("corrupt session file: %w", err)
	}

	s.active = &rec
	return &rec, nil
}

// List returns all saved sessions sorted by update time descending.
func (s *Storage) List() ([]*SessionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}

	var sessions []*SessionRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}

		var rec SessionRecord
		if err := json.Unmarshal(data, &rec); err == nil {
			sessions = append(sessions, &rec)
		}
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})

	return sessions, nil
}
