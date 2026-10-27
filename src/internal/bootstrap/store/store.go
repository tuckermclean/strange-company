// Package store is the object-store interface of the bootstrap engine (plan 50 sections 17.3 and 17.4): the journal
// and the run lock live in S3, reached through this small interface. A fake backs the unit tests; the SigV4 client
// arrives with plan 06 (F08 S4).
package store

import "context"

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

// Error is a store failure of one Kind.
//
// SC-STUB(F08 50-AC-5 50-AC-6 50-AC-17)
type Error struct {
	Kind Kind
	Msg  string
}

// Error returns the message.
//
// SC-STUB(F08 50-AC-5 50-AC-6 50-AC-17)
func (e *Error) Error() string { return "" }

// Is reports whether err is a store Error of kind k.
//
// SC-STUB(F08 50-AC-5 50-AC-6 50-AC-17)
func Is(err error, k Kind) (ok bool) { return }

// Object is one stored object.
//
// SC-STUB(F08 50-AC-5 50-AC-6 50-AC-17)
type Object struct {
	Data []byte
	ETag string
}

// S3 is the subset of S3 the engine needs. PutIfAbsent is the conditional create (If-None-Match: *); a non-empty
// ifMatch on Put or Delete is a conditional write (If-Match) and must only be sent to a store that supports it.
//
// SC-STUB(F08 50-AC-5 50-AC-6 50-AC-17)
type S3 interface {
	Get(ctx context.Context, key string) (Object, error)
	PutIfAbsent(ctx context.Context, key string, data []byte) (etag string, err error)
	Put(ctx context.Context, key string, data []byte, ifMatch string) (etag string, err error)
	List(ctx context.Context, prefix string) ([]string, error)
	Delete(ctx context.Context, key string, ifMatch string) error
}
