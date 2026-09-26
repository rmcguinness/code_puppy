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

	"github.com/retail-cortex/code_puppy/internal/app"
	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/runtime"
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
		{name: "args frame piped", args: []string{"review", "this"}, in: "diff text\n", want: "review this\n\ndiff text", wantStdin: true},
		{name: "args with empty pipe", args: []string{"hello"}, in: "", want: "hello", wantStdin: true},
		{name: "empty pipe no args", in: "  \n", wantErr: true, wantStdin: true},
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
		if err == nil && got != c.want {
			t.Errorf("%s: prompt = %q, want %q", c.name, got, c.want)
		}
		if err == nil && used != c.wantStdin {
			t.Errorf("%s: stdinUsed = %v, want %v", c.name, used, c.wantStdin)
		}
	}
	// "-" with args: args frame the piped content.
	if got, _, _ := resolvePrompt("-", []string{"summarize"}, true, false, stdin("body")); got != "summarize\n\nbody" {
		t.Errorf("dash with args = %q", got)
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
		"plan no prompt": {"--plan", "-i"},
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

// --dir names the workspace; the process's working directory stays put.
func TestDirFlagDoesNotChangeTheWorkingDirectory(t *testing.T) {
	isolate(t)
	before, _ := os.Getwd()
	ws := t.TempDir()
	cfg, err := loadConfig(&globalFlags{dir: ws})
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Getwd(); after != before {
		t.Errorf("working directory changed to %s", after)
	}
	if cfg.Tools.WorkspaceDir != ws {
		t.Errorf("workspace %q, want %q", cfg.Tools.WorkspaceDir, ws)
	}
	file := filepath.Join(ws, "f")
	os.WriteFile(file, nil, 0o600)
	if _, err := loadConfig(&globalFlags{dir: file}); exitCodeFor(err) != exitUsage {
		t.Errorf("a file as --dir: %v", err)
	}
}

// isolate points HOME and config at temp dirs, and hides API keys.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MODENV_PREFIX", "")
	// No real model: a key in the developer's environment would make tests
	// call the provider (slowly, and billed). Tests that need a key set one.
	for _, k := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "LLM_PROVIDER"} {
		t.Setenv(k, "")
	}
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

// testEnv opens a workspace around a mock model.
func testEnv(t *testing.T, responses ...*genai.Content) *app.Workspace {
	return testEnvWith(t, nil, responses...)
}

func testEnvWith(t *testing.T, mutate func(*config.Config), responses ...*genai.Content) *app.Workspace {
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
	llm := runtime.NewMockLLM("gemini-3.8-flash", responses...)
	llm.Usage = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 100, CandidatesTokenCount: 10}
	e, err := app.Open(context.Background(), cfg, app.Options{Model: llm})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func toolCall(name string, args map[string]any) *genai.Content {
	return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: name, Args: args}}}}
}

func TestOneShotJSON(t *testing.T) {
	e := testEnv(t, toolCall("list_files", map[string]any{}), genai.NewContentFromText("all done", genai.RoleModel))
	sess, _ := e.Storage().CreateSession("", "t", "code-puppy")
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
	sess, _ := e.Storage().CreateSession("", "t", "code-puppy")
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
	sess, _ := e.Storage().CreateSession("", "t", "code-puppy")
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

func TestDoctorPricingCheck(t *testing.T) {
	home := isolate(t)
	os.MkdirAll(filepath.Join(home, ".code_puppy"), 0o700)
	os.WriteFile(filepath.Join(home, ".code_puppy", ".env.toml"), []byte("[code_puppy]\ndefault_model = \"mystery-model-1\"\n"), 0o600)
	checks := runDoctor(context.Background(), &globalFlags{}, false)
	found := false
	for _, c := range checks {
		if c.name == "pricing" {
			found = true
			if c.status != statusWarn || !strings.Contains(c.detail, "mystery-model-1") {
				t.Errorf("pricing check %+v", c)
			}
		}
	}
	if !found {
		t.Error("doctor has no pricing check")
	}
}

func TestOneShotPlanRefusesEdits(t *testing.T) {
	e := testEnv(t,
		toolCall("create_file", map[string]any{"path": "x.txt", "content": "x"}),
		genai.NewContentFromText("1. make x.txt", genai.RoleModel))
	sess, _ := e.Storage().CreateSession("", "t", "code-puppy")
	var out bytes.Buffer
	if err := runOneShot(context.Background(), e, oneShotOptions{prompt: "add x.txt", sessionID: sess.ID, format: formatJSON, plan: true, stdout: &out}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.Tools().Workspace().Dir(), "x.txt")); err == nil {
		t.Fatal("--plan created a file")
	}
	var res runResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil || res.Result != "1. make x.txt" {
		t.Fatalf("result %+v %v", res, err)
	}
	if e, _ := res.ToolCalls[0].Result["error"].(string); !strings.Contains(e, "plan mode") {
		t.Fatalf("create_file result %+v", res.ToolCalls[0])
	}
}

func TestDoctorSkillsCheck(t *testing.T) {
	home := isolate(t)
	cp := filepath.Join(home, ".code_puppy")
	for name, doc := range map[string]string{
		"ok":     "---\nname: ok-skill\nscripts:\n  - name: run\n    language: python\n    inline_code: \"print(1)\"\n---\n",
		"net":    "---\nname: net-skill\nexecution_hints: {custom_hints: {network: \"true\"}}\nscripts:\n  - name: run\n    language: python\n    inline_code: \"print(1)\"\n---\n",
		"broken": "---\nname: broken\nexecution_hints: {hitl_tier: TIER_9}\n---\n",
		"plain":  "---\nname: plain\n---\nJust instructions.\n",
	} {
		os.MkdirAll(filepath.Join(cp, "skills", name), 0o700)
		os.WriteFile(filepath.Join(cp, "skills", name, "SKILL.md"), []byte(doc), 0o600)
	}
	os.WriteFile(filepath.Join(cp, ".env.toml"), []byte("[skills.policy]\nsandbox = \"docker\"\n"), 0o600)
	byName := map[string][]check{}
	for _, c := range runDoctor(context.Background(), &globalFlags{}, false) {
		byName[c.name] = append(byName[c.name], c)
	}
	has := func(name string, st checkStatus, text string) {
		t.Helper()
		for _, c := range byName[name] {
			if c.status == st && strings.Contains(c.detail, text) {
				return
			}
		}
		t.Errorf("no %q check with %q: %+v", name, text, byName[name])
	}
	has("skills policy", statusWarn, `sandbox = "docker"`)
	has("skills", statusWarn, "TIER_9")
	has("skill net-skill", statusWarn, "needs the network")
	has("skills", statusOK, "2 with scripts, 1 blocked")
	if len(byName["skill ok-skill"]) != 0 {
		t.Errorf("ok-skill reported: %+v", byName["skill ok-skill"])
	}
}
