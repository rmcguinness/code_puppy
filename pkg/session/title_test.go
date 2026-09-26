package session

import (
	"strings"
	"testing"
)

func TestTitleFrom(t *testing.T) {
	cases := map[string]string{
		"\n\n  fix   the\tlogin bug  \nmore detail": "fix the login bug",
		"   ":                   "",
		strings.Repeat("é", 80): strings.Repeat("é", 59) + "…",
	}
	for in, want := range cases {
		if got := TitleFrom(in); got != want {
			t.Errorf("TitleFrom(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSessionsAreNamedByTheirFirstPromptAndRenamed(t *testing.T) {
	st, _ := newStorage(t)
	rec, _ := st.CreateSession("", "", "a")
	if rec.Title != "" {
		t.Fatalf("title %q before any prompt", rec.Title)
	}
	st.AddMessage("model", "hello from the model")
	st.AddMessage("user", "add retries to the client")
	st.AddMessage("user", "and tests")
	if got := st.Active().Title; got != "add retries to the client" {
		t.Fatalf("title %q", got)
	}
	if err := st.Rename("  Retry work  "); err != nil {
		t.Fatal(err)
	}
	if err := st.Rename(" \n "); err == nil {
		t.Fatal("empty name accepted")
	}
	loaded, err := st.Load(rec.ID)
	if err != nil || loaded.Title != "Retry work" {
		t.Fatalf("after reload: %q %v", loaded.Title, err)
	}
	// An explicit title is kept.
	st.CreateSession("", "given", "a")
	st.AddMessage("user", "first prompt")
	if st.Active().Title != "given" {
		t.Fatalf("explicit title replaced: %q", st.Active().Title)
	}
}
