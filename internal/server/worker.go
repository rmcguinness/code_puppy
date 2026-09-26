package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/retail-cortex/code_puppy/internal/app"
	pb "github.com/retail-cortex/code_puppy/internal/gen/codepuppy/v1"
	"github.com/retail-cortex/code_puppy/internal/workers"
	"google.golang.org/protobuf/types/known/durationpb"
)

// workerService implements WorkerService.
type workerService struct{ s *Server }

var errNoScheduler = apiError(connect.CodeFailedPrecondition, "NO_SCHEDULER", errors.New("this server doesn't run workers"))

func workerState(s workers.State) pb.WorkerState {
	switch s {
	case workers.StateNew:
		return pb.WorkerState_WORKER_STATE_NEW
	case workers.StateEnabled:
		return pb.WorkerState_WORKER_STATE_ENABLED
	case workers.StateDisabled:
		return pb.WorkerState_WORKER_STATE_DISABLED
	case workers.StateChanged:
		return pb.WorkerState_WORKER_STATE_CHANGED
	case workers.StateInvalid:
		return pb.WorkerState_WORKER_STATE_INVALID
	}
	return pb.WorkerState_WORKER_STATE_UNSPECIFIED
}

func workerMsg(i app.WorkerInfo) *pb.Worker {
	return &pb.Worker{
		Workspace: i.Workspace, Name: i.Name, Description: i.Description, Path: i.Path, Hash: i.Hash,
		State: workerState(i.State), Schedule: i.Schedule, Cron: i.Cron, Timezone: i.Timezone, NextRun: timestamp(i.Next),
		Agent: i.Agent, Model: i.Model, Permissions: i.Permissions,
		Limits:   &pb.WorkerLimits{MaxTurns: int32(i.Limits.MaxTurns), MaxCostUsd: i.Limits.MaxCostUSD, Timeout: durationpb.New(i.Limits.Timeout)},
		Problems: i.Problems,
	}
}

func runStatus(s workers.RunStatus) pb.RunStatus {
	switch s {
	case workers.RunRunning:
		return pb.RunStatus_RUN_STATUS_RUNNING
	case workers.RunSucceeded:
		return pb.RunStatus_RUN_STATUS_SUCCEEDED
	case workers.RunFailed:
		return pb.RunStatus_RUN_STATUS_FAILED
	case workers.RunLimited:
		return pb.RunStatus_RUN_STATUS_LIMITED
	case workers.RunSkipped:
		return pb.RunStatus_RUN_STATUS_SKIPPED
	}
	return pb.RunStatus_RUN_STATUS_UNSPECIFIED
}

func runMsg(r workers.Run) *pb.WorkerRun {
	out := &pb.WorkerRun{
		Id: r.ID, Workspace: r.Workspace, Worker: r.Worker, Hash: r.Hash, Status: runStatus(r.Status), Manual: r.Manual,
		Started: timestamp(r.Started), Duration: durationpb.New(r.Duration),
		Usage: &pb.Usage{Calls: int32(r.Calls), CostUsd: r.CostUSD}, SessionId: r.SessionID,
	}
	for _, f := range r.Refusals {
		out.Refusals = append(out.Refusals, &pb.Refusal{Tool: f.Tool, Kind: actionKind(f.Kind), Detail: f.Detail, Time: timestamp(f.Time)})
	}
	if r.Error != "" {
		out.Error = &pb.ErrorInfo{Reason: "RUN_" + strings.ToUpper(string(r.Status)), Message: r.Error}
	}
	return out
}

func (h workerService) ListWorkers(ctx context.Context, r req[pb.ListWorkersRequest]) (*connect.Response[pb.ListWorkersResponse], error) {
	sc := h.s.sched
	if sc == nil {
		return nil, errNoScheduler
	}
	dirs := []string{r.Msg.Workspace}
	if r.Msg.Workspace == "" {
		dirs = sc.cfg.Store.Workspaces()
	}
	out := &pb.ListWorkersResponse{}
	for _, dir := range dirs {
		w, err := h.s.workspace(ctx, dir)
		if err != nil {
			return nil, err
		}
		list, err := w.ListWorkers()
		if err != nil {
			return nil, toAPI(err)
		}
		for _, info := range list {
			out.Workers = append(out.Workers, workerMsg(info))
		}
	}
	return ok(out)
}

func (h workerService) EnableWorker(ctx context.Context, r req[pb.EnableWorkerRequest]) (*connect.Response[pb.EnableWorkerResponse], error) {
	sc := h.s.sched
	if sc == nil {
		return nil, errNoScheduler
	}
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	info, err := w.EnableWorker(r.Msg.Name, r.Msg.Hash)
	if err != nil {
		return nil, toAPI(err)
	}
	sc.rescan(ctx)
	return ok(&pb.EnableWorkerResponse{Worker: workerMsg(info)})
}

func (h workerService) DisableWorker(ctx context.Context, r req[pb.DisableWorkerRequest]) (*connect.Response[pb.DisableWorkerResponse], error) {
	sc := h.s.sched
	if sc == nil {
		return nil, errNoScheduler
	}
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	info, err := w.DisableWorker(r.Msg.Name)
	if err != nil {
		return nil, toAPI(err)
	}
	sc.rescan(ctx)
	return ok(&pb.DisableWorkerResponse{Worker: workerMsg(info)})
}

func (h workerService) RunWorker(ctx context.Context, r req[pb.RunWorkerRequest]) (*connect.Response[pb.RunWorkerResponse], error) {
	sc := h.s.sched
	if sc == nil {
		return nil, errNoScheduler
	}
	key, err := canonical(r.Msg.Workspace)
	if err != nil {
		return nil, apiError(connect.CodeInvalidArgument, "INVALID_WORKSPACE", err, "workspace", r.Msg.Workspace)
	}
	run, err := sc.start(ctx, key, r.Msg.Name, true)
	if err != nil {
		return nil, toAPI(err)
	}
	return ok(&pb.RunWorkerResponse{Run: runMsg(run)})
}

func (h workerService) ListWorkerRuns(ctx context.Context, r req[pb.ListWorkerRunsRequest]) (*connect.Response[pb.ListWorkerRunsResponse], error) {
	sc := h.s.sched
	if sc == nil {
		return nil, errNoScheduler
	}
	w, err := h.s.workspace(ctx, r.Msg.Workspace)
	if err != nil {
		return nil, err
	}
	out := &pb.ListWorkerRunsResponse{}
	for _, run := range sc.liveRuns(w.Dir(), r.Msg.Name) {
		out.Runs = append(out.Runs, runMsg(run))
	}
	runs, err := w.WorkerRuns(r.Msg.Name, int(r.Msg.Limit))
	if err != nil {
		return nil, toAPI(err)
	}
	for _, run := range runs {
		out.Runs = append(out.Runs, runMsg(run))
	}
	return ok(out)
}

func (h workerService) GetWorkerRun(_ context.Context, r req[pb.GetWorkerRunRequest]) (*connect.Response[pb.GetWorkerRunResponse], error) {
	sc := h.s.sched
	if sc == nil {
		return nil, errNoScheduler
	}
	if lr := sc.liveRun(r.Msg.RunId); lr != nil {
		lr.mu.Lock()
		defer lr.mu.Unlock()
		return ok(&pb.GetWorkerRunResponse{Run: runMsg(lr.run)})
	}
	run, found, err := sc.cfg.Runs.Get(r.Msg.RunId)
	if err != nil {
		return nil, toAPI(err)
	}
	if !found {
		return nil, apiError(connect.CodeNotFound, "UNKNOWN_RUN", fmt.Errorf("no run %q", r.Msg.RunId), "run_id", r.Msg.RunId)
	}
	return ok(&pb.GetWorkerRunResponse{Run: runMsg(run)})
}

// WatchWorkerRun streams a running run's events from its start, then live
// ones, until it finishes. Events aren't kept once a run has finished.
func (h workerService) WatchWorkerRun(ctx context.Context, r req[pb.WatchWorkerRunRequest], stream *connect.ServerStream[pb.WatchWorkerRunResponse]) error {
	sc := h.s.sched
	if sc == nil {
		return errNoScheduler
	}
	lr := sc.liveRun(r.Msg.RunId)
	if lr == nil {
		return apiError(connect.CodeNotFound, "RUN_NOT_RUNNING", fmt.Errorf("run %q isn't running (GetWorkerRun has its record)", r.Msg.RunId), "run_id", r.Msg.RunId)
	}
	sent := 0
	for {
		lr.mu.Lock()
		pending, done, changed := lr.events[sent:], lr.done, lr.changed
		lr.mu.Unlock()
		for _, ev := range pending {
			if err := stream.Send(&pb.WatchWorkerRunResponse{Event: ev}); err != nil {
				return err
			}
		}
		sent += len(pending)
		if done {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
