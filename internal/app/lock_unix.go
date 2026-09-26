//go:build unix

package app

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var errLocked = errors.New("locked")

func tryLock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return errLocked
	}
	return err
}

func unlock(f *os.File) { unix.Flock(int(f.Fd()), unix.LOCK_UN) }
