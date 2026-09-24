package tools

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

type patchOp int

const (
	opUpdate patchOp = iota
	opAdd
	opDelete
)

func (o patchOp) String() string {
	return [...]string{"update", "add", "delete"}[o]
}

// filePatch is one file's change, parsed from either a unified diff or the
// "*** Begin Patch" format.
type filePatch struct {
	op      patchOp
	path    string
	moveTo  string
	hunks   []patchHunk
	content string // full content for opAdd
}

type patchHunk struct {
	anchor string   // "@@ <anchor>" line to search for first (Begin Patch format)
	hint   int      // 0-based line where the hunk is expected, or -1
	old    []string // context + removed lines
	new    []string // context + added lines
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// parsePatch accepts a unified diff (git or plain) or a "*** Begin Patch" block.
func parsePatch(text string) ([]filePatch, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.Contains(text, "*** Begin Patch") {
		return parseBeginPatch(text)
	}
	return parseUnified(text)
}

func parseUnified(text string) ([]filePatch, error) {
	lines := strings.Split(text, "\n")
	var patches []filePatch
	var cur *filePatch
	var hunk *patchHunk
	flush := func() {
		if cur != nil {
			if hunk != nil {
				cur.hunks = append(cur.hunks, *hunk)
				hunk = nil
			}
			patches = append(patches, *cur)
			cur = nil
		}
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ "):
			flush()
			oldPath := diffPath(line[4:], "a/")
			newPath := diffPath(lines[i+1][4:], "b/")
			i++
			fp := filePatch{op: opUpdate, path: oldPath}
			switch {
			case oldPath == "/dev/null" && newPath == "/dev/null":
				return nil, fmt.Errorf("line %d: both sides are /dev/null", i)
			case oldPath == "/dev/null":
				fp.op, fp.path = opAdd, newPath
			case newPath == "/dev/null":
				fp.op = opDelete
			case newPath != oldPath:
				fp.moveTo = newPath
			}
			cur = &fp
		case strings.HasPrefix(line, "@@"):
			if cur == nil {
				return nil, fmt.Errorf("line %d: hunk before any file header", i+1)
			}
			if hunk != nil {
				cur.hunks = append(cur.hunks, *hunk)
			}
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("line %d: malformed hunk header %q", i+1, line)
			}
			start, _ := strconv.Atoi(m[1])
			hunk = &patchHunk{hint: max(start-1, 0)}
		case hunk != nil && strings.HasPrefix(line, "\\"):
			// "\ No newline at end of file": trailing newlines are preserved from the original.
		case hunk != nil && (strings.HasPrefix(line, " ") || line == ""):
			// Some models strip the leading space from blank context lines.
			if line == "" && i == len(lines)-1 {
				continue
			}
			ctxLine := strings.TrimPrefix(line, " ")
			hunk.old = append(hunk.old, ctxLine)
			hunk.new = append(hunk.new, ctxLine)
		case hunk != nil && strings.HasPrefix(line, "-"):
			hunk.old = append(hunk.old, line[1:])
		case hunk != nil && strings.HasPrefix(line, "+"):
			hunk.new = append(hunk.new, line[1:])
		default:
			// git metadata (diff --git, index, mode lines) and prose between files.
			if hunk != nil {
				cur.hunks = append(cur.hunks, *hunk)
				hunk = nil
			}
		}
	}
	flush()

	if len(patches) == 0 {
		return nil, errors.New("no file changes found; expected a unified diff (---/+++/@@) or a *** Begin Patch block")
	}
	for i := range patches {
		p := &patches[i]
		if p.op == opAdd {
			var content []string
			for _, h := range p.hunks {
				content = append(content, h.new...)
			}
			p.content = joinLines(content, true)
			p.hunks = nil
		} else if p.op == opUpdate && len(p.hunks) == 0 && p.moveTo == "" {
			return nil, fmt.Errorf("%s: no hunks", p.path)
		}
	}
	return patches, nil
}

// diffPath extracts a path from a ---/+++ header, dropping timestamps and the
// conventional a/ or b/ prefix.
func diffPath(s, prefix string) string {
	if i := strings.IndexByte(s, '\t'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if s == "/dev/null" {
		return s
	}
	return strings.TrimPrefix(s, prefix)
}

func parseBeginPatch(text string) ([]filePatch, error) {
	lines := strings.Split(text, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "*** Begin Patch" {
			start = i + 1
			break
		}
	}
	var patches []filePatch
	var cur *filePatch
	var hunk *patchHunk
	var addLines []string
	flush := func() {
		if cur == nil {
			return
		}
		if hunk != nil && (len(hunk.old) > 0 || len(hunk.new) > 0) {
			cur.hunks = append(cur.hunks, *hunk)
		}
		hunk = nil
		if cur.op == opAdd {
			cur.content = joinLines(addLines, true)
			addLines = nil
		}
		patches = append(patches, *cur)
		cur = nil
	}

	ended := false
	for i := start; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "*** End Patch":
			flush()
			ended = true
		case strings.HasPrefix(line, "*** Add File: "):
			flush()
			cur = &filePatch{op: opAdd, path: strings.TrimSpace(line[len("*** Add File: "):])}
		case strings.HasPrefix(line, "*** Delete File: "):
			flush()
			cur = &filePatch{op: opDelete, path: strings.TrimSpace(line[len("*** Delete File: "):])}
		case strings.HasPrefix(line, "*** Update File: "):
			flush()
			cur = &filePatch{op: opUpdate, path: strings.TrimSpace(line[len("*** Update File: "):])}
		case strings.HasPrefix(line, "*** Move to: ") && cur != nil:
			cur.moveTo = strings.TrimSpace(line[len("*** Move to: "):])
		case trimmed == "*** End of File":
		case cur == nil:
			if trimmed != "" {
				return nil, fmt.Errorf("line %d: expected a *** file header, got %q", i+1, line)
			}
		case cur.op == opAdd:
			if !strings.HasPrefix(line, "+") && line != "" {
				return nil, fmt.Errorf("line %d: lines in an added file must start with '+'", i+1)
			}
			if line != "" {
				addLines = append(addLines, line[1:])
			}
		case cur.op == opUpdate && strings.HasPrefix(line, "@@"):
			if hunk != nil && (len(hunk.old) > 0 || len(hunk.new) > 0) {
				cur.hunks = append(cur.hunks, *hunk)
			}
			hunk = &patchHunk{hint: -1, anchor: strings.TrimSpace(strings.TrimPrefix(line, "@@"))}
		case cur.op == opUpdate:
			if hunk == nil {
				hunk = &patchHunk{hint: -1}
			}
			switch {
			case strings.HasPrefix(line, "+"):
				hunk.new = append(hunk.new, line[1:])
			case strings.HasPrefix(line, "-"):
				hunk.old = append(hunk.old, line[1:])
			default:
				c := strings.TrimPrefix(line, " ")
				hunk.old = append(hunk.old, c)
				hunk.new = append(hunk.new, c)
			}
		}
		if ended {
			break
		}
	}
	if !ended {
		return nil, errors.New("missing *** End Patch")
	}
	if len(patches) == 0 {
		return nil, errors.New("patch contains no file operations")
	}
	for _, p := range patches {
		if p.op == opUpdate && len(p.hunks) == 0 && p.moveTo == "" {
			return nil, fmt.Errorf("%s: update has no changes", p.path)
		}
	}
	return patches, nil
}

// applyHunks applies hunks to content. Matching is exact first, then ignores
// trailing whitespace, then surrounding whitespace, so small formatting slips
// by the model don't make an otherwise unambiguous patch fail.
func applyHunks(content string, hunks []patchHunk) (string, error) {
	// Match on LF-normalised text and restore CRLF afterwards, since patch
	// lines never carry the carriage returns.
	crlf := strings.Contains(content, "\r\n")
	if crlf {
		content = strings.ReplaceAll(content, "\r\n", "\n")
		out, err := applyHunks(content, hunks)
		return strings.ReplaceAll(out, "\n", "\r\n"), err
	}
	hadNewline := strings.HasSuffix(content, "\n")
	lines := splitLines(content)
	pos, delta := 0, 0

	for n, h := range hunks {
		searchFrom := pos
		if h.anchor != "" {
			idx := findLine(lines, h.anchor, pos)
			if idx < 0 {
				return "", fmt.Errorf("hunk %d: anchor %q not found", n+1, h.anchor)
			}
			searchFrom = idx
		}

		var at int
		if len(h.old) == 0 {
			// Pure insertion: at the hinted line, after the anchor, or at EOF.
			switch {
			case h.hint >= 0:
				at = min(max(h.hint+delta, 0), len(lines))
			case h.anchor != "":
				at = searchFrom + 1
			default:
				at = len(lines)
			}
		} else {
			expected := -1
			if h.hint >= 0 {
				expected = h.hint + delta
			}
			at = findBlock(lines, h.old, searchFrom, expected)
			if at < 0 {
				return "", fmt.Errorf("hunk %d: could not find the lines to change:\n%s", n+1, preview(h.old))
			}
		}

		replaced := make([]string, 0, len(lines)-len(h.old)+len(h.new))
		replaced = append(replaced, lines[:at]...)
		replaced = append(replaced, h.new...)
		replaced = append(replaced, lines[at+len(h.old):]...)
		lines = replaced
		delta += len(h.new) - len(h.old)
		pos = at + len(h.new)
	}
	return joinLines(lines, hadNewline || content == ""), nil
}

// findBlock returns where block occurs in lines at or after from, preferring
// the occurrence nearest expected (if >= 0); -1 if absent.
func findBlock(lines, block []string, from, expected int) int {
	normalizers := []func(string) string{
		func(s string) string { return s },
		func(s string) string { return strings.TrimRight(s, " \t") },
		strings.TrimSpace,
	}
	for _, norm := range normalizers {
		best := -1
		for i := from; i+len(block) <= len(lines); i++ {
			if matchAt(lines, block, i, norm) {
				if expected < 0 {
					return i
				}
				if best < 0 || abs(i-expected) < abs(best-expected) {
					best = i
				}
			}
		}
		if best >= 0 {
			return best
		}
	}
	// The hint may point before `from` when hunks are out of order.
	if from > 0 {
		return findBlock(lines, block, 0, expected)
	}
	return -1
}

func matchAt(lines, block []string, at int, norm func(string) string) bool {
	for j, b := range block {
		if norm(lines[at+j]) != norm(b) {
			return false
		}
	}
	return true
}

func findLine(lines []string, target string, from int) int {
	t := strings.TrimSpace(target)
	for i := from; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == t {
			return i
		}
	}
	for i := from; i < len(lines); i++ {
		if strings.Contains(lines[i], t) {
			return i
		}
	}
	return -1
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func joinLines(lines []string, trailingNewline bool) string {
	s := strings.Join(lines, "\n")
	if trailingNewline && len(lines) > 0 {
		s += "\n"
	}
	return s
}

func preview(lines []string) string {
	const max = 8
	var sb strings.Builder
	for i, l := range lines {
		if i == max {
			fmt.Fprintf(&sb, "  ... (%d more lines)\n", len(lines)-max)
			break
		}
		sb.WriteString("  | " + l + "\n")
	}
	return sb.String()
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ApplyPatchInput defines arguments for apply_patch.
type ApplyPatchInput struct {
	Patch string `json:"patch" jsonschema:"A unified diff (---/+++/@@ hunks, may span files) or a '*** Begin Patch' ... '*** End Patch' block with Add File / Update File / Delete File sections"`
}

// PatchedFile summarises one file's change.
type PatchedFile struct {
	Path    string `json:"path"`
	Op      string `json:"op"`
	MovedTo string `json:"moved_to,omitempty"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// ApplyPatchOutput holds the result of apply_patch.
type ApplyPatchOutput struct {
	Success bool          `json:"success"`
	Files   []PatchedFile `json:"files,omitempty"`
	Error   string        `json:"error,omitempty"`
}

type plannedChange struct {
	summary  PatchedFile
	path     string // display path of the file being changed
	target   string // where the result is written (differs on move)
	before   string
	after    string
	existed  bool
	deleteIt bool
}

// NewApplyPatchTool creates the apply_patch tool. The whole patch is validated
// before anything is written, and it is applied all-or-nothing: if any write
// fails, files already changed are restored.
func NewApplyPatchTool(ws *Workspace, hooks *Hooks) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "apply_patch",
			Description: "Apply a multi-file patch (unified diff or *** Begin Patch format) atomically",
		},
		func(ctx agent.Context, input ApplyPatchInput) (ApplyPatchOutput, error) {
			fail := func(err error) (ApplyPatchOutput, error) { return ApplyPatchOutput{Error: err.Error()}, nil }

			patches, err := parsePatch(input.Patch)
			if err != nil {
				return fail(fmt.Errorf("invalid patch: %w", err))
			}
			plan, err := planPatch(ws, patches)
			if err != nil {
				return fail(err)
			}

			var diff strings.Builder
			var names []string
			for _, c := range plan {
				d := unifiedDiff(c.path, c.before, c.after)
				if c.target != c.path {
					d = fmt.Sprintf("rename %s -> %s\n", c.path, c.target) + unifiedDiff(c.target, c.before, c.after)
				}
				diff.WriteString(d)
				names = append(names, c.path)
			}
			if err := hooks.Approve(ctx, writeApproval(ws, "apply_patch",
				fmt.Sprintf("Apply patch to %d file(s): %s", len(plan), strings.Join(names, ", ")), diff.String())); err != nil {
				return fail(err)
			}

			if err := executePlan(ws, plan); err != nil {
				return fail(err)
			}
			out := ApplyPatchOutput{Success: true}
			for _, c := range plan {
				out.Files = append(out.Files, c.summary)
			}
			return out, nil
		},
	)
}

func planPatch(ws *Workspace, patches []filePatch) ([]plannedChange, error) {
	seen := map[string]bool{}
	var plan []plannedChange
	for _, p := range patches {
		path, err := ws.WritablePath(p.path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.path, err)
		}
		if seen[path] {
			return nil, fmt.Errorf("%s: appears more than once in the patch", path)
		}
		seen[path] = true

		c := plannedChange{path: path, target: path, summary: PatchedFile{Path: path, Op: p.op.String()}}
		data, readErr := ws.ReadFile(path)
		c.existed = readErr == nil
		c.before = string(data)

		switch p.op {
		case opAdd:
			if c.existed {
				return nil, fmt.Errorf("%s: cannot add, file already exists", path)
			}
			c.after = p.content
		case opDelete:
			if !c.existed {
				return nil, fmt.Errorf("%s: cannot delete: %v", path, readErr)
			}
			c.deleteIt = true
		case opUpdate:
			if !c.existed {
				return nil, fmt.Errorf("%s: cannot update: %v", path, readErr)
			}
			c.after, err = applyHunks(c.before, p.hunks)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			if p.moveTo != "" {
				target, err := ws.WritablePath(p.moveTo)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", p.moveTo, err)
				}
				if _, err := ws.Stat(target); err == nil {
					return nil, fmt.Errorf("%s: cannot move, destination exists", target)
				}
				c.target, c.summary.MovedTo = target, target
			}
		}
		c.summary.Added, c.summary.Removed = diffStats(unifiedDiff(path, c.before, c.after))
		plan = append(plan, c)
	}
	return plan, nil
}

func executePlan(ws *Workspace, plan []plannedChange) error {
	type undo struct {
		path    string
		content string
		existed bool
	}
	var done []undo
	rollback := func(cause error) error {
		var errs []error
		for i := len(done) - 1; i >= 0; i-- {
			u := done[i]
			var err error
			if u.existed {
				err = ws.WriteFileAtomic(u.path, []byte(u.content))
			} else {
				err = ws.RemoveFile(u.path)
			}
			if err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("%w; rollback also failed: %v", cause, errors.Join(errs...))
		}
		return fmt.Errorf("%w (no files were changed)", cause)
	}

	for _, c := range plan {
		switch {
		case c.deleteIt:
			if err := ws.RemoveFile(c.path); err != nil {
				return rollback(fmt.Errorf("delete %s: %w", c.path, err))
			}
			done = append(done, undo{c.path, c.before, true})
		case c.target != c.path:
			if err := ws.CreateExclusive(c.target, []byte(c.after)); err != nil {
				return rollback(fmt.Errorf("write %s: %w", c.target, err))
			}
			done = append(done, undo{c.target, "", false})
			if err := ws.RemoveFile(c.path); err != nil {
				return rollback(fmt.Errorf("remove %s: %w", c.path, err))
			}
			done = append(done, undo{c.path, c.before, true})
		case !c.existed:
			if err := ws.CreateExclusive(c.path, []byte(c.after)); err != nil {
				return rollback(fmt.Errorf("create %s: %w", c.path, err))
			}
			done = append(done, undo{c.path, "", false})
		default:
			if err := ws.WriteFileAtomic(c.path, []byte(c.after)); err != nil {
				return rollback(fmt.Errorf("write %s: %w", c.path, err))
			}
			done = append(done, undo{c.path, c.before, true})
		}
	}
	return nil
}
