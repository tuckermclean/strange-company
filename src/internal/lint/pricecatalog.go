package lint

import "time"

// SC-STUB(F09 04-AC-1)
// LocksCheckAt is LocksCheck with an injected clock for the price-catalogue freshness rule.
func LocksCheckAt(root string, now time.Time) (Result, error) {
	return Result{}, nil
}
