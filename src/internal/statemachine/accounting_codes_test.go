package statemachine

// Codes moved here from the declared stub file: a RED commit may not add a const
// declaration outside test paths (plan 52 section 8.3a); the GREEN commit restores them.

// Advance error codes.
const (
	AdvanceNotApplied    = "SM_ADVANCE_NOT_APPLIED"
	AdvanceStateMismatch = "SM_ADVANCE_STATE_MISMATCH"
)
