package statemachine

// Retry accounting (plan 29 section 16, F05 slice S3). Declared stubs: the types are the specification of the
// accounting layer, the functions return zero values until GREEN. Everything here is pure (no I/O, no clock, no
// randomness). The layer sits beside Fire, which stays driven by raw Facts:
//
//	AccountingFacts(task, policy, input, base) Facts  answers the counter and replay guards from the task and policy
//	Fire(m, task.Snapshot, ev, thoseFacts)            decides; Applied.Deltas holds the counter changes
//	Advance(m, task, ev, decision, input, policy)     applies the decision: counters, executions, history
//	Apply(...)                                        is the three together; Replay folds recorded steps

// RetryPolicy is the spec.retry section of policy/verification.yaml (contracts/verification.schema.yaml RetryPolicy).
type RetryPolicy struct {
	MaxAutonomousAttempts  int // maxAutonomousAttempts (schema example and platform default: 5)
	MaxTesterAttempts      int // maxTesterAttempts (3)
	MaxReviewExecutions    int // maxReviewExecutions (3)
	MaxAuxiliaryExecutions int // maxAuxiliaryExecutions (8)
	MaxBlockedResults      int // maxBlockedResults (2)
	MaxConsecutive         int // infrastructure.maxConsecutive (3)
	MaxTotal               int // infrastructure.maxTotal (8)
	MaxConsecutiveTimeouts int // infrastructure.maxConsecutiveTimeouts (2)
	MaxPolicyViolations    int // policy.maxViolations (0)
	HumanGrant             int // humanGrant (3)
}

// DispatchSpec is what identifies a dispatch (plan 29 section 16.4): role, harness, resolved model id, profile,
// start commit, acceptance-criteria version, the orchestrator notes (their sha256 enters the fingerprint), the
// context references and the failure signatures delivered as prior_attempt_evidence.
type DispatchSpec struct {
	Role              string
	Harness           string
	Model             string
	Profile           string
	StartCommit       string
	ACVersion         int
	OrchestratorNotes string
	ContextRefs       []string
	FailureSignatures []string
}

// StepInput is what the Runner knows about an event beyond the Event and the Facts: the dispatch being made
// (dispatch events), the terminal failure code of the execution (execution_failed; also answers G_FAILURE_CODE)
// and the failure signature S of a merit or policy failure (green_classified, red_classified, review and
// execution_failed events).
type StepInput struct {
	Dispatch         DispatchSpec
	FailureCode      string
	FailureSignature string
}

// Execution is one execution of the task as the accounting sees it. Outcome is "" while running, "succeeded"
// for an accepted result, else the failure class (merit, infrastructure, policy, budget). Counted says the
// dispatch consumed an attempt; Refunded says the attempt was given back.
type Execution struct {
	Role        string
	Generation  int
	Fingerprint string
	Harness     string
	Model       string
	Profile     string
	NotesDigest string
	Counted     bool
	Refunded    bool
	Outcome     string
	Signature   string
}

// Step is one applied event in the task history.
type Step struct {
	Event   Event
	Input   StepInput
	Applied Applied
}

// Task is a task record as far as the library keeps it: the snapshot Fire reads, the executions and the history
// of applied steps, and the two reason fields (task.schema needs_human.reason and blocked.reason_code).
type Task struct {
	Snapshot         TaskSnapshot
	Executions       []Execution
	History          []Step
	NeedsHumanReason string
	BlockedReason    string
}

// Advance error codes.
const (
	AdvanceNotApplied    = "SM_ADVANCE_NOT_APPLIED"
	AdvanceStateMismatch = "SM_ADVANCE_STATE_MISMATCH"
)

// Fingerprint is F of plan 29 section 16.4: the lowercase hex sha256 of the canonical JSON (keys in ascending
// order, no insignificant whitespace) of {ac_version, context_refs (sorted), failure_signatures (sorted),
// harness, model, notes_sha256 (lowercase hex sha256 of the orchestrator notes), profile, role, start_commit}.
//
// SC-STUB(F05 29-AC-8)
func Fingerprint(d DispatchSpec) string { return "" }

// AccountingFacts answers G_ATTEMPTS_REMAIN, G_NOT_IDENTICAL_REPLAY, G_INFRA_REQUEUE_ALLOWED, G_REVIEWS_REMAIN
// and G_AUXILIARY_REMAIN (always known) from the task, the policy and the step input, answers G_FAILURE_CODE
// from in.FailureCode when that is set, and delegates every other guard to base.
//
// SC-STUB(F05 29-AC-7 29-AC-8)
func AccountingFacts(t Task, p RetryPolicy, in StepInput, base Facts) Facts { return nil }

// Advance applies an Applied decision to the task and returns the new task (the argument is never modified).
//
// SC-STUB(F05 29-AC-7 29-AC-4)
func Advance(m *Machine, t Task, ev Event, d Decision, in StepInput, p RetryPolicy) (Task, error) {
	return Task{}, nil
}

// Apply is AccountingFacts, Fire and Advance in one call. A Rejected decision leaves the task unchanged.
//
// SC-STUB(F05 29-AC-7 29-AC-8 29-AC-4)
func Apply(m *Machine, t Task, ev Event, in StepInput, p RetryPolicy, base Facts) (Decision, Task, error) {
	return Decision{}, Task{}, nil
}

// Replay folds the recorded steps over the initial task with Advance (under the same policy).
//
// SC-STUB(F05 29-AC-4)
func Replay(m *Machine, initial Task, steps []Step, p RetryPolicy) (Task, error) { return Task{}, nil }

// Allowed is the MCP details.allowed list of the rejection (plan 29 section 7.2): ["identical_replay"] when
// G_NOT_IDENTICAL_REPLAY alone failed, else nil.
//
// SC-STUB(F05 29-AC-8)
func (r Rejected) Allowed() []string { return nil }
