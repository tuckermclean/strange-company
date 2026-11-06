package price_test

import (
	"math/big"
	"testing"
	"time"

	"strange.company/sc/internal/provider/price"
)

func cat(e price.Entry) *price.Catalog {
	return &price.Catalog{Provider: "fake", Currency: "EUR", Version: "2026.10.01", Entries: []price.Entry{e}}
}

// 03-AC-10 / 05-AC-5 (plan 03 14.6): monthly_total = count x unitMonthly, committed_minimum = count x unitMonthly x
// commitment months + count x setup fee.
func TestCompute_MonthlyCommitmentAndSetup_05AC5(t *testing.T) {
	cases := []struct {
		name                   string
		e                      price.Entry
		count                  int
		wantMonthly, wantFloor string
	}{
		{"one-month", price.Entry{ProductID: "p", MonthlyPrice: "9.99", SetupFee: "0", Commitment: "P1M"}, 1, "9.99", "9.99"},
		{"twelve-months", price.Entry{ProductID: "p", MonthlyPrice: "10", SetupFee: "0", Commitment: "P12M"}, 1, "10", "120"},
		{"one-year", price.Entry{ProductID: "p", MonthlyPrice: "10", SetupFee: "0", Commitment: "P1Y"}, 1, "10", "120"},
		{"counts-and-setup", price.Entry{ProductID: "p", MonthlyPrice: "10", SetupFee: "5.5", Commitment: "P12M"}, 2, "20", "251"},
		{"hourly-times-730", price.Entry{ProductID: "p", HourlyPrice: "0.0137", SetupFee: "0", Commitment: "P1M"}, 2, "20.002", "20.002"},
		{"free", price.Entry{ProductID: "p", MonthlyPrice: "0", SetupFee: "0", Commitment: "P1M"}, 3, "0", "0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			est := cat(c.e).Compute("p", c.count)
			if !est.Complete || est.MonthlyTotal != c.wantMonthly || est.CommittedMinimum != c.wantFloor {
				t.Errorf("Compute = monthly %q committed %q complete %v, want %q, %q, complete",
					est.MonthlyTotal, est.CommittedMinimum, est.Complete, c.wantMonthly, c.wantFloor)
			}
			if est.Currency != "EUR" || est.CatalogVersion != "2026.10.01" {
				t.Errorf("Compute currency %q version %q", est.Currency, est.CatalogVersion)
			}
		})
	}
}

// 03-AC-10 (plan 03 14.6): a product missing from the catalogue, or priced with something that is not money, is an
// incomplete estimate (the caller maps it to COST_INCOMPLETE); a malformed verifiedAt is an AgeDays error.
func TestCompute_IncompleteEstimates_03AC10(t *testing.T) {
	for name, c := range map[string]*price.Catalog{
		"unknown-product": cat(price.Entry{ProductID: "other", MonthlyPrice: "9.99", SetupFee: "0"}),
		"bad-price":       cat(price.Entry{ProductID: "p", MonthlyPrice: "9,99", SetupFee: "0"}),
		"bad-setup":       cat(price.Entry{ProductID: "p", MonthlyPrice: "9.99", SetupFee: "x"}),
		"no-price":        cat(price.Entry{ProductID: "p", SetupFee: "0"}),
	} {
		t.Run(name, func(t *testing.T) {
			if est := c.Compute("p", 1); est.Complete {
				t.Errorf("Compute reports a complete estimate for %s: %+v", name, est)
			}
		})
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	if _, err := (price.Entry{ProductID: "p", VerifiedAt: "2026-13-45"}).AgeDays(now); err == nil {
		t.Error("AgeDays accepts a malformed verifiedAt")
	}
	if got, err := (price.Entry{ProductID: "p", VerifiedAt: "2026-05-06"}).AgeDays(now); err != nil || got != 180 {
		t.Errorf("AgeDays(2026-05-06 at 2026-11-02) = %d, %v, want 180", got, err)
	}
}

// 05-AC-5: Money prints at most four fractional digits without trailing zeros; WithinLimit is inclusive.
func TestMoneyAndLimit_05AC5(t *testing.T) {
	for in, want := range map[string]string{"0": "0", "9.99": "9.99", "10.0": "10", "0.0137": "0.0137"} {
		r, ok := price.ParseMoney(in)
		if !ok {
			t.Fatalf("ParseMoney(%q) rejected", in)
		}
		if got := price.Money(r); got != want {
			t.Errorf("Money(%s) = %q, want %q", in, got, want)
		}
	}
	if got := price.Money(big.NewRat(2, 3)); got != "0.6667" {
		t.Errorf("Money(2/3) = %q, want 0.6667", got)
	}
	if !price.WithinLimit("25", "25") || price.WithinLimit("25.0001", "25") || !price.WithinLimit("0", "0") || price.WithinLimit("1", "x") {
		t.Error("WithinLimit boundary wrong")
	}
}
