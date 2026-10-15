// Package lint implements the repository static checks behind cmd/sc-lint (plan 01 section 6.2).
package lint

import "io"

// Finding is one rule violation. Severity is "error" or "warning".
type Finding struct {
	Rule     string
	Path     string
	Message  string
	Severity string
}

// Result lists the rule IDs that were evaluated and what they found.
type Result struct {
	Rules    []string
	Findings []Finding
}

// SC-STUB(F01 01-AC-1 01-AC-10 01-AC-14)
func (f Finding) String() string { return "" }

// SC-STUB(F01 01-AC-1 01-AC-10 01-AC-12)
func LayoutCheck(root string) (Result, error) { return Result{}, nil }

// SC-STUB(F01 01-AC-14)
func DagCheck(root string) (Result, error) { return Result{}, nil }

// SC-STUB(F01 01-AC-1 01-AC-14)
func Main(args []string, stdout, stderr io.Writer) int { return 0 }
