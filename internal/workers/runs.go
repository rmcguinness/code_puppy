package workers

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/retail-cortex/code_puppy/internal/tools"
)

// RunStatus is where a run stands.
type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	// RunLimited: stopped at a limit (turns, cost or time).
	RunLimited RunStatus = "limited"
	// RunSkipped: not started, because the previous run was still going.
	RunSkipped RunStatus = "skipped"
)

// Refusal is an action a run wasn't permitted.
type Refusal struct {
	Tool   string           `json:"tool"`
	Kind   tools.ActionKind `json:"kind"`
	Detail string           `json:"detail"`
	Time   time.Time        `json:"time"`
}

// Run is one run of a worker.
type Run struct {
	ID        string    `json:"id"`
	Workspace string    `json:"workspace"`
	Worker    string    `json:"worker"`
	Hash      string    `json:"hash"`
	Status    RunStatus `json:"status"`
	// Manual: started on request, not by the schedule.
	Manual    bool          `json:"manual,omitempty"`
	Started   time.Time     `json:"started"`
	Duration  time.Duration `json:"duration"`
	CostUSD   float64       `json:"cost_usd"`
	Calls     int           `json:"calls"`
	SessionID string        `json:"session_id,omitempty"`
	Refusals  []Refusal     `json:"refusals,omitempty"`
	// Error is why the run failed or stopped.
	Error string `json:"error,omitempty"`
}

// RunLog keeps finished runs, one JSON line each, in a file per worker.
type RunLog struct {
	dir string
	mu  sync.Mutex
}

// OpenRunLog keeps runs under dir.
func OpenRunLog(dir string) *RunLog { return &RunLog{dir: dir} }

func (l *RunLog) file(workspace, worker string) string {
	sum := sha256.Sum256([]byte(workspace))
	return filepath.Join(l.dir, hex.EncodeToString(sum[:8])+"-"+worker+".jsonl")
}

// Append records a finished run.
func (l *RunLog) Append(r Run) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.file(r.Workspace, r.Worker), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

// List returns a worker's runs, newest first, at most limit (0: all).
func (l *RunLog) List(workspace, worker string, limit int) ([]Run, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	runs, err := readRuns(l.file(workspace, worker))
	slices.Reverse(runs)
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, err
}

// Get finds a run by ID.
func (l *RunLog) Get(id string) (Run, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entries, err := os.ReadDir(l.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		runs, err := readRuns(filepath.Join(l.dir, e.Name()))
		if err != nil {
			return Run{}, false, err
		}
		for _, r := range runs {
			if r.ID == id {
				return r, true, nil
			}
		}
	}
	return Run{}, false, nil
}

// Last is the start of a worker's latest run (zero if none).
func (l *RunLog) Last(workspace, worker string) time.Time {
	runs, _ := l.List(workspace, worker, 1)
	if len(runs) == 0 {
		return time.Time{}
	}
	return runs[0].Started
}

func readRuns(path string) ([]Run, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Run
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var r Run
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			out = append(out, r) // a torn last line is skipped
		}
	}
	return out, sc.Err()
}
