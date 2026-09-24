// Package audit writes an append-only JSONL record of what tools did and
// what the user approved or denied. Secrets are masked before writing.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/redact"
)

// Kinds of audit entries.
const (
	KindSession    = "session_start"
	KindPrompt     = "prompt"
	KindToolCall   = "tool_call"
	KindToolResult = "tool_result"
	KindApproval   = "approval"
	KindDenial     = "denial"
	KindHook       = "hook"
	KindUndo       = "undo"
)

// Entry is one audit record.
type Entry struct {
	Time      time.Time      `json:"time"`
	Session   string         `json:"session,omitempty"`
	Kind      string         `json:"kind"`
	Tool      string         `json:"tool,omitempty"`
	Args      map[string]any `json:"args,omitempty"`
	Detail    string         `json:"detail,omitempty"`
	Decision  string         `json:"decision,omitempty"`
	Error     string         `json:"error,omitempty"`
	Workspace string         `json:"workspace,omitempty"`
}

// Logger appends entries to <dir>/audit-YYYY-MM-DD.jsonl. A nil *Logger is a
// valid no-op, so callers never need to check whether auditing is enabled.
type Logger struct {
	mu        sync.Mutex
	dir       string
	day       string
	f         *os.File
	redactor  *redact.Redactor
	session   string
	workspace string
	now       func() time.Time
}

// Open creates the audit directory (owner-only) and returns a logger.
func Open(dir string, r *redact.Redactor) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create audit dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return &Logger{dir: dir, redactor: r, now: time.Now}, nil
}

// SetContext sets the session and workspace recorded on later entries.
func (l *Logger) SetContext(session, workspace string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.session, l.workspace = session, workspace
}

// Log appends an entry. Failures are reported once to stderr and otherwise
// ignored: auditing must never break the session.
func (l *Logger) Log(e Entry) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	e.Time = l.now().UTC()
	if e.Session == "" {
		e.Session = l.session
	}
	if e.Workspace == "" {
		e.Workspace = l.workspace
	}
	e.Detail = l.redactor.String(e.Detail)
	e.Error = l.redactor.String(e.Error)
	if e.Args != nil {
		e.Args, _ = l.redactor.Value(e.Args).(map[string]any)
	}

	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	if err := l.rotateLocked(e.Time); err != nil {
		fmt.Fprintf(os.Stderr, "audit: %v\n", err)
		return
	}
	_, _ = l.f.Write(append(line, '\n'))
}

func (l *Logger) rotateLocked(t time.Time) error {
	day := t.Format("2006-01-02")
	if l.f != nil && day == l.day {
		return nil
	}
	if l.f != nil {
		l.f.Close()
	}
	f, err := os.OpenFile(filepath.Join(l.dir, "audit-"+day+".jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		l.f = nil
		return err
	}
	l.f, l.day = f, day
	return nil
}

// Close closes the current file.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}
