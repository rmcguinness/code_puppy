package app

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/retail-cortex/code_puppy/internal/session"
)

// Saved sessions: listing, starting, resuming, snapshots and renaming.

// SessionInfo describes a saved session.
type SessionInfo struct {
	ID    string
	Title string // "" until the first prompt names it
	Agent string
	// Workspace is the directory the session belongs to ("" for sessions
	// saved before sessions were scoped to one).
	Workspace string
	// Snapshot is the name of a snapshot saved with SaveSnapshot ("" for an
	// ordinary session).
	Snapshot string
	// From is the session this one was copied from: the saved session for a
	// snapshot, the snapshot for a session started from one.
	From         string
	MessageCount int
	Created      time.Time
	Updated      time.Time
	// Messages are filled in for the active session and for sessions
	// opened by OpenSession or LoadSession; lists leave them out.
	Messages []Message
}

// Message is one message in a session's transcript.
type Message struct {
	Role string // "user" or "model"
	Text string
	Time time.Time
}

// ErrNoActiveSession reports an operation on the active session when there
// is none.
var ErrNoActiveSession = errors.New("no active session")

// ErrSnapshotNameTaken reports a snapshot name already in use (SaveSnapshot
// with force replaces it).
var ErrSnapshotNameTaken = session.ErrNameTaken

func sessionInfo(r *session.SessionRecord) SessionInfo {
	info := SessionInfo{
		ID: r.ID, Title: r.Title, Agent: r.Agent, Workspace: r.Workspace, Snapshot: r.Name, From: r.From,
		MessageCount: r.MessageCount, Created: r.CreatedAt, Updated: r.UpdatedAt,
	}
	for _, m := range r.Messages {
		info.Messages = append(info.Messages, Message{Role: m.Role, Text: m.Content, Time: m.Timestamp})
	}
	return info
}

func sessionInfos(list []*session.SessionRecord) []SessionInfo {
	out := make([]SessionInfo, len(list))
	for i, r := range list {
		out[i] = sessionInfo(r)
	}
	return out
}

// Dir is the workspace directory.
func (w *Workspace) Dir() string { return w.tools.Workspace().Dir() }

// ListSessions returns this workspace's sessions, or with all every saved
// session, newest first. Messages are left out.
func (w *Workspace) ListSessions(all bool) ([]SessionInfo, error) {
	var list []*session.SessionRecord
	var err error
	if all {
		list, err = w.storage.List()
	} else {
		list, err = w.storage.ListWorkspace(w.storage.Workspace())
	}
	return sessionInfos(list), err
}

// ActiveSession returns the session prompts go to, with its messages.
func (w *Workspace) ActiveSession() (SessionInfo, bool) {
	r := w.storage.Active()
	if r == nil {
		return SessionInfo{}, false
	}
	return sessionInfo(r), true
}

// NewSession starts a new session for the active agent and makes it
// active. It is named after its first prompt.
func (w *Workspace) NewSession() (SessionInfo, error) {
	r, err := w.storage.CreateSession(session.NewSessionID(), "", w.engine.ActiveAgent())
	if err != nil {
		return SessionInfo{}, err
	}
	return sessionInfo(r), nil
}

// LoadSession makes the session ref names active: an ID, or the name of a
// snapshot, which starts a new session copied from it (branched). A
// session from another workspace can be loaded by ID.
func (w *Workspace) LoadSession(ref string) (s SessionInfo, branched bool, err error) {
	r, branched, err := w.storage.Open(ref)
	if err != nil {
		return SessionInfo{}, false, err
	}
	return sessionInfo(r), branched, nil
}

// SaveSnapshot saves a copy of the active session under name. Snapshots are
// never continued in place: loading one starts a new session from it.
func (w *Workspace) SaveSnapshot(name string, force bool) (SessionInfo, error) {
	active := w.storage.Active()
	if active == nil {
		return SessionInfo{}, ErrNoActiveSession
	}
	r, err := w.storage.Snapshot(active.ID, name, force)
	if err != nil {
		return SessionInfo{}, err
	}
	return sessionInfo(r), nil
}

// RenameSession sets the active session's title. An empty title is refused.
func (w *Workspace) RenameSession(title string) (SessionInfo, error) {
	if w.storage.Active() == nil {
		return SessionInfo{}, ErrNoActiveSession
	}
	if err := w.storage.Rename(title); err != nil {
		return SessionInfo{}, err
	}
	s, _ := w.ActiveSession()
	return s, nil
}

// OpenSession picks the session to use and points the audit log at it: the
// session resume names (an ID, or a snapshot to start a new session from),
// the most recent one in this workspace when cont is set or resume is
// "latest", or else a new session. It reports whether a session was resumed.
func (w *Workspace) OpenSession(resume string, cont bool) (SessionInfo, bool, error) {
	// A new session is named after its first prompt.
	rec, resumed, err := selectSession(w.storage, resume, cont, "", w.engine.ActiveAgent())
	if err != nil {
		return SessionInfo{}, false, err
	}
	w.audit.SetContext(rec.ID, w.Dir())
	return sessionInfo(rec), resumed, nil
}

func selectSession(st *session.Storage, resume string, cont bool, title, agent string) (*session.SessionRecord, bool, error) {
	if resume == "" && !cont {
		rec, err := st.CreateSession("", title, agent)
		return rec, false, err
	}
	if resume == "" || resume == "latest" {
		list, err := st.ListWorkspace(st.Workspace())
		if err != nil {
			return nil, false, err
		}
		// Snapshots are saved copies, not conversations to carry on.
		list = slices.DeleteFunc(list, func(r *session.SessionRecord) bool { return r.Name != "" })
		if len(list) == 0 {
			return nil, false, &ResumeError{errNoSavedSessions(st.Workspace())}
		}
		resume = list[0].ID
	}
	rec, _, err := st.Open(resume) // an ID, or a snapshot name to start from
	if err != nil {
		return nil, false, &ResumeError{err}
	}
	return rec, true, nil
}

func errNoSavedSessions(workspace string) error {
	return fmt.Errorf("no saved sessions for %s (use --resume <id> for a session from another directory)", workspace)
}
