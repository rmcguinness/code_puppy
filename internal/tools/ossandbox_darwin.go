//go:build darwin

package tools

import (
	"fmt"
	"os"
	"os/exec"
)

const sandboxExec = "/usr/bin/sandbox-exec"

// nativeSandbox uses macOS Seatbelt via sandbox-exec. It probes the profile
// once, since sandbox-exec can be unusable (e.g. inside another sandbox).
func nativeSandbox(spec OSSandboxSpec) (sandboxWrapper, error) {
	if _, err := os.Stat(sandboxExec); err != nil {
		return nil, fmt.Errorf("%s not found", sandboxExec)
	}
	profile := seatbeltProfile(spec)
	if out, err := exec.Command(sandboxExec, "-p", profile, "/usr/bin/true").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("sandbox-exec probe failed: %v: %s", err, out)
	}
	return prefixWrapper(sandboxExec, "-p", profile), nil
}
