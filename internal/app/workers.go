package app

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"github.com/retail-cortex/code_puppy/internal/config"
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
		Schedule: wk.Schedule.Text, Cron: wk.Schedule.Cron, Agent: wk.Agent, Model: wk.Model,
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
