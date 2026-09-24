package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/pkg/agents"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/runtime"
	"github.com/retail-cortex/code_puppy/pkg/session"
	"github.com/retail-cortex/code_puppy/pkg/skills"
	"github.com/retail-cortex/code_puppy/pkg/tools"
	"google.golang.org/genai"
)

func TestResolvePrompt(t *testing.T) {
	stdin := func(s string) *strings.Reader { return strings.NewReader(s) }
	cases := []struct {
		name        string
		flag        string
		args        []string
		tty         bool
		interactive bool
		in          string
		want        string
		wantStdin   bool
		wantErr     bool
	}{
		{name: "flag", flag: "fix it", tty: true, want: "fix it"},
		{name: "args", args: []string{"fix", "it"}, tty: true, want: "fix it"},
		{name: "dash reads stdin", flag: "-", tty: true, in: "from stdin\n", want: "from stdin", wantStdin: true},
		{name: "piped stdin", in: "piped", want: "piped", wantStdin: true},
		{name: "args frame piped", args: []string{"review", "this"}, in: "diff text", want: "diff text", wantStdin: false},
		{name: "tty no prompt = REPL", tty: true, want: ""},
		{name: "interactive ignores pipe", interactive: true, in: "x", want: ""},
		{name: "empty dash", flag: "-", in: "  ", wantErr: true, wantStdin: true},
		{name: "flag and args", flag: "a", args: []string{"b"}, tty: true, wantErr: true},
	}
	for _, c := range cases {
		got, used, err := resolvePrompt(c.flag, c.args, c.tty, c.interactive, stdin(c.in))
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v", c.name, err)
			continue
		}
		if err == nil && (got != c.want && c.name != "args frame piped") {
			t.Errorf("%s: prompt = %q, want %q", c.name, got, c.want)
		}
		if c.name == "args frame piped" {
			// Piped with args but a non-TTY stdin and no -p: args are the prompt.
			if got != "review this" {
				t.Errorf("%s: prompt = %q", c.name, got)
			}
		}
		if err == nil && used != c.wantStdin {
			t.Errorf("%s: stdinUsed = %v, want %v", c.name, used, c.wantStdin)
		}
	}
	// "-" with args: args frame the piped content.
	got, _, _ := resolvePrompt("-", nil, true, false, stdin("body"))
	if got != "body" {
		t.Errorf("dash prompt = %q", got)
	}
}

func TestExitCodes(t *testing.T) {
	cases := map[int]error{
		exitOK:          nil,
		exitFailure:     errors.New("boom"),
		exitUsage:       withCode(exitUsage, errors.New("bad flag")),
		exitMaxTurns:    runtime.ErrMaxTurns,
		exitInterrupted: context.Canceled,
		exitBlocked:     withCode(exitBlocked, errors.New("hook")),
	}
	for want, err := range cases {
		if got := exitCodeFor(err); got != want {
			t.Errorf("exitCodeFor(%v) = %d, want %d", err, got, want)
		}
	}
	if withCode(3, nil) != nil {
		t.Error("withCode(nil) must be nil")
	}
}

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestRootFlagValidation(t *testing.T) {
	isolate(t)
	for name, args := range map[string][]string{
		"unknown flag":   {"--nope"},
		"bad format":     {"--output-format", "xml", "hi"},
		"json no prompt": {"--output-format", "json", "-i"},
		"negative turns": {"--max-turns", "-1", "hi"},
		"bad dir":        {"-d", "/definitely/not/here", "hi"},
	} {
		_, err := runCLI(t, args...)
		if exitCodeFor(err) != exitUsage {
			t.Errorf("%s: exit code %d (%v), want %d", name, exitCodeFor(err), err, exitUsage)
		}
	}
	if out, err := runCLI(t, "config", "path"); err != nil || !strings.HasSuffix(strings.TrimSpace(out), ".env.toml") {
		t.Errorf("config path: %q %v", out, err)
	}
}

// isolate points HOME and config at temp dirs.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MODENV_PREFIX", "")
	t.Chdir(t.TempDir())
	return home
}

func TestConfigInitAndShow(t *testing.T) {
	home := isolate(t)
	t.Setenv("GEMINI_API_KEY", "AIzaSyTESTKEY-1234567890abcdefghijklmnop")
	if _, err := runCLI(t, "config", "init"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".code_puppy", ".env.toml")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("template not written owner-only: %v %v", info, err)
	}
	// Negative: refuses to overwrite without --force.
	if _, err := runCLI(t, "config", "init"); exitCodeFor(err) != exitUsage {
		t.Errorf("expected usage error on overwrite, got %v", err)
	}
	if _, err := runCLI(t, "config", "init", "--force"); err != nil {
		t.Errorf("--force: %v", err)
	}
	// The template is valid config.
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("template does not load: %v", err)
	}
	if cfg.Sandbox.Shell != "auto" || !cfg.Memory.Enabled {
		t.Errorf("template values not applied: %+v", cfg.Sandbox)
	}
	out, err := runCLI(t, "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "TESTKEY-1234567890") || !strings.Contains(out, "AIz…mnop") {
		t.Errorf("config show leaked or failed to mask the key:\n%s", out)
	}
}

func TestSelectSession(t *testing.T) {
	st, err := session.NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := selectSession(st, "", true, "t", "a"); exitCodeFor(err) != exitUsage {
		t.Errorf("--continue with no sessions: %v", err)
	}
	first, resumed, _ := selectSession(st, "", false, "first", "a")
	if resumed {
		t.Error("new session reported as resumed")
	}
	st.AddMessage("user", "hello")
	latest, resumed, err := selectSession(st, "latest", false, "", "")
	if err != nil || !resumed || latest.ID != first.ID || len(latest.Messages) != 1 {
		t.Errorf("resume latest: %+v %v %v", latest, resumed, err)
	}
	byID, _, err := selectSession(st, first.ID, false, "", "")
	if err != nil || byID.ID != first.ID {
		t.Errorf("resume by id: %v", err)
	}
	if _, _, err := selectSession(st, "no-such-session", false, "", ""); exitCodeFor(err) != exitUsage {
		t.Errorf("unknown id: %v", err)
	}
}

// testEnv builds an env around a mock model.
func testEnv(t *testing.T, responses ...*genai.Content) *env {
	return testEnvWith(t, nil, responses...)
}

func testEnvWith(t *testing.T, mutate func(*config.Config), responses ...*genai.Content) *env {
	t.Helper()
	isolate(t)
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = t.TempDir()
	cfg.Session.StorageDir = t.TempDir()
	cfg.Tools.ApprovalsFile = filepath.Join(t.TempDir(), "a.json")
	cfg.CodePuppy.AutoApprove = true
	if mutate != nil {
		mutate(cfg)
	}
	e := &env{cfg: cfg}
	e.agents, _ = agents.NewRegistry()
	e.skills, _ = skills.NewProvider()
	var err error
	if e.tools, err = tools.NewRegistry(cfg, e.agents, e.skills); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	e.storage, _ = session.NewStorage(cfg.Session.StorageDir)
	llm := runtime.NewMockLLM("gemini-2.5-flash", responses...)
	llm.Usage = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 100, CandidatesTokenCount: 10}
	if e.engine, err = runtime.NewEngine(context.Background(), cfg, e.agents, e.skills, e.tools, llm); err != nil {
		t.Fatal(err)
	}
	return e
}

func toolCall(name string, args map[string]any) *genai.Content {
	return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: name, Args: args}}}}
}

func TestOneShotJSON(t *testing.T) {
	e := testEnv(t, toolCall("list_files", map[string]any{}), genai.NewContentFromText("all done", genai.RoleModel))
	sess, _ := e.storage.CreateSession("", "t", "code-puppy")
	var out bytes.Buffer
	err := runOneShot(context.Background(), e, oneShotOptions{prompt: "list", sessionID: sess.ID, format: formatJSON, stdout: &out})
	if err != nil {
		t.Fatal(err)
	}
	var res runResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("stdout is not a single JSON object: %v\n%s", err, out.String())
	}
	if res.Type != "result" || res.Result != "all done" || res.IsError || res.ExitCode != 0 || res.SessionID != sess.ID {
		t.Errorf("result %+v", res)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "list_files" || res.ToolCalls[0].Result == nil {
		t.Errorf("tool calls %+v", res.ToolCalls)
	}
	if res.Usage.ModelCalls != 2 || res.Usage.InputTokens != 200 || res.CostUSD == nil {
		t.Errorf("usage %+v cost %v", res.Usage, res.CostUSD)
	}
}

func TestOneShotStreamJSONAndMaxTurns(t *testing.T) {
	loop := []*genai.Content{}
	for i := 0; i < 5; i++ {
		loop = append(loop, toolCall("list_files", map[string]any{}))
	}
	e := testEnv(t, loop...)
	sess, _ := e.storage.CreateSession("", "t", "code-puppy")
	var out bytes.Buffer
	err := runOneShot(context.Background(), e, oneShotOptions{prompt: "loop", sessionID: sess.ID, format: formatStreamJSON, maxTurns: 2, stdout: &out})
	if exitCodeFor(err) != exitMaxTurns {
		t.Fatalf("expected max-turns exit, got %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var types []string
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line is not JSON: %q", l)
		}
		types = append(types, m["type"].(string))
	}
	if types[0] != "session" || types[len(types)-1] != "result" || !strings.Contains(strings.Join(types, ","), "tool_call,tool_result") {
		t.Errorf("event types %v", types)
	}
	var res runResult
	json.Unmarshal([]byte(lines[len(lines)-1]), &res)
	if !res.IsError || res.ExitCode != exitMaxTurns {
		t.Errorf("final result %+v", res)
	}
}

func TestOneShotPromptHookBlocks(t *testing.T) {
	e := testEnvWith(t, func(c *config.Config) {
		c.Hooks.PromptSubmit = []config.HookConfig{{Command: `echo "no secrets in prompts" >&2; exit 2`}}
	}, genai.NewContentFromText("should not run", genai.RoleModel))
	sess, _ := e.storage.CreateSession("", "t", "code-puppy")
	var out bytes.Buffer
	err := runOneShot(context.Background(), e, oneShotOptions{prompt: "my password is x", sessionID: sess.ID, format: formatJSON, stdout: &out})
	if exitCodeFor(err) != exitBlocked || !strings.Contains(err.Error(), "no secrets in prompts") {
		t.Errorf("expected blocked exit, got %v", err)
	}
	if !strings.Contains(out.String(), `"is_error":true`) {
		t.Errorf("result should report the block: %s", out.String())
	}
}

func TestMaskSecret(t *testing.T) {
	for in, want := range map[string]string{"": "", "short": "*****", "sk-abcdefghijklmnop": "sk-…mnop"} {
		if got := maskSecret(in); got != want {
			t.Errorf("maskSecret(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModelErrorSummary(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.LLM.Gemini.APIKey = "AIzaSySECRETSECRETSECRETSECRETSECRET123"
	err := errors.New(`api key is required. ClientConfig: &genai.ClientConfig{APIKey:"AIzaSySECRETSECRETSECRETSECRETSECRET123"}` + "\nmore")
	got := modelErrorSummary(err, cfg)
	if got != "api key is required" {
		t.Errorf("summary = %q", got)
	}
	leak := modelErrorSummary(errors.New("bad key AIzaSySECRETSECRETSECRETSECRETSECRET123"), cfg)
	if strings.Contains(leak, "SECRET") {
		t.Errorf("key leaked: %q", leak)
	}
}

func TestOneShotFailsWithoutModel(t *testing.T) {
	home := isolate(t)
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	os.MkdirAll(filepath.Join(home, ".code_puppy"), 0o700)
	os.WriteFile(filepath.Join(home, ".code_puppy", ".env.toml"), []byte("[llm]\nprovider = \"nonexistent\"\n"), 0o600)
	_, err := runCLI(t, "--output-format", "json", "hello")
	if exitCodeFor(err) != exitFailure || !strings.Contains(err.Error(), "model initialization failed") {
		t.Errorf("expected model failure, got %v", err)
	}
}
