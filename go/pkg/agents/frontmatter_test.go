package agents

import (
	"testing"
)

func TestParseMarkdownSpec(t *testing.T) {
	sample := `---
name: test-agent
display_name: "Test Agent 🤖"
description: "A test agent for unit testing"
agency_level: "extreme"
tools:
  - read_file
  - run_shell_command
---
You are {puppy_name}, working for {owner_name}.
{agency_instructions}
`

	spec, err := ParseMarkdownSpec([]byte(sample))
	if err != nil {
		t.Fatalf("ParseMarkdownSpec failed: %v", err)
	}

	if spec.Name != "test-agent" {
		t.Errorf("expected 'test-agent', got '%s'", spec.Name)
	}
	if spec.DisplayName != "Test Agent 🤖" {
		t.Errorf("expected 'Test Agent 🤖', got '%s'", spec.DisplayName)
	}
	if len(spec.Tools) != 2 || spec.Tools[0] != "read_file" {
		t.Errorf("unexpected tools: %v", spec.Tools)
	}

	interpolated := spec.InterpolatePrompt("Pup", "Bob", "extreme")
	if !contains(interpolated, "Pup") || !contains(interpolated, "Bob") {
		t.Errorf("interpolation failed to replace variables: %s", interpolated)
	}
	if !contains(interpolated, "EXTREME agency") {
		t.Errorf("expected extreme agency instructions, got: %s", interpolated)
	}
}

func TestEmbeddedRegistry(t *testing.T) {
	reg, err := NewRegistry()
	if err != nil {
		t.Fatalf("failed to create registry: %v", err)
	}

	list := reg.List()
	if len(list) < 7 {
		t.Errorf("expected at least 7 embedded agents, got %d", len(list))
	}

	puppy, ok := reg.Get("code-puppy")
	if !ok || puppy == nil {
		t.Fatalf("expected to find 'code-puppy' agent")
	}

	if puppy.DisplayName != "Code-Puppy 🐶" {
		t.Errorf("expected 'Code-Puppy 🐶', got '%s'", puppy.DisplayName)
	}

	helios, ok := reg.Get("helios")
	if !ok || helios == nil {
		t.Fatalf("expected to find 'helios' agent")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && len(substr) > 0 && (s[:len(substr)] == substr || (len(s) > len(substr) && (s[len(s)-len(substr):] == substr || checkSubstr(s, substr)))))
}

func checkSubstr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
