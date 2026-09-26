//go:build linux

package tools

import (
	"fmt"
	"os/exec"
)

// nativeSandbox uses bubblewrap (bwrap). It probes once, since bwrap needs
// unprivileged user namespaces, which some distributions and containers
// disable.
func nativeSandbox(spec OSSandboxSpec) (sandboxWrapper, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("bubblewrap (bwrap) not found; install it (e.g. apt install bubblewrap)")
	}
	probe := bwrapArgs(bwrap, spec, nil, nil)
	probe = append(probe, "/bin/true")
	if out, err := exec.Command(probe[0], probe[1:]...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("bwrap probe failed (are user namespaces enabled?): %v: %s", err, out)
	}
	return func(argv []string) []string {
		files, dirs := expandBlocked(spec)
		return append(bwrapArgs(bwrap, spec, files, dirs), argv...)
	}, nil
}
