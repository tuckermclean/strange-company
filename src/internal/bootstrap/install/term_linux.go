package install

import (
	"os"

	"golang.org/x/sys/unix"
)

// disableEcho turns terminal echo off on f and returns the function that restores it; on anything that is not a
// terminal it does nothing.
func disableEcho(f *os.File) (restore func()) {
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return func() {}
	}
	n := *old
	n.Lflag &^= unix.ECHO
	if unix.IoctlSetTermios(fd, unix.TCSETS, &n) != nil {
		return func() {}
	}
	return func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, old) }
}
