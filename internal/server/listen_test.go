package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1"
	"github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1/codepuppyv1connect"
)

// socketDir returns a short directory: Unix socket paths are limited to
// about 104 bytes, and macOS temp directories are long.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestListenIsPrivateAndSingle(t *testing.T) {
	path := filepath.Join(socketDir(t), "run", "s.sock")
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Dir(path)); fi.Mode().Perm() != 0o700 {
		t.Errorf("directory mode %v", fi.Mode().Perm())
	}
	if _, err := Listen(path); !errors.Is(err, ErrRunning) {
		t.Errorf("second service: %v", err)
	}
	l.Close()

	// A socket left behind by a service that died is replaced.
	os.WriteFile(path, nil, 0o600)
	l, err = Listen(path)
	if err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	l.Close()
}

func TestServeOverTheSocket(t *testing.T) {
	_, s := serve(t, nil)
	path := filepath.Join(socketDir(t), "s.sock")
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, l, s.Handler(), time.Second) }()

	c := codepuppyv1connect.NewWorkspaceServiceClient(Client(path), BaseURL)
	res, err := c.GetModel(context.Background(), connect.NewRequest(&pb.GetModelRequest{Workspace: t.TempDir()}))
	if err != nil || res.Msg.Name == "" {
		t.Fatalf("over the socket: %v %v", res, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve didn't stop")
	}
}
