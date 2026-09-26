package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/retail-cortex/code_puppy/internal/app"
	"github.com/retail-cortex/code_puppy/internal/config"
	pb "github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1"
	"github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1/codepuppyv1connect"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

type clients struct {
	sessions   codepuppyv1connect.SessionServiceClient
	workspaces codepuppyv1connect.WorkspaceServiceClient
}

// serve starts a server whose workspaces run on mock models answering with
// replies, and returns clients for it. mutate adjusts each workspace's
// configuration.
func serve(t *testing.T, mutate func(*config.Config), replies ...*genai.Content) (clients, *Server) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MODENV_PREFIX", "")
	t.Chdir(t.TempDir())
	s := New(func(ctx context.Context, dir string) (*app.Workspace, error) {
		cfg := config.DefaultConfig()
		cfg.Tools.WorkspaceDir = dir
		cfg.Session.StorageDir = t.TempDir()
		if mutate != nil {
			mutate(cfg)
		}
		return app.Open(ctx, cfg, app.Options{
			Model: runtime.NewMockLLM("gemini-3.8-flash", replies...),
			NewModel: func(_ context.Context, _ *config.Config, ref string) (model.LLM, error) {
				_, name := runtime.ParseModelRef(ref, "")
				return runtime.NewMockLLM(name), nil
			},
		})
	})
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.Close() })
	return clients{
		sessions:   codepuppyv1connect.NewSessionServiceClient(http.DefaultClient, srv.URL),
		workspaces: codepuppyv1connect.NewWorkspaceServiceClient(http.DefaultClient, srv.URL),
	}, s
}

// errorReason returns the ErrorInfo reason and metadata of a failed call.
func errorReason(t *testing.T, err error) (connect.Code, *pb.ErrorInfo) {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("not a Connect error: %v", err)
	}
	for _, d := range ce.Details() {
		if v, derr := d.Value(); derr == nil {
			if info, ok := v.(*pb.ErrorInfo); ok {
				return ce.Code(), info
			}
		}
	}
	t.Fatalf("no ErrorInfo on %v", err)
	return 0, nil
}

func text(s string) *genai.Content { return genai.NewContentFromText(s, genai.RoleModel) }

func TestWorkspaceOperationsOverTheAPI(t *testing.T) {
	c, _ := serve(t, nil)
	ctx := context.Background()
	dir := t.TempDir()

	agents, err := c.workspaces.ListAgents(ctx, connect.NewRequest(&pb.ListAgentsRequest{Workspace: dir}))
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(agents.Msg.Agents, func(a *pb.AgentInfo) bool { return a.Active }); i < 0 || agents.Msg.Agents[i].Name != "code-puppy" {
		t.Errorf("agents %v", agents.Msg.Agents)
	}
	if _, err := c.workspaces.SetAgent(ctx, connect.NewRequest(&pb.SetAgentRequest{Workspace: dir, Name: "qa-kitten"})); err != nil {
		t.Fatal(err)
	}
	pin, err := c.workspaces.PinModel(ctx, connect.NewRequest(&pb.PinModelRequest{Workspace: dir, Agent: "qa-kitten", Ref: "anthropic/claude-haiku-4-5"}))
	if err != nil || pin.Msg.Model != "claude-haiku-4-5" || pin.Msg.Saved.Error != "" {
		t.Fatalf("pin %v %v", pin, err)
	}
	if m, _ := c.workspaces.GetModel(ctx, connect.NewRequest(&pb.GetModelRequest{Workspace: dir})); m.Msg.Name != "claude-haiku-4-5" {
		t.Errorf("model %v", m.Msg)
	}

	// Typed errors arrive as codes with an ErrorInfo detail.
	_, err = c.workspaces.PinModel(ctx, connect.NewRequest(&pb.PinModelRequest{Workspace: dir, Agent: "nobody", Ref: "x"}))
	if code, info := errorReason(t, err); code != connect.CodeNotFound || info.Reason != "UNKNOWN_AGENT" || info.Metadata["name"] != "nobody" {
		t.Errorf("unknown agent: %v %v", code, info)
	}
	_, err = c.workspaces.UpdateModelSettings(ctx, connect.NewRequest(&pb.UpdateModelSettingsRequest{Workspace: dir, Ref: "gpt-5", Changes: []*pb.Setting{{Key: "temperature", Value: "9"}}}))
	if code, info := errorReason(t, err); code != connect.CodeInvalidArgument || info.Reason != "INVALID_SETTING" {
		t.Errorf("invalid setting: %v %v", code, info)
	}
	_, err = c.workspaces.ListAgents(ctx, connect.NewRequest(&pb.ListAgentsRequest{Workspace: "relative/dir"}))
	if code, info := errorReason(t, err); code != connect.CodeInvalidArgument || info.Reason != "INVALID_WORKSPACE" {
		t.Errorf("relative workspace: %v %v", code, info)
	}

	tools, err := c.workspaces.ListTools(ctx, connect.NewRequest(&pb.ListToolsRequest{Workspace: dir}))
	if err != nil || tools.Msg.Agent != "qa-kitten" || len(tools.Msg.Tools) == 0 {
		t.Errorf("tools %v %v", tools, err)
	}
}

func TestTurnsAndSessionsOverTheAPI(t *testing.T) {
	c, _ := serve(t, nil, text("hello there"))
	ctx := context.Background()
	dir := t.TempDir()

	opened, err := c.sessions.OpenSession(ctx, connect.NewRequest(&pb.OpenSessionRequest{Workspace: dir}))
	if err != nil || opened.Msg.Resumed {
		t.Fatalf("open %v %v", opened, err)
	}
	stream, err := c.sessions.RunTurn(ctx, connect.NewRequest(&pb.RunTurnRequest{Workspace: dir, SessionId: opened.Msg.Session.Id, Turn: &pb.Turn{Text: "hi"}}))
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	var finished *pb.TurnFinished
	for stream.Receive() {
		ev := stream.Msg().Event
		switch k := ev.Kind.(type) {
		case *pb.TurnEvent_Accepted:
			kinds = append(kinds, "accepted")
		case *pb.TurnEvent_Text:
			kinds = append(kinds, "text:"+k.Text.Text)
		case *pb.TurnEvent_Finished:
			kinds = append(kinds, "finished")
			finished = k.Finished
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(kinds, []string{"accepted", "text:hello there", "finished"}) || finished.Output != "hello there" || finished.Error != nil {
		t.Errorf("events %v, finished %v", kinds, finished)
	}

	active, err := c.sessions.GetActiveSession(ctx, connect.NewRequest(&pb.GetActiveSessionRequest{Workspace: dir}))
	if err != nil || len(active.Msg.Session.Messages) != 2 || active.Msg.Session.Title != "hi" {
		t.Errorf("active %v %v", active, err)
	}
	if _, err := c.sessions.SaveSnapshot(ctx, connect.NewRequest(&pb.SaveSnapshotRequest{Workspace: dir, Name: "s"})); err != nil {
		t.Fatal(err)
	}
	_, err = c.sessions.SaveSnapshot(ctx, connect.NewRequest(&pb.SaveSnapshotRequest{Workspace: dir, Name: "s"}))
	if code, info := errorReason(t, err); code != connect.CodeAlreadyExists || info.Reason != "SNAPSHOT_NAME_TAKEN" {
		t.Errorf("taken: %v %v", code, info)
	}

	// An image must be loaded or added before a turn can use it.
	stream, _ = c.sessions.RunTurn(ctx, connect.NewRequest(&pb.RunTurnRequest{Workspace: dir, SessionId: opened.Msg.Session.Id, Turn: &pb.Turn{Text: "see", ImageIds: []string{"nope"}}}))
	for stream.Receive() {
	}
	if code, info := errorReason(t, stream.Err()); code != connect.CodeNotFound || info.Reason != "UNKNOWN_IMAGE" {
		t.Errorf("unknown image: %v %v", code, info)
	}
}

func TestBlockedTurnReportsTheReason(t *testing.T) {
	c, _ := serve(t, func(cfg *config.Config) {
		cfg.Hooks.PromptSubmit = []config.HookConfig{{Command: `echo "no secrets" >&2; exit 2`}}
	})
	ctx := context.Background()
	dir := t.TempDir()
	s, _ := c.sessions.NewSession(ctx, connect.NewRequest(&pb.NewSessionRequest{Workspace: dir}))
	stream, err := c.sessions.RunTurn(ctx, connect.NewRequest(&pb.RunTurnRequest{Workspace: dir, SessionId: s.Msg.Session.Id, Turn: &pb.Turn{Text: "my password"}}))
	if err != nil {
		t.Fatal(err)
	}
	var finished *pb.TurnFinished
	for stream.Receive() {
		if f := stream.Msg().Event.GetFinished(); f != nil {
			finished = f
		}
	}
	if finished == nil || finished.Error.GetReason() != "PROMPT_BLOCKED" || finished.Error.Metadata["reason"] == "" {
		t.Errorf("finished %v", finished)
	}
}

func TestWorkspacesAreKeptAndClosed(t *testing.T) {
	c, s := serve(t, nil)
	ctx := context.Background()
	a, b := t.TempDir(), t.TempDir()
	// Another spelling of a, through a symlink, is the same workspace.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{a, b, link} {
		if _, err := c.workspaces.GetModel(ctx, connect.NewRequest(&pb.GetModelRequest{Workspace: dir})); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := c.workspaces.ListWorkspaces(ctx, connect.NewRequest(&pb.ListWorkspacesRequest{}))
	if len(list.Msg.Workspaces) != 2 {
		t.Fatalf("workspaces %v", list.Msg.Workspaces)
	}
	if _, err := c.workspaces.CloseWorkspace(ctx, connect.NewRequest(&pb.CloseWorkspaceRequest{Workspace: link})); err != nil {
		t.Fatal(err)
	}
	if got := s.openDirs(); len(got) != 1 {
		t.Errorf("after close: %v", got)
	}
}
