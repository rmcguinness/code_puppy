package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shellCfg(ws *Workspace) ShellConfig {
	return ShellConfig{Workspace: ws, Hooks: allowAll(), Processes: NewProcessManager(0, 0), DefaultTimeout: 10 * time.Second}
}

func TestShellBasicAndExitCodes(t *testing.T) {
	ws, dir := newTestWorkspace(t)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := shellCfg(ws)
	ctx := context.Background()

	// Positive: stdout and stderr both captured.
	out := runShellCommand(ctx, cfg, RunShellCommandInput{Command: "echo out; echo err >&2"})
	if out.ExitCode != 0 || !strings.Contains(out.Output, "out") || !strings.Contains(out.Output, "err") {
		t.Errorf("unexpected result %+v", out)
	}
	// Positive: cwd inside workspace.
	out = runShellCommand(ctx, cfg, RunShellCommandInput{Command: "basename \"$PWD\"", Cwd: "sub"})
	if strings.TrimSpace(out.Output) != "sub" {
		t.Errorf("expected cwd sub, got %q", out.Output)
	}
	// Non-zero exit code reported without Error.
	out = runShellCommand(ctx, cfg, RunShellCommandInput{Command: "exit 3"})
	if out.ExitCode != 3 || out.Error != "" {
		t.Errorf("expected exit code 3, got %+v", out)
	}

	// Negative: empty command, cwd outside workspace, missing cwd.
	if out := runShellCommand(ctx, cfg, RunShellCommandInput{}); out.Error == "" {
		t.Error("expected error for empty command")
	}
	if out := runShellCommand(ctx, cfg, RunShellCommandInput{Command: "pwd", Cwd: "/"}); out.Error == "" {
		t.Error("expected error for cwd outside workspace")
	}
	if out := runShellCommand(ctx, cfg, RunShellCommandInput{Command: "pwd", Cwd: "missing"}); out.Error == "" {
		t.Error("expected error for missing cwd")
	}
}

func TestShellOutputCapped(t *testing.T) {
	ws, _ := newTestWorkspace(t)
	// 5MB of output must not be buffered in full.
	out := runShellCommand(context.Background(), shellCfg(ws), RunShellCommandInput{Command: "head -c 5000000 /dev/zero | tr '\\0' 'a'"})
	if !out.Truncated {
		t.Error("expected truncated=true")
	}
	if len(out.Output) > shellOutputLimit+200 {
		t.Errorf("output not capped: %d bytes", len(out.Output))
	}
	if !strings.Contains(out.Output, "output truncated") {
		t.Error("expected truncation notice")
	}

	// Negative: small output is not marked truncated.
	out = runShellCommand(context.Background(), shellCfg(ws), RunShellCommandInput{Command: "echo small"})
	if out.Truncated {
		t.Error("small output marked truncated")
	}
}

func TestShellTimeoutKillsProcessGroup(t *testing.T) {
	ws, _ := newTestWorkspace(t)
	start := time.Now()
	// The backgrounded sleep inherits the output pipe; without a process-group
	// kill and WaitDelay, Run would block for the full 30s.
	out := runShellCommand(context.Background(), shellCfg(ws), RunShellCommandInput{
		Command:        "sleep 30 & echo started; sleep 30",
		TimeoutSeconds: 1,
	})
	elapsed := time.Since(start)
	if elapsed > 10*time.Second {
		t.Fatalf("timeout did not kill descendants promptly: took %s", elapsed)
	}
	if !strings.Contains(out.Error, "timed out") || out.ExitCode != -1 {
		t.Errorf("expected timeout error, got %+v", out)
	}
	if !strings.Contains(out.Output, "started") {
		t.Errorf("expected partial output preserved, got %q", out.Output)
	}
}

func TestShellParentCancellation(t *testing.T) {
	ws, _ := newTestWorkspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	out := runShellCommand(ctx, shellCfg(ws), RunShellCommandInput{Command: "sleep 30"})
	if out.Error != "command cancelled" {
		t.Errorf("expected cancellation, got %+v", out)
	}
}

func TestShellTimeoutIsClamped(t *testing.T) {
	ws, _ := newTestWorkspace(t)
	// A huge requested timeout must not overflow or exceed the maximum.
	out := runShellCommand(context.Background(), shellCfg(ws), RunShellCommandInput{Command: "true", TimeoutSeconds: 1 << 40})
	if out.Error != "" || out.ExitCode != 0 {
		t.Errorf("unexpected result %+v", out)
	}
}

func TestBackgroundProcessLifecycle(t *testing.T) {
	ws, _ := newTestWorkspace(t)
	cfg := shellCfg(ws)
	t.Cleanup(cfg.Processes.Shutdown)

	out := runShellCommand(context.Background(), cfg, RunShellCommandInput{Command: "echo ready; sleep 30", Background: true})
	if out.Error != "" || !out.IsBackground || out.ProcessID == 0 {
		t.Fatalf("failed to start background process: %+v", out)
	}
	id := out.ProcessID

	mgr := toolOf(t)(NewManageBackgroundTool(cfg.Processes))

	// Output eventually contains the echo while still running.
	deadline := time.Now().Add(5 * time.Second)
	for {
		res := runTool(t, mgr, map[string]any{"action": "output", "process_id": id})
		if s, _ := res["output"].(string); strings.Contains(s, "ready") {
			if res["process"].(map[string]any)["running"] != true {
				t.Error("expected process to still be running")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background output never appeared")
		}
		time.Sleep(50 * time.Millisecond)
	}

	list := runTool(t, mgr, map[string]any{"action": "list"})
	if procs, _ := list["processes"].([]any); len(procs) != 1 {
		t.Errorf("expected 1 listed process, got %v", list)
	}

	res := runTool(t, mgr, map[string]any{"action": "kill", "process_id": id})
	if errOf(res) != "" || res["process"].(map[string]any)["running"] != false {
		t.Errorf("kill failed: %v", res)
	}

	// Negative: unknown ID and unknown action.
	if res := runTool(t, mgr, map[string]any{"action": "output", "process_id": 999}); errOf(res) == "" {
		t.Error("expected error for unknown process id")
	}
	if res := runTool(t, mgr, map[string]any{"action": "explode"}); errOf(res) == "" {
		t.Error("expected error for unknown action")
	}
}

func TestProcessManagerLimits(t *testing.T) {
	dir := t.TempDir()
	pm := NewProcessManager(1, 500*time.Millisecond)
	t.Cleanup(pm.Shutdown)

	bp, err := pm.Start("sleep 30", dir)
	if err != nil {
		t.Fatal(err)
	}
	// Negative: concurrency limit enforced.
	if _, err := pm.Start("sleep 30", dir); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("expected concurrency limit error, got %v", err)
	}
	// Lifetime limit kills the process.
	select {
	case <-bp.done:
	case <-time.After(5 * time.Second):
		t.Fatal("process outlived its max lifetime")
	}
	// Positive: slot is free again.
	if _, err := pm.Start("true", dir); err != nil {
		t.Errorf("expected start to succeed after exit, got %v", err)
	}

	// Output is capped.
	pm2 := NewProcessManager(0, 0)
	t.Cleanup(pm2.Shutdown)
	big, err := pm2.Start("head -c 2000000 /dev/zero | tr '\\0' 'b'", dir)
	if err != nil {
		t.Fatal(err)
	}
	<-big.done
	out, info, _ := pm2.Output(big.ID)
	if len(out) > backgroundOutputLimit+200 || info.Running {
		t.Errorf("background output not capped (%d bytes) or still running", len(out))
	}

	// After shutdown new processes are refused.
	pm2.Shutdown()
	if _, err := pm2.Start("true", dir); err == nil {
		t.Error("expected start after shutdown to fail")
	}
}

func TestProcessManagerPrunesFinished(t *testing.T) {
	dir := t.TempDir()
	pm := NewProcessManager(0, 0)
	t.Cleanup(pm.Shutdown)
	for i := 0; i < maxFinishedProcsRetained+5; i++ {
		bp, err := pm.Start("true", dir)
		if err != nil {
			t.Fatal(err)
		}
		<-bp.done
	}
	if n := len(pm.List()); n > maxFinishedProcsRetained+1 {
		t.Errorf("finished processes not pruned: %d retained", n)
	}
}

func TestCappedBuffer(t *testing.T) {
	b := newCappedBuffer(4)
	n, err := b.Write([]byte("ab"))
	if n != 2 || err != nil || b.Truncated() {
		t.Fatalf("unexpected write result %d %v", n, err)
	}
	// Writes always report full length so producers aren't failed.
	n, _ = b.Write([]byte("cdef"))
	if n != 4 || !b.Truncated() {
		t.Fatalf("expected truncation, n=%d", n)
	}
	if s := b.String(); !strings.HasPrefix(s, "abcd") || !strings.Contains(s, "2 bytes") {
		t.Errorf("unexpected String %q", s)
	}
	// Partial runes at the cut are trimmed.
	u := newCappedBuffer(2)
	u.Write([]byte("é!")) // é is 2 bytes; cut after it is fine
	u2 := newCappedBuffer(3)
	u2.Write([]byte("a🐶"))
	if s := u2.String(); !strings.HasPrefix(s, "a\n") {
		t.Errorf("expected partial rune trimmed, got %q", s)
	}
}
