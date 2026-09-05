package policy

import (
	"testing"
	"time"
)

// at is any instant: every rate card in this file is flat, so the schedule
// cannot affect the result -- which TestARateCardWithNoScheduleIsFlat asserts
// directly.
var at = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)


// A run that reads 9728 cached tokens against 552 fresh ones is a real ratio
// from this system. Charging cache reads at the full input rate would bill it
// almost entirely wrong.
func TestCacheReadsArePricedAtTheirOwnRate(t *testing.T) {
	p := &Pricing{InputPerMTok: 1.00, OutputPerMTok: 2.00, CachedInputPerMTok: 0.10}

	full := p.CostUSD(9728+552, 0, 0, 0, at, at)
	split := p.CostUSD(552, 0, 9728, 0, at, at)

	if split >= full {
		t.Fatalf("cached reads cost %v, the same tokens at full rate cost %v", split, full)
	}
	want := 552*1.00/1e6 + 9728*0.10/1e6
	if diff := split - want; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("cost = %v, want %v", split, want)
	}
}

// Providers bill reasoning as output and report it inside the output count, so
// pricing it again would double-charge the thinking. This is why CostUSD has
// no reasoning parameter, and the test states it so nobody adds one.
func TestOutputAlreadyIncludesReasoning(t *testing.T) {
	p := &Pricing{OutputPerMTok: 3.00}

	// 164 output tokens, of which 49 were reasoning: priced once, on 164.
	if got, want := p.CostUSD(0, 164, 0, 0, at, at), 164*3.00/1e6; got != want {
		t.Errorf("cost = %v, want %v", got, want)
	}
}

// An alias with no rate card is normal -- a provider the harness prices itself
// needs none -- and must not panic or invent a number.
func TestAnUnpricedAliasCostsNothingAndDoesNotPanic(t *testing.T) {
	var p *Pricing
	if got := p.CostUSD(1000, 1000, 1000, 1000, at, at); got != 0 {
		t.Errorf("cost = %v, want 0", got)
	}
}

// Rates are per million because that is the unit every provider publishes.
func TestRatesArePerMillionTokens(t *testing.T) {
	p := &Pricing{InputPerMTok: 0.28}
	if got, want := p.CostUSD(1_000_000, 0, 0, 0, at, at), 0.28; got != want {
		t.Errorf("a million input tokens cost %v, want %v", got, want)
	}
}
