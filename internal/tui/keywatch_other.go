//go:build !darwin && !linux

package tui

import (
	"errors"
	"time"
)

var errKeysUnsupported = errors.New("key watching is not supported on this platform")

// Steering needs raw key input; elsewhere the watcher exits at once and
// turns behave as before.
type ttyKeys struct{}

func newTTYKeys(int) keyTerm                      { return ttyKeys{} }
func (ttyKeys) enter() error                      { return errKeysUnsupported }
func (ttyKeys) leave() error                      { return nil }
func (ttyKeys) ready(time.Duration) (bool, error) { return false, errKeysUnsupported }
func (ttyKeys) read([]byte) (int, error)          { return 0, errKeysUnsupported }
