package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1"
	"github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1/codepuppyv1connect"
	"github.com/retail-cortex/code_puppy/internal/server"
)

// The serve command answers over its socket, opens workspaces on demand,
// and removes the socket when stopped.
func TestServeCommand(t *testing.T) {
	isolate(t)
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	dir, err := os.MkdirTemp("/tmp", "cp") // socket paths must be short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, &globalFlags{}, socket) }()
	deadline := time.Now().Add(10 * time.Second)
	for !server.Running(socket) {
		if time.Now().After(deadline) {
			t.Fatal("service didn't start")
		}
		time.Sleep(20 * time.Millisecond)
	}

	ws := t.TempDir()
	c := codepuppyv1connect.NewWorkspaceServiceClient(server.Client(socket), server.BaseURL)
	agents, err := c.ListAgents(context.Background(), connect.NewRequest(&pb.ListAgentsRequest{Workspace: ws}))
	if err != nil || len(agents.Msg.Agents) == 0 {
		t.Fatalf("list agents: %v %v", agents, err)
	}
	// A second service on the same socket is refused.
	if err := runServe(context.Background(), &globalFlags{}, socket); exitCodeFor(err) != exitUsage {
		t.Errorf("second service: %v", err)
	}
	// The service owns the workspace now: the CLI can't also open it.
	if _, err := runCLI(t, "-d", ws, "hello"); exitCodeFor(err) != exitUsage || !strings.Contains(err.Error(), "service") {
		t.Errorf("CLI on a workspace the service holds: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve didn't stop")
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Error("socket left behind")
	}
}
