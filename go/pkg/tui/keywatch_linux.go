package tui

import "golang.org/x/sys/unix"

const (
	getTermios = unix.TCGETS
	setTermios = unix.TCSETS
)

func disableStatusKey(*unix.Termios) {} // Linux has no STATUS key
