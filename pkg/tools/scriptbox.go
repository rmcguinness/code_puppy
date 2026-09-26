package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Skill scripts run through a ScriptBox: gVisor on Linux when runsc is
// available (a user-space kernel, so a kernel exploit in a script or one
// of its packages doesn't reach the host), otherwise the OS sandbox
// (Seatbelt or bubblewrap). There is no unsandboxed fallback.

// ScriptRequest is one command to run in isolation.
type ScriptRequest struct {
	Argv []string
	// Dir is the working directory; it must be one of the mounted paths.
	Dir string
	// Env is the whole environment besides PATH, HOME, TMPDIR and LANG,
	// which the box sets. Nothing is inherited from Code Puppy's own.
	Env []string
	// Network gives the command the host's network; otherwise it has none.
	Network bool
	// ReadOnly and Writable are host paths mounted at the same path.
	ReadOnly, Writable []string
	Stdin              io.Reader
	Stdout, Stderr     io.Writer
	Timeout            time.Duration // 0: no limit besides ctx
}

// ScriptResult is how a command ended.
type ScriptResult struct {
	ExitCode int
	TimedOut bool
	Elapsed  time.Duration
}

// ScriptBox runs commands in isolation.
type ScriptBox interface {
	// Name is "gvisor" or "os".
	Name() string
	Run(ctx context.Context, req ScriptRequest) (ScriptResult, error)
}

// ErrNoScriptBox means no sandbox is available, so scripts can't run.
var ErrNoScriptBox = errors.New("no sandbox is available for scripts")

// ScriptBoxConfig selects and configures a ScriptBox.
type ScriptBoxConfig struct {
	// Mode is "gvisor", "os" or "auto" (gVisor if it works, else the OS
	// sandbox); skills.policy.sandbox.
	Mode string
	// Blocked are paths no script may read, on backends that can hide
	// them (the OS sandbox; gVisor doesn't mount them at all).
	Blocked *PathMatcher
	// StateDir holds gVisor's container state and bundles
	// (~/.code_puppy/sandboxes).
	StateDir string
}

// NewScriptBox returns the configured backend. With "auto" it also returns
// why gVisor isn't used, for doctor.
func NewScriptBox(cfg ScriptBoxConfig) (box ScriptBox, note string, err error) {
	mode := strings.ToLower(cfg.Mode)
	if mode == "" {
		mode = "auto"
	}
	if mode == "gvisor" || mode == "auto" {
		g, gerr := newGVisorBox(cfg)
		if gerr == nil {
			return g, "", nil
		}
		if mode == "gvisor" {
			return nil, "", fmt.Errorf("skills.policy.sandbox is \"gvisor\": %w", gerr)
		}
		note = "gVisor unavailable: " + gerr.Error()
	}
	if mode != "os" && mode != "auto" {
		return nil, "", fmt.Errorf("unknown skills.policy.sandbox %q (auto, gvisor, os)", cfg.Mode)
	}
	if err := platformSandboxProbe(); err != nil {
		return nil, note, fmt.Errorf("%w: %v", ErrNoScriptBox, err)
	}
	return &osBox{blocked: cfg.Blocked}, note, nil
}

// platformSandboxProbe checks that the OS sandbox works here. Replaced in tests.
var platformSandboxProbe = func() error {
	_, err := platformSandbox(OSSandboxSpec{Mode: SandboxRequired})
	return err
}

// scriptEnv is the environment a script sees: req.Env plus a fixed PATH, a
// private home and temp directory, and LANG.
func scriptEnv(req ScriptRequest, tmp string) []string {
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + tmp,
		"TMPDIR=" + tmp,
		"LANG=C.UTF-8",
	}
	return append(env, req.Env...)
}

// osBox runs commands under the OS sandbox, built per request: writes only
// to req.Writable and a private temp directory, blocked paths hidden, the
// network as requested. Unlike gVisor, the rest of the file system stays
// readable, as for shell commands.
type osBox struct {
	blocked *PathMatcher
}

func (b *osBox) Name() string { return "os" }

func (b *osBox) Run(ctx context.Context, req ScriptRequest) (ScriptResult, error) {
	if len(req.Argv) == 0 {
		return ScriptResult{}, errors.New("no command")
	}
	tmp, err := os.MkdirTemp("", "code-puppy-script-*")
	if err != nil {
		return ScriptResult{}, err
	}
	defer os.RemoveAll(tmp)
	writable := []string{tmp}
	for _, d := range req.Writable {
		if c, err := canonicalDir(d); err == nil {
			writable = append(writable, c)
		}
	}
	if c, err := canonicalDir(tmp); err == nil {
		writable[0] = c
	}
	// Outside the writable paths everything is read-only already. A
	// read-only path is only listed when it lies inside a writable one:
	// the sandbox lets read-only win over nested writable paths, which would
	// otherwise block e.g. an output directory inside the workspace.
	var readOnly []string
	for _, d := range req.ReadOnly {
		c, err := canonicalDir(d)
		if err != nil {
			continue
		}
		for _, w := range writable {
			if c != w && strings.HasPrefix(c, w+string(os.PathSeparator)) {
				readOnly = append(readOnly, c)
				break
			}
		}
	}
	box, err := NewOSSandbox(OSSandboxSpec{
		Mode: SandboxRequired, WritableDirs: writable, ReadOnlyDirs: readOnly,
		Blocked: b.blocked, AllowNetwork: req.Network,
	})
	if err != nil {
		return ScriptResult{}, fmt.Errorf("%w: %v", ErrNoScriptBox, err)
	}

	runCtx, cancel := withOptionalTimeout(ctx, req.Timeout)
	defer cancel()
	cmd, err := (&ExecEnv{Sandbox: box}).command(runCtx, req.Argv)
	if err != nil {
		return ScriptResult{}, err
	}
	cmd.Env = scriptEnv(req, writable[0])
	cmd.Dir = req.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = req.Stdin, req.Stdout, req.Stderr
	start := time.Now()
	err = cmd.Run()
	res := ScriptResult{Elapsed: time.Since(start), TimedOut: errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil}
	var exitErr *exec.ExitError
	switch {
	case ctx.Err() != nil: // cancelled: the kill shows up as an exit, so check first
		return res, ctx.Err()
	case err == nil:
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	case !res.TimedOut:
		return res, err
	}
	if res.TimedOut && res.ExitCode == 0 {
		res.ExitCode = -1
	}
	return res, nil
}

func withOptionalTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d > 0 {
		return context.WithTimeout(ctx, d)
	}
	return context.WithCancel(ctx)
}
