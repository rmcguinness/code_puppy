package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
)

func TestModelSettingsCommand(t *testing.T) {
	app, _ := newCommandApp(t, "")
	local(app).Config().LLM.Provider = "openai"
	run := func(cmd string) string {
		return captureStdout(t, func() { HandleCommand(context.Background(), cmd, app) })
	}

	if out := run("/model_settings"); !strings.Contains(out, "No model has settings") {
		t.Errorf("empty list:\n%s", out)
	}
	if out := run("/model_settings temperature=1"); !strings.Contains(out, "Usage: /model_settings") {
		t.Errorf("missing model:\n%s", out)
	}

	// Set several at once; a provider prefix is dropped from the name.
	out := run("/model_settings openai/gpt-5 temperature=0.3 max_tokens=2048 seed=7")
	if !strings.Contains(out, "gpt-5 now uses temperature=0.3 max_tokens=2048 seed=7") || !strings.Contains(out, "Saved in") {
		t.Errorf("set:\n%s", out)
	}
	if !strings.Contains(out, "openai doesn't accept seed for gpt-5") {
		t.Errorf("no warning about the unsupported seed:\n%s", out)
	}
	if s := local(app).Engine().ModelSettings("gpt-5"); s.Temperature == nil || *s.Temperature != 0.3 || *s.MaxTokens != 2048 {
		t.Fatalf("engine: %+v", s)
	}
	if s, ok := savedConfig(t).ModelSettings["gpt-5"]; !ok || s.Seed == nil || *s.Seed != 7 {
		t.Fatalf("saved = %+v", savedConfig(t).ModelSettings)
	}

	// An invalid value changes nothing, including the valid pair before it.
	if out := run("/model_settings gpt-5 top_p=0.5 temperature=9"); !strings.Contains(out, "Nothing changed") {
		t.Errorf("invalid:\n%s", out)
	}
	if s := local(app).Engine().ModelSettings("gpt-5"); s.TopP != nil || savedConfig(t).ModelSettings["gpt-5"].TopP != nil {
		t.Fatalf("partly applied: %+v", s)
	}
	if out := run("/model_settings gpt-5 temperature"); !strings.Contains(out, "Expected key=value") {
		t.Errorf("bad pair:\n%s", out)
	}

	// Clear one key, then show where the others come from.
	run("/model_settings gpt-5 temperature=")
	out = run("/model_settings gpt-5")
	if !strings.Contains(out, "(global: 0.2)") || !strings.Contains(out, "2048") || !strings.Contains(out, "(provider default)") {
		t.Errorf("show:\n%s", out)
	}
	if out := run("/model_settings"); !strings.Contains(out, "gpt-5") || !strings.Contains(out, "max_tokens=2048 seed=7") {
		t.Errorf("list:\n%s", out)
	}

	out = run("/model_settings gpt-5 reset")
	if !strings.Contains(out, "gpt-5 now uses the global settings") || !local(app).Engine().ModelSettings("gpt-5").IsZero() {
		t.Errorf("reset:\n%s", out)
	}
	if s := savedConfig(t).ModelSettings["gpt-5"]; !s.IsZero() {
		t.Fatalf("reset not saved: %+v", s)
	}
}

// A hand-written "provider/model" table is edited in place, not duplicated.
func TestModelSettingsSavesUnderTheExistingKey(t *testing.T) {
	app, _ := newCommandApp(t, "")
	local(app).Config().ModelSettings = map[string]config.ModelSettings{"openai/gpt-5": {}}
	captureStdout(t, func() { HandleCommand(context.Background(), "/model_settings gpt-5 temperature=0.4", app) })
	saved := savedConfig(t).ModelSettings
	if s, ok := saved["openai/gpt-5"]; !ok || s.Temperature == nil || len(saved) != 1 {
		t.Fatalf("saved = %+v", saved)
	}
}
