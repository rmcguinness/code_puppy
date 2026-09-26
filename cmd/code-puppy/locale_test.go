package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/i18n"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"google.golang.org/genai"
)

func lastSystemText(m *runtime.MockLLM) string {
	req := m.Requests[len(m.Requests)-1]
	var sb strings.Builder
	if req.Config != nil && req.Config.SystemInstruction != nil {
		for _, p := range req.Config.SystemInstruction.Parts {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

// /locale es must reach the model (reply in Spanish), survive a restart via
// the config file, and switching back to English must remove the instruction.
func TestLocaleReachesModelAndConfig(t *testing.T) {
	defer i18n.SetCurrent(nil)
	ctx := context.Background()
	e := testEnv(t)
	cfgDir := t.TempDir()
	t.Setenv("MODENV_PREFIX", cfgDir)
	cfgFile := filepath.Join(cfgDir, ".env.toml")
	os.WriteFile(cfgFile, []byte("# my settings\n[ui]\nlocale = \"en-US\"   # keep me\n"), 0o600)

	e.locales = setupLocale(e.cfg, func(s string) { t.Errorf("warning: %s", s) })
	reply := func() *genai.Content { return genai.NewContentFromText("ok", genai.RoleModel) }
	llm := runtime.NewMockLLM("gemini-3.8-flash", reply(), reply())
	if err := e.engine.SetModel(ctx, llm); err != nil {
		t.Fatal(err)
	}
	run := func() string {
		sess, _ := e.storage.CreateSession("", "t", "code-puppy")
		var out bytes.Buffer
		if err := runOneShot(ctx, e, oneShotOptions{prompt: "hi", sessionID: sess.ID, format: formatText, stdout: &out}); err != nil {
			t.Fatal(err)
		}
		return lastSystemText(llm)
	}

	tag, _ := e.locales.Resolve("ES-sp")
	l := e.locales.Localizer(tag)
	i18n.SetCurrent(l)
	path, err := e.setLocale(ctx, l)
	if err != nil || path != cfgFile {
		t.Fatalf("setLocale = %q, %v", path, err)
	}
	if sys := run(); !strings.Contains(sys, "## Response Language") || !strings.Contains(sys, "Spanish") {
		t.Errorf("system prompt lacks the reply-language instruction:\n%s", sys)
	}
	data, _ := os.ReadFile(cfgFile)
	if !strings.Contains(string(data), "# my settings\n[ui]\nlocale = \"es\"   # keep me") {
		t.Errorf("config not edited in place:\n%s", data)
	}

	// Restart: the saved locale is picked up.
	i18n.SetCurrent(nil)
	cfg, err := config.Load(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	setupLocale(cfg, func(string) {})
	if got := i18n.Current().Tag().String(); got != "es" || i18n.T("exit.cancelled") != "Salida cancelada." {
		t.Errorf("after reload: %s", got)
	}

	enTag, _ := e.locales.Resolve("en-US")
	en := e.locales.Localizer(enTag)
	i18n.SetCurrent(en)
	if _, err := e.setLocale(ctx, en); err != nil {
		t.Fatal(err)
	}
	if sys := run(); strings.Contains(sys, "Response Language") {
		t.Errorf("English should not add a language instruction:\n%s", sys)
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
	setupLocale(cfg, func(s string) { warnings = append(warnings, s) })
	if len(warnings) != 2 || !strings.Contains(warnings[0], "bad.json") || !strings.Contains(warnings[1], "not a language") {
		t.Errorf("warnings = %q", warnings)
	}
	if i18n.Current().Tag().String() != "en-US" {
		t.Errorf("fallback locale = %s", i18n.Current().Tag())
	}
}
