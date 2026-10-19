// Package statemachine is the pure task state-machine library (plan 29, F05).
package statemachine

import "io"

// Error codes returned by Load and Check (plan 29 section 8.2).
const (
	CodeSchemaInvalid          = "SM_SCHEMA_INVALID"
	CodeSchemaUnavailable      = "SM_SCHEMA_UNAVAILABLE"
	CodeUnreadable             = "SM_UNREADABLE"
	CodeYAMLInvalid            = "SM_YAML_INVALID"
	CodeYAMLAnchor             = "SM_YAML_ANCHOR"
	CodeStateUndeclared        = "SM_STATE_UNDECLARED"
	CodeReturnTargetUndeclared = "SM_RETURN_TARGET_UNDECLARED"
	CodeProjectionStateUndecl  = "SM_PROJECTION_STATE_UNDECLARED"
	CodeInitialStateInvalid    = "SM_INITIAL_STATE_INVALID"
	CodeTerminalMismatch       = "SM_TERMINAL_MISMATCH"
	CodeGuardUndeclared        = "SM_GUARD_UNDECLARED"
	CodeEffectUndeclared       = "SM_EFFECT_UNDECLARED"
	CodeGuardUnknown           = "SM_GUARD_UNKNOWN"
	CodeEffectUnknown          = "SM_EFFECT_UNKNOWN"
	CodeEventUndeclared        = "SM_EVENT_UNDECLARED"
	CodeTransitionIDDuplicate  = "SM_TRANSITION_ID_DUPLICATE"
	CodeUnreachable            = "SM_UNREACHABLE"
	CodeNonTerminating         = "SM_NON_TERMINATING"
	CodeTerminalHasOutgoing    = "SM_TERMINAL_HAS_OUTGOING"
	CodeReturnFromNonExecution = "SM_RETURN_FROM_NON_EXECUTION"
	CodeHeldExitNotHuman       = "SM_HELD_EXIT_NOT_HUMAN"
	CodeHumanTargetsExecution  = "SM_HUMAN_TARGETS_EXECUTION"
	CodeProjectionUncovered    = "SM_PROJECTION_UNCOVERED"
	CodeStallEscalateInvalid   = "SM_STALL_ESCALATE_INVALID"
	CodeStallNoTransition      = "SM_STALL_NO_TRANSITION"
	CodeHumanCommandMissing    = "SM_HUMAN_COMMAND_MISSING"
	CodeOutcomeIncomplete      = "SM_OUTCOME_INCOMPLETE"
	CodeFailureClassIncomplete = "SM_FAILURE_CLASS_INCOMPLETE"
)

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
