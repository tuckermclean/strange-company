// Package redgate implements the §11.3 red gate's decision.
//
// §11.3: "After tests are written, no model grades them. The runner executes
// them against the unimplemented feature state." So this package contains no
// model call and no heuristic reading of test output -- only a comparison of
// two runs of the same command at two refs.
//
// # What this gate does and does not guarantee
//
// §11.3 asks for three things: that newly introduced acceptance tests fail,
// that the failure is attributable to missing behaviour, and that the
// pre-existing baseline remains understood.
//
// The first and third are decidable and are decided here. The second is
// decided only in the sense that matters most: the same command passes at the
// base ref and fails on the candidate, so the difference IS the new tests.
//
// What this cannot catch: a test that runs, fails, and asserts the wrong
// thing. Nothing short of judgement distinguishes that from a correct failing
// test, and §11.3 forbids a model from grading. That gap is real, and stated
// here rather than implied by silence.
package redgate

import "fmt"

// Outcome is the gate's verdict.
type Outcome string

const (
	// Red is the only outcome that proceeds: the suite passed at the base
	// ref and fails with the new tests, so the failure is theirs.
	Red Outcome = "red"

	// NotRed means the new tests pass against the unimplemented state, so
	// they are not testing this work (§11.3).
	NotRed Outcome = "not_red"

	// Malformed means the tests cannot be run at all on the candidate,
	// although the same command ran at the base ref.
	Malformed Outcome = "malformed"

	// BaselineBroken means the suite was already failing before the new
	// tests, so nothing in the candidate run is attributable to them.
	BaselineBroken Outcome = "baseline_broken"

	// Inconclusive means a run did not complete. This is an infrastructure
	// outcome, not a verdict about the tests (§12.1).
	Inconclusive Outcome = "inconclusive"
)

// Proceeds reports whether the card may move to implementation.
//
// A method rather than a caller-side comparison, so "which outcomes are safe"
// is answered in one place instead of at every call site.
func (o Outcome) Proceeds() bool { return o == Red }

// commandNotFound is the conventional shell exit code for "no such command".
// The command demonstrably ran at the base ref, so seeing it on the candidate
// means the change broke the ability to run the tests.
const commandNotFound = 127

// RunOutcome is one execution of the test command.
type RunOutcome struct {
	// Completed is whether the run finished and its exit code is real. A
	// run cut short by a timeout or a scheduling failure did not.
	Completed bool
	ExitCode  int
}

// Evaluate compares a baseline run at the base ref with a candidate run
// carrying the new tests, and returns the verdict with the reason a human
// will read when a card stops here.
func Evaluate(baseline, candidate RunOutcome) (Outcome, string) {
	// Checked first: a run that did not finish says nothing about the
	// tests, and reading a verdict out of it would attribute an
	// infrastructure failure to the work (§12.1).
	if !baseline.Completed {
		return Inconclusive, "the baseline test run did not complete, so there is nothing to compare against"
	}
	if !candidate.Completed {
		return Inconclusive, "the test run against the new tests did not complete"
	}

	// Before anything about the candidate: if the suite was already
	// failing, no difference between the runs is attributable to the new
	// tests, and calling that red would be a guess dressed as a verdict.
	if baseline.ExitCode != 0 {
		return BaselineBroken, fmt.Sprintf(
			"the baseline already fails at the base ref (exit %d), so a failure with the new tests cannot be attributed to them",
			baseline.ExitCode)
	}

	switch {
	case candidate.ExitCode == 0:
		return NotRed, "the new tests pass without the feature implemented, so they are not testing this work"
	case candidate.ExitCode == commandNotFound:
		return Malformed, "the test command ran at the base ref but cannot be found with the new tests applied"
	default:
		return Red, fmt.Sprintf(
			"the suite passes at the base ref and fails with the new tests (exit %d), so the failure is theirs",
			candidate.ExitCode)
	}
}
