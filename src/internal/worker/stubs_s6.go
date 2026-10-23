package worker

import "io"

// Declared stubs of slice S6a (RED). The implementer replaces this file's functions with real code.

// SelfTestEnv is what `sc-worker selftest` probes (plan 28 section 13.1).
type SelfTestEnv struct {
	Uid, Gid        int
	WritableDirs    []string
	ReadOnlyDirs    []string
	PackageManagers []string
}

// DefaultSelfTestEnv is the production environment of the selftest.
//
// SC-STUB(F07 28-AC-3)
func DefaultSelfTestEnv() (env SelfTestEnv) {
	return
}

// SelfTest runs the image self-check and returns 0 (ok) or 1 (a check failed).
//
// SC-STUB(F07 28-AC-3)
func SelfTest(env SelfTestEnv, stdout, stderr io.Writer) (code int) {
	return
}

// ShimSweepMode names how the shim kills the leftovers of a job: "all" as PID 1, "descendants" otherwise.
//
// SC-STUB(F07 S4-N4)
func ShimSweepMode(pid int) (mode string) {
	return
}
