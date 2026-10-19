package statemachine

// Codes moved here from the declared stub file: a RED commit may not add a const
// declaration outside test paths (plan 52 section 8.3a); the GREEN commit restores them.

// Rejection codes.
const (
	CodeGateNotSatisfied = "GATE_NOT_SATISFIED"
	CodeAlreadyRunning   = "ALREADY_RUNNING"
	CodeAttemptsExhaust  = "ATTEMPTS_EXHAUSTED"
)

// Fire error codes: malformed input fails closed with a *FireError and a zero Decision.
const (
	FireMachineNil    = "SM_FIRE_MACHINE_NIL"
	FireFactsNil      = "SM_FIRE_FACTS_NIL"
	FireUnknownMode   = "SM_FIRE_UNKNOWN_MODE"
	FireUnknownState  = "SM_FIRE_UNKNOWN_STATE"
	FireUnknownEvent  = "SM_FIRE_UNKNOWN_EVENT"
	FireUnknownActor  = "SM_FIRE_UNKNOWN_ACTOR"
	FireReturnInvalid = "SM_FIRE_RETURN_INVALID"
	FireResumeInvalid = "SM_FIRE_RESUME_INVALID"
)
