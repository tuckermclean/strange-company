// Package engine is the bootstrap phase engine (plan 50 sections 8, 16 and 17): the journal with its local and S3
// copies, the run lock, the phase runner with input digests, freeze rules, retry ceiling and the crash hook, and
// `status`.
package engine

// CheckState is the outcome of a Check.
//
// SC-STUB(F08 50-AC-16 50-AC-17)
type CheckState int

// The Check outcomes.
//
// SC-STUB(F08 50-AC-16 50-AC-17)
const (
	NotDone CheckState = iota
	Done
	Drifted
)
