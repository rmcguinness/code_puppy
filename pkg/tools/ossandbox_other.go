//go:build !darwin && !linux

package tools

import (
	"fmt"
	"runtime"
)

// nativeSandbox: only macOS (Seatbelt) and Linux (bubblewrap) are supported.
func nativeSandbox(spec OSSandboxSpec) (sandboxWrapper, error) {
	return nil, fmt.Errorf("no OS sandbox implementation for %s", runtime.GOOS)
}
