package tools

import (
	"context"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
)

// ExecEnv builds every process Code Puppy runs on the model's behalf: shell
// commands, background processes, and forged tools. Each process is placed
// in its own process group, optionally wrapped in the OS sandbox, and guarded
// so the group is killed if Code Puppy exits for any reason.
type ExecEnv struct {
	Sandbox *OSSandbox
	// ScrubEnv lists environment variable names (globs allowed, e.g.
	// "*_API_KEY") removed from every child process, so commands the model
	// runs can't read Code Puppy's own credentials.
	ScrubEnv []string
	// Dir is where commands run unless they set their own directory: the
	// workspace root, never the process's working directory.
	Dir string
}

// guardedCmd is an exec.Cmd whose process group dies with Code Puppy.
type guardedCmd struct {
	*exec.Cmd
	childEnd *os.File
	once     sync.Once
	release  func()
}

// command prepares argv to run under ctx. A nil ExecEnv runs unsandboxed.
func (e *ExecEnv) command(ctx context.Context, argv []string) (*guardedCmd, error) {
	if e != nil {
		argv = e.Sandbox.wrap(argv)
	}
	wrapped, childEnd, release, err := guardArgv(argv)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, wrapped[0], wrapped[1:]...)
	if e != nil {
		cmd.Dir = e.Dir
	}
	if childEnd != nil {
		cmd.ExtraFiles = []*os.File{childEnd}
	}
	if e != nil && len(e.ScrubEnv) > 0 {
		cmd.Env = scrubEnv(os.Environ(), e.ScrubEnv)
	}
	configureProcessGroup(cmd)
	cmd.WaitDelay = shellWaitDelay
	return &guardedCmd{Cmd: cmd, childEnd: childEnd, release: release}, nil
}

// Start starts the command and drops the parent's copy of the child's guard fd.
func (g *guardedCmd) Start() error {
	err := g.Cmd.Start()
	if g.childEnd != nil {
		g.childEnd.Close()
	}
	if err != nil {
		g.Release()
	}
	return err
}

// Wait waits for the command, then releases the guard, which kills anything
// the command left running in its process group.
func (g *guardedCmd) Wait() error {
	err := g.Cmd.Wait()
	g.Release()
	return err
}

// Run starts the command and waits for it.
func (g *guardedCmd) Run() error {
	if err := g.Start(); err != nil {
		return err
	}
	return g.Wait()
}

// Release closes the guard pipe; safe to call more than once.
func (g *guardedCmd) Release() { g.once.Do(g.release) }

// scrubEnv drops variables whose names match any pattern (case-insensitive).
func scrubEnv(env, patterns []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, p := range patterns {
			if ok, _ := path.Match(strings.ToUpper(p), strings.ToUpper(name)); ok {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}
