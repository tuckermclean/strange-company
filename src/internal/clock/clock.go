// Package clock is the one place that reads the wall clock (plan 52 D8). Worker code takes a Clock; tests
// inject their own.
package clock

import (
	"context"
	"sync"
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

// Now returns the current wall-clock instant.
func (Real) Now() time.Time { return time.Now() }

// Sleep blocks for d or until ctx is done.
func (Real) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Fake is a manually driven clock for tests: Sleep advances it by the slept duration and returns at once, so
// retry and backoff logic runs without waiting. It is safe for concurrent use.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a Fake whose current instant is start.
func NewFake(start time.Time) *Fake { return &Fake{now: start} }

// Now returns the fake instant.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the fake instant forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

// Sleep advances the clock by d unless ctx is already done.
func (f *Fake) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.Advance(d)
	return nil
}
