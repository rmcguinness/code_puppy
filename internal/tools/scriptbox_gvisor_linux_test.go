//go:build linux

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gvisor.dev/gvisor/sandboxexec/sandbox"
)

func gvisorBoxForTest(t *testing.T) *gvisorBox {
	t.Helper()
	if _, err := findRunsc(); err != nil {
		t.Skipf("gVisor tests need runsc: %v", err)
	}
	box, _, err := NewScriptBox(ScriptBoxConfig{Mode: "gvisor", StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("runsc is installed but gVisor doesn't work: %v", err)
	}
	return box.(*gvisorBox)
}

func runscList(t *testing.T, b *gvisorBox) []string {
	t.Helper()
	out, err := exec.Command(b.runsc, "--root", b.stateDir, "list", "-quiet").Output()
	if err != nil {
		t.Fatalf("runsc list: %v", err)
	}
	return strings.Fields(string(out))
}

func TestGVisorScriptBox(t *testing.T) {
	b := gvisorBoxForTest(t)
	testScriptBoxBehaviour(t, b)
	d := newScriptDirs(t)

	// Only what is mounted exists: not the outside directory, not $HOME.
	home, _ := os.UserHomeDir()
	for _, p := range []string{d.outside, home} {
		if res, out, _ := runScript(t, b, d, "ls "+p, 0); res.ExitCode == 0 {
			t.Errorf("%s exists inside the sandbox:\n%s", p, out)
		}
	}
	if _, out, _ := runScript(t, b, d, "uname -r", 0); !strings.Contains(out, "gvisor") {
		t.Errorf("not running under gVisor: %s", out)
	}
	// Every run cleans up after itself, including timeouts and cancellations
	// (run above by testScriptBoxBehaviour).
	if left := runscList(t, b); len(left) != 0 {
		t.Fatalf("sandboxes left behind: %v", left)
	}
	if entries, _ := os.ReadDir(b.bundles); len(entries) != 0 {
		t.Fatalf("bundles left behind: %d", len(entries))
	}
}

// A sandbox whose Code Puppy died (so its Close never ran) is removed the
// next time a box is set up.
func TestGVisorSweepsOrphans(t *testing.T) {
	b := gvisorBoxForTest(t)
	dead := exec.Command("/bin/true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	orphan := "cp-" + strconv.Itoa(dead.Process.Pid) + "-1"
	live := "cp-" + strconv.Itoa(os.Getpid()) + "-999"
	os.Setenv(sandbox.RunscPathEnvVar, b.runsc)
	for _, id := range []string{orphan, live} {
		if _, err := sandbox.New(context.Background(), sandbox.WithID(id), sandbox.WithStateDir(b.stateDir), sandbox.WithRuntimeDir(b.bundles)); err != nil {
			t.Fatal(err)
		}
	}
	if got := runscList(t, b); len(got) != 2 {
		t.Fatalf("before: %v", got)
	}
	b.sweep(context.Background())
	got := runscList(t, b)
	if len(got) != 1 || got[0] != live {
		t.Fatalf("after sweep: %v (want only %s)", got, live)
	}
	if _, err := os.Stat(filepath.Join(b.bundles, orphan)); err == nil {
		t.Fatal("orphan's bundle left behind")
	}
	exec.Command(b.runsc, "--root", b.stateDir, "kill", live, "SIGKILL").Run()
	exec.Command(b.runsc, "--root", b.stateDir, "delete", "--force", live).Run()
}

// Blocked files and directories inside a mounted path are hidden.
func TestGVisorHidesBlockedPaths(t *testing.T) {
	if _, err := findRunsc(); err != nil {
		t.Skipf("gVisor tests need runsc: %v", err)
	}
	m, err := NewPathMatcher([]string{".env", "secrets"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	box, _, err := NewScriptBox(ScriptBoxConfig{Mode: "gvisor", StateDir: t.TempDir(), Blocked: m})
	if err != nil {
		t.Fatal(err)
	}
	d := newScriptDirs(t)
	os.WriteFile(filepath.Join(d.readOnly, ".env"), []byte("API_KEY=topsecret"), 0o600)
	os.MkdirAll(filepath.Join(d.readOnly, "secrets"), 0o700)
	os.WriteFile(filepath.Join(d.readOnly, "secrets", "key.pem"), []byte("PRIVATE"), 0o600)
	_, out, _ := runScript(t, box, d, "cat "+d.readOnly+"/.env; ls "+d.readOnly+"/secrets; cat "+d.readOnly+"/hello.txt", 0)
	if strings.Contains(out, "topsecret") || strings.Contains(out, "key.pem") || !strings.Contains(out, "hello") {
		t.Fatalf("blocked paths visible, or allowed file hidden:\n%s", out)
	}
}
