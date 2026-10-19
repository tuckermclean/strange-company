// Package statemachine is the pure task state-machine library (plan 29, F05).
package statemachine

import "io"

// GuardRegistry knows every guard identifier the library implements (the part after ":" is not part of the id).
type GuardRegistry interface{ HasGuard(id string) bool }

// EffectRegistry knows every effect identifier the library implements.
type EffectRegistry interface{ HasEffect(id string) bool }

// Options configures Load and Check. ContractsDir holds state-machine.schema.yaml (the single source).
type Options struct{ ContractsDir string }

// LoadError is a load failure: Code is one of the Code constants; Mode is set for per-mode graph failures.
type LoadError struct {
	Code    string
	Pointer string
	Mode    string
	Message string
}

// Machine is a loaded, validated transition table.
type Machine struct{}

// ModeReport is the reachability and termination result for one verification mode.
type ModeReport struct {
	Mode           string
	States         int
	Reachable      int
	Unreachable    []string
	NonTerminating []string
}

// Report holds one ModeReport per mode, in table order.
type Report struct{ Modes []ModeReport }

// SC-STUB(F05 29-AC-2)
func (e *LoadError) Error() string { return "" }

// SC-STUB(F05 29-AC-1 29-AC-2)
func Load(table []byte, guards GuardRegistry, effects EffectRegistry, opts Options) (*Machine, error) {
	return nil, nil
}

// SC-STUB(F05 29-AC-1)
func Revision(m *Machine) int { return 0 }

// SC-STUB(F05 29-AC-1)
func (m *Machine) Report() Report { return Report{} }

// SC-STUB(F05 29-AC-1)
func Check(w io.Writer, tablePath string, opts Options) error { return nil }
