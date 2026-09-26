package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanModeRefusesChangesButAllowsReading(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{},
		toolCall("create_file", map[string]any{"path": "new.txt", "content": "x"}),
		toolCall("read_file", map[string]any{"path": "notes.txt"}),
		textContent("1. Do the thing"))
	os.WriteFile(filepath.Join(f.cfg.Tools.WorkspaceDir, "notes.txt"), []byte("existing notes"), 0o600)

	got, err := functionResponses(t, f.eng, "s", PlanPrompt("add a file"), WithPlanOnly())
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := got["create_file"]["error"].(string); !strings.Contains(e, "plan mode") {
		t.Fatalf("create_file not refused: %v", got["create_file"])
	}
	if _, err := os.Stat(filepath.Join(f.cfg.Tools.WorkspaceDir, "new.txt")); err == nil {
		t.Fatal("file created in plan mode")
	}
	if c, _ := got["read_file"]["content"].(string); !strings.Contains(c, "existing notes") {
		t.Fatalf("read_file blocked in plan mode: %v", got["read_file"])
	}
}

func TestPlanModeCoversSubagents(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{},
		toolCall("invoke_agent", map[string]any{"agent_name": "qa-kitten", "prompt": "write a test"}),
		toolCall("create_file", map[string]any{"path": "sub.txt", "content": "x"}), // the sub-agent tries to write
		textContent("sub-agent done"),
		textContent("plan ready"))
	if _, err := functionResponses(t, f.eng, "s", PlanPrompt("tests"), WithPlanOnly()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.Tools.WorkspaceDir, "sub.txt")); err == nil {
		t.Fatal("sub-agent wrote a file in plan mode")
	}
	// The sub-agent really ran and was refused (not skipped): its second
	// model call carries the refusal of its create_file.
	refused := false
	for _, req := range f.llm.Requests {
		for _, c := range req.Contents {
			for _, p := range c.Parts {
				if r := p.FunctionResponse; r != nil && r.Name == "create_file" {
					e, _ := r.Response["error"].(string)
					refused = refused || strings.Contains(e, "plan mode")
				}
			}
		}
	}
	if !refused {
		t.Fatal("sub-agent's create_file was never attempted and refused")
	}
}

func TestWithoutPlanModeToolsRunNormally(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{},
		toolCall("create_file", map[string]any{"path": "new.txt", "content": "x"}),
		textContent("done"))
	if _, err := functionResponses(t, f.eng, "s", "add a file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.Tools.WorkspaceDir, "new.txt")); err != nil {
		t.Fatal("create_file did not run outside plan mode")
	}
}
