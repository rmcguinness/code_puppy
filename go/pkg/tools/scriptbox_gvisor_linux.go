//go:build linux

package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/config"
	"gvisor.dev/gvisor/sandboxexec/sandbox"
)

// gvisorBox runs each command in its own gVisor sandbox (rootless runsc,
// driven through gvisor.dev/gvisor/sandboxexec). The sandbox's root is an
// empty directory; the host's binaries and libraries, /etc, and the
// request's paths are mounted in, and /tmp is a private tmpfs. Home
// directories and anything else not mounted don't exist inside.
type gvisorBox struct {
	blocked  *PathMatcher // hidden inside mounted paths (e.g. the workspace's .env)
	empty    string       // an empty read-only file, mounted over blocked files
	runsc    string
	stateDir string // runsc --root: container state, shared by this user's sandboxes
	bundles  string // OCI bundles, one per sandbox, removed by Close
	seq      atomic.Int64
}

// gvisorCloseTimeout bounds the cleanup after a run, which uses its own
// context so a cancelled turn still tears its sandbox down.
const gvisorCloseTimeout = 15 * time.Second

// gvisorProbes caches, per runsc binary, whether a test run worked.
var (
	gvisorProbeMu sync.Mutex
	gvisorProbes  = map[string]error{}
)

func newGVisorBox(cfg ScriptBoxConfig) (ScriptBox, error) {
	runsc, err := findRunsc()
	if err != nil {
		return nil, err
	}
	dir := cfg.StateDir
	if dir == "" {
		dir = config.ExpandHome("~/.code_puppy/sandboxes")
	}
	b := &gvisorBox{blocked: cfg.Blocked, runsc: runsc, stateDir: filepath.Join(dir, "state"), bundles: filepath.Join(dir, "bundles"), empty: filepath.Join(dir, "empty")}
	for _, d := range []string{b.stateDir, b.bundles} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(b.empty, nil, 0o400); err != nil && !os.IsPermission(err) {
		return nil, err
	}
	b.sweep(context.Background())
	gvisorProbeMu.Lock()
	defer gvisorProbeMu.Unlock()
	perr, done := gvisorProbes[runsc]
	if !done {
		perr = b.probe()
		gvisorProbes[runsc] = perr
	}
	if perr != nil {
		return nil, perr
	}
	return b, nil
}

// probe runs /bin/true once, to check that gVisor works here (it needs
// unprivileged user namespaces, which some systems disable).
func (b *gvisorBox) probe() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	res, err := b.Run(ctx, ScriptRequest{Argv: []string{"/bin/true"}, Dir: "/tmp", Stdout: &out, Stderr: &out})
	switch {
	case err != nil:
		return fmt.Errorf("test run failed: %w", err)
	case res.ExitCode != 0:
		return fmt.Errorf("test run exited %d: %s", res.ExitCode, strings.TrimSpace(out.String()))
	}
	return nil
}

// findRunsc looks for runsc in RUNSC_PATH, then PATH, then
// ~/.code_puppy/bin. It must sit beside its gvisor-bin directory, as the
// release tarball unpacks.
func findRunsc() (string, error) {
	if p := os.Getenv(sandbox.RunscPathEnvVar); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("RUNSC_PATH: %w", err)
		}
		return p, nil
	}
	if p, err := exec.LookPath("runsc"); err == nil {
		return p, nil
	}
	p := config.ExpandHome("~/.code_puppy/bin/runsc")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	return "", errors.New("runsc not found (set RUNSC_PATH, put it on PATH, or unpack gVisor into ~/.code_puppy/bin)")
}

func (b *gvisorBox) Name() string { return "gvisor" }

func (b *gvisorBox) Run(ctx context.Context, req ScriptRequest) (ScriptResult, error) {
	if len(req.Argv) == 0 {
		return ScriptResult{}, errors.New("no command")
	}
	mounts := []sandbox.Mount{
		{Source: "/etc", Destination: "/etc", Type: sandbox.MountTypeBind, ReadOnly: true, Recursive: true},
		{Destination: "/tmp", Type: sandbox.MountTypeTmpfs},
	}
	for _, p := range req.ReadOnly {
		mounts = append(mounts, sandbox.Mount{Source: p, Destination: p, Type: sandbox.MountTypeBind, ReadOnly: true, Recursive: true, NoSuid: true, NoDev: true})
	}
	for _, p := range req.Writable {
		mounts = append(mounts, sandbox.Mount{Source: p, Destination: p, Type: sandbox.MountTypeBind, Recursive: true, NoSuid: true, NoDev: true})
	}
	// Blocked paths inside what's mounted (a .env in the workspace) are
	// covered: files by an empty file, directories by an empty tmpfs.
	files, dirs := expandBlocked(OSSandboxSpec{WritableDirs: req.Writable, ReadOnlyDirs: req.ReadOnly, Blocked: b.blocked})
	for _, f := range files {
		mounts = append(mounts, sandbox.Mount{Source: b.empty, Destination: f, Type: sandbox.MountTypeBind, ReadOnly: true})
	}
	for _, d := range dirs {
		mounts = append(mounts, sandbox.Mount{Destination: d, Type: sandbox.MountTypeTmpfs, ReadOnly: true})
	}
	network := sandbox.NetworkModeNone
	if req.Network {
		network = sandbox.NetworkModeHost
	}
	dir := req.Dir
	if dir == "" {
		dir = "/tmp"
	}
	// The sandbox's name carries this process's ID, so a sandbox left behind
	// by a Code Puppy that was killed can be found and removed (sweep).
	id := fmt.Sprintf("cp-%d-%d", os.Getpid(), b.seq.Add(1))
	os.Setenv(sandbox.RunscPathEnvVar, b.runsc) // how sandboxexec finds runsc

	start := time.Now()
	sb, err := sandbox.New(ctx,
		sandbox.WithID(id), sandbox.WithStateDir(b.stateDir), sandbox.WithRuntimeDir(b.bundles),
		sandbox.WithNetwork(network), sandbox.WithMount(mounts...), sandbox.WithWorkingDir(dir),
		sandbox.WithoutBaseEnv(), sandbox.WithEnv(scriptEnv(req, "/tmp")...),
	)
	if err != nil {
		return ScriptResult{}, fmt.Errorf("start gVisor sandbox: %w", err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gvisorCloseTimeout)
		defer cancel()
		_ = sb.Close(cctx)
	}()

	runCtx, cancel := withOptionalTimeout(ctx, req.Timeout)
	defer cancel()
	// Killing the runsc exec client doesn't stop the command inside the
	// sandbox, which keeps the output pipes open, so Exec wouldn't return.
	// On timeout or cancellation, kill the whole sandbox instead.
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-runCtx.Done():
			_ = exec.Command(b.runsc, "--root", b.stateDir, "kill", id, "SIGKILL").Run()
		case <-finished:
		}
	}()
	out, err := sb.Exec(runCtx, req.Argv, sandbox.WithExecStdio(req.Stdin, req.Stdout, req.Stderr))
	res := ScriptResult{Elapsed: time.Since(start), TimedOut: errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil}
	switch {
	case res.TimedOut:
		res.ExitCode = -1
		return res, nil
	case ctx.Err() != nil:
		return res, ctx.Err()
	case err != nil:
		return res, err
	}
	res.ExitCode = out.ExitCode
	return res, nil
}

// sweep kills and deletes sandboxes left by Code Puppy processes that no
// longer exist (killed before their deferred Close ran).
func (b *gvisorBox) sweep(ctx context.Context) {
	out, err := exec.CommandContext(ctx, b.runsc, "--root", b.stateDir, "list", "-quiet").Output()
	if err != nil {
		return
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		id := strings.TrimSpace(sc.Text())
		pid, ok := sandboxOwner(id)
		if !ok || processAlive(pid) {
			continue
		}
		_ = exec.CommandContext(ctx, b.runsc, "--root", b.stateDir, "kill", id, "SIGKILL").Run()
		_ = exec.CommandContext(ctx, b.runsc, "--root", b.stateDir, "delete", "--force", id).Run()
		_ = os.RemoveAll(filepath.Join(b.bundles, id))
	}
}

// sandboxOwner returns the Code Puppy process ID in a sandbox name
// ("cp-<pid>-<n>").
func sandboxOwner(id string) (int, bool) {
	rest, ok := strings.CutPrefix(id, "cp-")
	if !ok {
		return 0, false
	}
	p, _, ok := strings.Cut(rest, "-")
	pid, err := strconv.Atoi(p)
	return pid, ok && err == nil && pid > 0
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
