package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/config"
)

// PyEnvs manages isolated Python environments for skill scripts, one per
// distinct set of requirements, in ~/.code_puppy/envs/<key>. An environment
// is built inside the script sandbox (network on, writes only to the
// environment and the package cache), and is only used once its marker
// file is written, so an interrupted build is rebuilt rather than used.
type PyEnvs struct {
	dir        string
	index      string
	wheelsOnly bool

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// PyEnv describes one environment.
type PyEnv struct {
	Key        string    `json:"key"`
	Python     string    `json:"python"` // base interpreter (real path)
	Deps       []string  `json:"deps"`
	Index      string    `json:"index"`
	WheelsOnly bool      `json:"wheels_only"`
	Created    time.Time `json:"created"`
	LastUsed   time.Time `json:"last_used"`
	Skills     []string  `json:"skills"`
	Dir        string    `json:"-"`
	Ready      bool      `json:"-"` // built completely
	Size       int64     `json:"-"`
}

// Interpreter is the environment's Python.
func (e PyEnv) Interpreter() string { return filepath.Join(e.Dir, "bin", "python") }

const (
	pyEnvMarker     = ".code-puppy-env.json"
	pyEnvCacheDir   = ".cache"
	pyEnvBuildLimit = 10 * time.Minute
)

// NewPyEnvs manages environments in dir (default ~/.code_puppy/envs) under
// the package policy.
func NewPyEnvs(dir string, p config.PackagePolicy) *PyEnvs {
	if dir == "" {
		dir = config.ExpandHome("~/.code_puppy/envs")
	}
	index := p.Index
	if index == "" {
		index = "https://pypi.org/simple"
	}
	return &PyEnvs{dir: dir, index: index, wheelsOnly: p.WheelsOnly, locks: map[string]*sync.Mutex{}}
}

// SystemPython is the python3 on PATH, as its real path (so it names the
// installed version, and so the directories it lives in can be mounted).
func SystemPython() (string, error) {
	p, err := exec.LookPath("python3")
	if err != nil {
		return "", errors.New("python3 not found on PATH")
	}
	return filepath.EvalSymlinks(p)
}

// Key identifies the environment for deps under this manager's policy.
func (m *PyEnvs) Key(python string, deps []string) string {
	norm := make([]string, len(deps))
	for i, d := range deps {
		norm[i] = strings.Join(strings.Fields(d), " ")
	}
	sort.Strings(norm)
	b, _ := json.Marshal(struct {
		Python     string
		Deps       []string
		Index      string
		WheelsOnly bool
	}{python, slices.Compact(norm), m.index, m.wheelsOnly})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// Lookup returns the environment for deps and whether it is ready to use.
func (m *PyEnvs) Lookup(python string, deps []string) (PyEnv, bool) {
	key := m.Key(python, deps)
	e, err := m.read(key)
	if err != nil {
		return PyEnv{Key: key, Dir: filepath.Join(m.dir, key), Python: python, Deps: deps, Index: m.index, WheelsOnly: m.wheelsOnly}, false
	}
	return e, true
}

// InstallCommands describes how Ensure installs deps, for the approval
// prompt.
func (m *PyEnvs) InstallCommands(deps []string) string {
	only := ""
	if m.wheelsOnly {
		only = " --only-binary :all:"
	}
	return fmt.Sprintf("pip install --index-url %s%s -- %s", m.index, only, strings.Join(deps, " "))
}

// Ensure returns the environment for deps, building it in box first if
// needed (the caller has approved the install). skill is recorded as a
// user of the environment.
func (m *PyEnvs) Ensure(ctx context.Context, box ScriptBox, python, skill string, deps []string) (PyEnv, error) {
	key := m.Key(python, deps)
	lock := m.lock(key)
	lock.Lock()
	defer lock.Unlock()

	if e, err := m.read(key); err == nil {
		return m.touch(e, skill), nil
	}
	e := PyEnv{Key: key, Dir: filepath.Join(m.dir, key), Python: python, Deps: slices.Clone(deps), Index: m.index, WheelsOnly: m.wheelsOnly, Created: time.Now()}
	// A directory without a marker is an interrupted build: start again.
	if err := os.RemoveAll(e.Dir); err != nil {
		return e, err
	}
	cache := filepath.Join(m.dir, pyEnvCacheDir)
	for _, d := range []string{e.Dir, cache} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return e, err
		}
	}
	if err := m.build(ctx, box, e, cache); err != nil {
		os.RemoveAll(e.Dir)
		return e, err
	}
	e.Ready = true
	return m.touch(e, skill), nil
}

// build creates the virtual environment and installs deps, inside box.
func (m *PyEnvs) build(ctx context.Context, box ScriptBox, e PyEnv, cache string) error {
	readOnly := MountsFor(e.Python)
	var steps [][]string
	env := []string{"PIP_DISABLE_PIP_VERSION_CHECK=1", "PIP_NO_INPUT=1", "PIP_CACHE_DIR=" + cache}
	if uv, err := findUV(); err == nil {
		readOnly = append(readOnly, filepath.Dir(uv))
		env = append(env, "UV_CACHE_DIR="+cache, "UV_NO_CONFIG=1", "UV_PYTHON_DOWNLOADS=never", "UV_LINK_MODE=copy")
		install := []string{uv, "pip", "install", "--quiet", "--python", e.Interpreter(), "--index-url", m.index}
		if m.wheelsOnly {
			install = append(install, "--only-binary", ":all:")
		}
		steps = [][]string{
			{uv, "venv", "--quiet", "--python", e.Python, e.Dir},
			append(append(install, "--"), e.Deps...),
		}
	} else {
		install := []string{e.Interpreter(), "-m", "pip", "install", "--quiet", "--index-url", m.index}
		if m.wheelsOnly {
			install = append(install, "--only-binary", ":all:")
		}
		steps = [][]string{
			{e.Python, "-m", "venv", e.Dir},
			append(install, e.Deps...), // validated as plain requirements by the skills policy
		}
	}
	ctx, cancel := context.WithTimeout(ctx, pyEnvBuildLimit)
	defer cancel()
	for _, argv := range steps {
		var out bytes.Buffer
		res, err := box.Run(ctx, ScriptRequest{
			Argv: argv, Dir: e.Dir, Env: env, Network: true,
			ReadOnly: readOnly, Writable: []string{e.Dir, cache},
			Stdout: &out, Stderr: &out,
		})
		switch {
		case err != nil:
			return fmt.Errorf("%s: %w", filepath.Base(argv[0]), err)
		case res.TimedOut:
			return fmt.Errorf("installing packages took longer than %s", pyEnvBuildLimit)
		case res.ExitCode != 0:
			return fmt.Errorf("%s failed (exit %d): %s", filepath.Base(argv[0]), res.ExitCode, lastLines(out.String(), 12))
		}
	}
	e.Created = time.Now()
	return m.writeMarker(e)
}

// MountsFor returns the directories an interpreter needs mounted, beyond
// the system directories every sandbox gets: its installation prefix
// when it lives elsewhere (e.g. /opt/python3.13).
func MountsFor(python string) []string {
	prefix := filepath.Dir(filepath.Dir(python)) // …/bin/python3 -> …
	for _, sys := range []string{"/usr", "/bin", "/lib", "/lib64"} {
		if prefix == sys || strings.HasPrefix(prefix, sys+"/") {
			return nil
		}
	}
	return []string{prefix}
}

// findUV looks for uv on PATH, then in ~/.code_puppy/bin.
func findUV() (string, error) {
	if p, err := exec.LookPath("uv"); err == nil {
		return filepath.EvalSymlinks(p)
	}
	p := config.ExpandHome("~/.code_puppy/bin/uv")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	return "", errors.New("uv not found")
}

// List returns every environment, complete or not, newest use first.
func (m *PyEnvs) List() []PyEnv {
	entries, _ := os.ReadDir(m.dir)
	var out []PyEnv
	for _, ent := range entries {
		if !ent.IsDir() || strings.HasPrefix(ent.Name(), ".") {
			continue
		}
		e, err := m.read(ent.Name())
		if err != nil {
			e = PyEnv{Key: ent.Name(), Dir: filepath.Join(m.dir, ent.Name())}
		}
		e.Size = dirSize(e.Dir)
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastUsed.After(out[j].LastUsed) })
	return out
}

// Remove deletes an environment.
func (m *PyEnvs) Remove(key string) error {
	if key == "" || strings.ContainsAny(key, `/\.`) {
		return fmt.Errorf("invalid environment %q", key)
	}
	lock := m.lock(key)
	lock.Lock()
	defer lock.Unlock()
	return os.RemoveAll(filepath.Join(m.dir, key))
}

func (m *PyEnvs) lock(key string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks[key] == nil {
		m.locks[key] = &sync.Mutex{}
	}
	return m.locks[key]
}

func (m *PyEnvs) read(key string) (PyEnv, error) {
	var e PyEnv
	b, err := os.ReadFile(filepath.Join(m.dir, key, pyEnvMarker))
	if err != nil {
		return e, err
	}
	if err := json.Unmarshal(b, &e); err != nil {
		return e, err
	}
	e.Dir, e.Ready = filepath.Join(m.dir, key), true
	return e, nil
}

// touch records a use of e by skill.
func (m *PyEnvs) touch(e PyEnv, skill string) PyEnv {
	e.LastUsed = time.Now()
	if skill != "" && !slices.Contains(e.Skills, skill) {
		e.Skills = append(e.Skills, skill)
	}
	_ = m.writeMarker(e)
	return e
}

func (m *PyEnvs) writeMarker(e PyEnv) error {
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(e.Dir, pyEnvMarker+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(e.Dir, pyEnvMarker))
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
