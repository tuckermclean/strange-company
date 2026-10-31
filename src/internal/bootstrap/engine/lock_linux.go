//go:build linux

package engine

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLockFile takes an exclusive open-file-description lock (F_OFD_SETLK) on the whole file. The lock belongs to the
// open file description, so two descriptors in one process exclude each other, and the kernel drops it when the
// process dies, whatever the signal. It reports false when another description holds the lock.
func tryLockFile(f *os.File) (bool, error) {
	fl := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	err := unix.FcntlFlock(f.Fd(), unix.F_OFD_SETLK, &fl)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.EAGAIN), errors.Is(err, unix.EACCES):
		return false, nil
	}
	return false, err
}

// killSelf delivers SIGKILL to this process (the crash hook of plan 50 section 22): no deferred cleanup runs and the
// lock object stays behind. It does not return.
func killSelf() {
	_ = unix.Kill(os.Getpid(), unix.SIGKILL)
	select {}
}
