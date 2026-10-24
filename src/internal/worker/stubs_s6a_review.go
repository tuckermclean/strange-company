package worker

import "strange.company/sc/internal/clock"

// SC-STUB(F07 S4-N4)
// SweepDescendantsOf is the descendant sweep of the shim against a /proc-shaped directory: it kills and reaps every
// live process below self and reports incomplete when one is still alive after rounds rounds or the directory is
// unreadable.
func SweepDescendantsOf(clk clock.Clock, procRoot string, self, rounds int) (incomplete bool) {
	return false
}
