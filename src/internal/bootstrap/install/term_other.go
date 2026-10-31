//go:build !linux

package install

import "os"

// disableEcho is a no-op where terminal control is not built.
func disableEcho(*os.File) func() { return func() {} }
