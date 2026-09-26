package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"github.com/retail-cortex/code_puppy/internal/session"
	"github.com/retail-cortex/code_puppy/internal/tools"
	"github.com/retail-cortex/code_puppy/internal/workers"
)

// Workers: scheduled workflows the workspace defines in
// workers/<name>/WORKER.md (ROADMAP item 24). Enabling one after reviewing
// it pins its content hash; scanning alone never schedules anything.

// WorkerInfo describes a worker and whether it may run.
type WorkerInfo struct {
	Workspace   string
	Name        string
	Description string
	Path        string // WORKER.md
	Hash        string
	State       workers.State
	// Schedule as written, as understood, and when it next runs (zero
	// unless it's enabled and valid).
	Schedule string
	Cron     string
	Timezone string
	Next     time.Time
	Agent    string
	Model    string
	// CatchUp is "once" when a run missed while nothing was running should
	// happen as soon as possible, otherwise "none".
	CatchUp string
	// Permissions and Limits are what the worker gets after the host
	// policy (see workers.Apply).
	Permissions []string
	Limits      workers.Limits
	// Problems say why the worker is invalid, or what the policy changed.
	Problems []string
}

// ErrUnknownWorker reports a worker name the workspace doesn't define.
var ErrUnknownWorker = errors.New("no such worker")

// ErrWorkersDisabled reports that workers are turned off ([workers] enabled).
var ErrWorkersDisabled = errors.New("workers are disabled")

// defaultWorkerStore is where enabled workers are recorded.
func defaultWorkerStore() (*workers.Store, error) {
	return workers.OpenStore(config.ExpandHome("~/.code_puppy/workers.json"))
}

// workerRoots are the directories holding the workspace's workers.
func (w *Workspace) workerRoots() []string {
	var out []string
	for _, p := range w.cfg.Workers.Paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(w.Dir(), p)
		}
		out = append(out, p)
	}
	return out
}

// discoverWorkers loads the workspace's workers, keyed by name (a later
// root doesn't override an earlier one).
func (w *Workspace) discoverWorkers() map[string]workers.Found {
	out := map[string]workers.Found{}
	for _, root := range w.workerRoots() {
		list, err := workers.Discover(root)
		if err != nil {
			w.warn(err.Error())
		}
		for _, f := range list {
			if _, dup := out[f.Worker.Name]; !dup {
				out[f.Worker.Name] = f
			}
		}
	}
	return out
}

func (w *Workspace) workerInfo(wk *workers.Worker, loadErr error, now time.Time) WorkerInfo {
	info := WorkerInfo{
		Workspace: w.Dir(), Name: wk.Name, Description: wk.Description, Path: wk.Path, Hash: wk.Hash,
		State:    w.workerStore.State(w.Dir(), wk, loadErr),
		Schedule: wk.Schedule.Text, Cron: wk.Schedule.Cron, Agent: wk.Agent, Model: wk.Model, CatchUp: wk.CatchUp,
	}
	if wk.Schedule.Location != nil {
		info.Timezone = wk.Schedule.Location.String()
	}
	var invalid *workers.InvalidError
	if errors.As(loadErr, &invalid) {
		info.Problems = invalid.Problems
	} else if loadErr != nil {
		info.Problems = []string{loadErr.Error()}
	}
	if loadErr == nil {
		eff := workers.Apply(wk, w.cfg.Workers.Policy)
		for _, p := range eff.Permissions {
			info.Permissions = append(info.Permissions, p.String())
		}
		info.Limits = eff.Limits
		info.Problems = append(info.Problems, eff.Notes...)
		if wk.Agent != "" || wk.Model != "" {
			info.Problems = append(info.Problems, "agent and model in WORKER.md aren't applied yet: runs use the workspace's active agent and model")
		}
		if info.State == workers.StateEnabled {
			info.Next = wk.Schedule.Next(now)
		}
	}
	return info
}

// ListWorkers returns the workspace's workers, by name.
func (w *Workspace) ListWorkers() ([]WorkerInfo, error) {
	if !w.cfg.Workers.Enabled {
		return nil, ErrWorkersDisabled
	}
	found := w.discoverWorkers()
	now := time.Now()
	var out []WorkerInfo
	for _, name := range slices.Sorted(maps.Keys(found)) {
		out = append(out, w.workerInfo(found[name].Worker, found[name].Err, now))
	}
	return out, nil
}

// worker loads one worker by name.
func (w *Workspace) worker(name string) (*workers.Worker, error, error) {
	if !w.cfg.Workers.Enabled {
		return nil, nil, ErrWorkersDisabled
	}
	for _, root := range w.workerRoots() {
		dir := filepath.Join(root, name)
		wk, loadErr := workers.Load(dir)
		if wk != nil {
			return wk, loadErr, nil
		}
	}
	return nil, nil, fmt.Errorf("%w: %q", ErrUnknownWorker, name)
}

// EnableWorker lets a worker run on its schedule, pinned to hash: the one
// the caller reviewed, which must still be current (workers.ErrHashMismatch
// otherwise). An invalid worker can't be enabled.
func (w *Workspace) EnableWorker(name, hash string) (WorkerInfo, error) {
	wk, loadErr, err := w.worker(name)
	if err != nil {
		return WorkerInfo{}, err
	}
	if loadErr != nil {
		return w.workerInfo(wk, loadErr, time.Now()), loadErr
	}
	if err := w.workerStore.Enable(w.Dir(), wk, hash); err != nil {
		return w.workerInfo(wk, nil, time.Now()), err
	}
	return w.workerInfo(wk, nil, time.Now()), nil
}

// DisableWorker stops a worker from running.
func (w *Workspace) DisableWorker(name string) (WorkerInfo, error) {
	wk, loadErr, err := w.worker(name)
	if err != nil {
		return WorkerInfo{}, err
	}
	if err := w.workerStore.Disable(w.Dir(), name); err != nil {
		return WorkerInfo{}, err
	}
	return w.workerInfo(wk, loadErr, time.Now()), nil
}

// ErrWorkerNotEnabled reports running a worker that isn't enabled at its
// current hash.
var ErrWorkerNotEnabled = errors.New("the worker isn't enabled")

// ErrRunInProgress reports a run of a worker that is already running.
var ErrRunInProgress = errors.New("the worker is already running")

// errOverBudget stops a run that spent its max_cost_usd.
var errOverBudget = errors.New("the run reached its cost limit")

// unattendedPreamble tells the agent how a worker run differs from a
// conversation.
const unattendedPreamble = "You are running unattended as the scheduled worker %q: nobody is watching or can answer questions. " +
	"You may only do what the worker is permitted; anything else is refused, and you should carry on without it or stop. " +
	"End with a short summary of what you did and anything you couldn't do.\n\n"

// RunWorker runs the named worker once, now, and records the run. The
// worker must be enabled at its current hash. The run has a session of its
// own (not the workspace's active one), gets exactly the worker's
// permissions after the host policy (anything else is refused and
// recorded), and stops at its limits. on receives the turn's events.
// A run of the same worker still going makes this ErrRunInProgress.
// RunOptions configure RunWorker.
type RunOptions struct {
	// Manual: started on request rather than by the schedule.
	Manual bool
	// OnStart receives the run's record as it starts (its ID and session).
	OnStart func(workers.Run)
	// OnEvent receives the turn's events.
	OnEvent func(Event)
}

func (w *Workspace) RunWorker(ctx context.Context, name string, o RunOptions) (workers.Run, error) {
	wk, loadErr, err := w.worker(name)
	if err != nil {
		return workers.Run{}, err
	}
	if state := w.workerStore.State(w.Dir(), wk, loadErr); state != workers.StateEnabled {
		return workers.Run{}, fmt.Errorf("%w (it is %s)", ErrWorkerNotEnabled, state)
	}
	w.runsMu.Lock()
	if w.running[name] {
		w.runsMu.Unlock()
		skipped := workers.Run{ID: newRunID(), Workspace: w.Dir(), Worker: name, Hash: wk.Hash, Status: workers.RunSkipped,
			Manual: o.Manual, Started: time.Now(), Error: ErrRunInProgress.Error()}
		if !o.Manual { // a scheduled run that couldn't happen is worth a record
			w.runLog.Append(skipped)
		}
		return skipped, ErrRunInProgress
	}
	w.running[name] = true
	w.runsMu.Unlock()
	defer func() {
		w.runsMu.Lock()
		delete(w.running, name)
		w.runsMu.Unlock()
	}()

	eff := workers.Apply(wk, w.cfg.Workers.Policy)
	run := workers.Run{ID: newRunID(), Workspace: w.Dir(), Worker: name, Hash: wk.Hash, Status: workers.RunRunning, Manual: o.Manual, Started: time.Now()}

	st, err := session.NewStorage(w.cfg.Session.StorageDir)
	if err != nil {
		return run, err
	}
	st.SetWorkspace(w.Dir())
	rec, err := st.CreateSession(session.NewSessionID(), fmt.Sprintf("⏰ %s %s", name, run.Started.Format("2006-01-02 15:04")), w.engine.ActiveAgent())
	if err != nil {
		return run, err
	}
	run.SessionID = rec.ID
	if o.OnStart != nil {
		o.OnStart(run)
	}

	var refusalsMu sync.Mutex
	decide := func(_ context.Context, req tools.ApprovalRequest) (tools.Decision, error) {
		if workers.Allows(eff.Permissions, req) {
			return tools.DecisionOnce, nil
		}
		refusalsMu.Lock()
		run.Refusals = append(run.Refusals, workers.Refusal{Tool: req.Tool, Kind: req.Kind, Detail: req.Detail, Time: time.Now()})
		refusalsMu.Unlock()
		return tools.DecisionDeny, nil
	}
	runCtx, cancel := context.WithCancelCause(tools.Unattended(ctx, decide))
	defer cancel(nil)
	if eff.Limits.Timeout > 0 {
		var stop context.CancelFunc
		runCtx, stop = context.WithTimeoutCause(runCtx, eff.Limits.Timeout, fmt.Errorf("the run reached its time limit (%s)", eff.Limits.Timeout))
		defer stop()
	}
	on := o.OnEvent
	if on == nil {
		on = func(Event) {}
	}
	_, runErr := w.run(runCtx, rec.ID, Turn{
		Text: wk.Prompt, Prompt: fmt.Sprintf(unattendedPreamble, name) + wk.Prompt, MaxTurns: eff.Limits.MaxTurns,
	}, func(e Event) {
		on(e)
		if eff.Limits.MaxCostUSD > 0 && w.engine.Usage(rec.ID).CostUSD > eff.Limits.MaxCostUSD {
			cancel(errOverBudget)
		}
	}, st)

	u := w.engine.Usage(rec.ID)
	run.Duration, run.CostUSD, run.Calls = time.Since(run.Started), u.CostUSD, u.Calls
	cause := context.Cause(runCtx)
	switch {
	case runErr == nil:
		run.Status = workers.RunSucceeded
	case errors.Is(runErr, runtime.ErrMaxTurns) || errors.Is(cause, errOverBudget) || errors.Is(runCtx.Err(), context.DeadlineExceeded):
		run.Status = workers.RunLimited
		run.Error = runErr.Error()
		if cause != nil && !errors.Is(cause, context.Canceled) {
			run.Error = cause.Error()
		}
	default:
		run.Status = workers.RunFailed
		run.Error = runErr.Error()
	}
	if err := w.runLog.Append(run); err != nil {
		w.warn("recording the worker run: " + err.Error())
	}
	return run, nil
}

// WorkerRuns returns a worker's recorded runs, newest first.
func (w *Workspace) WorkerRuns(name string, limit int) ([]workers.Run, error) {
	return w.runLog.List(w.Dir(), name, limit)
}

func newRunID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
}
