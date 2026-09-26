package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/internal/server"
)

func addWorker(t *testing.T, ws, name, content string) {
	t.Helper()
	dir := filepath.Join(ws, "workers", name)
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "WORKER.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runCLIWithInput is runCLI with stdin.
func runCLIWithInput(t *testing.T, in string, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(in))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestWorkersCommandsLocally(t *testing.T) {
	isolate(t)
	t.Setenv("CODE_PUPPY_SOCKET", filepath.Join(t.TempDir(), "none.sock")) // no service
	ws := t.TempDir()
	addWorker(t, ws, "deps", "---\ndescription: Report outdated modules\nschedule: Weekdays at 9:30\npermissions: [\"write:reports/\"]\n---\nWrite reports/deps.md.\n")

	out, err := runCLI(t, "-d", ws, "workers")
	if err != nil || !strings.Contains(out, "deps") || !strings.Contains(out, "new") || !strings.Contains(out, "30 9 * * 1-5") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	out, err = runCLIWithInput(t, "n\n", "-d", ws, "workers", "enable", "deps")
	if err == nil || !strings.Contains(out, "write:reports/") || !strings.Contains(out, "sha256:") {
		t.Errorf("declined enable: %v\n%s", err, out)
	}
	if out, _ := runCLI(t, "-d", ws, "workers"); !strings.Contains(out, "new") {
		t.Errorf("declining enabled it:\n%s", out)
	}
	out, err = runCLI(t, "-d", ws, "workers", "enable", "deps", "--yes")
	if err != nil || !strings.Contains(out, "deps enabled") || !strings.Contains(out, "code-puppy serve") {
		t.Fatalf("enable: %v\n%s", err, out)
	}
	out, err = runCLI(t, "-d", ws, "workers", "run", "deps")
	if err != nil || !strings.Contains(out, "succeeded") {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "-d", ws, "workers", "runs", "deps"); err != nil || !strings.Contains(out, "succeeded") || !strings.Contains(out, "manual") {
		t.Errorf("runs: %v\n%s", err, out)
	}
	if _, err := runCLI(t, "-d", ws, "workers", "run", "nope"); exitCodeFor(err) != exitUsage {
		t.Errorf("unknown worker: %v", err)
	}
	if out, err := runCLI(t, "-d", ws, "workers", "disable", "deps"); err != nil || !strings.Contains(out, "disabled") {
		t.Errorf("disable: %v\n%s", err, out)
	}
	if _, err := runCLI(t, "-d", ws, "workers", "run", "deps"); exitCodeFor(err) != exitUsage {
		t.Errorf("running a disabled worker: %v", err)
	}
}

func TestWorkersCommandsThroughTheService(t *testing.T) {
	isolate(t)
	dir, err := os.MkdirTemp("/tmp", "cp") // socket paths must be short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")
	t.Setenv("CODE_PUPPY_SOCKET", socket)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, &globalFlags{}, socket) }()
	defer func() { cancel(); <-done }()
	for deadline := time.Now().Add(10 * time.Second); !server.Running(socket); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("service didn't start")
		}
	}

	ws := t.TempDir()
	addWorker(t, ws, "hello", "---\nschedule: daily at noon\n---\nSay hello.\n")
	if out, err := runCLI(t, "-d", ws, "workers", "enable", "hello", "--yes"); err != nil || !strings.Contains(out, "hello enabled") || strings.Contains(out, "code-puppy serve") {
		t.Fatalf("enable through the service: %v\n%s", err, out)
	}
	out, err := runCLI(t, "-d", ws, "workers", "run", "hello")
	if err != nil || !strings.Contains(out, "succeeded") {
		t.Fatalf("run through the service: %v\n%s", err, out)
	}
}
