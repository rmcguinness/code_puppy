// Package workers reads workers: scheduled workflows a workspace defines in
// workers/<name>/WORKER.md (ROADMAP item 24). The frontmatter says when the
// worker runs and what it may do unattended; the body is the workflow the
// agent is given.
package workers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// FileName is the file that defines a worker, in its own directory.
const FileName = "WORKER.md"

// Limits bound one run. Every run has them: the host policy fills in what
// a worker leaves out.
type Limits struct {
	MaxTurns   int           `yaml:"max_turns"`
	MaxCostUSD float64       `yaml:"max_cost_usd"`
	Timeout    time.Duration `yaml:"-"`
	TimeoutRaw string        `yaml:"timeout"`
}

// frontmatter is WORKER.md's YAML.
type frontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Schedule    string   `yaml:"schedule"`
	Timezone    string   `yaml:"timezone"`
	Agent       string   `yaml:"agent"`
	Model       string   `yaml:"model"`
	Permissions []string `yaml:"permissions"`
	Limits      Limits   `yaml:"limits"`
	Overlap     string   `yaml:"overlap"`
	CatchUp     string   `yaml:"catch_up"`
}

// Worker is a worker as read from its directory.
type Worker struct {
	Name        string
	Description string
	// Dir is the worker's directory; Path its WORKER.md.
	Dir, Path string
	// Hash identifies the worker's files: enabling pins it, so any change
	// needs re-enabling. Format "sha256:<hex>", as for skills.
	Hash     string
	Schedule Schedule
	Agent    string
	Model    string
	// Permissions are what the worker asks to do without approval.
	Permissions []Permission
	Limits      Limits
	// Overlap is "skip" (a run still going when the next is due skips it).
	Overlap string
	// CatchUp is "none" or "once" (run once for runs missed while the
	// service wasn't running).
	CatchUp string
	// Prompt is the workflow: WORKER.md's body.
	Prompt string
}

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Load reads the worker in dir (a directory holding WORKER.md). The
// directory's name is the worker's name; a name in the frontmatter must
// match it.
func Load(dir string) (*Worker, error) {
	path := filepath.Join(dir, FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fm, body, err := split(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var f frontmatter
	dec := yaml.NewDecoder(bytes.NewReader(fm))
	dec.KnownFields(true) // a misspelled key would silently change behaviour
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	w := &Worker{
		Name: filepath.Base(dir), Description: strings.TrimSpace(f.Description), Dir: dir, Path: path,
		Agent: f.Agent, Model: f.Model, Limits: f.Limits, Overlap: f.Overlap, CatchUp: f.CatchUp,
		Prompt: strings.TrimSpace(string(body)),
	}
	var problems []string
	if !validName.MatchString(w.Name) {
		problems = append(problems, fmt.Sprintf("directory name %q isn't a worker name (lowercase letters, digits, - and _)", w.Name))
	}
	if f.Name != "" && f.Name != w.Name {
		problems = append(problems, fmt.Sprintf("name %q doesn't match the directory %q", f.Name, w.Name))
	}
	if w.Prompt == "" {
		problems = append(problems, "the workflow (the text after the frontmatter) is empty")
	}
	if w.Schedule, err = ParseSchedule(f.Schedule, f.Timezone); err != nil {
		problems = append(problems, err.Error())
	}
	for _, p := range f.Permissions {
		perm, err := ParsePermission(p)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		w.Permissions = append(w.Permissions, perm)
	}
	if w.Limits.TimeoutRaw != "" {
		if w.Limits.Timeout, err = time.ParseDuration(w.Limits.TimeoutRaw); err != nil || w.Limits.Timeout <= 0 {
			problems = append(problems, fmt.Sprintf("limits.timeout %q isn't a duration like 20m", w.Limits.TimeoutRaw))
		}
	}
	if w.Limits.MaxTurns < 0 || w.Limits.MaxCostUSD < 0 {
		problems = append(problems, "limits can't be negative")
	}
	switch w.Overlap {
	case "":
		w.Overlap = "skip"
	case "skip":
	default:
		problems = append(problems, fmt.Sprintf("overlap %q: only \"skip\" is supported", w.Overlap))
	}
	switch w.CatchUp {
	case "":
		w.CatchUp = "none"
	case "none", "once":
	default:
		problems = append(problems, fmt.Sprintf("catch_up %q: use \"none\" or \"once\"", w.CatchUp))
	}
	if w.Hash, err = hashDir(dir); err != nil {
		problems = append(problems, "hashing: "+err.Error())
	}
	if len(problems) > 0 {
		return w, &InvalidError{Path: path, Problems: problems}
	}
	return w, nil
}

// InvalidError lists what makes a WORKER.md unusable. Load still returns
// what it could read, so a listing can show the worker and why.
type InvalidError struct {
	Path     string
	Problems []string
}

func (e *InvalidError) Error() string {
	return fmt.Sprintf("%s: %s", e.Path, strings.Join(e.Problems, "; "))
}

// split separates the frontmatter between "---" lines from the body.
func split(data []byte) (fm, body []byte, err error) {
	s := bytes.TrimLeft(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}), " \t\r\n") // a byte-order mark, then blank lines
	if !bytes.HasPrefix(s, []byte("---")) {
		return nil, nil, errors.New("missing frontmatter: start with a line of ---")
	}
	s = s[3:]
	end := bytes.Index(s, []byte("\n---"))
	if end < 0 {
		return nil, nil, errors.New("missing the --- line that ends the frontmatter")
	}
	body = s[end+4:]
	if i := bytes.IndexByte(body, '\n'); i >= 0 {
		body = body[i+1:]
	} else {
		body = nil
	}
	return s[:end], body, nil
}

// maxHashedBytes bounds how much of a worker's directory is hashed.
const maxHashedBytes = 64 << 20

// hashDir is the SHA-256 of every regular file under dir, in path order,
// each as its relative path, size and content (as skills' ContentHash).
// Symbolic links are skipped: they could point anywhere.
func hashDir(dir string) (string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	h := sha256.New()
	var total int64
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		if total += info.Size(); total > maxHashedBytes {
			return "", fmt.Errorf("worker directory is larger than %d MB", maxHashedBytes>>20)
		}
		f, err := os.Open(p)
		if err != nil {
			return "", err
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), info.Size())
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return "", err
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Found is a worker found by Discover, with why it can't be used if it
// can't.
type Found struct {
	Worker *Worker
	Err    error
}

// Discover loads every worker under the directories given (each holding
// <name>/WORKER.md), invalid ones included with their errors. Directories
// without a WORKER.md are ignored; a root that can't be read is an error.
func Discover(dirs ...string) ([]Found, error) {
	var out []Found
	var errs []error
	for _, root := range dirs {
		entries, err := os.ReadDir(root)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(root, e.Name())
			if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
				continue
			}
			w, err := Load(dir)
			if w != nil {
				out = append(out, Found{Worker: w, Err: err})
			} else {
				errs = append(errs, err)
			}
		}
	}
	return out, errors.Join(errs...)
}
