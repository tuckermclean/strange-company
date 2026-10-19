package statemachine

// Codes moved here from the declared stub file: a RED commit may not add a const
// declaration outside test paths (plan 52 section 8.3a); the GREEN commit restores them.

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
