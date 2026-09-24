package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ergochat/readline"
)

// TerminalInput is an Input backed by a line editor: arrow-key editing,
// persistent history, reverse search (Ctrl+R) and tab completion. Every
// terminal read goes through it so prompts never compete for stdin.
type TerminalInput struct {
	rl   *readline.Instance
	turn chan struct{}

	mu          sync.Mutex
	onInterrupt func()
}

// SetInterruptHandler sets what Ctrl+C does while a prompt is shown during a
// turn (e.g. an approval). The terminal is in raw mode then, so Ctrl+C is a
// keypress rather than SIGINT; the REPL uses this to cancel the turn.
func (t *TerminalInput) SetInterruptHandler(f func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.onInterrupt = f
}

// TerminalOptions configure a TerminalInput.
type TerminalOptions struct {
	HistoryFile string
	HistorySize int
	Completer   readline.AutoCompleter
}

// NewTerminalInput creates the line editor on stdin/stdout.
func NewTerminalInput(o TerminalOptions) (*TerminalInput, error) {
	if o.HistoryFile != "" {
		if err := os.MkdirAll(filepath.Dir(o.HistoryFile), 0o700); err != nil {
			return nil, err
		}
		// Create owner-only before the editor opens it: prompts can contain secrets.
		if f, err := os.OpenFile(o.HistoryFile, os.O_CREATE|os.O_RDONLY, 0o600); err == nil {
			f.Close()
		}
	}
	rl, err := readline.NewFromConfig(&readline.Config{
		HistoryFile:            o.HistoryFile,
		HistoryLimit:           o.HistorySize,
		DisableAutoSaveHistory: true, // multi-line entries are saved whole
		HistorySearchFold:      true,
		AutoComplete:           o.Completer,
		InterruptPrompt:        "^C",
		EOFPrompt:              "",
	})
	if err != nil {
		return nil, err
	}
	return &TerminalInput{rl: rl, turn: make(chan struct{}, 1)}, nil
}

// Close restores the terminal.
func (t *TerminalInput) Close() error { return t.rl.Close() }

// Ask prints text (which may span lines) and reads one answer without
// recording it in history.
func (t *TerminalInput) Ask(ctx context.Context, text string) (string, error) {
	return t.read(ctx, text, false)
}

// ReadInput reads a REPL entry (multi-line aware) and saves it to history.
func (t *TerminalInput) ReadInput(ctx context.Context, prompt string) (string, error) {
	entry, err := readMultiline(ctx, prompt, func(ctx context.Context, p string) (string, error) {
		return t.read(ctx, p, true)
	})
	if err == nil && strings.TrimSpace(entry) != "" {
		_ = t.rl.SaveToHistory(entry)
	}
	return entry, err
}

func (t *TerminalInput) read(ctx context.Context, text string, history bool) (string, error) {
	select {
	case t.turn <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-t.turn }()

	// The editor redraws only the last prompt line; print the rest first.
	prompt := text
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		io.WriteString(t.rl.Stdout(), text[:i+1])
		prompt = text[i+1:]
	}
	if !history {
		t.rl.DisableHistory()
		defer t.rl.EnableHistory()
	}
	t.rl.SetPrompt(prompt)

	// The editor can't abandon a read; closing it is the only way to unblock,
	// which is fine because cancellation here means the process is exiting.
	stop := context.AfterFunc(ctx, func() { t.rl.Close() })
	defer stop()

	line, err := t.rl.ReadLine()
	switch {
	case errors.Is(err, readline.ErrInterrupt):
		t.mu.Lock()
		f := t.onInterrupt
		t.mu.Unlock()
		if f != nil {
			f()
		}
		return "", context.Canceled // Ctrl+C: same meaning as SIGINT at the prompt
	case err != nil && ctx.Err() != nil:
		return "", ctx.Err()
	}
	return line, err
}

// Completer completes slash commands, their arguments, and @path references
// to files in the workspace.
type Completer struct {
	mu        sync.RWMutex
	commands  map[string][]string        // command -> static argument choices
	dynamic   map[string]func() []string // command -> argument choices computed on demand
	workspace string
}

// NewCompleter creates a completer rooted at workspace.
func NewCompleter(workspace string) *Completer {
	return &Completer{commands: map[string][]string{}, dynamic: map[string]func() []string{}, workspace: workspace}
}

// Command registers a slash command (without "/") and static argument choices.
func (c *Completer) Command(name string, args ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.commands[name] = args
}

// Dynamic registers a function supplying argument choices for a command.
func (c *Completer) Dynamic(name string, f func() []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.commands[name]; !ok {
		c.commands[name] = nil
	}
	c.dynamic[name] = f
}

// Do implements readline.AutoCompleter: it returns candidate suffixes for the
// word before the cursor and the length of that word.
func (c *Completer) Do(line []rune, pos int) ([][]rune, int) {
	before := string(line[:pos])
	words := strings.Fields(before)
	endsWithSpace := strings.HasSuffix(before, " ")
	current := ""
	if !endsWithSpace && len(words) > 0 {
		current = words[len(words)-1]
	}

	var candidates []string
	switch {
	case strings.HasPrefix(before, "/") && len(words) <= 1 && !endsWithSpace:
		c.mu.RLock()
		for name := range c.commands {
			candidates = append(candidates, "/"+name+" ")
		}
		c.mu.RUnlock()
	case strings.HasPrefix(current, "@"):
		candidates = c.paths(current[1:])
		for i := range candidates {
			candidates[i] = "@" + candidates[i]
		}
	case strings.HasPrefix(before, "/") && len(words) >= 1 && (len(words) == 1 && endsWithSpace || len(words) == 2 && !endsWithSpace):
		cmd := strings.TrimPrefix(words[0], "/")
		c.mu.RLock()
		candidates = append(candidates, c.commands[cmd]...)
		if f := c.dynamic[cmd]; f != nil {
			c.mu.RUnlock()
			candidates = append(candidates, f()...)
		} else {
			c.mu.RUnlock()
		}
	}
	return suffixes(candidates, current), len([]rune(current))
}

func suffixes(candidates []string, prefix string) [][]rune {
	sort.Strings(candidates)
	var out [][]rune
	seen := map[string]bool{}
	for _, cand := range candidates {
		if strings.HasPrefix(cand, prefix) && cand != prefix && !seen[cand] {
			seen[cand] = true
			out = append(out, []rune(cand[len(prefix):]))
		}
	}
	return out
}

// paths lists workspace entries matching partial ("src/ma" -> "src/main.go").
// Hidden entries are offered only when the partial name starts with ".".
func (c *Completer) paths(partial string) []string {
	if strings.Contains(partial, "..") || filepath.IsAbs(partial) {
		return nil
	}
	dir, base := filepath.Split(partial)
	entries, err := os.ReadDir(filepath.Join(c.workspace, dir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		if !strings.HasPrefix(name, base) {
			continue
		}
		p := dir + name
		if e.IsDir() {
			p += "/"
		}
		out = append(out, p)
		if len(out) >= 200 {
			break
		}
	}
	return out
}
