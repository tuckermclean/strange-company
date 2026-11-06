package price_test

import (
	"testing"

	"strange.company/sc/internal/provider/price"
)

// 03-AC-10 (plan 03 14.6): the commitment is an ISO-8601 duration of the schema pattern ^P(T?[0-9]+[HDMY])+$; its
// months multiply the monthly price for committed_minimum. P1Y2M is 14 months; PT1H is one hour of an hourly product,
// not one month.
func TestCompute_CommitmentDurations_03AC10(t *testing.T) {
	monthly := func(commitment string) price.Entry {
		return price.Entry{ProductID: "p", MonthlyPrice: "10", SetupFee: "0", Commitment: commitment}
	}
	cases := []struct {
		name  string
		e     price.Entry
		count int
		want  string
	}{
		{"P1M", monthly("P1M"), 1, "10"},
		{"P12M", monthly("P12M"), 1, "120"},
		{"P1Y", monthly("P1Y"), 1, "120"},
		{"P1Y2M", monthly("P1Y2M"), 1, "140"},
		{"P2Y", monthly("P2Y"), 2, "480"},
		// one hour of billing: count x hourlyPrice, so 2 x 0.0137
		{"PT1H-hourly", price.Entry{ProductID: "p", HourlyPrice: "0.0137", SetupFee: "0", Commitment: "PT1H"}, 2, "0.0274"},
		{"PT1H-hourly-with-setup", price.Entry{ProductID: "p", HourlyPrice: "0.0137", SetupFee: "1", Commitment: "PT1H"}, 2, "2.0274"},
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

// 03-AC-10 / 05-AC-5: money is exactly the schema pattern ^(0|[1-9][0-9]{0,6})(\.[0-9]{1,4})?$; Go's big.Rat syntaxes
// (hex, underscores, leading dot, exponent) are not prices.
func TestParseMoney_OnlyTheSchemaPattern_03AC10(t *testing.T) {
	for _, s := range []string{"0", "1", "9.99", "10.5", "1234567", "0.0001", "1234567.9999", "100"} {
		t.Run("accept-"+s, func(t *testing.T) {
			if _, ok := price.ParseMoney(s); !ok {
				t.Errorf("ParseMoney(%q) rejected a schema-valid amount", s)
			}
		})
	}
	for _, s := range []string{"0x10", "0X1F", "1_0", ".5", "1e3", "1E3", "1.", "01", "00", "1.23456", "12345678", "", "-1", "+1",
		" 1", "1 ", "1/2", "0b11", "0o7", "1,5", "NaN", "Inf"} {
		t.Run("reject-"+s, func(t *testing.T) {
			if r, ok := price.ParseMoney(s); ok {
				t.Errorf("ParseMoney(%q) = %v, want it rejected (not the schema money pattern)", s, r)
			}
		})
	}
}
