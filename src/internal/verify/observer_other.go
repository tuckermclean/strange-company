//go:build !linux

package verify

import "fmt"

// Main is sc-verify on a platform without the Linux process controls the observer depends on (subreaper, process
// groups, /proc): it refuses to run (exit 2) rather than observe with weaker guarantees.
func Main(d Deps) int {
	fmt.Fprintln(d.Stderr, "sc-verify: unsupported platform: the observer needs Linux")
	return 2
}
