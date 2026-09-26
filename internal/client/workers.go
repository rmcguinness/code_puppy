package client

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/retail-cortex/code_puppy/internal/app"
	pb "github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1"
	"github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1/codepuppyv1connect"
	"github.com/retail-cortex/code_puppy/internal/server"
	"github.com/retail-cortex/code_puppy/internal/workers"
)

// Workers are one workspace's workers in the service.
type Workers struct {
	dir string
	c   codepuppyv1connect.WorkerServiceClient
}

// AttachWorkers reaches the workers of the workspace dir (absolute) in the
// service listening on socket.
func AttachWorkers(socket, dir string) *Workers {
	return AttachWorkersHTTP(server.Client(socket), server.BaseURL, dir)
}

// AttachWorkersHTTP is AttachWorkers over any HTTP client.
func AttachWorkersHTTP(hc connect.HTTPClient, baseURL, dir string) *Workers {
	return &Workers{dir: dir, c: codepuppyv1connect.NewWorkerServiceClient(hc, baseURL)}
}

func (w *Workers) ListWorkers() ([]app.WorkerInfo, error) {
	res, err := w.c.ListWorkers(context.Background(), connect.NewRequest(&pb.ListWorkersRequest{Workspace: w.dir}))
	if err != nil {
		return nil, fromAPI(err)
	}
	out := make([]app.WorkerInfo, len(res.Msg.Workers))
	for i, wk := range res.Msg.Workers {
		out[i] = workerInfo(wk)
	}
	return out, nil
}

func (w *Workers) EnableWorker(name, hash string) (app.WorkerInfo, error) {
	res, err := w.c.EnableWorker(context.Background(), connect.NewRequest(&pb.EnableWorkerRequest{Workspace: w.dir, Name: name, Hash: hash}))
	if err != nil {
		return app.WorkerInfo{}, fromAPI(err)
	}
	return workerInfo(res.Msg.Worker), nil
}

func (w *Workers) DisableWorker(name string) (app.WorkerInfo, error) {
	res, err := w.c.DisableWorker(context.Background(), connect.NewRequest(&pb.DisableWorkerRequest{Workspace: w.dir, Name: name}))
	if err != nil {
		return app.WorkerInfo{}, fromAPI(err)
	}
	return workerInfo(res.Msg.Worker), nil
}

// RunWorker runs the worker now in the service, passes its events to
// on while it runs, and returns its record when it finishes.
func (w *Workers) RunWorker(ctx context.Context, name string, on func(app.Event)) (workers.Run, error) {
	res, err := w.c.RunWorker(ctx, connect.NewRequest(&pb.RunWorkerRequest{Workspace: w.dir, Name: name}))
	if err != nil {
		return workers.Run{}, fromAPI(err)
	}
	id := res.Msg.Run.Id
	if stream, err := w.c.WatchWorkerRun(ctx, connect.NewRequest(&pb.WatchWorkerRunRequest{RunId: id})); err == nil {
		for stream.Receive() {
			if e, ok := event(stream.Msg().Event); ok && on != nil {
				on(e)
			}
		}
		stream.Close()
	}
	// The record appears once the run has finished (and at once if it
	// finished before watching began).
	for {
		got, err := w.c.GetWorkerRun(ctx, connect.NewRequest(&pb.GetWorkerRunRequest{RunId: id}))
		if err != nil {
			return workers.Run{}, fromAPI(err)
		}
		if got.Msg.Run.Status != pb.RunStatus_RUN_STATUS_RUNNING {
			return workerRun(got.Msg.Run), nil
		}
		select {
		case <-ctx.Done():
			return workerRun(got.Msg.Run), ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (w *Workers) WorkerRuns(name string, limit int) ([]workers.Run, error) {
	res, err := w.c.ListWorkerRuns(context.Background(), connect.NewRequest(&pb.ListWorkerRunsRequest{Workspace: w.dir, Name: name, Limit: int32(limit)}))
	if err != nil {
		return nil, fromAPI(err)
	}
	out := make([]workers.Run, len(res.Msg.Runs))
	for i, r := range res.Msg.Runs {
		out[i] = workerRun(r)
	}
	return out, nil
}

var workerStates = map[pb.WorkerState]workers.State{
	pb.WorkerState_WORKER_STATE_NEW:      workers.StateNew,
	pb.WorkerState_WORKER_STATE_ENABLED:  workers.StateEnabled,
	pb.WorkerState_WORKER_STATE_DISABLED: workers.StateDisabled,
	pb.WorkerState_WORKER_STATE_CHANGED:  workers.StateChanged,
	pb.WorkerState_WORKER_STATE_INVALID:  workers.StateInvalid,
}

func workerInfo(w *pb.Worker) app.WorkerInfo {
	return app.WorkerInfo{
		Workspace: w.Workspace, Name: w.Name, Description: w.Description, Path: w.Path, Hash: w.Hash,
		State: workerStates[w.State], Schedule: w.Schedule, Cron: w.Cron, Timezone: w.Timezone, Next: timeOf(w.NextRun),
		Agent: w.Agent, Model: w.Model, Permissions: w.Permissions, Problems: w.Problems,
		Limits: workers.Limits{MaxTurns: int(w.Limits.GetMaxTurns()), MaxCostUSD: w.Limits.GetMaxCostUsd(), Timeout: w.Limits.GetTimeout().AsDuration()},
	}
}

var runStatuses = map[pb.RunStatus]workers.RunStatus{
	pb.RunStatus_RUN_STATUS_RUNNING:   workers.RunRunning,
	pb.RunStatus_RUN_STATUS_SUCCEEDED: workers.RunSucceeded,
	pb.RunStatus_RUN_STATUS_FAILED:    workers.RunFailed,
	pb.RunStatus_RUN_STATUS_LIMITED:   workers.RunLimited,
	pb.RunStatus_RUN_STATUS_SKIPPED:   workers.RunSkipped,
}

func workerRun(r *pb.WorkerRun) workers.Run {
	out := workers.Run{
		ID: r.Id, Workspace: r.Workspace, Worker: r.Worker, Hash: r.Hash, Status: runStatuses[r.Status], Manual: r.Manual,
		Started: timeOf(r.Started), Duration: r.GetDuration().AsDuration(), CostUSD: r.Usage.GetCostUsd(), Calls: int(r.Usage.GetCalls()),
		SessionID: r.SessionId, Error: r.GetError().GetMessage(),
	}
	for _, f := range r.Refusals {
		out.Refusals = append(out.Refusals, workers.Refusal{Tool: f.Tool, Kind: actionKind(f.Kind), Detail: f.Detail, Time: timeOf(f.Time)})
	}
	return out
}
