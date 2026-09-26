package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	adksession "google.golang.org/adk/v2/session"
)

// conversation creates an active session with two messages and a model
// event log (as the engine writes it) in the storage directory.
func conversation(t *testing.T) (*Storage, *PersistentService, *SessionRecord, string) {
	t.Helper()
	st, dir := newStorage(t)
	st.SetWorkspace("/work")
	svc, err := NewPersistentService(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := st.CreateSession("", "refactor", "code-puppy")
	if err != nil {
		t.Fatal(err)
	}
	st.AddMessage("user", "remember pineapple")
	st.AddMessage("model", "noted")
	created, err := svc.Create(context.Background(), &adksession.CreateRequest{AppName: "app", UserID: "u", SessionID: rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	appendText(t, svc, created.Session, "user", "user", "remember pineapple", false)
	appendText(t, svc, created.Session, "agent", "model", "noted", false)
	return st, svc, rec, dir
}

func TestSnapshotCopiesTheSessionAndLeavesItActive(t *testing.T) {
	st, _, src, dir := conversation(t)
	snap, err := st.Snapshot(src.ID, "before-refactor", false)
	if err != nil {
		t.Fatal(err)
	}
	if snap.ID == src.ID || snap.Name != "before-refactor" || snap.From != src.ID || snap.Workspace != "/work" || snap.MessageCount != 2 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if a := st.Active(); a.ID != src.ID {
		t.Fatalf("active changed to %s", a.ID)
	}
	for _, suffix := range []string{metaSuffix, messagesSuffix, eventsSuffix} {
		info, err := os.Stat(filepath.Join(dir, snap.ID+suffix))
		if err != nil || info.Mode().Perm() != filePerm {
			t.Fatalf("%s: %v %v", suffix, err, info)
		}
	}
	// The source keeps growing on its own.
	st.AddMessage("user", "more")
	found, err := st.FindName("before-refactor")
	if err != nil || found == nil || found.ID != snap.ID || found.MessageCount != 2 {
		t.Fatalf("FindName = %+v, %v", found, err)
	}
}

func TestSnapshotNamesAreUniqueUnlessReplaced(t *testing.T) {
	st, _, src, dir := conversation(t)
	first, err := st.Snapshot(src.ID, "v1", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Snapshot(src.ID, "v1", false); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}
	st.AddMessage("user", "third")
	second, err := st.Snapshot(src.ID, "v1", true)
	if err != nil {
		t.Fatal(err)
	}
	if found, _ := st.FindName("v1"); found == nil || found.ID != second.ID || found.MessageCount != 3 {
		t.Fatalf("after replace: %+v", found)
	}
	for _, suffix := range []string{metaSuffix, messagesSuffix, eventsSuffix} {
		if _, err := os.Stat(filepath.Join(dir, first.ID+suffix)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("replaced snapshot's %s still there: %v", suffix, err)
		}
	}
}

func TestSnapshotRefusesBadNamesAndEmptySessions(t *testing.T) {
	st, _, src, _ := conversation(t)
	for _, name := range []string{"", "../x", "a b", "session-20260101-000000-abcd", ".hidden"} {
		if _, err := st.Snapshot(src.ID, name, false); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
	empty, _ := st.CreateSession("", "empty", "a")
	if _, err := st.Snapshot(empty.ID, "nothing", false); err == nil {
		t.Error("saved an empty session")
	}
	if _, err := st.Snapshot("no-such-session", "x", false); err == nil {
		t.Error("saved a missing session")
	}
}

// Opening a snapshot by name starts a new session with its history, for the
// transcript and for the model (the event log replays); the snapshot itself
// doesn't change when the new session continues.
func TestOpenByNameBranchesAndKeepsTheSnapshot(t *testing.T) {
	st, svc, src, _ := conversation(t)
	snap, err := st.Snapshot(src.ID, "checkpoint", false)
	if err != nil {
		t.Fatal(err)
	}
	st.SetWorkspace("/other")

	rec, branched, err := st.Open("checkpoint")
	if err != nil || !branched {
		t.Fatalf("Open: %v, branched %v", err, branched)
	}
	if rec.ID == snap.ID || rec.ID == src.ID || rec.Name != "" || rec.From != snap.ID || rec.Workspace != "/other" {
		t.Fatalf("branch = %+v", rec)
	}
	if st.Active().ID != rec.ID || len(rec.Messages) != 2 || rec.Messages[0].Content != "remember pineapple" {
		t.Fatalf("active %s, messages %+v", st.Active().ID, rec.Messages)
	}
	// A new process's session service replays the copied event log.
	fresh, err := NewPersistentService(st.dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := eventTexts(t, fresh, rec.ID); !slices.Equal(got, []string{"remember pineapple", "noted"}) {
		t.Fatalf("replayed events = %v", got)
	}

	// Continue the branch: the snapshot keeps its two messages and events.
	st.AddMessage("user", "new direction")
	resp, _ := svc.Get(context.Background(), &adksession.GetRequest{AppName: "app", UserID: "u", SessionID: rec.ID})
	appendText(t, svc, resp.Session, "user", "user", "new direction", false)
	if got := eventTexts(t, fresh, snap.ID); len(got) != 2 {
		t.Fatalf("snapshot events changed: %v", got)
	}
	if again, _ := st.FindName("checkpoint"); again.MessageCount != 2 {
		t.Fatalf("snapshot messages changed: %d", again.MessageCount)
	}

	// Opening it again starts another branch from the same point.
	second, branched, err := st.Open("checkpoint")
	if err != nil || !branched || second.ID == rec.ID || len(second.Messages) != 2 {
		t.Fatalf("second open: %+v %v %v", second, branched, err)
	}
}

// A snapshot opened by its ID is copied too, never continued in place.
func TestOpenASnapshotByIDBranches(t *testing.T) {
	st, _, src, _ := conversation(t)
	snap, _ := st.Snapshot(src.ID, "fixed", false)
	rec, branched, err := st.Open(snap.ID)
	if err != nil || !branched || rec.ID == snap.ID || rec.From != snap.ID {
		t.Fatalf("Open(snapshot id) = %+v %v %v", rec, branched, err)
	}
}

func TestOpenByIDResumesInPlace(t *testing.T) {
	st, _, src, _ := conversation(t)
	st.CreateSession("", "other", "a")
	rec, branched, err := st.Open(src.ID)
	if err != nil || branched || rec.ID != src.ID {
		t.Fatalf("Open(id) = %+v %v %v", rec, branched, err)
	}
	if _, _, err := st.Open("no-such-name"); err == nil {
		t.Fatal("opened a missing name")
	}
}

// Sessions in the single-file format of earlier versions can be saved too.
func TestSnapshotOfALegacySession(t *testing.T) {
	st, dir := newStorage(t)
	legacy := SessionRecord{ID: "legacy-1", Title: "legacy", UpdatedAt: time.Unix(0, 0), Messages: []Message{{Role: "user", Content: "x"}}}
	b, _ := json.Marshal(legacy)
	os.WriteFile(filepath.Join(dir, "legacy-1.json"), b, 0o600)
	snap, err := st.Snapshot("legacy-1", "old-one", false)
	if err != nil {
		t.Fatal(err)
	}
	rec, branched, err := st.Open("old-one")
	if err != nil || !branched || len(rec.Messages) != 1 || snap.MessageCount != 1 {
		t.Fatalf("%+v %v %v", rec, branched, err)
	}
}
