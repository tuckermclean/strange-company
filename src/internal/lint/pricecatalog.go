package lint

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"strange.company/sc/internal/clock"
)

// Rule IDs of the price-catalogue checks of `sc-lint locks` (plan 01 section 6.3 check 5, plan 04 04-AC-1).
const (
	rulePriceFresh  = "price-catalog-fresh"
	rulePriceSchema = "price-catalog-schema"
)

// priceMaxAgeDays is the oldest verifiedAt a merged catalogue may carry (plan 04 04-AC-1).
const priceMaxAgeDays = 90

// LocksCheck evaluates the version-lock rules of plan 01 section 6.3 on the tree under root, against the wall clock.
func LocksCheck(root string) (Result, error) {
	return LocksCheckAt(root, clock.Real{}.Now())
}

// LocksCheckAt is LocksCheck with an injected clock for the price-catalogue freshness rule.
func LocksCheckAt(root string, now time.Time) (Result, error) {
	r, err := locksCheck(root)
	if err != nil {
		return r, err
	}
	err = checkPriceCatalogs(root, now, &r)
	return r, err
}

func checkPriceCatalogs(root string, now time.Time, r *Result) error {
	r.ran(rulePriceSchema)
	r.ran(rulePriceFresh)
	files, err := filepath.Glob(filepath.Join(root, "infra/catalog/prices/*.yaml"))
	if err != nil || len(files) == 0 {
		return err
	}
	sort.Strings(files)
	var items []string
	for _, f := range files {
		items = append(items, f+":InfraPriceCatalog")
	}
	vs, err := validateSchema(root, "provider.schema.yaml", "InfraPriceCatalog", items)
	if err != nil {
		return err
	}
	for _, v := range vs {
		rel, _ := filepath.Rel(root, v.File)
		r.add(rulePriceSchema, filepath.ToSlash(rel), "%s: %s", v.Path, v.Message)
	}
	today := now.UTC().Truncate(24 * time.Hour)
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		doc, err := readYAML(root, rel)
		if err != nil {
			continue // reported by the schema rule
		}
		entries, _ := at(doc, "spec", "entries").([]any)
		for _, e := range entries {
			id, v := str(at(e, "productId")), strings.TrimSpace(str(at(e, "verifiedAt")))
			t, err := time.Parse("2006-01-02", v)
			if err != nil {
				continue // reported by the schema rule (format: date)
			}
			if age := int(today.Sub(t) / (24 * time.Hour)); age > priceMaxAgeDays {
				r.add(rulePriceFresh, rel, "%s: verifiedAt %s is %d days old (maximum %d)", id, v, age, priceMaxAgeDays)
			}
		}
	}
	return nil
}
