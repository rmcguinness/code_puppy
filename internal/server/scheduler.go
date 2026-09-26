package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/retail-cortex/blitz/internal/app"
	pb "github.com/retail-cortex/blitz/internal/gen/blitz/v1"
	"github.com/retail-cortex/blitz/internal/workers"
	"github.com/robfig/cron/v3"
)

// SchedulerConfig configures the service's worker scheduler.
type SchedulerConfig struct {
	// Store says which workers are enabled; the Opener must give each
	// workspace the same one (app.Options.Workers).
	Store *workers.Store
	// Runs is where workspaces record finished runs (the same directory
	// app.Workspace uses), for looking runs up by ID.
	Runs *workers.RunLog
	// MaxConcurrent is how many runs may go at once (at least 1).
	MaxConcurrent int
	// Rescan is how often workers are rescanned, to pick up new, edited
	// and removed WORKER.md files (0: a minute).
	Rescan time.Duration
}

// Option configures a Server.
type Option func(*Server)

// WithScheduler makes the server run enabled workers on their schedules
// (StartScheduler) and serve WorkerService.
func WithScheduler(c SchedulerConfig) Option {
	return func(s *Server) {
		if c.MaxConcurrent < 1 {
			c.MaxConcurrent = 1
		}
		if c.Rescan <= 0 {
			c.Rescan = time.Minute
		}
		base, cancel := context.WithCancel(context.Background())
		s.sched = &scheduler{
			s: s, cfg: c, cron: cron.New(), slots: make(chan struct{}, c.MaxConcurrent),
			base: base, cancel: cancel,
			entries: map[string]entry{}, live: map[string]*liveRun{}, waiting: map[string]bool{},
		}
	}
}

// StartScheduler runs workers on their schedules until ctx is done, which
// also stops the runs going then (as Close does). It does nothing without
// WithScheduler.
func (s *Server) StartScheduler(ctx context.Context) {
	sc := s.sched
	if sc == nil {
		return
	}
	context.AfterFunc(ctx, sc.cancel)
	sc.cron.Start()
	if !sc.track() {
		return
	}
	go func() {
		defer sc.wg.Done()
		t := time.NewTicker(sc.cfg.Rescan)
		defer t.Stop()
		for {
			sc.rescan(sc.base)
			select {
			case <-sc.base.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// scheduler runs enabled workers on their schedules.
type scheduler struct {
	s     *Server
	cfg   SchedulerConfig
	cron  *cron.Cron
	slots chan struct{} // one per run allowed at once

	// base is cancelled when the scheduler stops, which stops every run;
	// wg counts the goroutines stop waits for.
	base   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	entries map[string]entry    // by workspace and worker
	live    map[string]*liveRun // runs going now, by ID
	waiting map[string]bool     // scheduled runs waiting for a slot, by workspace and worker
}

var errStopped = errors.New("the worker scheduler has stopped")

// track counts a goroutine about to start, unless the scheduler has
// stopped (checked under mu, so stop's Wait never races an Add).
func (sc *scheduler) track() bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.base.Err() != nil {
		return false
	}
	sc.wg.Add(1)
	return true
}

// stop cancels every run and waits for them, and for cron's jobs, to end.
func (sc *scheduler) stop() {
	sc.mu.Lock()
	sc.cancel()
	sc.mu.Unlock()
	<-sc.cron.Stop().Done()
	sc.wg.Wait()
}

// entry is a worker registered with cron, as registered.
type entry struct {
	id             cron.EntryID
	hash, cron, tz string
}

func key(dir, name string) string { return dir + "\x00" + name }

// cronSchedule adapts a worker's schedule, which has its own time zone.
type cronSchedule struct{ workers.Schedule }

func (c cronSchedule) Next(t time.Time) time.Time { return c.Schedule.Next(t) }

// rescan brings cron in line with the enabled workers of every workspace
// that has any.
func (sc *scheduler) rescan(ctx context.Context) {
	seen := map[string]bool{}
	for _, dir := range sc.cfg.Store.Workspaces() {
		w, err := sc.s.workspace(ctx, dir)
		if err != nil {
			slog.Warn("workers: can't open workspace", "workspace", dir, "error", err)
			continue
		}
		list, err := w.ListWorkers()
		if err != nil {
			continue
		}
		for _, info := range list {
			if info.State != workers.StateEnabled {
				continue
			}
			k := key(dir, info.Name)
			seen[k] = true
			sc.register(dir, info)
		}
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for k, e := range sc.entries {
		if !seen[k] {
			sc.cron.Remove(e.id)
			delete(sc.entries, k)
		}
	}
}

// register adds or updates a worker's cron entry, and catches up on a
// missed run when the worker asks for it.
func (sc *scheduler) register(dir string, info app.WorkerInfo) {
	k := key(dir, info.Name)
	sc.mu.Lock()
	old, had := sc.entries[k]
	if had && old.hash == info.Hash && old.cron == info.Cron && old.tz == info.Timezone {
		sc.mu.Unlock()
		return
	}
	if had {
		sc.cron.Remove(old.id)
	}
	sched, err := workers.ParseSchedule(info.Cron, info.Timezone)
	if err != nil {
		delete(sc.entries, k)
		sc.mu.Unlock()
		slog.Warn("workers: bad schedule", "workspace", dir, "worker", info.Name, "error", err)
		return
	}
	name := info.Name
	id := sc.cron.Schedule(cronSchedule{sched}, cron.FuncJob(func() {
		if _, err := sc.start(sc.base, dir, name, false); err != nil && !errors.Is(err, app.ErrRunInProgress) && !errors.Is(err, errStopped) {
			slog.Warn("workers: run failed to start", "workspace", dir, "worker", name, "error", err)
		}
	}))
	sc.entries[k] = entry{id: id, hash: info.Hash, cron: info.Cron, tz: info.Timezone}
	sc.mu.Unlock()
	slog.Info("workers: scheduled", "workspace", dir, "worker", name, "cron", info.Cron, "next", sched.Next(time.Now()))

	if !had && info.CatchUp == "once" {
		if last := sc.cfg.Runs.Last(dir, name); !last.IsZero() && sched.Next(last).Before(time.Now()) && sc.track() {
			go func() {
				defer sc.wg.Done()
				sc.start(sc.base, dir, name, false)
			}()
		}
	}
}

// liveRun is a run going now: its record and its events so far, for
// watchers.
type liveRun struct {
	mu      sync.Mutex
	run     workers.Run
	events  []*pb.TurnEvent
	done    bool
	changed chan struct{} // closed and replaced when events arrive or the run ends
}

func (lr *liveRun) add(ev *pb.TurnEvent, done bool) {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if ev != nil {
		lr.events = append(lr.events, ev)
	}
	lr.done = lr.done || done
	close(lr.changed)
	lr.changed = make(chan struct{})
}

// start runs a worker in the background once a slot is free, and returns
// its record as it starts. A manual run fails at once when every slot is
// busy; a scheduled one waits, unless the same worker is already waiting
// (a worker that falls behind runs once, not once per missed tick).
func (sc *scheduler) start(ctx context.Context, dir, name string, manual bool) (workers.Run, error) {
	w, err := sc.s.workspace(ctx, dir)
	if err != nil {
		return workers.Run{}, err
	}
	if err := sc.acquire(dir, name, manual); err != nil {
		return workers.Run{}, err
	}
	if !sc.track() {
		<-sc.slots
		return workers.Run{}, errStopped
	}

	started := make(chan workers.Run, 1)
	failed := make(chan error, 1)
	go func() {
		defer sc.wg.Done()
		defer func() { <-sc.slots }()
		var lr *liveRun
		run, err := w.RunWorker(sc.base, name, app.RunOptions{
			Manual: manual,
			OnStart: func(r workers.Run) {
				lr = &liveRun{run: r, changed: make(chan struct{})}
				sc.mu.Lock()
				sc.live[r.ID] = lr
				sc.mu.Unlock()
				started <- r
			},
			OnEvent: func(e app.Event) { lr.add(eventMsg(e), false) },
		})
		if lr == nil { // never started
			if err == nil {
				err = fmt.Errorf("worker %q did not start", name)
			}
			failed <- err
			return
		}
		finished := &pb.TurnFinished{}
		if run.Status != workers.RunSucceeded {
			finished.Error = &pb.ErrorInfo{Reason: "RUN_" + strings.ToUpper(string(run.Status)), Message: run.Error}
		}
		lr.mu.Lock()
		lr.run = run
		lr.mu.Unlock()
		lr.add(&pb.TurnEvent{Kind: &pb.TurnEvent_Finished{Finished: finished}}, true)
		slog.Info("workers: run finished", "workspace", dir, "worker", name, "status", run.Status, "cost_usd", run.CostUSD, "refusals", len(run.Refusals))
		sc.mu.Lock()
		delete(sc.live, run.ID)
		sc.mu.Unlock()
	}()
	select {
	case r := <-started:
		return r, nil
	case err := <-failed:
		return workers.Run{}, err
	}
}

// acquire takes a run slot. See start.
func (sc *scheduler) acquire(dir, name string, manual bool) error {
	if manual {
		select {
		case sc.slots <- struct{}{}:
			return nil
		default:
			return apiError(connect.CodeResourceExhausted, "TOO_MANY_RUNS", fmt.Errorf("%d worker runs are already going", cap(sc.slots)))
		}
	}
	k := key(dir, name)
	sc.mu.Lock()
	if sc.waiting[k] {
		sc.mu.Unlock()
		return app.ErrRunInProgress
	}
	sc.waiting[k] = true
	sc.mu.Unlock()
	defer func() {
		sc.mu.Lock()
		delete(sc.waiting, k)
		sc.mu.Unlock()
	}()
	select {
	case sc.slots <- struct{}{}:
		return nil
	case <-sc.base.Done():
		return errStopped
	}
}

// liveRuns returns the runs going now, for a workspace and worker ("" for
// any).
func (sc *scheduler) liveRuns(dir, name string) []workers.Run {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	var out []workers.Run
	for _, lr := range sc.live {
		lr.mu.Lock()
		r := lr.run
		lr.mu.Unlock()
		if (dir == "" || r.Workspace == dir) && (name == "" || r.Worker == name) {
			out = append(out, r)
		}
	}
	return out
}

func (sc *scheduler) liveRun(id string) *liveRun {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.live[id]
}
