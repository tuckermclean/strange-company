package redgate_test

import (
	"strings"
	"testing"

	"github.com/tuckermclean/strange-company/control-plane/internal/redgate"
)

func run(completed bool, exit int) redgate.RunOutcome {
	return redgate.RunOutcome{Completed: completed, ExitCode: exit}
}

// The whole point of §11.3: the new tests fail, and they fail because the
// behaviour is missing rather than because they were already failing.
func TestBaselineGreenAndCandidateFailingIsRed(t *testing.T) {
	got, why := redgate.Evaluate(run(true, 0), run(true, 1))
	if got != redgate.Red {
		t.Fatalf("outcome = %q (%s), want red", got, why)
	}
}

// §11.3: "If the new tests pass without implementation, the test phase fails."
// A test that passes against the unimplemented state is not testing this work.
func TestCandidatePassingIsNotRed(t *testing.T) {
	got, why := redgate.Evaluate(run(true, 0), run(true, 0))
	if got != redgate.NotRed {
		t.Fatalf("outcome = %q, want not_red", got)
	}
	if !strings.Contains(why, "without") {
		t.Errorf("reason does not explain the problem: %q", why)
	}
}

// §11.3: "pre-existing test baseline remains understood." If the suite was
// already failing at the base ref, nothing about the candidate run is
// attributable to the new tests -- the gate cannot conclude anything, and
// saying "red" would be a guess dressed as a verdict.
func TestAnAlreadyFailingBaselineIsNotAVerdict(t *testing.T) {
	got, why := redgate.Evaluate(run(true, 1), run(true, 1))
	if got != redgate.BaselineBroken {
		t.Fatalf("outcome = %q, want baseline_broken", got)
	}
	if !strings.Contains(strings.ToLower(why), "baseline") {
		t.Errorf("reason does not name the baseline: %q", why)
	}
}

// The command ran at the base ref, so it exists. A candidate run that cannot
// find it means the change broke the ability to run the tests at all, which
// §11.3 counts as malformed rather than red.
func TestACandidateThatCannotRunTheCommandIsMalformed(t *testing.T) {
	got, _ := redgate.Evaluate(run(true, 0), run(true, 127))
	if got != redgate.Malformed {
		t.Fatalf("outcome = %q, want malformed", got)
	}
}

// A run that never completed says nothing about the tests. Reporting red or
// not-red from it would attribute an infrastructure failure to the work.
func TestAnIncompleteRunIsInconclusive(t *testing.T) {
	for _, tc := range []struct {
		name               string
		baseline, candidate redgate.RunOutcome
	}{
		{"baseline never ran", run(false, 0), run(true, 1)},
		{"candidate never ran", run(true, 0), run(false, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := redgate.Evaluate(tc.baseline, tc.candidate)
			if got != redgate.Inconclusive {
				t.Fatalf("outcome = %q, want inconclusive", got)
			}
		})
	}
}

// Only Red may proceed. Every other outcome has to stop the card, and a
// caller should not have to remember which.
func TestOnlyRedProceeds(t *testing.T) {
	for outcome, wantProceed := range map[redgate.Outcome]bool{
		redgate.Red:            true,
		redgate.NotRed:         false,
		redgate.Malformed:      false,
		redgate.BaselineBroken: false,
		redgate.Inconclusive:   false,
	} {
		if got := outcome.Proceeds(); got != wantProceed {
			t.Errorf("%q.Proceeds() = %v, want %v", outcome, got, wantProceed)
		}
	}
}

// The reason is what a human reads when a card stops here, so every outcome
// must have one.
func TestEveryOutcomeExplainsItself(t *testing.T) {
	cases := []struct{ baseline, candidate redgate.RunOutcome }{
		{run(true, 0), run(true, 1)},
		{run(true, 0), run(true, 0)},
		{run(true, 1), run(true, 1)},
		{run(true, 0), run(true, 127)},
		{run(false, 0), run(true, 1)},
	}
	for _, c := range cases {
		if _, why := redgate.Evaluate(c.baseline, c.candidate); strings.TrimSpace(why) == "" {
			t.Errorf("no reason for baseline=%+v candidate=%+v", c.baseline, c.candidate)
		}
	}
}
