package tui

import (
	"context"
	"strings"
	"testing"
)

func TestRenameTitleAndResumeHint(t *testing.T) {
	app, _ := newCommandApp(t, "/session list\nfix the \x1b[31mlogin\x07 bug\n/session list\n/rename\n/rename Login work\n/exit\n")
	app.TerminalTitle = true
	out := captureStdout(t, func() { RunREPL(context.Background(), app) })

	for _, want := range []string{
		"(untitled)",              // before the first prompt
		"\033]0;🐶 (untitled)\007", // terminal title
		"fix the [31mlogin bug",   // named by the prompt, control characters dropped
		"\033]0;🐶 fix the [31mlogin bug\007",
		"Usage: /rename <name>",
		"Session renamed to Login work.",
		"\033]0;\007", // restored at exit
		"Resume with: code-puppy --resume=" + app.Storage.Active().ID,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%q", want, out)
		}
	}
	if app.Storage.Active().Title != "Login work" {
		t.Fatalf("title %q", app.Storage.Active().Title)
	}
}

func TestNoTerminalTitleOrHintWhenOff(t *testing.T) {
	app, _ := newCommandApp(t, "/exit\n")
	out := captureStdout(t, func() { RunREPL(context.Background(), app) })
	if strings.Contains(out, "\033]0;") || strings.Contains(out, "Resume with") {
		t.Fatalf("output:\n%q", out)
	}
}
