package price_test

import (
	"testing"

	"strange.company/sc/internal/provider/price"
)

// 03-AC-10 (plan 03 14.6, S5b attempt 2): an ISO-8601 commitment splits once at T into a date part (Y, M, D) and a time
// part (H, M, S). PT1H30M is 1 hour 30 minutes, 1.5 hours, not 1 hour and 30 months. An hour is 1/730 of a month, so
// with an hourly price of 1 (730 a month) the committed minimum is the hours times the count.
func TestCompute_CommitmentSplitsOnceAtT_03AC10(t *testing.T) {
	hourly := func(commitment string) price.Entry {
		return price.Entry{ProductID: "p", HourlyPrice: "1", SetupFee: "0", Commitment: commitment}
	}
	cases := []struct {
		name  string
		e     price.Entry
		count int
		want  string
	}{
		{"PT1H30M-is-1.5-hours", hourly("PT1H30M"), 1, "1.5"},
		{"PT1H30M-two-nodes", hourly("PT1H30M"), 2, "3"},
		{"P1DT12H-is-36-hours", hourly("P1DT12H"), 1, "36"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			est := cat(c.e).Compute("p", c.count)
			if !est.Complete || est.CommittedMinimum != c.want {
				t.Errorf("committed_minimum = %q (complete %v), want %q", est.CommittedMinimum, est.Complete, c.want)
			}
		})
	}
}

// 03-AC-10 (plan 03 14.6, S5b attempt 2): a commitment that is empty or does not parse is no reason to assume one
// month: the estimate is incomplete (COST_INCOMPLETE through the cost check).
func TestCompute_UnparseableCommitmentIsIncomplete_03AC10(t *testing.T) {
	for _, commitment := range []string{"P1W", "P1y", "1M", ""} {
		t.Run("commitment-"+commitment, func(t *testing.T) {
			est := cat(price.Entry{ProductID: "p", MonthlyPrice: "10", SetupFee: "0", Commitment: commitment}).Compute("p", 1)
			if est.Complete {
				t.Errorf("Compute with commitment %q is complete (committed_minimum %q), want an incomplete estimate", commitment, est.CommittedMinimum)
			}
		})
	}
}
