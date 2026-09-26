package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// ErrNameTaken is returned by Snapshot when another snapshot has the name.
var ErrNameTaken = errors.New("a snapshot with this name already exists")

// ValidateName reports whether name can label a snapshot. Names follow the
// ID rules (they never reach a path, but are shown and typed like IDs) and
// can't start with "session-", so a name is never mistaken for an ID.
// "latest" is taken by --resume.
func ValidateName(name string) error {
	if !validID.MatchString(name) || strings.HasPrefix(name, "session-") || name == "latest" {
		return fmt.Errorf("invalid snapshot name %q: use letters, digits, '.', '_' and '-', not starting with \"session-\", and not \"latest\"", name)
	}
	return nil
}

// FindName returns the snapshot called name, or nil. Names are unique
// across workspaces, like IDs.
func (s *Storage) FindName(name string) (*SessionRecord, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, r := range all {
		if r.Name == name {
			return r, nil
		}
	}
	return nil, nil
}

// Snapshot saves a named copy of session srcID: its transcript, metadata
// and the model's event log, under a new ID. The source is left as it is
// and stays active. An existing snapshot with the name is an ErrNameTaken
// error unless replace is set, in which case it is deleted once the new
// one is written.
func (s *Storage) Snapshot(srcID, name string, replace bool) (*SessionRecord, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	old, err := s.FindName(name)
	if err != nil {
		return nil, err
	}
	if old != nil && !replace {
		return nil, fmt.Errorf("%w: %s", ErrNameTaken, name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	src, err := s.readSourceLocked(srcID)
	if err != nil {
		return nil, err
	}
	if len(src.Messages) == 0 && !s.hasEventsLocked(srcID) {
		return nil, errors.New("the session has nothing to save yet")
	}
	now := time.Now()
	rec := &SessionRecord{
		ID: NewSessionID(), Title: src.Title, Agent: src.Agent, CreatedAt: now, UpdatedAt: now,
		Workspace: src.Workspace, Name: name, From: srcID,
	}
	if err := s.copyLocked(src, rec); err != nil {
		return nil, err
	}
	if old != nil && old.ID != rec.ID {
		s.removeLocked(old.ID)
	}
	return rec, nil
}

// Open makes a session active by ID or snapshot name. An ID resumes that
// session in place. A snapshot, by name or by ID, is never continued: a
// new session copied from it starts instead (branched is true), so the
// snapshot stays as it was saved.
func (s *Storage) Open(ref string) (rec *SessionRecord, branched bool, err error) {
	snap, err := s.snapshotFor(ref)
	if err != nil {
		return nil, false, err
	}
	if snap != "" {
		rec, err := s.branch(snap)
		return rec, err == nil, err
	}
	rec, err = s.Load(ref)
	return rec, false, err
}

// snapshotFor returns the ID of the snapshot ref names or is, or "".
func (s *Storage) snapshotFor(ref string) (string, error) {
	if ValidateName(ref) == nil {
		snap, err := s.FindName(ref)
		if err != nil {
			return "", err
		}
		if snap != nil {
			return snap.ID, nil
		}
	}
	if ValidateID(ref) != nil {
		return "", nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if rec, err := s.readMeta(ref); err == nil && rec.Name != "" {
		return rec.ID, nil
	}
	return "", nil
}

// branch copies session srcID to a new, unnamed session and makes it active.
func (s *Storage) branch(srcID string) (*SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, err := s.readSourceLocked(srcID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	rec := &SessionRecord{
		ID: NewSessionID(), Title: src.Title, Agent: src.Agent, CreatedAt: now, UpdatedAt: now,
		Workspace: s.workspace, From: srcID,
	}
	if rec.Workspace == "" {
		rec.Workspace = src.Workspace
	}
	if err := s.copyLocked(src, rec); err != nil {
		return nil, err
	}
	rec.Messages = src.Messages
	rec.MessageCount = len(src.Messages)
	s.active = rec
	return rec, nil
}

// readSourceLocked reads a session with its messages, including sessions
// in the single-file format of earlier versions.
func (s *Storage) readSourceLocked(id string) (*SessionRecord, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	rec, err := s.readMeta(id)
	if errors.Is(err, os.ErrNotExist) {
		rec, err = s.readLegacy(id)
	} else if err == nil {
		rec.Messages, err = s.readMessages(id)
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("session '%s' not found", id)
	}
	return rec, err
}

func (s *Storage) hasEventsLocked(id string) bool {
	info, err := os.Stat(s.path(id, eventsSuffix))
	return err == nil && info.Size() > 0
}

// copyLocked writes rec as a copy of src: messages and the event log
// first, then the metadata, which is what makes a session visible, so an
// interrupted copy never shows up half-written.
func (s *Storage) copyLocked(src, rec *SessionRecord) (err error) {
	defer func() {
		if err != nil {
			s.removeLocked(rec.ID)
		}
	}()
	var msgs []byte
	for _, m := range src.Messages {
		line, err := jsonLine(m)
		if err != nil {
			return err
		}
		msgs = append(msgs, line...)
	}
	if err := writeNew(s.path(rec.ID, messagesSuffix), bytes.NewReader(msgs)); err != nil {
		return err
	}
	if f, err := os.Open(s.path(src.ID, eventsSuffix)); err == nil {
		err = writeNew(s.path(rec.ID, eventsSuffix), f)
		f.Close()
		if err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	rec.MessageCount = len(src.Messages)
	return s.writeMeta(rec)
}

// removeLocked deletes a session's files, metadata first so it stops being
// listed even if a later removal fails.
func (s *Storage) removeLocked(id string) {
	for _, suffix := range []string{metaSuffix, messagesSuffix, eventsSuffix, legacySuffix} {
		_ = os.Remove(s.path(id, suffix))
	}
}

func jsonLine(m Message) ([]byte, error) {
	line, err := json.Marshal(m)
	return append(line, '\n'), err
}

// writeNew creates path (owner-only; it must not exist) with r's contents.
func writeNew(path string, r io.Reader) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
