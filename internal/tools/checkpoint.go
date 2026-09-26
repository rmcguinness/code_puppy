package tools

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// fileState is a file's content before or after a change.
type fileState struct {
	exists bool
	data   []byte
	mode   fs.FileMode
	// tooLarge marks a file that existed but exceeded the snapshot limit, so
	// it cannot be restored.
	tooLarge bool
}

type fileChange struct {
	abs, display string
	before       fileState
	afterExists  bool
	afterHash    [32]byte
}

// Turn is the set of file changes made while handling one user prompt.
type Turn struct {
	ID      int
	Label   string
	Started time.Time
	changes []*fileChange
	index   map[string]*fileChange
}

// TurnSummary describes a checkpoint for display.
type TurnSummary struct {
	ID    int
	Label string
	Time  time.Time
	Files []string
}

// UndoResult reports what an undo restored.
type UndoResult struct {
	Turn     TurnSummary
	Restored []string
}

// ErrUndoConflict is returned when files changed after the checkpointed edit.
var ErrUndoConflict = errors.New("files changed since the edit")

// Checkpoints snapshots files before tools modify them so changes can be
// undone turn by turn. Only changes made through the file tools are tracked;
// shell commands are not (their effects are detected as conflicts).
type Checkpoints struct {
	mu       sync.Mutex
	ws       *Workspace
	turns    []*Turn
	nextID   int
	bytes    int64
	maxBytes int64
	// originals keeps each file's earliest snapshot for the session-wide diff.
	originals map[string]*fileChange
}

// NewCheckpoints attaches a checkpoint store to ws.
func NewCheckpoints(ws *Workspace, maxBytes int64) *Checkpoints {
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	c := &Checkpoints{ws: ws, maxBytes: maxBytes, originals: map[string]*fileChange{}}
	ws.checkpoints = c
	return c
}

// Begin starts a new turn; later changes are grouped under it.
func (c *Checkpoints) Begin(label string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.beginLocked(label)
}

func (c *Checkpoints) beginLocked(label string) *Turn {
	// Reuse an empty current turn rather than stacking empty ones.
	if n := len(c.turns); n > 0 && len(c.turns[n-1].changes) == 0 {
		t := c.turns[n-1]
		t.Label, t.Started = label, time.Now()
		return t
	}
	c.nextID++
	t := &Turn{ID: c.nextID, Label: label, Started: time.Now(), index: map[string]*fileChange{}}
	c.turns = append(c.turns, t)
	return t
}

// before is called by the workspace before it modifies abs. It reports
// whether a new entry was added for this turn.
func (c *Checkpoints) before(abs, display string, st fileState) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var t *Turn
	if n := len(c.turns); n > 0 {
		t = c.turns[n-1]
	} else {
		t = c.beginLocked("(no prompt)")
	}
	if _, seen := t.index[abs]; seen {
		return false // keep the state from before the turn's first change
	}
	ch := &fileChange{abs: abs, display: display, before: st}
	t.changes = append(t.changes, ch)
	t.index[abs] = ch
	if _, ok := c.originals[abs]; !ok {
		c.originals[abs] = ch
	}
	c.bytes += int64(len(st.data))
	c.trimLocked()
	return true
}

// discard drops the current turn's entry for abs after a failed change.
func (c *Checkpoints) discard(abs string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.turns)
	if n == 0 {
		return
	}
	t := c.turns[n-1]
	ch := t.index[abs]
	if ch == nil {
		return
	}
	delete(t.index, abs)
	for i, x := range t.changes {
		if x == ch {
			t.changes = append(t.changes[:i], t.changes[i+1:]...)
			break
		}
	}
	if c.originals[abs] == ch {
		delete(c.originals, abs)
	}
	c.bytes -= int64(len(ch.before.data))
}

// after records the state the workspace left abs in.
func (c *Checkpoints) after(abs string, data []byte, exists bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := len(c.turns); n > 0 {
		if ch := c.turns[n-1].index[abs]; ch != nil {
			ch.afterExists = exists
			ch.afterHash = sha256.Sum256(data)
		}
	}
}

// trimLocked drops the oldest turns while over the memory budget, always
// keeping the most recent one.
func (c *Checkpoints) trimLocked() {
	for c.bytes > c.maxBytes && len(c.turns) > 1 {
		old := c.turns[0]
		for _, ch := range old.changes {
			c.bytes -= int64(len(ch.before.data))
			if c.originals[ch.abs] == ch {
				delete(c.originals, ch.abs)
			}
		}
		c.turns = c.turns[1:]
	}
}

// List returns turns with changes, newest first.
func (c *Checkpoints) List() []TurnSummary {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []TurnSummary
	for i := len(c.turns) - 1; i >= 0; i-- {
		if len(c.turns[i].changes) > 0 {
			out = append(out, summarize(c.turns[i]))
		}
	}
	return out
}

func summarize(t *Turn) TurnSummary {
	s := TurnSummary{ID: t.ID, Label: t.Label, Time: t.Started}
	for _, ch := range t.changes {
		s.Files = append(s.Files, ch.display)
	}
	return s
}

// Undo reverts the most recent turn that changed files. Unless force is set,
// it refuses when a file no longer matches what the tools wrote, so edits
// made afterwards (by the shell or the user) aren't silently discarded.
func (c *Checkpoints) Undo(force bool) (UndoResult, error) {
	if c == nil {
		return UndoResult{}, errors.New("checkpoints are disabled")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	idx := -1
	for i := len(c.turns) - 1; i >= 0; i-- {
		if len(c.turns[i].changes) > 0 {
			idx = i
			break
		}
	}
	if idx < 0 {
		return UndoResult{}, errors.New("nothing to undo")
	}
	t := c.turns[idx]

	var conflicts, unrestorable []string
	for _, ch := range t.changes {
		if ch.before.tooLarge {
			unrestorable = append(unrestorable, ch.display)
			continue
		}
		data, err := os.ReadFile(ch.abs)
		exists := err == nil
		if exists != ch.afterExists || (exists && sha256.Sum256(data) != ch.afterHash) {
			conflicts = append(conflicts, ch.display)
		}
	}
	if len(unrestorable) > 0 && !force {
		return UndoResult{}, fmt.Errorf("cannot restore files larger than the snapshot limit: %s (use --force to restore the rest)", strings.Join(unrestorable, ", "))
	}
	if len(conflicts) > 0 && !force {
		return UndoResult{}, fmt.Errorf("%w: %s (use /undo --force to overwrite)", ErrUndoConflict, strings.Join(conflicts, ", "))
	}

	res := UndoResult{Turn: summarize(t)}
	var errs []error
	for i := len(t.changes) - 1; i >= 0; i-- {
		ch := t.changes[i]
		if ch.before.tooLarge {
			continue
		}
		if err := c.ws.restore(ch.abs, ch.before); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ch.display, err))
			continue
		}
		res.Restored = append(res.Restored, ch.display)
		c.bytes -= int64(len(ch.before.data))
		if c.originals[ch.abs] == ch {
			delete(c.originals, ch.abs)
		}
	}
	c.turns = append(c.turns[:idx], c.turns[idx+1:]...)
	sort.Strings(res.Restored)
	return res, errors.Join(errs...)
}

// SessionDiff returns a unified diff of every tracked file from its earliest
// snapshot to its current content.
func (c *Checkpoints) SessionDiff() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	chs := make([]*fileChange, 0, len(c.originals))
	for _, ch := range c.originals {
		chs = append(chs, ch)
	}
	c.mu.Unlock()
	sort.Slice(chs, func(i, j int) bool { return chs[i].display < chs[j].display })

	var sb strings.Builder
	for _, ch := range chs {
		if ch.before.tooLarge {
			fmt.Fprintf(&sb, "# %s: too large to diff\n", ch.display)
			continue
		}
		cur, _ := os.ReadFile(ch.abs)
		sb.WriteString(unifiedDiff(ch.display, string(ch.before.data), string(cur)))
	}
	return sb.String()
}
