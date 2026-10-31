//go:build !linux

package engine

import (
	"errors"
	"os"
)

// tryLockFile is Linux-only: the toolbox image is Linux (plan 02), and OFD locks are a Linux feature.
func tryLockFile(*os.File) (bool, error) {
	return false, errors.New("the run lock needs open-file-description locks (Linux)")
}

// killSelf ends the process at once without cleanup.
func killSelf() { os.Exit(137) }
