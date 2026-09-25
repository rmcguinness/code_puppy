package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStorage(t *testing.T) (*Storage, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sessions")
	s, err := NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestStoragePermissions(t *testing.T) {
	s, dir := newStorage(t)
	rec, err := s.CreateSession("", "t", "code-puppy")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddMessage("user", "my password is hunter2"); err != nil {
		t.Fatal(err)
	}

	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("session dir mode = %v, want 0700", info.Mode().Perm())
	}
	for _, suffix := range []string{metaSuffix, messagesSuffix} {
		info, err := os.Stat(filepath.Join(dir, rec.ID+suffix))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", suffix, info.Mode().Perm())
		}
	}
}

func TestStorageTightensExistingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "old")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStorage(dir); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("existing dir not tightened: %v", info.Mode().Perm())
	}
}

func TestSessionIDValidation(t *testing.T) {
	s, dir := newStorage(t)
	outside := filepath.Join(filepath.Dir(dir), "stolen.json")
	_ = os.WriteFile(outside, []byte(`{"id":"x"}`), 0o600)

	// Negative: traversal and unsafe IDs rejected for both create and load.
	for _, id := range []string{"../stolen", "a/b", ".hidden", "..", "x y", strings.Repeat("a", 200)} {
		if _, err := s.CreateSession(id, "t", "a"); !errors.Is(err, ErrInvalidID) {
			t.Errorf("CreateSession(%q) expected ErrInvalidID, got %v", id, err)
		}
		if _, err := s.Load(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Load(%q) expected ErrInvalidID, got %v", id, err)
		}
	}
	// Positive: well-formed IDs accepted.
	for _, id := range []string{"session-1", "abc.DEF_9"} {
		if _, err := s.CreateSession(id, "t", "a"); err != nil {
			t.Errorf("CreateSession(%q): %v", id, err)
		}
	}
}

func TestNewSessionIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewSessionID()
		if err := ValidateID(id); err != nil {
			t.Fatalf("generated invalid id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	// Default-ID sessions created in the same second no longer collide.
	s, _ := newStorage(t)
	a, _ := s.CreateSession("", "a", "x")
	b, _ := s.CreateSession("", "b", "x")
	if a.ID == b.ID {
		t.Errorf("sessions created back-to-back share ID %q", a.ID)
	}
}

func TestAddMessageAppendsAndLoadRoundTrip(t *testing.T) {
	s, dir := newStorage(t)

	// Negative: no active session.
	if err := s.AddMessage("user", "hi"); err == nil {
		t.Error("expected error without active session")
	}

	rec, _ := s.CreateSession("", "Chat", "helios")
	for i := 0; i < 3; i++ {
		if err := s.AddMessage("user", "msg"); err != nil {
			t.Fatal(err)
		}
	}
	// Messages file is append-only JSONL: one line per message.
	data, _ := os.ReadFile(filepath.Join(dir, rec.ID+messagesSuffix))
	if n := strings.Count(string(data), "\n"); n != 3 {
		t.Errorf("expected 3 JSONL lines, got %d", n)
	}
	// Metadata stays small and does not embed messages.
	meta, _ := os.ReadFile(filepath.Join(dir, rec.ID+metaSuffix))
	if strings.Contains(string(meta), `"messages"`) {
		t.Error("metadata file should not contain messages")
	}

	s2, _ := NewStorage(dir)
	loaded, err := s2.Load(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 3 || loaded.MessageCount != 3 || loaded.Agent != "helios" {
		t.Errorf("unexpected loaded session %+v", loaded)
	}
	if s2.Active().ID != rec.ID {
		t.Error("Load should make the session active")
	}

	// Negative: unknown session.
	if _, err := s2.Load("nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not found, got %v", err)
	}
}

func TestLoadToleratesTornLine(t *testing.T) {
	s, dir := newStorage(t)
	rec, _ := s.CreateSession("", "t", "a")
	_ = s.AddMessage("user", "complete")
	f, _ := os.OpenFile(filepath.Join(dir, rec.ID+messagesSuffix), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"role":"model","content":"trunc`)
	f.Close()

	loaded, err := s.Load(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 1 || loaded.Messages[0].Content != "complete" {
		t.Errorf("expected torn line skipped, got %+v", loaded.Messages)
	}
}

func TestListMetadataAndLegacy(t *testing.T) {
	s, dir := newStorage(t)
	old, _ := s.CreateSession("", "old", "a")
	_ = s.AddMessage("user", "1")
	time.Sleep(10 * time.Millisecond)
	newer, _ := s.CreateSession("", "new", "a")
	_ = s.AddMessage("user", "1")
	_ = s.AddMessage("user", "2")

	legacy := SessionRecord{ID: "legacy-1", Title: "legacy", UpdatedAt: time.Unix(0, 0), Messages: []Message{{Role: "user", Content: "x"}}}
	b, _ := json.Marshal(legacy)
	_ = os.WriteFile(filepath.Join(dir, "legacy-1.json"), b, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "garbage.meta.json"), []byte("{not json"), 0o600)

	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 sessions (corrupt skipped), got %d", len(list))
	}
	if list[0].ID != newer.ID || list[1].ID != old.ID || list[2].ID != "legacy-1" {
		t.Errorf("unexpected order: %s, %s, %s", list[0].ID, list[1].ID, list[2].ID)
	}
	if list[0].MessageCount != 2 || list[2].MessageCount != 1 {
		t.Errorf("unexpected message counts %d, %d", list[0].MessageCount, list[2].MessageCount)
	}
	for _, r := range list {
		if len(r.Messages) != 0 {
			t.Errorf("List should not load message bodies for %s", r.ID)
		}
	}

	// Legacy sessions still load.
	if rec, err := s.Load("legacy-1"); err != nil || len(rec.Messages) != 1 {
		t.Errorf("legacy load failed: %v %+v", err, rec)
	}
}

func TestActiveReturnsSnapshot(t *testing.T) {
	s, _ := newStorage(t)
	if s.Active() != nil {
		t.Error("expected nil active session initially")
	}
	_, _ = s.CreateSession("", "t", "a")
	snap := s.Active()
	snap.Title = "mutated"
	_ = s.AddMessage("user", "x")
	if s.Active().Title == "mutated" || len(snap.Messages) != 0 {
		t.Error("Active should return an independent copy")
	}
}

func TestWorkspaceScopedSessions(t *testing.T) {
	s, dir := newStorage(t)
	s.SetWorkspace("/work/a")
	a1, _ := s.CreateSession("", "a1", "x")
	time.Sleep(5 * time.Millisecond)
	s.SetWorkspace("/work/b")
	b1, _ := s.CreateSession("", "b1", "x")
	time.Sleep(5 * time.Millisecond)
	s.SetWorkspace("/work/a")
	a2, _ := s.CreateSession("", "a2", "x")

	if a1.Workspace != "/work/a" || b1.Workspace != "/work/b" {
		t.Errorf("workspace not recorded: %q %q", a1.Workspace, b1.Workspace)
	}
	listA, _ := s.ListWorkspace("/work/a")
	if len(listA) != 2 || listA[0].ID != a2.ID || listA[1].ID != a1.ID {
		t.Errorf("workspace a sessions: %+v", listA)
	}
	if listB, _ := s.ListWorkspace("/work/b"); len(listB) != 1 || listB[0].ID != b1.ID {
		t.Errorf("workspace b sessions: %+v", listB)
	}
	if all, _ := s.List(); len(all) != 3 {
		t.Errorf("List should include every workspace, got %d", len(all))
	}
	if none, _ := s.ListWorkspace("/work/c"); len(none) != 0 {
		t.Errorf("unknown workspace should be empty: %+v", none)
	}

	// Workspace survives restarts (it's in the metadata file).
	s2, _ := NewStorage(dir)
	if l, _ := s2.ListWorkspace("/work/b"); len(l) != 1 {
		t.Error("workspace not persisted")
	}
}

func TestLegacySessionAdoptedOnResume(t *testing.T) {
	s, dir := newStorage(t)
	legacy, _ := s.CreateSession("", "old", "x") // no workspace set: legacy record
	if legacy.Workspace != "" {
		t.Fatal("expected legacy session without workspace")
	}
	s2, _ := NewStorage(dir)
	s2.SetWorkspace("/work/a")
	if l, _ := s2.ListWorkspace("/work/a"); len(l) != 0 {
		t.Error("legacy sessions must not appear in a workspace listing before being resumed")
	}
	rec, err := s2.Load(legacy.ID)
	if err != nil || rec.Workspace != "/work/a" {
		t.Fatalf("legacy session not adopted: %+v %v", rec, err)
	}
	s3, _ := NewStorage(dir)
	if l, _ := s3.ListWorkspace("/work/a"); len(l) != 1 {
		t.Error("adoption not persisted")
	}
	// Sessions that already have a workspace keep it when loaded elsewhere.
	s3.SetWorkspace("/work/b")
	if rec, _ := s3.Load(legacy.ID); rec.Workspace != "/work/a" {
		t.Errorf("owned session re-assigned to %q", rec.Workspace)
	}
}
