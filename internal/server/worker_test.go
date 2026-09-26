package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/retail-cortex/code_puppy/internal/app"
	"github.com/retail-cortex/code_puppy/internal/config"
	pb "github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1"
	"github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1/codepuppyv1connect"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"github.com/retail-cortex/code_puppy/internal/workers"
	"google.golang.org/genai"
)

// serveWorkers starts a server that runs workers, on mock models answering
// with replies, and returns a worker client for it.
func serveWorkers(t *testing.T, rescan time.Duration, replies ...*genai.Content) (codepuppyv1connect.WorkerServiceClient, *Server) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MODENV_PREFIX", "")
	store, err := workers.OpenStore(filepath.Join(t.TempDir(), "workers.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(func(ctx context.Context, dir string) (*app.Workspace, error) {
		cfg := config.DefaultConfig()
		cfg.Tools.WorkspaceDir = dir
		cfg.Session.StorageDir = t.TempDir()
		return app.Open(ctx, cfg, app.Options{Model: runtime.NewMockLLM("m", replies...), Workers: store})
	}, WithScheduler(SchedulerConfig{Store: store, Runs: workers.OpenRunLog(filepath.Join(os.Getenv("HOME"), ".code_puppy", "worker-runs")), MaxConcurrent: 2, Rescan: rescan}))
	srv := httptest.NewServer(s.Handler())
	ctx, cancel := context.WithCancel(context.Background())
	s.StartScheduler(ctx)
	t.Cleanup(func() { cancel(); srv.Close(); s.Close() })
	return codepuppyv1connect.NewWorkerServiceClient(http.DefaultClient, srv.URL), s
}

func addWorker(t *testing.T, dir, name, content string) {
	t.Helper()
	wd := filepath.Join(dir, "workers", name)
	os.MkdirAll(wd, 0o755)
	if err := os.WriteFile(filepath.Join(wd, workers.FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWorkersOverTheAPI(t *testing.T) {
	c, _ := serveWorkers(t, time.Hour, text("Report written."))
	ctx := context.Background()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	addWorker(t, dir, "deps", "---\nschedule: Daily at 6 AM\n---\nWrite the report.\n")

	list, err := c.ListWorkers(ctx, connect.NewRequest(&pb.ListWorkersRequest{Workspace: dir}))
	if err != nil || len(list.Msg.Workers) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	wk := list.Msg.Workers[0]
	if wk.State != pb.WorkerState_WORKER_STATE_NEW || wk.Cron != "0 6 * * *" || wk.Limits.GetMaxTurns() == 0 {
		t.Errorf("worker %v", wk)
	}
	_, err = c.RunWorker(ctx, connect.NewRequest(&pb.RunWorkerRequest{Workspace: dir, Name: "deps"}))
	if code, info := errorReason(t, err); code != connect.CodeFailedPrecondition || info.Reason != "WORKER_DISABLED" {
		t.Errorf("run before enabling: %v %v", code, info)
	}
	_, err = c.EnableWorker(ctx, connect.NewRequest(&pb.EnableWorkerRequest{Workspace: dir, Name: "deps", Hash: "sha256:stale"}))
	if code, info := errorReason(t, err); code != connect.CodeFailedPrecondition || info.Reason != "HASH_MISMATCH" {
		t.Errorf("stale hash: %v %v", code, info)
	}
	_, err = c.EnableWorker(ctx, connect.NewRequest(&pb.EnableWorkerRequest{Workspace: dir, Name: "nope", Hash: "x"}))
	if code, info := errorReason(t, err); code != connect.CodeNotFound || info.Reason != "UNKNOWN_WORKER" {
		t.Errorf("unknown worker: %v %v", code, info)
	}
	on, err := c.EnableWorker(ctx, connect.NewRequest(&pb.EnableWorkerRequest{Workspace: dir, Name: "deps", Hash: wk.Hash}))
	if err != nil || on.Msg.Worker.State != pb.WorkerState_WORKER_STATE_ENABLED || on.Msg.Worker.NextRun == nil {
		t.Fatalf("enable %v %v", on, err)
	}
	if all, _ := c.ListWorkers(ctx, connect.NewRequest(&pb.ListWorkersRequest{})); len(all.Msg.Workers) != 1 {
		t.Errorf("every registered workspace: %v", all.Msg.Workers)
	}

	started, err := c.RunWorker(ctx, connect.NewRequest(&pb.RunWorkerRequest{Workspace: dir, Name: "deps"}))
	if err != nil || started.Msg.Run.Status != pb.RunStatus_RUN_STATUS_RUNNING || !started.Msg.Run.Manual {
		t.Fatalf("run %v %v", started, err)
	}
	id := started.Msg.Run.Id
	// Watching may begin after the run has; the events come from its start.
	var text string
	var finished *pb.TurnFinished
	if stream, err := c.WatchWorkerRun(ctx, connect.NewRequest(&pb.WatchWorkerRunRequest{RunId: id})); err == nil {
		for stream.Receive() {
			ev := stream.Msg().Event
			if t := ev.GetText(); t != nil {
				text += t.Text
			}
			if f := ev.GetFinished(); f != nil {
				finished = f
			}
		}
	}
	// A run too quick to watch is already recorded; either way it succeeded.
	if finished != nil && (finished.Error != nil || text != "Report written.") {
		t.Errorf("watched %q, finished %v", text, finished)
	}
	var run *pb.WorkerRun
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if got, err := c.GetWorkerRun(ctx, connect.NewRequest(&pb.GetWorkerRunRequest{RunId: id})); err == nil && got.Msg.Run.Status != pb.RunStatus_RUN_STATUS_RUNNING {
			run = got.Msg.Run
			break
		}
	}
	if run == nil || run.Status != pb.RunStatus_RUN_STATUS_SUCCEEDED || run.SessionId == "" {
		t.Fatalf("run record %v", run)
	}
	runs, err := c.ListWorkerRuns(ctx, connect.NewRequest(&pb.ListWorkerRunsRequest{Workspace: dir, Name: "deps"}))
	if err != nil || len(runs.Msg.Runs) != 1 || runs.Msg.Runs[0].Id != id {
		t.Errorf("runs %v %v", runs, err)
	}
	_, err = c.GetWorkerRun(ctx, connect.NewRequest(&pb.GetWorkerRunRequest{RunId: "nope"}))
	if code, info := errorReason(t, err); code != connect.CodeNotFound || info.Reason != "UNKNOWN_RUN" {
		t.Errorf("unknown run: %v %v", code, info)
	}
	off, err := c.DisableWorker(ctx, connect.NewRequest(&pb.DisableWorkerRequest{Workspace: dir, Name: "deps"}))
	if err != nil || off.Msg.Worker.State != pb.WorkerState_WORKER_STATE_DISABLED {
		t.Errorf("disable %v %v", off, err)
	}
}

// An enabled worker runs on its schedule without anyone asking.
func TestWorkersRunOnSchedule(t *testing.T) {
	c, s := serveWorkers(t, 50*time.Millisecond, text("tick"), text("tick"), text("tick"), text("tick"))
	ctx := context.Background()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	addWorker(t, dir, "ticker", "---\nschedule: \"@every 1s\"\n---\nSay tick.\n")
	list, err := c.ListWorkers(ctx, connect.NewRequest(&pb.ListWorkersRequest{Workspace: dir}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.EnableWorker(ctx, connect.NewRequest(&pb.EnableWorkerRequest{Workspace: dir, Name: "ticker", Hash: list.Msg.Workers[0].Hash})); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		runs, err := c.ListWorkerRuns(ctx, connect.NewRequest(&pb.ListWorkerRunsRequest{Workspace: dir, Name: "ticker"}))
		if err == nil {
			for _, r := range runs.Msg.Runs {
				if r.Status == pb.RunStatus_RUN_STATUS_SUCCEEDED && !r.Manual {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			s.sched.mu.Lock()
			n := len(s.sched.entries)
			s.sched.mu.Unlock()
			t.Fatalf("no scheduled run happened; %d workers scheduled", n)
		}
	}
}
