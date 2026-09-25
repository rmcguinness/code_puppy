package tui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/audit"
	"github.com/retail-cortex/code_puppy/pkg/i18n"
)

// runShellPassthrough runs a command typed after "!" directly, as the user's
// own terminal would: in the workspace, with the terminal attached (so
// interactive programs work) and the user's full environment. It is not an
// agent action, so the sandbox, command policy and approvals don't apply;
// the agent never sees it; the audit log records it.
func runShellPassthrough(ctx context.Context, app *App, command string, interrupts <-chan os.Signal) {
	command = strings.TrimSpace(command)
	if command == "" {
		fmt.Printf("%s%s%s\n", Yellow, i18n.T("shell.usage"), Reset)
		return
	}
	dir := "."
	if app.Tools != nil {
		dir = app.Tools.Workspace().Dir()
	}
	fmt.Printf("%s🐚 $ %s%s  %s%s%s\n", Bold, safe(command), Reset, Dim, i18n.T("shell.direct"), Reset)

	cmd := userShell(ctx, command)
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	// Ctrl+C goes to the command (it shares the terminal); the REPL gets a
	// copy too, which must not count as "exit" at the next prompt.
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-interrupts:
			case <-done:
				return
			}
		}
	}()
	start := time.Now()
	err := cmd.Run()
	close(done)
	elapsed := fmt.Sprintf("%.1fs", time.Since(start).Seconds())

	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	switch {
	case err == nil:
		fmt.Printf("%s✅ %s%s %s(%s)%s\n\n", Green, i18n.T("shell.done"), Reset, Dim, elapsed, Reset)
	case cmd.ProcessState == nil: // didn't start
		fmt.Printf("%s❌ %s%s\n\n", Red, i18n.T("shell.error", "error", safe(err.Error())), Reset)
	case code < 0: // killed by a signal, e.g. Ctrl+C
		fmt.Printf("%s⚡ %s%s %s(%s)%s\n\n", Yellow, i18n.T("repl.interrupted"), Reset, Dim, elapsed, Reset)
	default:
		fmt.Printf("%s❌ %s%s %s(%s)%s\n\n", Red, i18n.T("shell.exit_code", "code", code), Reset, Dim, elapsed, Reset)
	}
	slog.InfoContext(ctx, "user shell command", "exit_code", code, "elapsed", elapsed)
	if app.Tools != nil {
		entry := audit.Entry{Kind: audit.KindUserShell, Detail: command, Decision: "exit " + strconv.Itoa(code)}
		if err != nil && cmd.ProcessState == nil {
			entry.Error = err.Error()
		}
		app.Tools.Hooks().Audit().Log(entry)
	}
}

// userShell builds the command as the user's shell would run it.
func userShell(ctx context.Context, command string) *exec.Cmd {
	if goruntime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	sh := "sh"
	if _, err := exec.LookPath("bash"); err == nil {
		sh = "bash"
	}
	return exec.CommandContext(ctx, sh, "-c", command)
}
