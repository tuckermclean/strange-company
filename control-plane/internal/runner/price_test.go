package runner

import "testing"

type flatRate struct{ perMTok float64 }

func (f flatRate) CostUSD(in, out, cached, write int) float64 {
	return float64(in+out+cached+write) * f.perMTok / 1e6
}

func f64(v float64) *float64 { return &v }

// opencode reports cost 0 for any provider models.dev cannot price -- which is
// every custom OpenAI-compatible provider, including the one this runs on. A
// reported zero is a real number and it is wrong.
func TestAReportedZeroIsPricedFromTheRateCard(t *testing.T) {
	r := &CodingRunResult{CostUSD: f64(0)}
	r.Usage.InputTokens = 1_000_000

	Price(r, flatRate{perMTok: 0.28})

	if r.CostUSD == nil || *r.CostUSD != 0.28 {
		t.Errorf("cost = %v, want 0.28", r.CostUSD)
	}
}

// The Hermes gateway reports no cost at all, so the review phase counted
// thousands of tokens against nothing.
func TestAnUnreportedCostIsPricedFromTheRateCard(t *testing.T) {
	r := &CodingRunResult{}
	r.Usage.OutputTokens = 8287

	Price(r, flatRate{perMTok: 1.0})

	if r.CostUSD == nil || *r.CostUSD == 0 {
		t.Errorf("cost = %v, want the tokens priced", r.CostUSD)
	}
}

// A harness that reports a real price knows the provider's actual rate.
// Overwriting it with a configured table would make the ledger disagree with
// the invoice.
func TestAHarnessThatReportsARealPriceIsBelieved(t *testing.T) {
	r := &CodingRunResult{CostUSD: f64(1.23)}
	r.Usage.InputTokens = 1_000_000

	Price(r, flatRate{perMTok: 99.0})

	if *r.CostUSD != 1.23 {
		t.Errorf("cost = %v, want the harness's own 1.23", *r.CostUSD)
	}
}

// With no tokens there is nothing to price, and a run recorded as free is
// exactly the lie /cards/{id}/cost exists to avoid.
func TestARunThatReportedNoUsageStaysUnpriced(t *testing.T) {
	r := &CodingRunResult{}

	Price(r, flatRate{perMTok: 5.0})

	if r.CostUSD != nil {
		t.Errorf("cost = %v, want nil so the card still reads as unpriced", *r.CostUSD)
	}
}

// An alias with no rate card must not panic or invent a number.
func TestNoRateCardLeavesTheRunAlone(t *testing.T) {
	r := &CodingRunResult{}
	r.Usage.InputTokens = 100

	Price(r, nil)

	if r.CostUSD != nil {
		t.Errorf("cost = %v, want nil", *r.CostUSD)
	}
}

// A nil *policy.Pricing passed as a Pricer is NOT a nil interface value: the
// method is called on a nil receiver, returns 0, and a naive implementation
// writes that zero into the ledger as though the run were free.
type nilRates struct{ rates *struct{} }

func (n nilRates) CostUSD(int, int, int, int) float64 { return 0 }

func TestATypedNilRateCardDoesNotRecordTheRunAsFree(t *testing.T) {
	r := &CodingRunResult{CostUSD: f64(0)}
	r.Usage.InputTokens = 5000

	Price(r, nilRates{})

	if r.CostUSD != nil {
		t.Errorf("cost = %v; an unpriceable run must read as unknown, not as free", *r.CostUSD)
	}
}

// opencode emits cost 0 for every provider it has no rates for. Believing it
// would have /cards/{id}/cost report a fully-priced card costing nothing --
// the exact lie that endpoint was built to stop telling.
func TestAHarnessZeroThatCannotBePricedIsClearedNotKept(t *testing.T) {
	r := &CodingRunResult{CostUSD: f64(0)}
	r.Usage.InputTokens = 7699
	r.Usage.OutputTokens = 164

	Price(r, nil)

	if r.CostUSD != nil {
		t.Errorf("cost = %v, want nil so the run counts as unpriced", *r.CostUSD)
	}
}
