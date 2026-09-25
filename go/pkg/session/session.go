package session

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Session transcripts can contain secrets pasted by the user or echoed by
// tools, so everything is written owner-only.
const (
	dirPerm  = 0o700
	filePerm = 0o600

	metaSuffix     = ".meta.json"
	messagesSuffix = ".jsonl"
	legacySuffix   = ".json"
)

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ErrInvalidID is returned for session IDs that could escape the storage directory.
var ErrInvalidID = errors.New("invalid session id")

// SessionRecord holds persistent metadata and chat history for a session.
type SessionRecord struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Agent        string    `json:"agent"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
	// Workspace is the canonical workspace directory the session belongs to;
	// empty for sessions created before sessions were workspace-scoped.
	Workspace string `json:"workspace,omitempty"`
	// LastTurn identifies the trace of the most recent turn so the next one,
	// even in a later process, can link to it. Empty with telemetry off.
	LastTurn *TurnRef `json:"last_turn,omitempty"`
	// Messages is populated by Load and for the active session; List leaves it
	// empty and reports MessageCount instead.
	Messages []Message `json:"messages,omitempty"`
}

// Message represents a single chat turn.
type Message struct {
	Role      string    `json:"role"` // "user", "model", "tool"
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Storage manages persistent sessions on disk. Metadata lives in
// <id>.meta.json (small, rewritten atomically) and messages are appended to
// <id>.jsonl, so adding a message costs O(message) rather than O(session).
type Storage struct {
	mu        sync.RWMutex
	dir       string
	active    *SessionRecord
	workspace string
}

// SetWorkspace records the workspace new sessions belong to.
func (s *Storage) SetWorkspace(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspace = dir
}

// Workspace returns the workspace new sessions belong to.
func (s *Storage) Workspace() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.workspace
}

// ListWorkspace returns sessions belonging to workspace, newest first.
func (s *Storage) ListWorkspace(workspace string) ([]*SessionRecord, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []*SessionRecord
	for _, r := range all {
		if r.Workspace == workspace {
			out = append(out, r)
		}
	}
	return out, nil
}

// NewStorage creates session storage in the specified directory.
func NewStorage(dir string) (*Storage, error) {
	if dir == "" || dir == "~" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home directory: %w", err)
		}
		if dir == "" {
			dir = filepath.Join(home, ".code_puppy", "sessions")
		} else {
			dir = filepath.Join(home, dir[1:])
		}
	}

	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("failed to create session dir: %w", err)
	}
	// Tighten permissions on directories created by older versions.
	if err := os.Chmod(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("failed to secure session dir: %w", err)
	}
	return &Storage{dir: dir}, nil
}

// NewSessionID returns a collision-resistant session identifier.
func NewSessionID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("session-%s-%s", time.Now().UTC().Format("20060102-150405"), hex.EncodeToString(b[:]))
}

// ValidateID reports whether id is safe to use as a file name component.
func ValidateID(id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return nil
}

// CreateSession starts a new session and makes it active.
func (s *Storage) CreateSession(id, title, agent string) (*SessionRecord, error) {
	if id == "" {
		id = NewSessionID()
	}
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	if title == "" {
		title = "New Session"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	rec := &SessionRecord{
		ID:        id,
		Title:     title,
		Agent:     agent,
		CreatedAt: now,
		UpdatedAt: now,
		Workspace: s.workspace,
		Messages:  []Message{},
	}
	if err := s.writeMeta(rec); err != nil {
		return nil, err
	}
	s.active = rec
	return rec, nil
}

// AddMessage appends a message to the active session.
func (s *Storage) AddMessage(role, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.active == nil {
		return errors.New("no active session")
	}

	msg := Message{Role: role, Content: content, Timestamp: time.Now()}
	line, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.path(s.active.ID, messagesSuffix), os.O_WRONLY|os.O_CREATE|os.O_APPEND, filePerm)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	s.active.Messages = append(s.active.Messages, msg)
	s.active.MessageCount = len(s.active.Messages)
	s.active.UpdatedAt = msg.Timestamp
	return s.writeMeta(s.active)
}

// Active returns a snapshot of the currently active session, or nil.
func (s *Storage) Active() *SessionRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.active == nil {
		return nil
	}
	cp := *s.active
	cp.Messages = append([]Message(nil), s.active.Messages...)
	return &cp
}

// Load loads a session by ID and makes it active.
func (s *Storage) Load(id string) (*SessionRecord, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.readMeta(id)
	if errors.Is(err, os.ErrNotExist) {
		rec, err = s.readLegacy(id)
	} else if err == nil {
		rec.Messages, err = s.readMessages(id)
		rec.MessageCount = len(rec.Messages)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("session '%s' not found", id)
		}
		return nil, err
	}

	// Sessions saved before sessions were workspace-scoped are adopted by
	// the workspace that resumes them, so --continue finds them afterwards.
	if rec.Workspace == "" && s.workspace != "" {
		rec.Workspace = s.workspace
		if err := s.writeMeta(rec); err != nil {
			return nil, err
		}
	}
	s.active = rec
	return rec, nil
}

// TurnRef is the W3C traceparent of a turn's span and the turn's number
// within the session (1-based).
type TurnRef struct {
	Traceparent string `json:"traceparent"`
	Index       int    `json:"index"`
}

// LastTurn returns the latest turn recorded for session id: from memory
// for the active session, otherwise from its metadata file. It returns
// ("", 0) when there is none.
func (s *Storage) LastTurn(id string) (traceparent string, index int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec := s.active
	if rec == nil || rec.ID != id {
		if ValidateID(id) != nil {
			return "", 0
		}
		var err error
		if rec, err = s.readMeta(id); err != nil {
			return "", 0
		}
	}
	if rec.LastTurn == nil {
		return "", 0
	}
	return rec.LastTurn.Traceparent, rec.LastTurn.Index
}

// SetLastTurn records the latest turn of session id in its metadata.
func (s *Storage) SetLastTurn(id, traceparent string, index int) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ref := &TurnRef{Traceparent: traceparent, Index: index}
	if s.active != nil && s.active.ID == id {
		s.active.LastTurn = ref
		return s.writeMeta(s.active)
	}
	rec, err := s.readMeta(id)
	if err != nil {
		return err
	}
	rec.LastTurn = ref
	return s.writeMeta(rec)
}

// List returns saved session metadata sorted by update time descending.
// Message bodies are not read.
func (s *Storage) List() ([]*SessionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var sessions []*SessionRecord
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, metaSuffix) {
			continue
		}
		if rec, err := s.readMeta(strings.TrimSuffix(name, metaSuffix)); err == nil {
			sessions = append(sessions, rec)
			seen[rec.ID] = true
		}
	}
	// Legacy single-file sessions from earlier versions.
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, legacySuffix) || strings.HasSuffix(name, metaSuffix) {
			continue
		}
		id := strings.TrimSuffix(name, legacySuffix)
		if seen[id] || ValidateID(id) != nil {
			continue
		}
		if rec, err := s.readLegacy(id); err == nil {
			rec.Messages = nil
			sessions = append(sessions, rec)
		}
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	return sessions, nil
}

func (s *Storage) path(id, suffix string) string {
	return filepath.Join(s.dir, id+suffix)
}

func (s *Storage) readMeta(id string) (*SessionRecord, error) {
	data, err := os.ReadFile(s.path(id, metaSuffix))
	if err != nil {
		return nil, err
	}
	var rec SessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("corrupt session metadata %s: %w", id, err)
	}
	rec.Messages = nil
	return &rec, nil
}

func (s *Storage) readMessages(id string) ([]Message, error) {
	f, err := os.Open(s.path(id, messagesSuffix))
	if errors.Is(err, os.ErrNotExist) {
		return []Message{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	msgs := []Message{}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var m Message
			// Skip a torn final line left by a crash mid-append.
			if json.Unmarshal(trimmed, &m) == nil {
				msgs = append(msgs, m)
			}
		}
		if err != nil {
			break
		}
	}
	return msgs, nil
}

func (s *Storage) readLegacy(id string) (*SessionRecord, error) {
	data, err := os.ReadFile(s.path(id, legacySuffix))
	if err != nil {
		return nil, err
	}
	var rec SessionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("corrupt session file: %w", err)
	}
	rec.MessageCount = len(rec.Messages)
	return &rec, nil
}

// writeMeta atomically replaces the metadata file (temp file + rename).
func (s *Storage) writeMeta(rec *SessionRecord) error {
	meta := *rec
	meta.Messages = nil
	data, err := json.MarshalIndent(&meta, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "."+rec.ID+".meta-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, filePerm); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, s.path(rec.ID, metaSuffix)); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
