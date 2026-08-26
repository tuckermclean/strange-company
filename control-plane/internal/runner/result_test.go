package runner

import "testing"

// TestCostUSD_NilDistinctFromZero is the test the plan calls for before any
// adapter exists: a result with no reported cost must be distinguishable
// from one that legitimately cost $0. If CostUSD collapsed nil and 0.0 into
// the same representation, a cost ledger reading Codex runs (which never
// report cost at all) would silently record every one of them as free.
func TestCostUSD_NilDistinctFromZero(t *testing.T) {
	zero := 0.0

	noCostReported := CodingRunResult{Harness: "codex", CostUSD: nil}
	costedZero := CodingRunResult{Harness: "claude-code", CostUSD: &zero}

	if noCostReported.CostUSD != nil {
		t.Fatalf("CostUSD = %v, want nil for a harness that does not report cost", noCostReported.CostUSD)
	}
	if costedZero.CostUSD == nil {
		t.Fatalf("CostUSD = nil, want a non-nil pointer to 0.0 for a harness that reported an actual $0 cost")
	}
	if *costedZero.CostUSD != 0.0 {
		t.Fatalf("*CostUSD = %v, want 0.0", *costedZero.CostUSD)
	}

	// The two must not be equal as far as any caller checking "was cost
	// reported at all?" is concerned.
	if (noCostReported.CostUSD == nil) == (costedZero.CostUSD == nil) {
		t.Fatalf("nil-cost and zero-cost results are indistinguishable by nilness")
	}
}

// TestStatus_ValuesAreDistinct asserts the Status classification is total
// and closed: every declared Status constant is non-empty and no two
// collapse to the same wire value, so a switch over Status can never
// silently conflate two different terminal dispositions.
func TestStatus_ValuesAreDistinct(t *testing.T) {
	all := []Status{
		StatusCompleted,
		StatusFailed,
		StatusPolicyViolation,
		StatusTimeout,
		StatusInfraError,
	}

	seen := make(map[Status]bool, len(all))
	for _, s := range all {
		if s == "" {
			t.Fatalf("a Status constant is empty")
		}
		if seen[s] {
			t.Fatalf("Status value %q is used by more than one constant", s)
		}
		seen[s] = true
	}
}
