package statemachine

// This file declares the evaluation API of plan 29 section 7.1 (F05 slice S2). The function and method bodies
// are declared stubs that only return zero values; the evaluation behavior is specified by the tests.

// Attempts is the retry accounting of a task (contracts/task.schema.yaml Attempts, plan 29 section 16.1).
type Attempts struct {
	AutonomousUsed, AutonomousMax int
	TesterUsed, TesterMax         int
	MeritByGeneration             map[string]int // "<role>:g<generation>"
	ByClass                       map[string]int // merit, infrastructure, policy, budget
	ConsecutiveInfrastructure     int
	ConsecutiveTimeouts           int
	ReviewsUsed, AuxiliaryUsed    int
	BlockedResults                int
	ConflictsRaised               int
}

// TaskSnapshot is the part of a task record the library reads. State is "" for a task that does not exist yet
// (creation). ReturnState is the dispatch-ready state an active execution came from ($return); ResumeState is
// the recorded resume_state ($resume).
type TaskSnapshot struct {
	Mode                     string
	State                    string
	ReturnState              string
	ResumeState              string
	Generation               int
	Attempts                 Attempts
	ExecutionDispatched      bool // an execution of the task was ever dispatched (projection: Ready)
	ConflictAwaitingApproval bool // the open conflict awaits human approval (projection: Needs Human)
}

// Event is an incoming event: its name, the actor class (runner, ci, hermes, human) and, for dispatch events,
// the role being dispatched (it selects role-dependent facts such as G_ROLE_AUXILIARY_HERE).
type Event struct {
	Name  string
	Actor string
	Role  string
}

// Facts is what the Runner knows and the library cannot derive (Git facts, evidence classification, policy
// values, counters). Bool answers a boolean guard by its identifier; Value answers a valued guard
// (G_VERIFICATION_OUTCOME, G_FAILURE_CLASS, G_FAILURE_CODE, G_REFUSAL_CODE, G_REVIEW_VERDICT, G_JUDGE_SOURCE,
// G_APPROVAL) with the actual value the guard's arguments are compared with. known=false means the fact is
// missing; the guard then evaluates false (fail closed).
type Facts interface {
	Bool(ev Event, guard string) (value, known bool)
	Value(ev Event, guard string) (value string, known bool)
}

// Applied is a fired transition. Effects are the transition's listed effects (implicit effects are not
// repeated). FailureClass is the argument of the transition's E_COUNT_FAILURE effect, ReasonCode the argument
// of its E_NOTIFY_HUMAN effect ("" when absent). Deltas are the counter changes (slice S3).
type Applied struct {
	TransitionID string
	From         string // the snapshot state, or "$none" for creation
	To           string // pseudo-targets resolved
	Effects      []string
	FailureClass string
	ReasonCode   string
	Deltas       map[string]int
}

// Rejected is an event that fires nothing. Code is an MCP error code (plan 29 section 7.2); FailedGuards lists
// the failed guard identifiers (without arguments) of every candidate, in candidate id then listed order,
// without duplicates.
type Rejected struct {
	Code         string
	FailedGuards []string
}

// Decision holds exactly one non-nil member on success of Fire.
type Decision struct {
	Applied  *Applied
	Rejected *Rejected
}

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

// FireError is a malformed Fire input.
type FireError struct {
	Code    string
	Message string
}

// SC-STUB(F05 29-AC-3)
func (e *FireError) Error() string { return "" }

// Role is a dispatchable role (researcher, planner, tester, implementer, reviewer).
type Role string

// HumanState is a human projection state (plan 29 section 14.13).
type HumanState string

// SC-STUB(F05 29-AC-3 29-AC-5 29-AC-6)
func Fire(m *Machine, snap TaskSnapshot, ev Event, facts Facts) (Decision, error) {
	return Decision{}, nil
}

// SC-STUB(F05 29-AC-3)
func NextAllowedRoles(m *Machine, snap TaskSnapshot, facts Facts) []Role { return nil }

// SC-STUB(F05 29-AC-9)
func Project(m *Machine, snap TaskSnapshot) (HumanState, string) { return "", "" }

// Guards is the library's guard implementation set; it implements GuardRegistry.
type Guards struct{}

// SC-STUB(F05 29-AC-3)
func (Guards) HasGuard(id string) bool { return false }

// SC-STUB(F05 29-AC-3)
func (Guards) GuardIDs() []string { return nil }

// Effects is the library's effect implementation set; it implements EffectRegistry.
type Effects struct{}

// SC-STUB(F05 29-AC-3)
func (Effects) HasEffect(id string) bool { return false }

// SC-STUB(F05 29-AC-3)
func (Effects) EffectIDs() []string { return nil }
