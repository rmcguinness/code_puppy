package tools

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
)

func TestPyEnvKey(t *testing.T) {
	m := NewPyEnvs(t.TempDir(), config.PackagePolicy{Index: "https://pypi.org/simple", WheelsOnly: true})
	a := m.Key("/usr/bin/python3.12", []string{"requests>=2", "rich==13.7.1"})
	if b := m.Key("/usr/bin/python3.12", []string{"rich==13.7.1", "requests>=2", "requests>=2"}); a != b {
		t.Error("key depends on order or duplicates")
	}
	if m.Key("/usr/bin/python3.13", []string{"requests>=2", "rich==13.7.1"}) == a {
		t.Error("key ignores the interpreter")
	}
	if NewPyEnvs(t.TempDir(), config.PackagePolicy{WheelsOnly: false}).Key("/usr/bin/python3.12", []string{"requests>=2", "rich==13.7.1"}) == a {
		t.Error("key ignores wheels_only")
	}
	if e, ok := m.Lookup("/usr/bin/python3.12", []string{"x"}); ok || e.Key == "" || !strings.HasPrefix(e.Dir, m.dir) {
		t.Errorf("lookup of a missing env: %+v %v", e, ok)
	}
	if err := m.Remove("../escape"); err == nil {
		t.Error("remove accepted a path")
	}
}

func TestMountsFor(t *testing.T) {
	for py, want := range map[string]string{
		"/usr/bin/python3.12":                                        "",
		"/usr/local/bin/python3.13":                                  "",
		"/opt/python3.13/bin/python3.13":                             "/opt/python3.13",
		"/home/u/.local/share/uv/python/cpython-3.13/bin/python3.13": "/home/u/.local/share/uv/python/cpython-3.13",
	} {
		got := strings.Join(MountsFor(py), ",")
		if got != want {
			t.Errorf("%s: %q, want %q", py, got, want)
		}
	}
}

// Builds a real environment from PyPI inside the script sandbox, then uses
// it with the network off. Needs the network: CODE_PUPPY_PYENV_TESTS=1.
func TestPyEnvBuildAndUse(t *testing.T) {
	if os.Getenv("CODE_PUPPY_PYENV_TESTS") != "1" {
		t.Skip("set CODE_PUPPY_PYENV_TESTS=1 to build a real environment (needs the network)")
	}
	box, _, err := NewScriptBox(ScriptBoxConfig{Mode: os.Getenv("CODE_PUPPY_PYENV_SANDBOX"), StateDir: t.TempDir()})
	if err != nil {
		t.Skipf("no script sandbox: %v", err)
	}
	python, err := SystemPython()
	if err != nil {
		t.Skip(err)
	}
	m := NewPyEnvs(filepath.Join(t.TempDir(), "envs"), config.PackagePolicy{Index: "https://pypi.org/simple", WheelsOnly: true})
	deps := []string{"six==1.16.0"}
	e, err := m.Ensure(context.Background(), box, python, "demo", deps)
	if err != nil {
		t.Fatalf("build with %s: %v", box.Name(), err)
	}
	if again, ok := m.Lookup(python, deps); !ok || again.Key != e.Key || again.Skills[0] != "demo" {
		t.Fatalf("lookup after build: %+v %v", again, ok)
	}
	var out bytes.Buffer
	res, err := box.Run(context.Background(), ScriptRequest{
		Argv: []string{e.Interpreter(), "-c", "import six; print('six', six.__version__)"},
		Dir:  e.Dir, ReadOnly: append([]string{e.Dir}, MountsFor(python)...), Stdout: &out, Stderr: &out,
	})
	if err != nil || res.ExitCode != 0 || !strings.Contains(out.String(), "six 1.16.0") {
		t.Fatalf("using the env (network off): %+v %v\n%s", res, err, out.String())
	}
	// A build that fails leaves nothing behind.
	if _, err := m.Ensure(context.Background(), box, python, "demo", []string{"no-such-package-code-puppy-test==9.9.9"}); err == nil {
		t.Fatal("impossible requirement installed")
	}
	if n := len(m.List()); n != 1 {
		t.Fatalf("%d environments after a failed build, want 1", n)
	}
	t.Logf("sandbox %s, env %s, %d KB", box.Name(), e.Key, m.List()[0].Size/1024)
}
