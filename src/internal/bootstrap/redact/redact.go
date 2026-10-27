// Package redact is the log redactor of the bootstrap engine (plan 50 section 10.2 rule 3, ADR-0101): a registered
// secret value is replaced by the marker «redacted:SC-CLASS» in every byte the engine writes.
package redact

import "io"

// Redactor holds the registered secret values.
//
// SC-STUB(F08 50-AC-8)
type Redactor struct{}

// New returns an empty Redactor.
//
// SC-STUB(F08 50-AC-8)
func New() (r *Redactor) { return }

// Register adds a secret value of the given class (for example SC-OBJECT-STORAGE) before its first use. Besides the
// value itself, its JSON-escaped form is redacted.
//
// SC-STUB(F08 50-AC-8)
func (r *Redactor) Register(class, value string) {}

// String returns s with every registered value replaced by «redacted:<class>».
//
// SC-STUB(F08 50-AC-8)
func (r *Redactor) String(s string) (out string) { return }

// Writer returns a writer that redacts what is written through it, also when a secret is split across Write calls.
// Close flushes the held-back bytes and closes w when it is an io.Closer.
//
// SC-STUB(F08 50-AC-8)
func (r *Redactor) Writer(w io.Writer) (wc io.WriteCloser) { return }
