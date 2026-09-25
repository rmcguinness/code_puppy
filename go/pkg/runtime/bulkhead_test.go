package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/config"
	"google.golang.org/genai"
)

func TestBoundedRunnerCapsConcurrencyAndRunsEveryTask(t *testing.T) {
	var running, peak, ran atomic.Int32
	tasks := make([]func(context.Context), 20)
	for i := range tasks {
		tasks[i] = func(context.Context) {
			n := running.Add(1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			time.Sleep(10 * time.Millisecond)
			running.Add(-1)
			ran.Add(1)
		}
	}
	boundedRunner(3)(context.Background(), tasks)
	if ran.Load() != 20 {
		t.Fatalf("ran %d of 20 tasks", ran.Load())
	}
	if p := peak.Load(); p != 3 {
		t.Fatalf("peak concurrency %d, want 3", p)
	}
}

// A task that fans out again (a sub-agent's tool calls) must not deadlock
// even when the limit is 1.
func TestBoundedRunnerNestedBatchesDoNotDeadlock(t *testing.T) {
	run := boundedRunner(1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(context.Background(), []func(context.Context){func(ctx context.Context) {
			run(ctx, []func(context.Context){func(context.Context) {}, func(context.Context) {}})
		}, func(context.Context) {}})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("nested batch deadlocked")
	}
}

func parallelShellTurn(t *testing.T, maxParallel int) time.Duration {
	t.Helper()
	var calls []*genai.Part
	for range 4 {
		calls = append(calls, &genai.Part{FunctionCall: &genai.FunctionCall{Name: "run_shell_command", Args: map[string]any{"command": "sleep 0.3"}}})
	}
	f := newEngineWith(t, fixtureOpts{cfg: func(c *config.Config) { c.Tools.MaxParallel = maxParallel }},
		&genai.Content{Role: genai.RoleModel, Parts: calls}, textContent("done"))
	start := time.Now()
	got, err := functionResponses(t, f.eng, "s", "run four sleeps")
	if err != nil {
		t.Fatal(err)
	}
	if r := got["run_shell_command"]; r == nil || r["error"] != nil && r["error"] != "" {
		t.Fatalf("shell call failed: %v", r)
	}
	return time.Since(start)
}

func TestEngineCapsParallelToolCalls(t *testing.T) {
	if d := parallelShellTurn(t, 1); d < 1100*time.Millisecond {
		t.Fatalf("max_parallel=1 ran 4×0.3s sleeps in %v; they overlapped", d)
	}
	if d := parallelShellTurn(t, 0); d > 1100*time.Millisecond {
		t.Fatalf("unlimited took %v; calls did not run in parallel", d)
	}
}
