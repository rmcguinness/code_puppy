package tools

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultMaxFileSize bounds how much of a single file the tools will load into memory.
const DefaultMaxFileSize int64 = 10 * 1024 * 1024

var (
	// ErrOutsideWorkspace is returned when a path is not under any sandbox root.
	ErrOutsideWorkspace = errors.New("path is outside the workspace")
	// ErrReadOnlyPath is returned when writing under a read-only root.
	ErrReadOnlyPath = errors.New("path is read-only")
	// ErrBlockedPath is returned when a path matches a blocked-path pattern.
	ErrBlockedPath = errors.New("path is blocked by sandbox policy")
)

// WorkspaceOptions configures the file sandbox.
type WorkspaceOptions struct {
	Dir           string   // the workspace (read-write); defaults to "."
	AllowedPaths  []string // additional read-write roots
	ReadOnlyPaths []string // additional read-only roots
	BlockedPaths  []string // glob patterns never readable or writable
	MaxFileSize   int64
}

// Workspace is the file sandbox. Every file tool goes through it: paths must
// fall under one of its roots, writes need a writable root, and blocked
// patterns are refused even inside a root. Each root is an os.Root, which
// rejects ".." traversal and symlinks leaving the root at the OS level.
type Workspace struct {
	roots       []*fsRoot // roots[0] is the workspace
	blocked     *PathMatcher
	maxFileSize int64
	checkpoints *Checkpoints // nil when disabled
}

type fsRoot struct {
	dir      string // absolute, symlink-resolved
	root     *os.Root
	writable bool
}

// location is a resolved path: a root plus a clean path relative to it.
type location struct {
	root *fsRoot
	rel  string
}

func (l location) abs() string { return filepath.Join(l.root.dir, l.rel) }

// NewWorkspace opens dir as a single read-write root with no blocked paths.
func NewWorkspace(dir string, maxFileSize int64) (*Workspace, error) {
	return OpenWorkspace(WorkspaceOptions{Dir: dir, MaxFileSize: maxFileSize})
}

// OpenWorkspace opens the workspace and any additional roots. Additional
// roots that don't exist are an error, so a typo can't silently widen or
// narrow the sandbox.
func OpenWorkspace(opts WorkspaceOptions) (*Workspace, error) {
	if opts.Dir == "" {
		opts.Dir = "."
	}
	w := &Workspace{maxFileSize: opts.MaxFileSize}
	if w.maxFileSize <= 0 {
		w.maxFileSize = DefaultMaxFileSize
	}

	add := func(dir string, writable bool) error {
		real, err := canonicalDir(dir)
		if err != nil {
			return fmt.Errorf("sandbox root %q: %w", dir, err)
		}
		root, err := os.OpenRoot(real)
		if err != nil {
			return fmt.Errorf("open sandbox root %q: %w", real, err)
		}
		w.roots = append(w.roots, &fsRoot{dir: real, root: root, writable: writable})
		return nil
	}
	if err := add(opts.Dir, true); err != nil {
		return nil, err
	}
	for _, d := range opts.AllowedPaths {
		if err := add(d, true); err != nil {
			w.Close()
			return nil, err
		}
	}
	for _, d := range opts.ReadOnlyPaths {
		if err := add(d, false); err != nil {
			w.Close()
			return nil, err
		}
	}

	blocked, err := NewPathMatcher(opts.BlockedPaths, w.RootDirs())
	if err != nil {
		w.Close()
		return nil, err
	}
	w.blocked = blocked
	return w, nil
}

// Dir returns the absolute workspace path.
func (w *Workspace) Dir() string { return w.roots[0].dir }

// MaxFileSize returns the per-file read limit in bytes.
func (w *Workspace) MaxFileSize() int64 { return w.maxFileSize }

// RootDirs returns every root directory, workspace first.
func (w *Workspace) RootDirs() []string {
	out := make([]string, 0, len(w.roots))
	for _, r := range w.roots {
		out = append(out, r.dir)
	}
	return out
}

// WritableDirs returns the roots that may be written to.
func (w *Workspace) WritableDirs() []string {
	var out []string
	for _, r := range w.roots {
		if r.writable {
			out = append(out, r.dir)
		}
	}
	return out
}

// ReadOnlyDirs returns the read-only roots.
func (w *Workspace) ReadOnlyDirs() []string {
	var out []string
	for _, r := range w.roots {
		if !r.writable {
			out = append(out, r.dir)
		}
	}
	return out
}

// Blocked returns the blocked-path matcher.
func (w *Workspace) Blocked() *PathMatcher { return w.blocked }

// Close releases all root handles.
func (w *Workspace) Close() error {
	var errs []error
	for _, r := range w.roots {
		errs = append(errs, r.root.Close())
	}
	return errors.Join(errs...)
}

// resolve maps a caller-supplied path to the most specific root containing
// it and enforces write access and blocked patterns. Relative paths are
// relative to the workspace.
func (w *Workspace) resolve(p string, write bool) (location, error) {
	if p == "" {
		p = "."
	}
	abs := p
	if !filepath.IsAbs(p) {
		clean := filepath.Clean(p)
		if escapesRoot(clean) {
			return location{}, fmt.Errorf("%w: %q", ErrOutsideWorkspace, p)
		}
		abs = filepath.Join(w.roots[0].dir, clean)
	}
	abs = filepath.Clean(abs)

	loc, ok := w.locate(abs)
	if !ok {
		// The caller may have used a non-canonical spelling of a root
		// (e.g. /var vs /private/var on macOS).
		if resolved, err := resolveExistingPrefix(abs); err == nil {
			loc, ok = w.locate(resolved)
		}
	}
	if !ok {
		return location{}, fmt.Errorf("%w: %q is not under %s", ErrOutsideWorkspace, p, strings.Join(w.RootDirs(), ", "))
	}
	if err := w.check(loc, write); err != nil {
		return location{}, err
	}
	return loc, nil
}

// locate finds the deepest root containing abs, so a read-only root nested
// inside the workspace takes precedence.
func (w *Workspace) locate(abs string) (location, bool) {
	var best *fsRoot
	var bestRel string
	for _, r := range w.roots {
		rel, err := filepath.Rel(r.dir, abs)
		if err != nil || escapesRoot(rel) {
			continue
		}
		if best == nil || len(r.dir) > len(best.dir) {
			best, bestRel = r, rel
		}
	}
	return location{root: best, rel: bestRel}, best != nil
}

func (w *Workspace) check(loc location, write bool) error {
	if write && !loc.root.writable {
		return fmt.Errorf("%w: %s", ErrReadOnlyPath, w.display(loc.abs()))
	}
	if pat, blocked := w.blocked.Match(loc.abs()); blocked {
		return fmt.Errorf("%w (%s): %s", ErrBlockedPath, pat, w.display(loc.abs()))
	}
	// A symlink can point at a blocked or read-only location that the
	// lexical path doesn't reveal; check where it really leads.
	if real, err := filepath.EvalSymlinks(loc.abs()); err == nil && real != loc.abs() {
		if pat, blocked := w.blocked.Match(real); blocked {
			return fmt.Errorf("%w (%s via symlink): %s", ErrBlockedPath, pat, w.display(loc.abs()))
		}
		if write {
			if target, ok := w.locate(real); ok && !target.root.writable {
				return fmt.Errorf("%w (via symlink): %s", ErrReadOnlyPath, w.display(loc.abs()))
			}
		}
	}
	return nil
}

// display renders abs relative to the workspace when inside it, else absolute.
func (w *Workspace) display(abs string) string {
	if rel, err := filepath.Rel(w.roots[0].dir, abs); err == nil && !escapesRoot(rel) {
		return rel
	}
	return abs
}

// Rel resolves p for reading and returns its display form: relative to the
// workspace when inside it, otherwise absolute.
func (w *Workspace) Rel(p string) (string, error) {
	loc, err := w.resolve(p, false)
	if err != nil {
		return "", err
	}
	return w.display(loc.abs()), nil
}

// WritablePath resolves p for writing and returns its display form. Tools call
// it before asking for approval so users aren't asked to approve a write the
// sandbox would refuse anyway.
func (w *Workspace) WritablePath(p string) (string, error) {
	loc, err := w.resolve(p, true)
	if err != nil {
		return "", err
	}
	return w.display(loc.abs()), nil
}

// Abs resolves p for reading and returns its absolute path after verifying,
// through os.Root, that it exists and does not escape via symlinks.
func (w *Workspace) Abs(p string) (string, error) {
	loc, err := w.resolve(p, false)
	if err != nil {
		return "", err
	}
	if _, err := loc.root.root.Stat(loc.rel); err != nil {
		return "", err
	}
	return loc.abs(), nil
}

// Stat stats p.
func (w *Workspace) Stat(p string) (fs.FileInfo, error) {
	loc, err := w.resolve(p, false)
	if err != nil {
		return nil, err
	}
	return loc.root.root.Stat(loc.rel)
}

// Open opens p for reading.
func (w *Workspace) Open(p string) (*os.File, error) {
	loc, err := w.resolve(p, false)
	if err != nil {
		return nil, err
	}
	return loc.root.root.Open(loc.rel)
}

// ReadFile reads a regular file, refusing files over the size limit.
func (w *Workspace) ReadFile(p string) ([]byte, error) {
	f, err := w.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", p)
	}
	if info.Size() > w.maxFileSize {
		return nil, fmt.Errorf("%s is %d bytes, exceeding the %d byte limit", p, info.Size(), w.maxFileSize)
	}
	// Guard against files that grow between Stat and Read.
	data, err := io.ReadAll(io.LimitReader(f, w.maxFileSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > w.maxFileSize {
		return nil, fmt.Errorf("%s exceeds the %d byte limit", p, w.maxFileSize)
	}
	return data, nil
}

// CreateExclusive creates a new file, failing with fs.ErrExist if it exists.
func (w *Workspace) CreateExclusive(p string, data []byte) error {
	loc, err := w.resolve(p, true)
	if err != nil {
		return err
	}
	if loc.rel == "." {
		return errors.New("path must name a file")
	}
	r := loc.root.root
	if err := mkdirParent(r, loc.rel); err != nil {
		return err
	}
	added := w.snapshot(loc)
	fail := func(err error) error {
		if added {
			w.checkpoints.discard(loc.abs())
		}
		return err
	}
	f, err := r.OpenFile(loc.rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = r.Remove(loc.rel)
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	w.checkpoints.after(loc.abs(), data, true)
	return nil
}

// WriteFileAtomic replaces p with data via a temp file and rename, so a crash
// or error never leaves a half-written file. An existing file's mode is kept.
// If p is itself a symlink, the target is rewritten in place (through
// os.Root, so it must stay inside the root) rather than replacing the link.
func (w *Workspace) WriteFileAtomic(p string, data []byte) error {
	loc, err := w.resolve(p, true)
	if err != nil {
		return err
	}
	if loc.rel == "." {
		return errors.New("path must name a file")
	}
	added := w.snapshot(loc)
	if err := w.writeAtomic(loc, data, nil); err != nil {
		if added {
			w.checkpoints.discard(loc.abs())
		}
		return err
	}
	w.checkpoints.after(loc.abs(), data, true)
	return nil
}

// writeAtomic implements WriteFileAtomic without checkpointing. mode, if set,
// overrides the file's permissions (used when restoring).
func (w *Workspace) writeAtomic(loc location, data []byte, modeOverride *fs.FileMode) error {
	r := loc.root.root
	if info, err := r.Lstat(loc.rel); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return writeInPlace(r, loc.rel, data)
	}

	mode := fs.FileMode(0o644)
	if info, err := r.Stat(loc.rel); err == nil {
		if info.IsDir() {
			return fmt.Errorf("%s is a directory", w.display(loc.abs()))
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := mkdirParent(r, loc.rel); err != nil {
		return err
	}

	tmp, err := tempName(loc.rel)
	if err != nil {
		return err
	}
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = r.Remove(tmp)
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	if modeOverride != nil {
		mode = *modeOverride
	}
	if err := r.Chmod(tmp, mode); err != nil {
		return fail(err)
	}
	if err := r.Rename(tmp, loc.rel); err != nil {
		return fail(err)
	}
	return nil
}

func writeInPlace(r *os.Root, rel string, data []byte) error {
	info, err := r.Stat(rel)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", rel)
	}
	f, err := r.OpenFile(rel, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// RemoveFile deletes a single non-directory entry.
func (w *Workspace) RemoveFile(p string) error {
	loc, err := w.resolve(p, true)
	if err != nil {
		return err
	}
	if loc.rel == "." {
		return errors.New("refusing to delete a sandbox root")
	}
	info, err := loc.root.root.Lstat(loc.rel)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory; delete_file only removes files", p)
	}
	added := w.snapshot(loc)
	if err := loc.root.root.Remove(loc.rel); err != nil {
		if added {
			w.checkpoints.discard(loc.abs())
		}
		return err
	}
	w.checkpoints.after(loc.abs(), nil, false)
	return nil
}

// snapshot records loc's current state in the checkpoint store, reporting
// whether it added a new entry (which the caller discards if the change fails).
// Directories are never snapshotted; the operation on them fails anyway.
func (w *Workspace) snapshot(loc location) bool {
	if w.checkpoints == nil {
		return false
	}
	st := fileState{}
	r := loc.root.root
	info, err := r.Lstat(loc.rel)
	if err == nil && info.IsDir() {
		return false
	}
	if err == nil {
		st.exists = true
		st.mode = info.Mode().Perm()
		if info.Mode()&fs.ModeSymlink != 0 {
			if real, err := r.Stat(loc.rel); err == nil {
				st.mode = real.Mode().Perm()
			}
		}
		if info.Size() > w.maxFileSize {
			st.tooLarge = true
		} else if data, err := r.ReadFile(loc.rel); err == nil {
			st.data = data
		} else {
			st.tooLarge = true
		}
	}
	return w.checkpoints.before(loc.abs(), w.display(loc.abs()), st)
}

// restore puts abs back into state st without checkpointing.
func (w *Workspace) restore(abs string, st fileState) error {
	loc, err := w.resolve(abs, true)
	if err != nil {
		return err
	}
	if !st.exists {
		err := loc.root.root.Remove(loc.rel)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := mkdirParent(loc.root.root, loc.rel); err != nil {
		return err
	}
	mode := st.mode
	return w.writeAtomic(loc, st.data, &mode)
}

// WalkEntry is one entry visited by Walk.
type WalkEntry struct {
	Path   string // display path: workspace-relative, or absolute outside it
	Entry  fs.DirEntry
	IsRoot bool // the starting path itself
	fsys   fs.FS
	fsPath string
}

// Open opens the entry for reading.
func (e WalkEntry) Open() (fs.File, error) { return e.fsys.Open(e.fsPath) }

// Walk visits p and everything beneath it in lexical order, skipping blocked
// entries. fn may return fs.SkipDir or fs.SkipAll.
func (w *Workspace) Walk(p string, fn func(WalkEntry) error) error {
	loc, err := w.resolve(p, false)
	if err != nil {
		return err
	}
	fsys := loc.root.root.FS()
	start := filepath.ToSlash(loc.rel)
	return fs.WalkDir(fsys, start, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == start {
				return err
			}
			return nil
		}
		abs := filepath.Join(loc.root.dir, filepath.FromSlash(path))
		if path != start {
			if _, blocked := w.blocked.Match(abs); blocked {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		return fn(WalkEntry{Path: w.display(abs), Entry: d, IsRoot: path == start, fsys: fsys, fsPath: path})
	})
}

// Describe summarises the sandbox for display.
func (w *Workspace) Describe() []string {
	lines := []string{"workspace (rw): " + w.Dir()}
	extra := append([]*fsRoot(nil), w.roots[1:]...)
	sort.Slice(extra, func(i, j int) bool { return extra[i].dir < extra[j].dir })
	for _, r := range extra {
		mode := "ro"
		if r.writable {
			mode = "rw"
		}
		lines = append(lines, fmt.Sprintf("allowed (%s): %s", mode, r.dir))
	}
	if pats := w.blocked.Patterns(); len(pats) > 0 {
		lines = append(lines, "blocked: "+strings.Join(pats, ", "))
	}
	return lines
}

func mkdirParent(r *os.Root, rel string) error {
	dir := filepath.Dir(rel)
	if dir == "." {
		return nil
	}
	return r.MkdirAll(dir, 0o755)
}

func tempName(rel string) (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(rel), "."+filepath.Base(rel)+".tmp-"+hex.EncodeToString(b[:])), nil
}

func escapesRoot(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveExistingPrefix evaluates symlinks in the longest existing ancestor of
// p and re-appends the non-existent remainder.
func resolveExistingPrefix(p string) (string, error) {
	var rest []string
	cur := p
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(append([]string{resolved}, rest...)...), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = parent
	}
}
