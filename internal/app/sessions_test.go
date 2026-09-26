package app

import (
	"context"
	"errors"
	"testing"
)

func TestSessionOperations(t *testing.T) {
	w, _ := openTestWith(t, nil, text("noted"))
	if _, err := w.SaveSnapshot("early", false); !errors.Is(err, ErrNoActiveSession) {
		t.Errorf("snapshot without a session: %v", err)
	}
	if _, err := w.RenameSession("x"); !errors.Is(err, ErrNoActiveSession) {
		t.Errorf("rename without a session: %v", err)
	}
	first, err := w.NewSession()
	if err != nil || first.Agent != "code-puppy" || first.Workspace != w.Dir() {
		t.Fatalf("new: %+v %v", first, err)
	}
	if _, err := w.Run(context.Background(), first.ID, Turn{Text: "remember pineapple"}, ignore); err != nil {
		t.Fatal(err)
	}
	if s, _ := w.ActiveSession(); s.Title != "remember pineapple" || len(s.Messages) != 2 || s.Messages[0].Text != "remember pineapple" {
		t.Errorf("active: %+v", s)
	}
	if _, err := w.RenameSession(" "); err == nil {
		t.Error("an empty title was accepted")
	}
	if s, err := w.RenameSession("fruit talk"); err != nil || s.Title != "fruit talk" {
		t.Errorf("rename: %+v %v", s, err)
	}

	snap, err := w.SaveSnapshot("fruit", false)
	if err != nil || snap.Snapshot != "fruit" || snap.MessageCount != 2 || snap.From != first.ID {
		t.Fatalf("snapshot: %+v %v", snap, err)
	}
	if _, err := w.SaveSnapshot("fruit", false); !errors.Is(err, ErrSnapshotNameTaken) {
		t.Errorf("taken: %v", err)
	}
	if s, _ := w.ActiveSession(); s.ID != first.ID {
		t.Error("saving a snapshot switched sessions")
	}

	branch, branched, err := w.LoadSession("fruit")
	if err != nil || !branched || branch.ID == first.ID || branch.ID == snap.ID || len(branch.Messages) != 2 {
		t.Fatalf("load snapshot: %+v %v %v", branch, branched, err)
	}
	back, branched, err := w.LoadSession(first.ID)
	if err != nil || branched || back.ID != first.ID {
		t.Fatalf("load by id: %+v %v %v", back, branched, err)
	}
	if _, _, err := w.LoadSession("nope"); err == nil {
		t.Error("loaded an unknown session")
	}

	list, err := w.ListSessions(false)
	if err != nil || len(list) != 3 || list[0].Messages != nil {
		t.Errorf("list: %d sessions, %v", len(list), err)
	}
}
