package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/retail-cortex/blitz/internal/app"
	"github.com/retail-cortex/blitz/internal/config"
	pb "github.com/retail-cortex/blitz/internal/gen/blitz/v1"
	"github.com/retail-cortex/blitz/internal/gen/blitz/v1/blitzv1connect"
	"github.com/retail-cortex/blitz/internal/runtime"
	"github.com/retail-cortex/blitz/internal/server"
	"google.golang.org/genai"
)

// The page's API calls, streamed turns and approvals included, reach the
// service through the proxy.
func TestProxyReachesTheService(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MODENV_PREFIX", "")
	dir, err := os.MkdirTemp("/tmp", "cpd") // socket paths must be short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s.sock")

	create := &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "create_file", Args: map[string]any{"path": "a.txt", "content": "x"}}}}}
	s := server.New(func(ctx context.Context, d string) (*app.Workspace, error) {
		cfg := config.DefaultConfig()
		cfg.Tools.WorkspaceDir = d
		cfg.Session.StorageDir = t.TempDir()
		return app.Open(ctx, cfg, app.Options{Model: runtime.NewMockLLM("m", create, genai.NewContentFromText("done", genai.RoleModel))})
	})
	l, err := server.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go server.Serve(ctx, l, s.Handler(), time.Second)
	t.Cleanup(func() { cancel(); s.Close() })

	page := httptest.NewServer(serviceProxy(socket))
	defer page.Close()
	sessions := blitzv1connect.NewSessionServiceClient(http.DefaultClient, page.URL)
	ws := t.TempDir()
	sess, err := sessions.NewSession(context.Background(), connect.NewRequest(&pb.NewSessionRequest{Workspace: ws}))
	if err != nil {
		t.Fatalf("through the proxy: %v", err)
	}
	stream, err := sessions.RunTurn(context.Background(), connect.NewRequest(&pb.RunTurnRequest{Workspace: ws, SessionId: sess.Msg.Session.Id, Turn: &pb.Turn{Text: "go"}}))
	if err != nil {
		t.Fatal(err)
	}
	var output string
	for stream.Receive() {
		ev := stream.Msg().Event
		if ar := ev.GetApprovalRequest(); ar != nil {
			if _, err := sessions.Approve(context.Background(), connect.NewRequest(&pb.ApproveRequest{Workspace: ws, RequestId: ar.RequestId, Decision: pb.Decision_DECISION_ONCE})); err != nil {
				t.Errorf("approve: %v", err)
			}
		}
		if f := ev.GetFinished(); f != nil {
			output = f.Output
		}
	}
	if err := stream.Err(); err != nil || output != "done" {
		t.Fatalf("streamed turn: %q %v", output, err)
	}
	if _, err := os.Stat(filepath.Join(ws, "a.txt")); err != nil {
		t.Errorf("approved write: %v", err)
	}

	// Only the API is forwarded.
	if res, err := http.Get(page.URL + "/index.html"); err != nil || res.StatusCode != http.StatusNotFound {
		t.Errorf("non-API path: %v %v", res, err)
	}
}

// With no service, the page gets an error its client can read.
func TestProxyWithoutAService(t *testing.T) {
	page := httptest.NewServer(serviceProxy(filepath.Join(t.TempDir(), "none.sock")))
	defer page.Close()
	c := blitzv1connect.NewWorkspaceServiceClient(http.DefaultClient, page.URL)
	_, err := c.GetModel(context.Background(), connect.NewRequest(&pb.GetModelRequest{Workspace: "/x"}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("err %v (code %v)", err, connect.CodeOf(err))
	}
}
