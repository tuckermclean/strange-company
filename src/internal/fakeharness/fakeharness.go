// Package fakeharness is the library entry of the sc-fake-harness binary (plan 28 section 8.2): the cooperative
// fake harness that sc-worker runs through the shim in the component tests.
package fakeharness

import "io"

// Run executes the fake harness for the given sc-fake-harness argv (without the program name) and environment
// and returns the process exit code. args carry --scenario, --role, --attempt, --task, --execution, --repo,
// --context and --out; getenv answers SC_FAKE_GATEWAY_URL and SC_FAKE_GATEWAY_TOKEN.
//
// SC-STUB(F07 27-AC-6 27-AC-7 27-AC-8)
func Run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	return 0
}
