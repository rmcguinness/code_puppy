package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/retail-cortex/code_puppy/pkg/i18n"
	"github.com/retail-cortex/code_puppy/pkg/textutil"
	"github.com/retail-cortex/code_puppy/pkg/tools"
)

// ExitPrompt configures ConfirmExit.
type ExitPrompt struct {
	// CanPrompt is false when stdin can't answer (EOF, not a terminal); running
	// processes are then killed without asking.
	CanPrompt bool
	// AllowCancel offers "cancel" to return to the REPL instead of exiting.
	AllowCancel bool
}

// ConfirmExit decides whether Code Puppy may exit while background processes
// are running. Nothing is left running either way: the user chooses to kill
// them now or wait for them to finish, and a further Ctrl+C (or EOF) at the
// prompt or while waiting force-quits, killing them. Returns false only when
// the user cancels the exit.
func ConfirmExit(ctx context.Context, in Input, pm *tools.ProcessManager, interrupts <-chan os.Signal, opts ExitPrompt) bool {
	if pm == nil {
		return true
	}
	running := pm.Running()
	if len(running) == 0 {
		return true
	}

	fmt.Printf("\n%s⚠️  %s%s\n", Yellow+Bold, i18n.N("exit.running", len(running)), Reset)
	for _, p := range running {
		fmt.Printf("   [%d] %s %s(%ds)%s\n", p.ID, safe(textutil.Ellipsize(p.Command, 70)), Dim, p.RuntimeMs/1000, Reset)
	}
	if !opts.CanPrompt {
		killAll(pm)
		return true
	}

	choices := i18n.T("exit.choices")
	if opts.AllowCancel {
		choices += i18n.T("exit.choices_cancel")
	}
	askCtx, stopAsk := cancelOnSignal(ctx, interrupts)
	answer, err := in.Ask(askCtx, fmt.Sprintf("   %s? %s(%s)%s ", choices, Dim, i18n.T("exit.force_hint"), Reset))
	stopAsk()
	if err != nil {
		fmt.Printf("\n%s⛔ %s%s\n", Red, i18n.T("exit.force_quit"), Reset)
		killAll(pm)
		return true
	}

	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "k", "kill":
		killAll(pm)
		return true
	case "w", "wait":
		fmt.Printf("%s⏳ %s%s\n", Cyan, i18n.T("exit.waiting"), Reset)
		waitCtx, stopWait := cancelOnSignal(ctx, interrupts)
		err := pm.WaitAll(waitCtx)
		stopWait()
		if err != nil {
			fmt.Printf("\n%s⛔ %s%s\n", Red, i18n.T("exit.force_quit"), Reset)
			killAll(pm)
		}
		return true
	default:
		if opts.AllowCancel {
			fmt.Println("   " + i18n.T("exit.cancelled"))
			return false
		}
		killAll(pm)
		return true
	}
}

func killAll(pm *tools.ProcessManager) {
	n := len(pm.Running())
	pm.Shutdown()
	if n > 0 {
		fmt.Printf("%s🛑 %s%s\n", Yellow, i18n.N("exit.stopped", n), Reset)
	}
}

// StdinIsTerminal reports whether stdin is interactive.
func StdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
