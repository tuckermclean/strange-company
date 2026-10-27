// Package commit creates the generated commits of plan 50 section 13 and enforces their scope.
package commit

// Kind is the kind of generated commit.
//
// SC-STUB(F08 50-AC-14)
type Kind int

// SC-STUB(F08 50-AC-14)
const (
	Install Kind = iota
	Recovery
)
