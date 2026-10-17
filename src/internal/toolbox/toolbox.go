// Package toolbox implements the sc-bootstrap commands that run inside the toolbox image (plan 02 section 8).
package toolbox

import "io"

// SC-STUB(F03 02-AC-3 02-AC-10 02-AC-11)
// Main runs sc-bootstrap with the given arguments and environment and returns the process exit code.
func Main(args []string, env map[string]string, stdout, stderr io.Writer) int {
	return 0
}
