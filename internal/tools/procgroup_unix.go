//go:build unix

package tools

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureProcessGroup starts the command in its own process group and makes
// context cancellation kill the whole group, so grandchildren (e.g. `sleep &`)
// cannot outlive a timeout or keep the output pipes open.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// guardScript runs a watcher in the command's process group that blocks
// reading fd 3 and SIGKILLs the whole group when it hits EOF. The write end
// stays in Code Puppy, so EOF arrives when Code Puppy closes it after the
// command exits (cleaning up stragglers) or when Code Puppy dies for any
// reason, including SIGKILL. Nothing Code Puppy starts can outlive it.
const guardScript = `( read -r -u 3 _ ; kill -KILL 0 ) </dev/null >/dev/null 2>&1 & exec 3<&- ; exec "$@"`

// guardArgv wraps argv with the parent-death guard. The returned file must be
// passed to the child as fd 3 (ExtraFiles[0]); release closes the parent's end.
func guardArgv(argv []string) (wrapped []string, childEnd *os.File, release func(), err error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, err
	}
	wrapped = append([]string{"bash", "-c", guardScript, "code-puppy-guard"}, argv...)
	return wrapped, r, func() { w.Close() }, nil
}
