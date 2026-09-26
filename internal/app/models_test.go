package app

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestAgentsAndModel(t *testing.T) {
	w := openTest(t)
	ctx := context.Background()
	if a := w.ActiveAgent(); a.Name != "code-puppy" || !a.Active || a.DisplayName == "" {
		t.Fatalf("active agent %+v", a)
	}
	if i := slices.IndexFunc(w.ListAgents(), func(a AgentInfo) bool { return a.Name == "qa-kitten" }); i < 0 {
		t.Fatal("qa-kitten not listed")
	}
	if _, err := w.SetAgent(ctx, "nobody"); err == nil {
		t.Error("switched to an unknown agent")
	}
	if a, err := w.SetAgent(ctx, "qa-kitten"); err != nil || !a.Active || w.ActiveAgent().Name != "qa-kitten" {
		t.Fatalf("switch: %+v %v", a, err)
	}

	if _, err := w.SetModel(ctx, "broken"); err == nil {
		t.Error("switched to a model that failed to build")
	}
	if pin, err := w.SetModel(ctx, "openai/gpt-5"); err != nil || pin != "" || w.Model().Name != "gpt-5" || w.Config().CodePuppy.DefaultModel != "openai/gpt-5" {
		t.Fatalf("set model: %q %v %+v", pin, err, w.Model())
	}
	// The active agent's pin still decides what it runs on.
	if _, err := w.PinModel(ctx, "qa-kitten", "anthropic/claude-haiku-4-5"); err != nil {
		t.Fatal(err)
	}
	if pin, err := w.SetModel(ctx, "gemini-3.8-flash"); err != nil || pin != "claude-haiku-4-5" {
		t.Errorf("active pin = %q, %v", pin, err)
	}
}

func TestPinAndUnpinSaveToTheConfigFile(t *testing.T) {
	w := openTest(t)
	ctx := context.Background()
	var unknown *UnknownAgentError
	if _, err := w.PinModel(ctx, "nobody", "x"); !errors.As(err, &unknown) || unknown.Name != "nobody" {
		t.Fatalf("unknown agent: %v", err)
	}
	res, err := w.PinModel(ctx, "qa-kitten", "anthropic/claude-haiku-4-5")
	if err != nil || res.Model != "claude-haiku-4-5" || res.Saved.Err != nil || res.Saved.Path == "" {
		t.Fatalf("pin: %+v %v", res, err)
	}
	if got := savedConfig(t).AgentModels["qa-kitten"]; got != "anthropic/claude-haiku-4-5" {
		t.Errorf("saved pin %q", got)
	}
	if a := w.ListAgents()[slices.IndexFunc(w.ListAgents(), func(a AgentInfo) bool { return a.Name == "qa-kitten" })]; a.PinnedModel != "claude-haiku-4-5" {
		t.Errorf("listed pin %q", a.PinnedModel)
	}
	if res, err = w.Unpin(ctx, "qa-kitten"); err != nil || res.Model != "gemini-3.8-flash" {
		t.Fatalf("unpin: %+v %v", res, err)
	}
	if got := savedConfig(t).AgentModels; len(got) != 0 {
		t.Errorf("pin still saved: %v", got)
	}
}

func TestUpdateModelSettings(t *testing.T) {
	w := openTest(t)
	w.Config().LLM.Provider = "openai"
	if _, err := w.UpdateModelSettings("temperature=1", false, nil); !errors.Is(err, ErrBadModelRef) {
		t.Errorf("bad ref: %v", err)
	}
	res, err := w.UpdateModelSettings("openai/gpt-5", false, []Setting{{"temperature", "0.3"}, {"seed", "7"}})
	if err != nil || res.Model != "gpt-5" || !slices.Equal(res.Unsupported, []string{"seed"}) || res.Saved.Err != nil {
		t.Fatalf("update: %+v %v", res, err)
	}
	// An invalid value changes nothing, including the valid change before it.
	var invalid *InvalidSettingError
	if _, err := w.UpdateModelSettings("gpt-5", false, []Setting{{"top_p", "0.5"}, {"temperature", "9"}}); !errors.As(err, &invalid) {
		t.Errorf("invalid: %v", err)
	}
	if s := savedConfig(t).ModelSettings["gpt-5"]; s.TopP != nil || s.Seed == nil || *s.Seed != 7 {
		t.Errorf("saved %+v", s)
	}
	info, _ := w.ModelSettings("gpt-5")
	if info.Settings.TopP != nil || info.GlobalTemperature != w.Config().CodePuppy.Temperature {
		t.Errorf("info %+v", info)
	}
	if res, _ := w.UpdateModelSettings("gpt-5", true, nil); !res.Settings.IsZero() || len(w.AllModelSettings()) != 0 {
		t.Errorf("reset: %+v", res)
	}
}

func TestSetChangesSettings(t *testing.T) {
	w := openTest(t)
	ctx := context.Background()
	if _, err := w.Set(ctx, "agency", "reckless"); !errors.Is(err, ErrInvalidAgency) {
		t.Errorf("agency: %v", err)
	}
	var unknown *UnknownSettingError
	if _, err := w.Set(ctx, "Colour", "blue"); !errors.As(err, &unknown) || unknown.Key != "colour" {
		t.Errorf("unknown: %v", err)
	}
	if key, err := w.Set(ctx, " Agency_Level ", "HIGH"); err != nil || key != "agency_level" {
		t.Fatalf("set: %q %v", key, err)
	}
	w.Set(ctx, "owner_name", "Sam")
	if s := w.Settings(); s.Agency != "high" || s.OwnerName != "Sam" || s.Agent != "code-puppy" || s.Locale == "" {
		t.Errorf("settings %+v", s)
	}
}
