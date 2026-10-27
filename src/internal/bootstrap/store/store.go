// Package store is the object-store interface of the bootstrap engine (plan 50 sections 17.3 and 17.4): the journal
// and the run lock live in S3, reached through this small interface. A fake backs the unit tests; the SigV4 client
// arrives with plan 06 (F08 S4).
package store

// Kind classifies a store error.
//
// SC-STUB(F08 50-AC-5 50-AC-6 50-AC-17)
type Kind int

// The error kinds a store returns.
//
// SC-STUB(F08 50-AC-5 50-AC-6 50-AC-17)
const (
	NotFound Kind = iota + 1
	Exists
	PreconditionFailed
	Unsupported
	Unavailable
)
