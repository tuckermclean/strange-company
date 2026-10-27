// Package store is the object-store interface of the bootstrap engine (plan 50 sections 17.3 and 17.4): the journal
// and the run lock live in S3, reached through this small interface. A fake backs the unit tests; the SigV4 client
// arrives with plan 06 (F08 S4).
package store

import (
	"context"
	"errors"
)

// Kind classifies a store error.
type Kind int

// The error kinds a store returns.
const (
	NotFound Kind = iota + 1
	Exists
	PreconditionFailed
	Unsupported
	Unavailable
)

// Error is a store failure of one Kind.
type Error struct {
	Kind Kind
	Msg  string
}

// Error returns the message.
func (e *Error) Error() string {
	if e.Msg == "" {
		return e.Kind.String()
	}
	return e.Kind.String() + ": " + e.Msg
}

// String names the kind.
func (k Kind) String() string {
	switch k {
	case NotFound:
		return "not found"
	case Exists:
		return "already exists"
	case PreconditionFailed:
		return "precondition failed"
	case Unsupported:
		return "unsupported"
	case Unavailable:
		return "unavailable"
	}
	return "store error"
}

// Is reports whether err is a store Error of kind k.
func Is(err error, k Kind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == k
}

// Object is one stored object.
type Object struct {
	Data []byte
	ETag string
}

// S3 is the subset of S3 the engine needs. PutIfAbsent is the conditional create (If-None-Match: *); a non-empty
// ifMatch on Put or Delete is a conditional write (If-Match) and must only be sent to a store that supports it.
type S3 interface {
	Get(ctx context.Context, key string) (Object, error)
	PutIfAbsent(ctx context.Context, key string, data []byte) (etag string, err error)
	Put(ctx context.Context, key string, data []byte, ifMatch string) (etag string, err error)
	List(ctx context.Context, prefix string) ([]string, error)
	Delete(ctx context.Context, key string, ifMatch string) error
}
