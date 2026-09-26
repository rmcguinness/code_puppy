package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/retail-cortex/code_puppy/internal/i18n"
	"github.com/retail-cortex/code_puppy/internal/textutil"
	"github.com/retail-cortex/code_puppy/internal/tools"
)

type lineResult struct {
	line string
	err  error
}

// LineReader is the single buffered reader over stdin. The REPL, approval
// prompts and ask_user_question must all share it: separate bufio readers on
// the same fd each buffer ahead and steal each other's input.
//
// Reads are cancellable. A read abandoned by cancellation stays in flight and
// its line is delivered to the next caller, so no input is lost.
type LineReader struct {
	turn chan struct{} // held for a whole prompt+read so prompts never interleave
	r    *bufio.Reader
	out  io.Writer

	mu      sync.Mutex
	pending chan lineResult // in-flight read, if any
}

// NewLineReader wraps in, echoing prompts to out.
func NewLineReader(in io.Reader, out io.Writer) *LineReader {
	return &LineReader{turn: make(chan struct{}, 1), r: bufio.NewReader(in), out: out}
}

// ReadLine prints prompt and returns the next line without its newline.
// io.EOF is returned only when no data remains.
func (l *LineReader) ReadLine(prompt string) (string, error) {
	return l.Ask(context.Background(), prompt)
}

// Ask prints text and reads one line, returning early with ctx.Err() if ctx
// is cancelled. Concurrent callers are serialised so their output and input
// don't interleave.
func (l *LineReader) Ask(ctx context.Context, text string) (string, error) {
	select {
	case l.turn <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-l.turn }()

	if text != "" {
		fmt.Fprint(l.out, text)
	}

	l.mu.Lock()
	ch := l.pending
	if ch == nil {
		ch = make(chan lineResult, 1)
		l.pending = ch
		go func() {
			line, err := l.r.ReadString('\n')
			if err != nil && (err != io.EOF || line == "") {
				ch <- lineResult{err: err}
				return
			}
			ch <- lineResult{line: strings.TrimRight(line, "\r\n")}
		}()
	}
	l.mu.Unlock()

	select {
	case res := <-ch:
		l.mu.Lock()
		l.pending = nil
		l.mu.Unlock()
		return res.line, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Input reads user input. LineReader serves pipes and tests; TerminalInput
// adds line editing, history and completion on a TTY.
type Input interface {
	// Ask reads a one-off answer (approvals, questions); not saved to history.
	Ask(ctx context.Context, text string) (string, error)
	// ReadInput reads a REPL entry, which may span lines: end a line with
	// "\\" to continue it, or enclose a block between lines of """.
	ReadInput(ctx context.Context, prompt string) (string, error)
}

// ReadInput implements Input.
func (l *LineReader) ReadInput(ctx context.Context, prompt string) (string, error) {
	return readMultiline(ctx, prompt, l.Ask)
}

const continuationPrompt = "... "

// readMultiline reads one entry, joining continuation lines.
func readMultiline(ctx context.Context, prompt string, ask func(context.Context, string) (string, error)) (string, error) {
	first, err := ask(ctx, prompt)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(first) == `"""` {
		var lines []string
		for {
			line, err := ask(ctx, continuationPrompt)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(line) == `"""` {
				return strings.Join(lines, "\n"), nil
			}
			lines = append(lines, line)
		}
	}
	lines := []string{first}
	for strings.HasSuffix(lines[len(lines)-1], "\\") {
		last := len(lines) - 1
		lines[last] = strings.TrimSuffix(lines[last], "\\")
		line, err := ask(ctx, continuationPrompt)
		if err != nil {
			return "", err
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), nil
}

// NewApprover returns a tools.Approver that asks on the terminal, showing
// the proposed diff (truncated to diffLines; "d" shows it all). Anything other
// than an explicit yes is a denial.
func NewApprover(in Input, diffLines int) tools.Approver {
	if diffLines <= 0 {
		diffLines = 120
	}
	return func(ctx context.Context, req tools.ApprovalRequest) (tools.Decision, error) {
		if ctx.Err() != nil {
			return tools.DecisionDeny, ctx.Err()
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "\n%s🔐 %s%s [%s]\n", Yellow+Bold, i18n.T("approve.title"), Reset, i18n.T("approve.via", "kind", req.Kind, "tool", safe(req.Tool)))
		for _, line := range strings.Split(safe(req.Detail), "\n") {
			fmt.Fprintf(&sb, "   %s%s%s\n", Dim, line, Reset)
		}
		truncated := false
		if req.Diff != "" {
			var d string
			d, truncated = RenderDiff(req.Diff, diffLines)
			sb.WriteString(d)
		}

		options := i18n.T("approve.yes")
		valid := "y/N"
		if req.Key != "" {
			label := req.KeyLabel
			if label == "" {
				label = i18n.T("approve.matching")
			}
			options = i18n.T("approve.options", "label", label)
			valid = "y/s/a/N"
		}
		if truncated {
			options += "  " + i18n.T("approve.show_diff")
			valid = strings.Replace(valid, "/N", "/d/N", 1)
		}
		fmt.Fprintf(&sb, "   %s%s  %s%s\n   %s [%s]: ", Dim, options, i18n.T("approve.no"), Reset, i18n.T("approve.ask"), valid)

		prompt := sb.String()
		for {
			answer, err := in.Ask(ctx, prompt)
			if err != nil {
				return tools.DecisionDeny, fmt.Errorf("no approval input: %w", err)
			}
			switch strings.ToLower(strings.TrimSpace(answer)) {
			case "y", "yes":
				return tools.DecisionOnce, nil
			case "s", "session":
				if req.Key != "" {
					return tools.DecisionSession, nil
				}
			case "a", "always":
				if req.Key != "" {
					return tools.DecisionAlways, nil
				}
			case "d", "diff":
				if truncated {
					full, _ := RenderDiff(req.Diff, 0)
					prompt = full + fmt.Sprintf("   %s [%s]: ", i18n.T("approve.ask"), valid)
					continue
				}
			}
			return tools.DecisionDeny, nil
		}
	}
}

// RenderDiff colourises a unified diff for the terminal, keeping at most
// maxLines lines (0 = all). It reports whether lines were cut.
func RenderDiff(diff string, maxLines int) (string, bool) {
	lines := strings.Split(strings.TrimRight(safe(diff), "\n"), "\n")
	cut := false
	if maxLines > 0 && len(lines) > maxLines {
		lines, cut = lines[:maxLines], true
	}
	var sb strings.Builder
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
			sb.WriteString("   " + Bold + l + Reset + "\n")
		case strings.HasPrefix(l, "+"):
			sb.WriteString("   " + Green + l + Reset + "\n")
		case strings.HasPrefix(l, "-"):
			sb.WriteString("   " + Red + l + Reset + "\n")
		case strings.HasPrefix(l, "@@"):
			sb.WriteString("   " + Cyan + l + Reset + "\n")
		default:
			sb.WriteString("   " + l + "\n")
		}
	}
	if cut {
		fmt.Fprintf(&sb, "   %s… %s%s\n", Dim, i18n.T("diff.truncated", "lines", maxLines), Reset)
	}
	return sb.String(), cut
}

// NewUserPrompter returns a tools.UserPromptFunc backed by the terminal.
// A numeric answer selects the matching option when options are offered.
func NewUserPrompter(in Input) tools.UserPromptFunc {
	return func(ctx context.Context, question string, options []string) (string, error) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "\n❓ [%s]: %s\n", i18n.T("question.title"), textutil.SanitizeTerminal(question))
		for i, opt := range options {
			fmt.Fprintf(&sb, "   [%d] %s\n", i+1, textutil.SanitizeTerminal(opt))
		}
		sb.WriteString("👉 Answer: ")

		answer, err := in.Ask(ctx, sb.String())
		if err != nil {
			return "", err
		}
		answer = strings.TrimSpace(answer)
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(options) {
			return options[n-1], nil
		}
		return answer, nil
	}
}
