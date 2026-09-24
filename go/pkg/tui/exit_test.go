package tui

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/tools"
)

func startSleep(t *testing.T, pm *tools.ProcessManager, secs string) {
	t.Helper()
	if _, err := pm.Start("sleep "+secs, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func newPM(t *testing.T) *tools.ProcessManager {
	pm := tools.NewProcessManager(0, 0)
	t.Cleanup(pm.Shutdown)
	return pm
}

func input(s string) *LineReader { return NewLineReader(strings.NewReader(s), io.Discard) }

var interactive = ExitPrompt{CanPrompt: true, AllowCancel: true}

func TestConfirmExitNoProcesses(t *testing.T) {
	ctx := context.Background()
	if !ConfirmExit(ctx, input(""), nil, nil, interactive) {
		t.Error("nil manager should allow exit")
	}
	if !ConfirmExit(ctx, input(""), newPM(t), nil, interactive) {
		t.Error("no running processes should allow exit without prompting")
	}
}

func TestConfirmExitKill(t *testing.T) {
	pm := newPM(t)
	startSleep(t, pm, "30")
	if !ConfirmExit(context.Background(), input("k\n"), pm, nil, interactive) {
		t.Fatal("kill should exit")
	}
	if n := len(pm.Running()); n != 0 {
		t.Errorf("%d processes still running after kill", n)
	}
}

func TestConfirmExitWait(t *testing.T) {
	pm := newPM(t)
	startSleep(t, pm, "0.3")
	start := time.Now()
	if !ConfirmExit(context.Background(), input("w\n"), pm, nil, interactive) {
		t.Fatal("wait should exit")
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Error("returned before the process finished")
	}
	list := pm.List()
	if len(list) != 1 || list[0].Running || list[0].ExitCode != 0 {
		t.Errorf("process should have finished normally: %+v", list)
	}
}

func TestConfirmExitCancel(t *testing.T) {
	pm := newPM(t)
	startSleep(t, pm, "30")
	if ConfirmExit(context.Background(), input("c\n"), pm, nil, interactive) {
		t.Fatal("cancel should not exit")
	}
	if len(pm.Running()) != 1 {
		t.Error("cancel must leave processes running")
	}
	// Without AllowCancel (one-shot mode), anything but wait kills.
	if !ConfirmExit(context.Background(), input("c\n"), pm, nil, ExitPrompt{CanPrompt: true}) {
		t.Fatal("expected exit when cancel is not offered")
	}
	if len(pm.Running()) != 0 {
		t.Error("processes should be killed")
	}
}

func TestConfirmExitForceQuit(t *testing.T) {
	// EOF at the prompt force-quits.
	pm := newPM(t)
	startSleep(t, pm, "30")
	if !ConfirmExit(context.Background(), input(""), pm, nil, interactive) || len(pm.Running()) != 0 {
		t.Error("EOF should force quit and kill")
	}

	// Cannot prompt (non-interactive): kill without reading input.
	pm = newPM(t)
	startSleep(t, pm, "30")
	if !ConfirmExit(context.Background(), input("c\n"), pm, nil, ExitPrompt{}) || len(pm.Running()) != 0 {
		t.Error("non-interactive exit should kill")
	}

	// Second Ctrl+C at the prompt force-quits.
	pm = newPM(t)
	startSleep(t, pm, "30")
	pr, pw := io.Pipe()
	defer pw.Close()
	sigs := make(chan os.Signal, 1)
	sigs <- os.Interrupt
	done := make(chan bool, 1)
	go func() {
		done <- ConfirmExit(context.Background(), NewLineReader(pr, io.Discard), pm, sigs, interactive)
	}()
	select {
	case ok := <-done:
		if !ok || len(pm.Running()) != 0 {
			t.Error("Ctrl+C at prompt should kill and exit")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl+C at prompt did not force quit")
	}

	// Ctrl+C while waiting force-quits.
	pm = newPM(t)
	startSleep(t, pm, "30")
	sigs = make(chan os.Signal, 1)
	go func() { time.Sleep(300 * time.Millisecond); sigs <- os.Interrupt }()
	start := time.Now()
	if !ConfirmExit(context.Background(), input("w\n"), pm, sigs, interactive) {
		t.Fatal("expected exit")
	}
	if time.Since(start) > 5*time.Second || len(pm.Running()) != 0 {
		t.Error("Ctrl+C while waiting should kill promptly")
	}
}

func TestREPLExitWithBackgroundProcesses(t *testing.T) {
	app := newTestApp(t, nil)
	app.Processes = newPM(t)
	startSleep(t, app.Processes, "30")

	// /exit -> cancel -> keep working -> /exit -> kill.
	app.Input = input("/exit\nc\n/exit\nk\n")
	done := make(chan error, 1)
	go func() { done <- RunREPL(context.Background(), app) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("REPL did not exit")
	}
	if len(app.Processes.Running()) != 0 {
		t.Error("background process survived REPL exit")
	}
}

func TestREPLCtrlCAtPromptWithBackgroundProcess(t *testing.T) {
	app := newTestApp(t, nil)
	app.Processes = newPM(t)
	startSleep(t, app.Processes, "30")
	pr, pw := io.Pipe()
	app.Input = NewLineReader(pr, io.Discard)
	sigs := make(chan os.Signal, 1)
	app.Interrupts = sigs

	done := make(chan error, 1)
	go func() { done <- RunREPL(context.Background(), app) }()
	time.Sleep(100 * time.Millisecond)
	sigs <- os.Interrupt // first Ctrl+C: warn and prompt instead of exiting
	time.Sleep(100 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("REPL exited on the first Ctrl+C despite running background processes")
	default:
	}
	go pw.Write([]byte("k\n"))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("REPL did not exit after choosing kill")
	}
	pw.Close()
	if len(app.Processes.Running()) != 0 {
		t.Error("background process survived")
	}
}
