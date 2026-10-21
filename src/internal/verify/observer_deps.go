package verify

import (
	"context"
	"io"
)

// Clock is the time the observer needs. The library never reads the clock itself (plan 52 D8): the main package
// supplies one. Now is an RFC 3339 UTC timestamp, Millis a monotonic millisecond counter, Sleep waits, WithTimeout
// derives a context that ends after the given seconds.
type Clock interface {
	Now() string
	Millis() int64
	Sleep(ms int)
	WithTimeout(ctx context.Context, seconds int) (context.Context, context.CancelFunc)
}

// Deps is what the sc-verify main package hands to the library: the clock (plan 52 D8), the process arguments and
// environment, and the output streams.
type Deps struct {
	Clock   Clock
	Args    []string // os.Args without the program name
	Environ []string
	Stdout  io.Writer
	Stderr  io.Writer
}
