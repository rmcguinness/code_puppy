package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/config"
)

// appendHook appends each event (one JSON document) as a line to out.
func appendHook(out string, extra string) config.HooksConfig {
	return config.HooksConfig{PostTool: []config.HookConfig{{Command: extra + "cat >> " + out + "; echo >> " + out}}}
}

func readHookLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func readHookEvents(t *testing.T, path string) []HookEvent {
	t.Helper()
	var evs []HookEvent
	for _, l := range readHookLines(t, path) {
		var ev HookEvent
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("bad event %q: %v", l, err)
		}
		evs = append(evs, ev)
	}
	return evs
}

func TestPostToolDoesNotWaitForHooks(t *testing.T) {
	h, _ := newHooks(t, config.HooksConfig{PostTool: []config.HookConfig{{Command: "sleep 2"}}})
	start := time.Now()
	h.PostTool(context.Background(), "s", "grep", nil, nil, nil)
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("PostTool blocked for %v", d)
	}
}

func TestPostToolHooksRunInOrderOnSnapshots(t *testing.T) {
	out := filepath.Join(t.TempDir(), "events.jsonl")
	h, _ := newHooks(t, appendHook(out, ""))
	const n = 20
	for i := range n {
		result := map[string]any{"v": "original"}
		h.PostTool(context.Background(), "s", "grep", map[string]any{"n": i}, result, nil)
		result["v"] = "mutated after the call" // must not reach the hook
	}
	if err := h.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	evs := readHookEvents(t, out)
	if len(evs) != n {
		t.Fatalf("want %d events, got %d", n, len(evs))
	}
	for i, ev := range evs {
		if ev.Tool != "grep" || ev.Args["n"] != float64(i) || ev.Result["v"] != "original" {
			t.Fatalf("event %d = %+v, want n=%d with the original result", i, ev, i)
		}
	}
}

// The turn's context is often cancelled (turn over, Ctrl+C) before queued
// hooks run; they must still run.
func TestPostToolHooksOutliveTheTurnContext(t *testing.T) {
	out := filepath.Join(t.TempDir(), "events.jsonl")
	h, _ := newHooks(t, appendHook(out, ""))
	ctx, cancel := context.WithCancel(context.Background())
	h.PostTool(ctx, "s", "grep", map[string]any{"n": 1}, nil, nil)
	cancel()
	h.flush(context.Background())
	if lines := readHookLines(t, out); len(lines) != 1 {
		t.Fatalf("hook did not run after cancellation: %v", lines)
	}
}

func TestPostToolHooksOnlyForMatchingTools(t *testing.T) {
	out := filepath.Join(t.TempDir(), "events.jsonl")
	h, _ := newHooks(t, config.HooksConfig{PostTool: []config.HookConfig{{Match: "run_*", Command: "cat >> " + out}}})
	h.PostTool(context.Background(), "s", "read_file", nil, nil, nil)
	if h.postQ.jobs != nil {
		t.Fatal("worker started for a tool no hook matches")
	}
	h.PostTool(context.Background(), "s", "run_shell_command", nil, nil, nil)
	h.flush(context.Background())
	if b, _ := os.ReadFile(out); !strings.Contains(string(b), "run_shell_command") {
		t.Fatalf("matching hook did not run: %s", b)
	}
}

func TestCloseDrainsQueuedHooks(t *testing.T) {
	out := filepath.Join(t.TempDir(), "events.jsonl")
	h, _ := newHooks(t, appendHook(out, "sleep 0.1; "))
	for i := range 3 {
		h.PostTool(context.Background(), "s", "grep", map[string]any{"n": i}, nil, nil)
	}
	h.Close()
	if lines := readHookLines(t, out); len(lines) != 3 {
		t.Fatalf("Close did not drain: %v", lines)
	}
	h.PostTool(context.Background(), "s", "grep", nil, nil, nil) // after Close: ignored, no panic
	h.Close()                                                    // idempotent
}

func TestCloseStopsHungHooks(t *testing.T) {
	old := postDrainTimeout
	postDrainTimeout = 200 * time.Millisecond
	defer func() { postDrainTimeout = old }()

	h, _ := newHooks(t, config.HooksConfig{PostTool: []config.HookConfig{{Command: "sleep 30"}}})
	h.PostTool(context.Background(), "s", "grep", nil, nil, nil)
	time.Sleep(50 * time.Millisecond) // let the hook start
	start := time.Now()
	h.Close()
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Close took %v with a hung hook", d)
	}
}

func TestFullQueueDropsInsteadOfBlocking(t *testing.T) {
	old := postQueueSize
	postQueueSize = 1
	defer func() { postQueueSize = old }()

	h, warnings := newHooks(t, config.HooksConfig{PostTool: []config.HookConfig{{Command: "sleep 1"}}})
	start := time.Now()
	for range 5 {
		h.PostTool(context.Background(), "s", "grep", nil, nil, nil)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("PostTool blocked on a full queue for %v", d)
	}
	if h.postQ.dropped.Load() == 0 {
		t.Fatal("no events dropped from a full queue")
	}
	if len(*warnings) != 1 {
		t.Fatalf("want one warning for the whole backlog, got %v", *warnings)
	}
}
