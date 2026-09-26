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
	"time"

	"github.com/retail-cortex/code_puppy/internal/agents"
	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"github.com/retail-cortex/code_puppy/internal/session"
	"github.com/retail-cortex/code_puppy/internal/skills"
	"github.com/retail-cortex/code_puppy/internal/tools"
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
	// --resume <name> starts a new session from the snapshot.
	snap, err := st.Snapshot(first.ID, "greeting", false)
	if err != nil {
		t.Fatal(err)
	}
	branch, resumed, err := selectSession(st, "greeting", false, "", "")
	if err != nil || !resumed || branch.ID == first.ID || branch.ID == snap.ID || branch.From != snap.ID || len(branch.Messages) != 1 {
		t.Errorf("resume by name: %+v %v %v", branch, resumed, err)
	}
	// --continue skips snapshots even when one is the newest session.
	time.Sleep(10 * time.Millisecond)
	if _, err := st.Snapshot(branch.ID, "newest", false); err != nil {
		t.Fatal(err)
	}
	cont, _, err := selectSession(st, "", true, "", "")
	if err != nil || cont.ID != branch.ID {
		t.Errorf("--continue picked %s, want %s: %v", cont.ID, branch.ID, err)
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
	llm := runtime.NewMockLLM("gemini-3.8-flash", responses...)
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

func TestSelectSessionScopedToWorkspace(t *testing.T) {
	st, _ := session.NewStorage(t.TempDir())
	st.SetWorkspace("/proj/one")
	one, _, _ := selectSession(st, "", false, "one", "a")
	st.SetWorkspace("/proj/two")
	two, _, _ := selectSession(st, "", false, "two", "a") // newest overall

	st.SetWorkspace("/proj/one")
	got, resumed, err := selectSession(st, "", true, "", "")
	if err != nil || !resumed || got.ID != one.ID {
		t.Errorf("--continue in /proj/one picked %v (%v), want %s", got, err, one.ID)
	}
	// Explicit IDs still work across workspaces.
	got, _, err = selectSession(st, two.ID, false, "", "")
	if err != nil || got.ID != two.ID || got.Workspace != "/proj/two" {
		t.Errorf("explicit resume across workspaces: %+v %v", got, err)
	}
	st.SetWorkspace("/proj/three")
	if _, _, err := selectSession(st, "latest", false, "", ""); exitCodeFor(err) != exitUsage || !strings.Contains(err.Error(), "/proj/three") {
		t.Errorf("empty workspace should be a usage error naming it: %v", err)
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
	sess, _ := e.storage.CreateSession("", "t", "code-puppy")
	var out bytes.Buffer
	if err := runOneShot(context.Background(), e, oneShotOptions{prompt: "add x.txt", sessionID: sess.ID, format: formatJSON, plan: true, stdout: &out}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.tools.Workspace().Dir(), "x.txt")); err == nil {
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

func TestAgentModelRefsPrecedence(t *testing.T) {
	dir := t.TempDir()
	for name, model := range map[string]string{"alpha": "anthropic/claude-haiku-4-5", "beta": "openai/gpt-5"} {
		os.WriteFile(filepath.Join(dir, name+".md"), []byte("---\nname: "+name+"\ndisplay_name: "+name+"\ndescription: d\ntools: []\ndefault_model: "+model+"\n---\nprompt\n"), 0o600)
	}
	reg, _ := agents.NewRegistry()
	if err := reg.LoadExternalAgents(dir); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.AgentModels = map[string]string{"alpha": "gemini-3.8-flash", "ghost": "x"}
	var warnings []string
	refs := agentModelRefs(cfg, reg, func(s string) { warnings = append(warnings, s) })
	if refs["alpha"] != "gemini-3.8-flash" || refs["beta"] != "openai/gpt-5" || len(refs) != 2 {
		t.Fatalf("refs = %v (config pin must win over default_model)", refs)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "ghost") {
		t.Fatalf("warnings = %v", warnings)
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
