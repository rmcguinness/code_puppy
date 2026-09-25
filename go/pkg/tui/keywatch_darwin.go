package tui

import "golang.org/x/sys/unix"

const (
	getTermios = unix.TIOCGETA
	setTermios = unix.TIOCSETA
)

func disableStatusKey(t *unix.Termios) { t.Cc[unix.VSTATUS] = 0xff } // _POSIX_VDISABLE
