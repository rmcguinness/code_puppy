package app

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/internal/agents"
	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/i18n"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"github.com/retail-cortex/code_puppy/internal/session"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// openTest opens a workspace around a mock model, isolated from the real
// home directory.
func openTest(t *testing.T) *Workspace {
	w, _ := openTestWith(t, nil)
	return w
}

// openTestWith is openTest with configuration changes and model replies.
func openTestWith(t *testing.T, mutate func(*config.Config), replies ...*genai.Content) (*Workspace, *runtime.MockLLM) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MODENV_PREFIX", "")
	t.Chdir(t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = t.TempDir()
	cfg.Session.StorageDir = t.TempDir()
	cfg.Tools.ApprovalsFile = filepath.Join(t.TempDir(), "a.json")
	if mutate != nil {
		mutate(cfg)
	}
	llm := runtime.NewMockLLM("gemini-3.8-flash", replies...)
	w, err := Open(context.Background(), cfg, Options{Model: llm, NewModel: mockModels})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w, llm
}

// mockModels builds a mock named after the reference, without its provider.
func mockModels(_ context.Context, _ *config.Config, ref string) (model.LLM, error) {
	if ref == "broken" {
		return nil, errors.New("no such model")
	}
	_, name := runtime.ParseModelRef(ref, "")
	return runtime.NewMockLLM(name), nil
}

// savedConfig reads back the config file that operations save to.
func savedConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join(os.Getenv("HOME"), ".code_puppy"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func isResumeError(err error) bool {
	var re *ResumeError
	return errors.As(err, &re)
}

func writePNG(t *testing.T, path string) {
	t.Helper()
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 12, 8)))
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenSessionSetsAuditContextAndMapsResumeErrors(t *testing.T) {
	w := openTest(t)
	if _, _, err := w.OpenSession("", true); !isResumeError(err) {
		t.Errorf("--continue with no sessions: %v", err)
	}
	rec, resumed, err := w.OpenSession("", false)
	if err != nil || resumed || rec.Agent != w.Engine().ActiveAgent() {
		t.Fatalf("new session: %+v %v %v", rec, resumed, err)
	}
	got, resumed, err := w.OpenSession(rec.ID, false)
	if err != nil || !resumed || got.ID != rec.ID {
		t.Errorf("resume by id: %+v %v %v", got, resumed, err)
	}
}

func TestSelectSession(t *testing.T) {
	st, err := session.NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := selectSession(st, "", true, "t", "a"); !isResumeError(err) {
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
	if _, _, err := selectSession(st, "no-such-session", false, "", ""); !isResumeError(err) {
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

func TestModelErrorSummary(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.LLM.Gemini.APIKey = "AIzaSySECRETSECRETSECRETSECRETSECRET123"
	err := errors.New(`api key is required. ClientConfig: &genai.ClientConfig{APIKey:"AIzaSySECRETSECRETSECRETSECRETSECRET123"}` + "\nmore")
	got := ModelErrorSummary(err, cfg)
	if got != "api key is required" {
		t.Errorf("summary = %q", got)
	}
	leak := ModelErrorSummary(errors.New("bad key AIzaSySECRETSECRETSECRETSECRETSECRET123"), cfg)
	if strings.Contains(leak, "SECRET") {
		t.Errorf("key leaked: %q", leak)
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
	if _, _, err := selectSession(st, "latest", false, "", ""); !isResumeError(err) || !strings.Contains(err.Error(), "/proj/three") {
		t.Errorf("empty workspace should be a usage error naming it: %v", err)
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

func TestLoadAttachments(t *testing.T) {
	e := openTest(t)
	ws := e.Tools().Workspace().Dir()
	writePNG(t, filepath.Join(ws, "a.png"))
	writePNG(t, filepath.Join(ws, "b.png")) // same bytes as a.png: deduplicated

	var warnings []string
	warn := func(s string) { warnings = append(warnings, s) }
	got, err := e.LoadAttachments([]string{"a.png"}, "diff mentions @b.png and @gone.png", warn)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d images, %v", len(got), err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "gone.png") {
		t.Errorf("a bad mention should warn, not fail: %q", warnings)
	}
	if _, err := e.LoadAttachments([]string{"missing.png"}, "", warn); err == nil || !strings.Contains(err.Error(), "missing.png") {
		t.Errorf("a bad image path must fail: %v", err)
	}
}

func TestSetupLocaleWarnsAndFallsBack(t *testing.T) {
	defer i18n.SetCurrent(nil)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0o600)
	cfg := config.DefaultConfig()
	cfg.UI.Locale = "not a language!"
	cfg.UI.LocalesDir = dir
	var warnings []string
	SetupLocale(cfg, func(s string) { warnings = append(warnings, s) })
	if len(warnings) != 2 || !strings.Contains(warnings[0], "bad.json") || !strings.Contains(warnings[1], "not a language") {
		t.Errorf("warnings = %q", warnings)
	}
	if i18n.Current().Tag().String() != "en-US" {
		t.Errorf("fallback locale = %s", i18n.Current().Tag())
	}
}
