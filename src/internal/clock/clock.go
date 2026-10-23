// Package clock is the one place that reads the wall clock (plan 52 D8). Worker code takes a Clock; tests
// inject their own.
package clock

import (
	"context"
	"time"
)

// Clock is the time source of worker code.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
	// Sleep blocks for d, or until ctx is done, in which case it returns ctx.Err().
	Sleep(ctx context.Context, d time.Duration) error
}

// Real is the wall clock.
type Real struct{}

// SC-STUB(F07 27-AC-2)
func (Real) Now() time.Time { return time.Time{} }

// SC-STUB(F07 27-AC-2)
func (Real) Sleep(ctx context.Context, d time.Duration) error { return nil }
