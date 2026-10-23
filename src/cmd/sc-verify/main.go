// Command sc-verify is the RED/GREEN verifier of plan 22 (sections 7.1, 8 and 14.1): `run` observes the suites
// next to the code under test, `emit` frames the sealed observation, `classify` classifies offline.
package main

import (
	"context"
	"os"
	"time"

	"strange.company/sc/internal/verify"
)

// sysClock is the one place sc-verify reads the clock (plan 52 D8); the library only ever sees this interface.
type sysClock struct{ start time.Time }

func (c sysClock) Now() string   { return time.Now().UTC().Format(time.RFC3339) }
func (c sysClock) Millis() int64 { return time.Since(c.start).Milliseconds() }
func (sysClock) Sleep(ms int)    { time.Sleep(time.Duration(ms) * time.Millisecond) }
func (sysClock) WithTimeout(ctx context.Context, seconds int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
}

func main() {
	os.Exit(verify.Main(verify.Deps{
		Clock: sysClock{start: time.Now()}, Args: os.Args[1:], Environ: os.Environ(), Stdout: os.Stdout, Stderr: os.Stderr,
	}))
}
