package client

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/app"
	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"github.com/retail-cortex/code_puppy/internal/server"
	"github.com/retail-cortex/code_puppy/internal/tools"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// attach starts a service whose workspaces run on mock models answering
// with replies, and attaches to a new workspace in it.
func attach(t *testing.T, mutate func(*config.Config), replies ...*genai.Content) *Remote {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MODENV_PREFIX", "")
	s := server.New(func(ctx context.Context, dir string) (*app.Workspace, error) {
		cfg := config.DefaultConfig()
		cfg.Tools.WorkspaceDir = dir
		cfg.Session.StorageDir = t.TempDir()
		cfg.Images.Dir = t.TempDir()
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
	var warnings []string
	r, err := AttachHTTP(context.Background(), http.DefaultClient, srv.URL, t.TempDir(), func(w string) { warnings = append(warnings, w) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if len(warnings) > 0 {
			t.Errorf("warnings: %q", warnings)
		}
	})
	return r
}

func text(s string) *genai.Content { return genai.NewContentFromText(s, genai.RoleModel) }

func call(name string, args map[string]any) *genai.Content {
	return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: name, Args: args}}}}
}

func TestRemoteOperationsAndTypedErrors(t *testing.T) {
	r := attach(t, nil)
	ctx := context.Background()
	if r.ModelErr() != nil || r.Model().Name == "" || r.ActiveAgent().Name != "code-puppy" {
		t.Fatalf("model %v %v, agent %v", r.ModelErr(), r.Model(), r.ActiveAgent())
	}
	var unknown *app.UnknownAgentError
	if _, err := r.PinModel(ctx, "nobody", "x"); !errors.As(err, &unknown) || unknown.Name != "nobody" {
		t.Errorf("unknown agent: %v", err)
	}
	var invalid *app.InvalidSettingError
	if _, err := r.UpdateModelSettings("gpt-5", false, []app.Setting{{Key: "temperature", Value: "9"}}); !errors.As(err, &invalid) {
		t.Errorf("invalid setting: %v", err)
	}
	if _, err := r.SaveSnapshot("s", false); !errors.Is(err, app.ErrNoActiveSession) {
		t.Errorf("no session: %v", err)
	}
	if _, err := r.Set(ctx, "agency", "reckless"); !errors.Is(err, app.ErrInvalidAgency) {
		t.Errorf("agency: %v", err)
	}
	res, err := r.PinModel(ctx, "qa-kitten", "anthropic/claude-haiku-4-5")
	if err != nil || res.Model != "claude-haiku-4-5" || res.Saved.Err != nil {
		t.Fatalf("pin %+v %v", res, err)
	}
	if i := slices.IndexFunc(r.ListAgents(), func(a app.AgentInfo) bool { return a.Name == "qa-kitten" }); i < 0 || r.ListAgents()[i].PinnedModel != "claude-haiku-4-5" {
		t.Error("pin not listed")
	}
	if !r.ImagesEnabled() || r.Processes() != nil || len(r.SandboxSummary()) == 0 {
		t.Errorf("images %v, processes %v, sandbox %v", r.ImagesEnabled(), r.Processes(), r.SandboxSummary())
	}
}

func TestRemoteTurnWithApprovalAndQuestion(t *testing.T) {
	create := call("create_file", map[string]any{"path": "made.txt", "content": "hi\n"})
	ask := call("ask_user_question", map[string]any{"question": "Tabs or spaces?"})
	r := attach(t, func(c *config.Config) { c.CodePuppy.AutoApprove = false }, create, ask, text("all done"))
	var asked []string
	r.SetUI(
		func(_ context.Context, req tools.ApprovalRequest) (tools.Decision, error) {
			asked = append(asked, "approve:"+string(req.Kind))
			return tools.DecisionOnce, nil
		},
		func(_ context.Context, q string, _ []string) (string, error) {
			asked = append(asked, "ask:"+q)
			return "tabs", nil
		},
	)
	s, err := r.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	accepted, finished := false, false
	res, err := r.Run(context.Background(), s.ID, app.Turn{
		Text: "make a file", OnAccepted: func() { accepted = true }, OnFinished: func() { finished = true },
	}, func(e app.Event) {
		switch {
		case e.ToolCall != nil:
			events = append(events, "call:"+e.ToolCall.Name)
		case e.ToolResult != nil:
			events = append(events, "result:"+e.ToolResult.Name)
		}
	})
	if err != nil || res.Output != "all done" || !accepted || !finished {
		t.Fatalf("run: %+v %v accepted=%v finished=%v", res, err, accepted, finished)
	}
	if !slices.Equal(asked, []string{"approve:write_file", "ask:Tabs or spaces?"}) {
		t.Errorf("asked %v", asked)
	}
	if !slices.Equal(events, []string{"call:create_file", "result:create_file", "call:ask_user_question", "result:ask_user_question"}) {
		t.Errorf("events %v", events)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "made.txt")); err != nil {
		t.Errorf("approved write: %v", err)
	}
	if active, _ := r.ActiveSession(); len(active.Messages) != 2 {
		t.Errorf("transcript %+v", active.Messages)
	}
}

func TestRemoteBlockedPrompt(t *testing.T) {
	r := attach(t, func(c *config.Config) {
		c.Hooks.PromptSubmit = []config.HookConfig{{Command: `echo "no secrets" >&2; exit 2`}}
	})
	s, _ := r.NewSession()
	_, err := r.Run(context.Background(), s.ID, app.Turn{Text: "my password"}, func(app.Event) {})
	var blocked *app.BlockedError
	if !errors.As(err, &blocked) || !strings.Contains(blocked.Reason, "no secrets") {
		t.Errorf("blocked: %v", err)
	}
}

func TestRemoteImages(t *testing.T) {
	r := attach(t, nil, text("a small image"))
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 12, 8)))
	os.WriteFile(filepath.Join(r.Dir(), "a.png"), buf.Bytes(), 0o644)

	imgs, err := r.LoadAttachments([]string{"a.png"}, "", func(w string) { t.Errorf("warning %s", w) })
	if err != nil || len(imgs) != 1 || imgs[0].Width != 12 || !strings.Contains(imgs[0].Summary(), "12×8") {
		t.Fatalf("attachments %v %v", imgs, err)
	}
	if _, err := r.LoadAttachments([]string{"missing.png"}, "", nil); err == nil || !strings.Contains(err.Error(), "missing.png") {
		t.Errorf("missing image: %v", err)
	}
	s, _ := r.NewSession()
	if _, err := r.Run(context.Background(), s.ID, app.Turn{Text: "what is this?", Images: imgs}, func(app.Event) {}); err != nil {
		t.Fatalf("turn with an image: %v", err)
	}
}

func TestAttachReportsAnUnavailableModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MODENV_PREFIX", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	s := server.New(func(ctx context.Context, dir string) (*app.Workspace, error) {
		cfg := config.DefaultConfig()
		cfg.Tools.WorkspaceDir = dir
		cfg.Session.StorageDir = t.TempDir()
		return app.Open(ctx, cfg, app.Options{})
	})
	srv := httptest.NewServer(s.Handler())
	defer func() { srv.Close(); s.Close() }()
	r, err := AttachHTTP(context.Background(), http.DefaultClient, srv.URL, t.TempDir(), nil)
	if err != nil || r.ModelErr() == nil {
		t.Errorf("attach %v, model error %v", err, r.ModelErr())
	}
}
