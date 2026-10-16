// Package version carries the build identity that `make build` injects with -ldflags -X (plan 01 section 6.2).
package version

// Version is `git describe` of the built commit; Commit is its SHA. Both are set at link time.
var (
	Version = "dev"
	Commit  = "unknown"
)
