// Package price reads the price catalogues of plan 03 section 14.5 (infra/catalog/prices/<provider>.yaml, an
// InfraPriceCatalog) and computes the cost estimate of section 14.6. Money is exact decimal arithmetic (math/big), never a
// binary float; ages are measured against a time the caller passes in.
package price

import (
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Freshness limits of plan 03 section 14.5, in whole days since verifiedAt.
const (
	WarnAfterDays = 90
	FailAfterDays = 180
)

// HoursPerMonth turns an hourly price into a monthly one (plan 03 section 14.6).
const HoursPerMonth = 730

// Entry is one catalogue product.
type Entry struct {
	ProductID    string `yaml:"productId"`
	ResourceType string `yaml:"resourceType"`
	MonthlyPrice string `yaml:"monthlyPrice"`
	HourlyPrice  string `yaml:"hourlyPrice"`
	SetupFee     string `yaml:"setupFee"`
	Commitment   string `yaml:"commitment"`
	TaxIncluded  bool   `yaml:"taxIncluded"`
	VerifiedAt   string `yaml:"verifiedAt"`
}

// Catalog is a parsed InfraPriceCatalog.
type Catalog struct {
	Provider string
	Currency string
	Version  string
	Entries  []Entry
}

// Load parses the catalogue file; a missing or malformed file is an error.
func Load(path string) (*Catalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the price catalogue cannot be read: %w", err)
	}
	var doc struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Version string `yaml:"version"`
		} `yaml:"metadata"`
		Spec struct {
			Provider string  `yaml:"provider"`
			Currency string  `yaml:"currency"`
			Entries  []Entry `yaml:"entries"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("the price catalogue %s is not YAML: %w", path, err)
	}
	if doc.Kind != "InfraPriceCatalog" || len(doc.Spec.Entries) == 0 {
		return nil, fmt.Errorf("%s is not an InfraPriceCatalog with entries", path)
	}
	return &Catalog{Provider: doc.Spec.Provider, Currency: doc.Spec.Currency, Version: doc.Metadata.Version, Entries: doc.Spec.Entries}, nil
}

// Entry returns the product with the id.
func (c *Catalog) Entry(productID string) (Entry, bool) {
	for _, e := range c.Entries {
		if e.ProductID == productID {
			return e, true
		}
	}
	return Entry{}, false
}

// AgeDays is the number of whole calendar days (UTC) between the entry's verifiedAt and now.
func (e Entry) AgeDays(now time.Time) (int, error) {
	v, err := time.Parse("2006-01-02", e.VerifiedAt)
	if err != nil {
		return 0, fmt.Errorf("product %s: verifiedAt %q is not a date", e.ProductID, e.VerifiedAt)
	}
	y, m, d := now.UTC().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return int(today.Sub(v) / (24 * time.Hour)), nil
}

// Item is one priced line of an estimate.
type Item struct {
	ResourceType     string
	ProductID        string
	Count            int
	UnitMonthly      string
	SetupFee         string
	CommitmentPeriod string
	PriceSource      string
}

// Estimate is the cost estimate of plan 03 section 14.6.
type Estimate struct {
	Currency         string
	MonthlyTotal     string
	CommittedMinimum string
	Complete         bool
	CatalogVersion   string
	Items            []Item
}

// Zero is the estimate of an unpaid provider: nothing is created that costs money (fake, existing).
func Zero(currency string) Estimate {
	return Estimate{Currency: currency, MonthlyTotal: "0", CommittedMinimum: "0", Complete: true, CatalogVersion: "none", Items: []Item{}}
}

// Money formats a non-negative amount as a decimal string of at most four fractional digits without trailing zeros.
func Money(r *big.Rat) string {
	s := r.FloatString(4)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// ParseMoney parses a decimal string.
func ParseMoney(s string) (*big.Rat, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "eE/+-") {
		return nil, false
	}
	return new(big.Rat).SetString(s)
}

// commitmentMonths reads the months of an ISO-8601 commitment (P1M, P12M, P1Y); other periods count as one month.
func commitmentMonths(p string) int64 {
	var n int64
	var unit byte
	if _, err := fmt.Sscanf(p, "P%d%c", &n, &unit); err == nil && n > 0 {
		switch unit {
		case 'M':
			return n
		case 'Y':
			return 12 * n
		}
	}
	return 1
}

// Compute prices count nodes of productID: monthly_total = count x unitMonthly (hourly x 730 for hourly-billed products),
// committed_minimum = count x unitMonthly x commitment months + count x setup fee. An unknown product or an
// unparseable price makes the estimate incomplete.
func (c *Catalog) Compute(productID string, count int) Estimate {
	est := Estimate{Currency: c.Currency, MonthlyTotal: "0", CommittedMinimum: "0", CatalogVersion: c.Version, Items: []Item{}}
	e, ok := c.Entry(productID)
	if !ok {
		return est
	}
	unit, ok := e.unitMonthly()
	setup, ok2 := ParseMoney(e.SetupFee)
	if !ok || !ok2 {
		return est
	}
	n := new(big.Rat).SetInt64(int64(count))
	total := new(big.Rat).Mul(n, unit)
	committed := new(big.Rat).Mul(total, new(big.Rat).SetInt64(commitmentMonths(e.Commitment)))
	committed.Add(committed, new(big.Rat).Mul(n, setup))
	est.MonthlyTotal, est.CommittedMinimum, est.Complete = Money(total), Money(committed), true
	est.Items = []Item{{ResourceType: e.ResourceType, ProductID: e.ProductID, Count: count, UnitMonthly: Money(unit),
		SetupFee: Money(setup), CommitmentPeriod: e.Commitment, PriceSource: "catalog"}}
	return est
}

func (e Entry) unitMonthly() (*big.Rat, bool) {
	if e.MonthlyPrice != "" {
		return ParseMoney(e.MonthlyPrice)
	}
	h, ok := ParseMoney(e.HourlyPrice)
	if !ok {
		return nil, false
	}
	return h.Mul(h, new(big.Rat).SetInt64(HoursPerMonth)), true
}

// ProductIDs lists the catalogue's products, sorted (for messages).
func (c *Catalog) ProductIDs() []string {
	var ids []string
	for _, e := range c.Entries {
		ids = append(ids, e.ProductID)
	}
	sort.Strings(ids)
	return ids
}

// WithinLimit reports whether total <= limit; an unparseable limit is not within.
func WithinLimit(total, limit string) bool {
	t, ok := ParseMoney(total)
	l, ok2 := ParseMoney(limit)
	return ok && ok2 && t.Cmp(l) <= 0
}
