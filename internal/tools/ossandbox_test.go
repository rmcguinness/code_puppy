package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
)

func TestSeatbeltProfile(t *testing.T) {
	m, _ := NewPathMatcher([]string{".env"}, nil)
	profile := seatbeltProfile(OSSandboxSpec{WritableDirs: []string{`/ws/with "quote"`}, ReadOnlyDirs: []string{"/ws/ro"}, Blocked: m})
	for _, want := range []string{
		"(allow default)",
		"(deny file-write*)",
		`(subpath "/ws/with \"quote\"")`,
		`(regex #"/\.env(/.*)?$")`,
		"(deny network*)",
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing %q:\n%s", want, profile)
		}
	}
	// Read-only and blocked rules must come after the writable allowances so they win.
	allowAt := strings.Index(profile, "(allow file-write*")
	if strings.Index(profile, "(deny file-read*") < allowAt || strings.Index(profile, "(subpath \"/ws/ro\")") < allowAt {
		t.Error("read-only and blocked rules must follow writable allowances")
	}
	if strings.Contains(seatbeltProfile(OSSandboxSpec{AllowNetwork: true}), "deny network") {
		t.Error("network should not be denied when allowed")
	}
}

func TestNewOSSandboxModes(t *testing.T) {
	orig := platformSandbox
	t.Cleanup(func() { platformSandbox = orig })

	if _, err := NewOSSandbox(OSSandboxSpec{Mode: "sometimes"}); err == nil {
		t.Error("expected error for invalid mode")
	}

	off, _ := NewOSSandbox(OSSandboxSpec{Mode: SandboxOff})
	if off.Active() || !strings.Contains(off.Status(), "disabled") {
		t.Errorf("off mode: %s", off.Status())
	}
	if got := off.wrap([]string{"ls"}); len(got) != 1 {
		t.Errorf("inactive sandbox must not wrap: %v", got)
	}

	platformSandbox = func(OSSandboxSpec) (sandboxWrapper, error) { return nil, errors.New("no sandbox here") }
	// Auto degrades gracefully and says why.
	auto, err := NewOSSandbox(OSSandboxSpec{Mode: SandboxAuto})
	if err != nil || auto.Active() || !strings.Contains(auto.Status(), "no sandbox here") {
		t.Errorf("auto without platform support: %v %s", err, auto.Status())
	}
	// Required fails closed.
	if _, err := NewOSSandbox(OSSandboxSpec{Mode: SandboxRequired}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Errorf("required without platform support should fail, got %v", err)
	}

	platformSandbox = func(OSSandboxSpec) (sandboxWrapper, error) { return prefixWrapper("sbx", "--"), nil }
	on, _ := NewOSSandbox(OSSandboxSpec{Mode: SandboxRequired})
	if got := strings.Join(on.wrap([]string{"ls", "-l"}), " "); got != "sbx -- ls -l" {
		t.Errorf("wrap = %q", got)
	}
}

func TestRegistryFailsClosedWhenSandboxRequired(t *testing.T) {
	orig := platformSandbox
	t.Cleanup(func() { platformSandbox = orig })
	platformSandbox = func(OSSandboxSpec) (sandboxWrapper, error) { return nil, errors.New("unsupported") }

	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = t.TempDir()
	cfg.Sandbox.Shell = "required"
	if _, err := NewRegistry(cfg, nil, nil); err == nil {
		t.Fatal("expected registry to refuse to start without the required sandbox")
	}

	// Negative config: bad pattern / missing allowed path are startup errors.
	cfg.Sandbox.Shell = "off"
	cfg.Sandbox.BlockedPaths = []string{`x"y`}
	if _, err := NewRegistry(cfg, nil, nil); err == nil {
		t.Error("expected error for invalid blocked pattern")
	}
	cfg.Sandbox.BlockedPaths = nil
	cfg.Sandbox.AllowedPaths = []string{filepath.Join(t.TempDir(), "nope")}
	if _, err := NewRegistry(cfg, nil, nil); err == nil {
		t.Error("expected error for missing allowed path")
	}
}

// TestOSSandboxEnforcement runs real commands under the platform sandbox
// (Seatbelt on macOS, bubblewrap on Linux) and is skipped where none works.
func TestOSSandboxEnforcement(t *testing.T) {
	f := newSandboxFixture(t)
	// The fixtures live under $TMPDIR, so the default temp allowances are left
	// out here; otherwise every fixture directory would be writable.
	osb, err := NewOSSandbox(OSSandboxSpec{
		Mode: SandboxRequired, WritableDirs: f.ws.WritableDirs(), ReadOnlyDirs: f.ws.ReadOnlyDirs(),
		Blocked: f.ws.Blocked(), AllowNetwork: true,
	})
	if err != nil {
		t.Skipf("no usable OS sandbox on %s: %v", runtime.GOOS, err)
	}
	cfg := ShellConfig{Workspace: f.ws, Hooks: allowAll(), Exec: &ExecEnv{Sandbox: osb}}
	run := func(cmd string) RunShellCommandOutput {
		return runShellCommand(context.Background(), cfg, RunShellCommandInput{Command: cmd})
	}

	// Positive: writes inside the workspace and allowed roots work.
	if out := run("echo ok > inside.txt && echo ok > " + filepath.Join(f.shared, "s.txt")); out.ExitCode != 0 {
		t.Errorf("allowed writes failed: %+v", out)
	}
	// Negative: writes to read-only roots and outside all roots are denied.
	for _, target := range []string{filepath.Join(f.docs, "x.md"), filepath.Join(f.outside, "x.txt"), filepath.Join(f.work, "vendor-ro", "../vendor-ro/x.go")} {
		if out := run("echo pwned > '" + target + "'"); out.ExitCode == 0 {
			t.Errorf("sandbox allowed write to %s", target)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(f.outside, "x.txt")); string(b) != "outside\n" {
		t.Error("file outside the sandbox was modified")
	}
	// Negative: blocked files can't be read by the shell, even inside the
	// workspace. (Seatbelt denies the read; bubblewrap shows an empty file.)
	if out := run("cat .env"); strings.Contains(out.Output, "API_KEY") {
		t.Errorf("shell read a blocked file: %+v", out)
	}
	run("cp .env leaked.txt")
	if b, _ := os.ReadFile(filepath.Join(f.work, "leaked.txt")); strings.Contains(string(b), "API_KEY") {
		t.Error("shell copied a blocked file's contents")
	}
	// A read-only root nested in the writable workspace stays read-only.
	if out := run("echo pwned > vendor-ro/lib.go"); out.ExitCode == 0 {
		t.Error("shell wrote into a read-only root nested in the workspace")
	}
	// Reads elsewhere still work.
	if out := run("cat main.go"); !strings.Contains(out.Output, "package main") {
		t.Errorf("normal read failed: %+v", out)
	}
}

// TestOSSandboxIsolationExtras covers home-directory secrets, symlinks to
// blocked files, system paths, and network denial under the real platform
// sandbox; skipped where none is usable.
func TestOSSandboxIsolationExtras(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, ".ssh", "id_ed25519"), "PRIVATE-KEY-MATERIAL")
	ws, dir := newTestWorkspace(t)
	writeFile(t, filepath.Join(dir, ".env"), "API_KEY=leak")
	if err := os.Symlink(".env", filepath.Join(dir, "innocent.txt")); err != nil {
		t.Fatal(err)
	}
	blocked, err := NewPathMatcher([]string{".env", "~/.ssh"}, ws.RootDirs())
	if err != nil {
		t.Fatal(err)
	}
	osb, err := NewOSSandbox(OSSandboxSpec{Mode: SandboxRequired, WritableDirs: ws.WritableDirs(), Blocked: blocked, AllowNetwork: false})
	if err != nil {
		t.Skipf("no usable OS sandbox on %s: %v", runtime.GOOS, err)
	}
	cfg := ShellConfig{Workspace: ws, Hooks: allowAll(), Exec: &ExecEnv{Sandbox: osb}}
	run := func(cmd string) RunShellCommandOutput {
		return runShellCommand(context.Background(), cfg, RunShellCommandInput{Command: cmd, TimeoutSeconds: 15})
	}

	if out := run("cat \"$HOME/.ssh/id_ed25519\"; ls -A \"$HOME/.ssh\""); strings.Contains(out.Output, "PRIVATE-KEY") || strings.Contains(out.Output, "id_ed25519\n") {
		t.Errorf("home secret visible: %q", out.Output)
	}
	if out := run("cat innocent.txt"); strings.Contains(out.Output, "leak") {
		t.Errorf("symlink bypassed blocked path: %q", out.Output)
	}
	if out := run("echo x >> .env"); out.ExitCode == 0 {
		t.Error("blocked file was writable")
	}
	if out := run("echo x > /etc/code-puppy-sandbox-test"); out.ExitCode == 0 {
		t.Error("system path was writable")
	}
	if out := run("echo x > \"$HOME/outside.txt\""); out.ExitCode == 0 {
		t.Error("home directory outside the workspace was writable")
	}
	// Network denied: a raw TCP connect must fail (never succeeds offline either).
	if out := run("timeout 5 bash -c 'echo > /dev/tcp/1.1.1.1/53' && echo CONNECTED"); strings.Contains(out.Output, "CONNECTED") {
		t.Error("network reachable with allow_network = false")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".env")); string(b) != "API_KEY=leak" {
		t.Error("blocked file was modified")
	}
}
