package tools

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// scriptDirs are host directories for a script: one it may write, one it
// may only read, and one outside everything it was given.
type scriptDirs struct{ writable, readOnly, outside string }

func newScriptDirs(t *testing.T) scriptDirs {
	t.Helper()
	d := scriptDirs{writable: t.TempDir(), readOnly: t.TempDir(), outside: t.TempDir()}
	for _, p := range []string{d.writable, d.readOnly, d.outside} {
		// Canonical paths: on macOS the temp dir is behind a symlink.
		c, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(c, "hello.txt"), []byte("hello"), 0o644)
		switch p {
		case d.writable:
			d.writable = c
		case d.readOnly:
			d.readOnly = c
		default:
			d.outside = c
		}
	}
	return d
}

func runScript(t *testing.T, box ScriptBox, d scriptDirs, script string, timeout time.Duration, env ...string) (ScriptResult, string, error) {
	t.Helper()
	var out bytes.Buffer
	res, err := box.Run(context.Background(), ScriptRequest{
		Argv: []string{"/bin/sh", "-c", script}, Dir: d.writable, Env: env,
		ReadOnly: []string{d.readOnly}, Writable: []string{d.writable},
		Stdout: &out, Stderr: &out, Timeout: timeout,
	})
	return res, out.String(), err
}

// testScriptBoxBehaviour checks what every backend must guarantee.
func testScriptBoxBehaviour(t *testing.T, box ScriptBox) {
	d := newScriptDirs(t)
	t.Setenv("CP_TEST_SECRET", "leaked")

	res, out, err := runScript(t, box, d, "cat "+d.readOnly+"/hello.txt && echo made > "+d.writable+"/out.txt && pwd", 0)
	if err != nil || res.ExitCode != 0 || !strings.Contains(out, "hello") || !strings.Contains(out, d.writable) {
		t.Fatalf("allowed work: %+v %v\n%s", res, err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(d.writable, "out.txt")); string(b) != "made\n" {
		t.Fatalf("write to the writable dir didn't reach the host: %q", b)
	}
	for _, target := range []string{d.readOnly + "/x.txt", d.outside + "/x.txt"} {
		if res, out, _ := runScript(t, box, d, "echo pwned > "+target, 0); res.ExitCode == 0 {
			t.Errorf("wrote %s: %s", target, out)
		}
		if _, err := os.Stat(target); err == nil {
			t.Errorf("%s exists on the host", target)
		}
	}

	res, out, _ = runScript(t, box, d, `echo "secret=[$CP_TEST_SECRET] given=[$GIVEN] home=[$HOME]"; echo tmp > "$TMPDIR/t" && cat "$TMPDIR/t"`, 0, "GIVEN=yes")
	if !strings.Contains(out, "secret=[]") || !strings.Contains(out, "given=[yes]") || strings.Contains(out, "home=[]") || !strings.Contains(out, "\ntmp") {
		t.Errorf("environment: %+v\n%s", res, out)
	}

	start := time.Now()
	res, _, err = runScript(t, box, d, "sleep 30", 500*time.Millisecond)
	if err != nil || !res.TimedOut || res.ExitCode == 0 || time.Since(start) > 20*time.Second {
		t.Errorf("timeout: %+v %v after %v", res, err, time.Since(start))
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(500 * time.Millisecond); cancel() }()
	_, err = box.Run(ctx, ScriptRequest{Argv: []string{"/bin/sh", "-c", "sleep 30"}, Dir: d.writable, Writable: []string{d.writable}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: %v", err)
	}
}

func TestOSScriptBox(t *testing.T) {
	box, _, err := NewScriptBox(ScriptBoxConfig{Mode: "os"})
	if err != nil {
		t.Skipf("no OS sandbox on %s: %v", runtime.GOOS, err)
	}
	if box.Name() != "os" {
		t.Fatalf("name %q", box.Name())
	}
	testScriptBoxBehaviour(t, box)
}

func TestScriptBoxSelection(t *testing.T) {
	if _, _, err := NewScriptBox(ScriptBoxConfig{Mode: "docker"}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("unknown mode: %v", err)
	}
	if runtime.GOOS != "linux" {
		if _, _, err := NewScriptBox(ScriptBoxConfig{Mode: "gvisor"}); err == nil || !strings.Contains(err.Error(), "only on Linux") {
			t.Errorf("gvisor off Linux: %v", err)
		}
	}
	// Without any sandbox, scripts don't run: there is no unsandboxed fallback.
	orig := platformSandboxProbe
	platformSandboxProbe = func() error { return errors.New("no sandbox here") }
	defer func() { platformSandboxProbe = orig }()
	t.Setenv("RUNSC_PATH", filepath.Join(t.TempDir(), "missing-runsc"))
	if _, note, err := NewScriptBox(ScriptBoxConfig{Mode: "auto", StateDir: t.TempDir()}); !errors.Is(err, ErrNoScriptBox) || note == "" {
		t.Errorf("auto with nothing available: %q %v", note, err)
	}
}
