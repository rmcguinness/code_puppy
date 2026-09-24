package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	adksession "google.golang.org/adk/v2/session"
)

const eventsSuffix = ".events.jsonl"

// PersistentService is an ADK session service that keeps sessions in memory
// and appends every final event to <dir>/<session>.events.jsonl, so a
// conversation (including tool calls and compaction summaries) can be resumed
// in a later process by using the same session ID.
type PersistentService struct {
	inner adksession.Service
	dir   string
	mu    sync.Mutex // serialises loads and file appends
}

// NewPersistentService stores event logs in dir (created owner-only).
func NewPersistentService(dir string) (*PersistentService, error) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, err
	}
	return &PersistentService{inner: adksession.InMemoryService(), dir: dir}, nil
}

func (p *PersistentService) eventsPath(id string) (string, error) {
	if err := ValidateID(id); err != nil {
		return "", err
	}
	return filepath.Join(p.dir, id+eventsSuffix), nil
}

// HasEvents reports whether a stored event log exists for id.
func (p *PersistentService) HasEvents(id string) bool {
	path, err := p.eventsPath(id)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// Create creates a session, replaying stored events if the ID was used before.
func (p *PersistentService) Create(ctx context.Context, req *adksession.CreateRequest) (*adksession.CreateResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	resp, err := p.inner.Create(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := p.replayLocked(ctx, resp.Session); err != nil {
		return nil, err
	}
	return resp, nil
}

// Get returns a session, loading it from disk on first access after a restart.
func (p *PersistentService) Get(ctx context.Context, req *adksession.GetRequest) (*adksession.GetResponse, error) {
	resp, err := p.inner.Get(ctx, req)
	if err == nil || !p.HasEvents(req.SessionID) {
		return resp, err
	}
	p.mu.Lock()
	if _, again := p.inner.Get(ctx, req); again != nil {
		created, cerr := p.inner.Create(ctx, &adksession.CreateRequest{AppName: req.AppName, UserID: req.UserID, SessionID: req.SessionID})
		if cerr != nil {
			p.mu.Unlock()
			return nil, cerr
		}
		if rerr := p.replayLocked(ctx, created.Session); rerr != nil {
			p.mu.Unlock()
			return nil, rerr
		}
	}
	p.mu.Unlock()
	return p.inner.Get(ctx, req)
}

// List delegates to the in-memory service.
func (p *PersistentService) List(ctx context.Context, req *adksession.ListRequest) (*adksession.ListResponse, error) {
	return p.inner.List(ctx, req)
}

// Delete removes the session and its stored events.
func (p *PersistentService) Delete(ctx context.Context, req *adksession.DeleteRequest) error {
	if path, err := p.eventsPath(req.SessionID); err == nil {
		_ = os.Remove(path)
	}
	return p.inner.Delete(ctx, req)
}

// AppendEvent records the event in memory, then durably appends final events.
func (p *PersistentService) AppendEvent(ctx context.Context, s adksession.Session, ev *adksession.Event) error {
	if err := p.inner.AppendEvent(ctx, s, ev); err != nil {
		return err
	}
	if ev == nil || ev.Partial {
		return nil
	}
	path, err := p.eventsPath(s.ID())
	if err != nil {
		return err
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, filePerm)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (p *PersistentService) replayLocked(ctx context.Context, s adksession.Session) error {
	path, err := p.eventsPath(s.ID())
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev adksession.Event
		if json.Unmarshal(line, &ev) != nil {
			continue // a torn final line from a crash
		}
		if err := p.inner.AppendEvent(ctx, s, &ev); err != nil {
			return fmt.Errorf("replay session %s: %w", s.ID(), err)
		}
	}
	return sc.Err()
}
