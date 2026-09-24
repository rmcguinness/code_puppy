package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const (
	defaultMaxBackground     = 8
	defaultBackgroundLife    = time.Hour
	backgroundOutputLimit    = 256 * 1024
	maxFinishedProcsRetained = 16
)

// BackgroundProcess is a command running (or finished) in the background.
type BackgroundProcess struct {
	ID        int
	Command   string
	StartTime time.Time

	cmd    *guardedCmd
	out    *cappedBuffer
	cancel context.CancelFunc
	done   chan struct{}

	// Guarded by ProcessManager.mu.
	finished bool
	exitCode int
	endTime  time.Time
}

// ProcessInfo is a snapshot of a background process's status.
type ProcessInfo struct {
	ID        int    `json:"id"`
	Command   string `json:"command"`
	Running   bool   `json:"running"`
	ExitCode  int    `json:"exit_code"`
	RuntimeMs int64  `json:"runtime_ms"`
}

// ProcessManager owns background processes: it caps how many run at once,
// bounds their lifetime and captured output, and kills them on shutdown.
// Processes are guarded (see ExecEnv) so none survive Code Puppy exiting.
type ProcessManager struct {
	mu          sync.Mutex
	next        int
	procs       map[int]*BackgroundProcess
	maxRunning  int
	maxLifetime time.Duration
	closed      bool
	exec        *ExecEnv
}

// NewProcessManager creates a manager; zero values select defaults.
func NewProcessManager(maxRunning int, maxLifetime time.Duration) *ProcessManager {
	if maxRunning <= 0 {
		maxRunning = defaultMaxBackground
	}
	if maxLifetime <= 0 {
		maxLifetime = defaultBackgroundLife
	}
	return &ProcessManager{procs: make(map[int]*BackgroundProcess), maxRunning: maxRunning, maxLifetime: maxLifetime}
}

// Start launches command in cwd as a background process.
func (m *ProcessManager) Start(command, cwd string) (*BackgroundProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil, errors.New("process manager is shut down")
	}
	running := 0
	for _, p := range m.procs {
		if !p.finished {
			running++
		}
	}
	if running >= m.maxRunning {
		return nil, fmt.Errorf("too many background processes running (limit %d); kill one first", m.maxRunning)
	}
	m.pruneLocked()

	ctx, cancel := context.WithTimeout(context.Background(), m.maxLifetime)
	cmd, err := m.exec.command(ctx, []string{"bash", "-c", command})
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.Dir = cwd
	out := newCappedBuffer(backgroundOutputLimit)
	cmd.Stdout = out
	cmd.Stderr = out

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	m.next++
	bp := &BackgroundProcess{
		ID:        m.next,
		Command:   command,
		StartTime: time.Now(),
		cmd:       cmd,
		out:       out,
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	m.procs[bp.ID] = bp

	go func() {
		_ = cmd.Wait()
		cancel()
		m.mu.Lock()
		bp.finished = true
		bp.exitCode = cmd.ProcessState.ExitCode()
		bp.endTime = time.Now()
		m.mu.Unlock()
		close(bp.done)
	}()
	return bp, nil
}

// pruneLocked drops the oldest finished processes beyond the retention limit.
func (m *ProcessManager) pruneLocked() {
	var finished []*BackgroundProcess
	for _, p := range m.procs {
		if p.finished {
			finished = append(finished, p)
		}
	}
	if len(finished) <= maxFinishedProcsRetained {
		return
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].ID < finished[j].ID })
	for _, p := range finished[:len(finished)-maxFinishedProcsRetained] {
		delete(m.procs, p.ID)
	}
}

func (m *ProcessManager) get(id int) (*BackgroundProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bp, ok := m.procs[id]
	if !ok {
		return nil, fmt.Errorf("no background process with ID %d", id)
	}
	return bp, nil
}

func (m *ProcessManager) info(bp *BackgroundProcess) ProcessInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	end := time.Now()
	if bp.finished {
		end = bp.endTime
	}
	return ProcessInfo{
		ID:        bp.ID,
		Command:   bp.Command,
		Running:   !bp.finished,
		ExitCode:  bp.exitCode,
		RuntimeMs: end.Sub(bp.StartTime).Milliseconds(),
	}
}

// Output returns captured output and status for a process.
func (m *ProcessManager) Output(id int) (string, ProcessInfo, error) {
	bp, err := m.get(id)
	if err != nil {
		return "", ProcessInfo{}, err
	}
	return bp.out.String(), m.info(bp), nil
}

// Kill terminates a process and its descendants, waiting briefly for exit.
func (m *ProcessManager) Kill(id int) (ProcessInfo, error) {
	bp, err := m.get(id)
	if err != nil {
		return ProcessInfo{}, err
	}
	bp.cancel()
	select {
	case <-bp.done:
	case <-time.After(shellWaitDelay + time.Second):
		return m.info(bp), fmt.Errorf("process %d did not exit after kill", id)
	}
	return m.info(bp), nil
}

// List returns all tracked processes ordered by ID.
func (m *ProcessManager) List() []ProcessInfo {
	infos := make([]ProcessInfo, 0)
	for _, p := range m.snapshot() {
		infos = append(infos, m.info(p))
	}
	return infos
}

// Running returns the processes that have not exited, ordered by ID.
func (m *ProcessManager) Running() []ProcessInfo {
	var running []ProcessInfo
	for _, info := range m.List() {
		if info.Running {
			running = append(running, info)
		}
	}
	return running
}

// WaitAll blocks until every running process exits or ctx is done.
func (m *ProcessManager) WaitAll(ctx context.Context) error {
	for _, p := range m.snapshot() {
		select {
		case <-p.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (m *ProcessManager) snapshot() []*BackgroundProcess {
	m.mu.Lock()
	procs := make([]*BackgroundProcess, 0, len(m.procs))
	for _, p := range m.procs {
		procs = append(procs, p)
	}
	m.mu.Unlock()
	sort.Slice(procs, func(i, j int) bool { return procs[i].ID < procs[j].ID })
	return procs
}

// Shutdown kills every running process and refuses new ones.
func (m *ProcessManager) Shutdown() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()

	procs := m.snapshot()
	for _, p := range procs {
		p.cancel()
	}
	deadline := time.After(shellWaitDelay + time.Second)
	for _, p := range procs {
		select {
		case <-p.done:
		case <-deadline:
			return
		}
	}
}

// ManageBackgroundInput defines arguments for manage_background_process.
type ManageBackgroundInput struct {
	Action    string `json:"action" jsonschema:"One of: list, output, kill"`
	ProcessID int    `json:"process_id,omitempty" jsonschema:"Background process ID (for output and kill)"`
}

// ManageBackgroundOutput holds the result of manage_background_process.
type ManageBackgroundOutput struct {
	Processes []ProcessInfo `json:"processes,omitempty"`
	Process   *ProcessInfo  `json:"process,omitempty"`
	Output    string        `json:"output,omitempty"`
	Error     string        `json:"error,omitempty"`
}

// NewManageBackgroundTool creates the tool for inspecting and stopping background processes.
func NewManageBackgroundTool(pm *ProcessManager) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "manage_background_process",
			Description: "List background processes started by run_shell_command, read their output, or kill them",
		},
		func(ctx agent.Context, input ManageBackgroundInput) (ManageBackgroundOutput, error) {
			switch input.Action {
			case "list":
				return ManageBackgroundOutput{Processes: pm.List()}, nil
			case "output":
				out, info, err := pm.Output(input.ProcessID)
				if err != nil {
					return ManageBackgroundOutput{Error: err.Error()}, nil
				}
				return ManageBackgroundOutput{Process: &info, Output: out}, nil
			case "kill":
				info, err := pm.Kill(input.ProcessID)
				if err != nil {
					return ManageBackgroundOutput{Process: &info, Error: err.Error()}, nil
				}
				return ManageBackgroundOutput{Process: &info}, nil
			default:
				return ManageBackgroundOutput{Error: "unknown action; use 'list', 'output', or 'kill'"}, nil
			}
		},
	)
}
