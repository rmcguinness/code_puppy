//go:build !linux

package tools

import "errors"

func newGVisorBox(ScriptBoxConfig) (ScriptBox, error) {
	return nil, errors.New("gVisor runs only on Linux")
}
