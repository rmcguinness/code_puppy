//go:build !unix

package tools

import (
	"os"
	"os/exec"
)

// configureProcessGroup is a no-op on platforms without POSIX process groups;
// cancellation falls back to killing the direct child.
func configureProcessGroup(cmd *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// guardArgv is not available without POSIX shells; commands run unguarded.
func guardArgv(argv []string) ([]string, *os.File, func(), error) {
	return argv, nil, func() {}, nil
}
