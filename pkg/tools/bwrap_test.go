package tools

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBwrapArgs(t *testing.T) {
	ws := t.TempDir()
	ro := filepath.Join(ws, "vendor-ro")
	os.Mkdir(ro, 0o755)
	spec := OSSandboxSpec{WritableDirs: []string{ws, "/definitely/missing"}, ReadOnlyDirs: []string{ro}}

	args := bwrapArgs("bwrap", spec, []string{ws + "/.env"}, []string{ws + "/secrets"})
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"bwrap --die-with-parent --ro-bind / / --dev-bind /dev /dev",
		"--bind " + ws + " " + ws,
		"--ro-bind " + ro + " " + ro,
		"--ro-bind /dev/null " + ws + "/.env",
		"--tmpfs " + ws + "/secrets --remount-ro " + ws + "/secrets",
		"--unshare-net",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "/definitely/missing") {
		t.Error("missing writable dir should be skipped (bwrap fails on it)")
	}
	if args[len(args)-1] != "--" {
		t.Error("args must end with --")
	}
	// Order: read-only and masks come after writable binds so they win.
	if slices.Index(args, "--bind") > slices.Index(args, ro) {
		t.Error("read-only bind must follow writable binds")
	}
	spec.AllowNetwork = true
	if strings.Contains(strings.Join(bwrapArgs("bwrap", spec, nil, nil), " "), "--unshare-net") {
		t.Error("network should be shared when allowed")
	}
}

func TestExpandBlocked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, ".env"), "x")
	writeFile(t, filepath.Join(ws, "deep", "a", "server.pem"), "x")
	writeFile(t, filepath.Join(ws, "node_modules", "pkg", ".env"), "x") // skipped dir
	writeFile(t, filepath.Join(ws, "ok.txt"), "x")
	writeFile(t, filepath.Join(home, ".ssh", "id_ed25519"), "x")

	m, err := NewPathMatcher([]string{".env", "*.pem", "~/.ssh"}, []string{ws})
	if err != nil {
		t.Fatal(err)
	}
	files, dirs := expandBlocked(OSSandboxSpec{WritableDirs: []string{ws}, Blocked: m})
	joined := strings.Join(append(files, dirs...), ",")
	for _, want := range []string{filepath.Join(ws, ".env"), filepath.Join(ws, "deep", "a", "server.pem"), filepath.Join(home, ".ssh")} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected %s blocked; got %s", want, joined)
		}
	}
	if strings.Contains(joined, "ok.txt") || strings.Contains(joined, "node_modules") {
		t.Errorf("unexpected entries: %s", joined)
	}
	if !slices.Contains(dirs, filepath.Join(home, ".ssh")) {
		t.Error("~/.ssh should be masked as a directory")
	}
	if f, d := expandBlocked(OSSandboxSpec{WritableDirs: []string{ws}}); len(f)+len(d) != 0 {
		t.Error("no matcher -> nothing blocked")
	}
}
