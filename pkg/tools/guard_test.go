//go:build unix

package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func waitDead(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process %d still alive after %s", pid, within)
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) > 0 {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err == nil {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid file %s never written", path)
	return 0
}

func TestForegroundStragglersKilled(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	pidFile := filepath.Join(dir, "straggler.pid")
	// The command returns immediately but leaves a daemon-like child behind.
	out := runShellCommand(context.Background(), ShellConfig{Workspace: ws, Hooks: allowAll()},
		RunShellCommandInput{Command: fmt.Sprintf("nohup sleep 60 >/dev/null 2>&1 & echo $! > %q", pidFile)})
	if out.ExitCode != 0 {
		t.Fatalf("command failed: %+v", out)
	}
	waitDead(t, readPid(t, pidFile), 5*time.Second)
}

// TestGuardKillsChildrenWhenParentKilled SIGKILLs a helper process that owns
// a background command and verifies the command's process group dies too.
func TestGuardKillsChildrenWhenParentKilled(t *testing.T) {
	if os.Getenv("CP_GUARD_HELPER") == "1" {
		pm := NewProcessManager(0, 0)
		if _, err := pm.Start(fmt.Sprintf("sleep 60 & echo $! > %q; wait", os.Getenv("CP_GUARD_PIDFILE")), os.TempDir()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		time.Sleep(time.Minute) // until SIGKILLed
		return
	}

	pidFile := filepath.Join(t.TempDir(), "child.pid")
	helper := exec.Command(os.Args[0], "-test.run=^TestGuardKillsChildrenWhenParentKilled$", "-test.v")
	helper.Env = append(os.Environ(), "CP_GUARD_HELPER=1", "CP_GUARD_PIDFILE="+pidFile)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	childPid := readPid(t, pidFile)
	if !pidAlive(childPid) {
		t.Fatal("background child not running")
	}

	// SIGKILL: no deferred cleanup, no signal handlers — only the guard can help.
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	waitDead(t, childPid, 5*time.Second)
}
