package statemachine

import (
	"fmt"
	"regexp"
)

// RecordStep is what the Runner knows about one applied step beyond the library's own history.
type RecordStep struct {
	At       string // RFC 3339 time of the transition (the library never reads a clock)
	Actor    string // task.schema.yaml Actor: runner, hermes, human:<login>, ci:woodpecker/<n>
	Reason   string // history reason, at least 3 characters
	EventKey string // idempotency key of the triggering event (plan 29 section 17)
	Pipeline int    // pipeline number of the verification the step started (0 when none)
}

// RecordMeta is everything Record needs besides the Task.
type RecordMeta struct {
	Fields     map[string]any
	Steps      []RecordStep
	ModelTiers map[string]string
	// Generations is one row per generation 1..Snapshot.Generation of the task (tdd.generations); the library
	// does not know the commits, so Record fails closed when a row is missing.
	Generations []RecordGeneration
	// PendingPipeline is the uuid of the pending verification pipeline (claimed_by ci:pending/<uuid>), required
	// in a verification state whose last step has no pipeline number.
	PendingPipeline string
}

// RecordInvalid is the code of a *FireError that Record returns for input that cannot be serialized.
const RecordInvalid = "SM_RECORD_INVALID"

// Record serializes the task as the record view of contracts/task.schema.yaml (plan 29 sections 4 and 6.2, F05
// slice S4, 29-AC-10): a JSON-shaped map (strings, integers, booleans, nil, map[string]any, []any) that must
// validate against the schema and satisfy the record invariants. It never reads a clock: every time comes from
// meta.Steps. It fails closed with a *FireError (RecordInvalid, or SM_FIRE_MACHINE_NIL for a nil machine) and a nil
// map when the history is empty, meta.Steps has a different length than t.History, the last history state is not
// the task's state, or the latest execution's model has no entry in meta.ModelTiers.
//
// The record is meta.Fields (task_id, title, ..., policy; overridden by the derived keys) plus: schema_version,
// state, state_entered_at and updated_at (the last step's At), created_at (the first), version (the number of
// history rows), state_machine_revision, human_state (Project), closed_at in a terminal state, resume_state in
// NEEDS_HUMAN and BLOCKED, needs_human {reason, since} (the step that entered the state), blocked {by, since,
// reason, reason_code}, attempts (every field of the schema, by_class with all four classes), tdd {required,
// current_generation, generations: []}, current_execution (the latest execution: execution_id <task_id>-x<n>,
// role, harness, profile, model, model_tier, attempt, state), claimed_by (execution:<id> while an execution is
// running, ci:woodpecker/<Pipeline of the last step> in a verification state, else null) and one history row per
// step (seq, at, actor, actor_class, from_state null for creation, to_state, event, transition_id, reason,
// reason_code, failure_class, policy_revision and policy_commit from Fields["policy"], state_machine_revision,
// event_key).
func Record(m *Machine, t Task, meta RecordMeta) (map[string]any, error) {
	if m == nil {
		return nil, ferr(FireMachineNil, "the machine is nil")
	}
	bad := func(format string, a ...any) (map[string]any, error) { return nil, ferr(RecordInvalid, format, a...) }
	n := len(t.History)
	if n == 0 {
		return bad("the task has no history (task.schema.yaml needs history)")
	}
	if len(meta.Steps) != n {
		return bad("%d step metas for %d history steps", len(meta.Steps), n)
	}
	snap := t.Snapshot
	if last := t.History[n-1].Applied.To; last != snap.State {
		return bad("the last history state %s is not the task state %s", last, snap.State)
	}
	st := m.findState(snap.State)
	if st == nil {
		return bad("state %q is not a state of the table", snap.State)
	}
	taskID, _ := meta.Fields["task_id"].(string)
	if !taskIDPattern.MatchString(taskID) {
		return bad("Fields[task_id] %q is not a task id (sc-<n>)", taskID)
	}
	policy, _ := meta.Fields["policy"].(map[string]any)
	rev := Revision(m)

	rec := make(map[string]any, len(meta.Fields)+24)
	for k, v := range meta.Fields {
		rec[k] = v
	}
	// history
	hist := make([]any, 0, n)
	for i, step := range t.History {
		ms := meta.Steps[i]
		if len(ms.Reason) < 3 {
			return bad("step %d: the reason %q is shorter than 3 characters", i+1, ms.Reason)
		}
		a := step.Applied
		var from any
		if a.From != "$none" {
			from = a.From
		}
		row := map[string]any{
			"seq": i + 1, "at": ms.At, "actor": ms.Actor, "actor_class": step.Event.Actor, "from_state": from, "to_state": a.To,
			"event": step.Event.Name, "transition_id": a.TransitionID, "reason": ms.Reason, "state_machine_revision": rev,
			"event_key": ms.EventKey, "policy_revision": policy["revision"], "policy_commit": policy["commit"],
		}
		if a.ReasonCode != "" {
			row["reason_code"] = a.ReasonCode
		}
		if a.FailureClass != "" {
			row["failure_class"] = a.FailureClass
		}
		hist = append(hist, row)
	}
	first, last := meta.Steps[0], meta.Steps[n-1]
	// the step that entered the current state: the last one that changed it
	entered := 0
	for i, step := range t.History {
		if step.Applied.From != step.Applied.To {
			entered = i
		}
	}
	enteredAt := meta.Steps[entered].At
	hs, _ := Project(m, snap)
	rec["schema_version"] = "v1alpha1"
	rec["state"] = snap.State
	rec["state_entered_at"] = enteredAt
	rec["created_at"] = first.At
	rec["updated_at"] = last.At
	rec["version"] = n
	rec["state_machine_revision"] = rev
	rec["human_state"] = string(hs)
	rec["history"] = hist
	delete(rec, "closed_at")
	delete(rec, "needs_human")
	delete(rec, "blocked")
	delete(rec, "resume_state")
	if st.terminal {
		rec["closed_at"] = last.At
	}
	held := snap.State == "NEEDS_HUMAN" || snap.State == "BLOCKED"
	if held {
		rec["resume_state"] = snap.ResumeState
	}
	if snap.State == "NEEDS_HUMAN" {
		rec["needs_human"] = map[string]any{"reason": t.NeedsHumanReason, "since": enteredAt}
	}
	if snap.State == "BLOCKED" {
		rec["blocked"] = map[string]any{"by": meta.Steps[entered].Actor, "since": enteredAt, "reason": meta.Steps[entered].Reason, "reason_code": t.BlockedReason}
	}
	// attempts
	at := snap.Attempts
	merit := map[string]any{}
	for k, v := range at.MeritByGeneration {
		merit[k] = v
	}
	rec["attempts"] = map[string]any{
		"autonomous_used": at.AutonomousUsed, "autonomous_max": at.AutonomousMax, "tester_used": at.TesterUsed, "tester_max": at.TesterMax,
		"by_class":            map[string]any{"merit": at.ByClass["merit"], "infrastructure": at.ByClass["infrastructure"], "policy": at.ByClass["policy"], "budget": at.ByClass["budget"]},
		"merit_by_generation": merit, "consecutive_infrastructure": at.ConsecutiveInfrastructure, "consecutive_timeouts": at.ConsecutiveTimeouts,
		"reviews_used": at.ReviewsUsed, "auxiliary_used": at.AuxiliaryUsed, "blocked_results": at.BlockedResults, "conflicts_raised": at.ConflictsRaised,
	}
	if len(meta.Generations) != snap.Generation {
		return bad("%d generation rows in RecordMeta.Generations for generation %d", len(meta.Generations), snap.Generation)
	}
	gens := make([]any, 0, len(meta.Generations))
	for i, g := range meta.Generations {
		switch {
		case g.Generation != i+1:
			return bad("RecordMeta.Generations[%d] is generation %d, want %d", i, g.Generation, i+1)
		case g.Kind != "test" && g.Kind != "judge":
			return bad("generation %d: kind %q is neither test nor judge", g.Generation, g.Kind)
		case g.Status != "active" && g.Status != "superseded" && g.Status != "completed":
			return bad("generation %d: status %q is not active, superseded or completed", g.Generation, g.Status)
		case !gitSHAPattern.MatchString(g.StartCommit):
			return bad("generation %d: start_commit %q is not a 40-hex commit", g.Generation, g.StartCommit)
		case g.ACVersion < 1 || g.StartedAt == "":
			return bad("generation %d: ac_version and started_at are required", g.Generation)
		}
		row := map[string]any{"generation": g.Generation, "kind": g.Kind, "status": g.Status, "start_commit": g.StartCommit,
			"ac_version": g.ACVersion, "started_at": g.StartedAt, "verifications": []any{}}
		if g.SupersededReason != "" {
			row["superseded_reason"] = g.SupersededReason
		}
		gens = append(gens, row)
	}
	rec["tdd"] = map[string]any{"required": snap.Mode == "test-first", "current_generation": snap.Generation, "generations": gens}
	// executions
	delete(rec, "current_execution")
	var claim any
	running := -1
	for i, e := range t.Executions {
		if e.Outcome == "" {
			running = i
		}
	}
	if k := len(t.Executions); k > 0 {
		e := t.Executions[k-1]
		tier := meta.ModelTiers[e.Model]
		if tier == "" {
			return bad("the model %q of the latest execution has no entry in ModelTiers", e.Model)
		}
		attempt := 0
		for _, o := range t.Executions {
			if o.Role == e.Role {
				attempt++
			}
		}
		ref := map[string]any{
			"execution_id": fmt.Sprintf("%s-x%d", taskID, k), "role": e.Role, "kind": executionKind(e), "harness": e.Harness, "model": e.Model,
			"model_tier": tier, "profile": e.Profile, "attempt": attempt, "state": executionState(e.Outcome),
		}
		if e.Fingerprint != "" {
			ref["fingerprint"] = "sha256:" + e.Fingerprint
		}
		if e.Outcome == "merit" || e.Outcome == "infrastructure" || e.Outcome == "policy" || e.Outcome == "budget" {
			ref["failure_class"] = e.Outcome
		}
		rec["current_execution"] = ref
	}
	switch {
	case running >= 0:
		claim = fmt.Sprintf("execution:%s-x%d", taskID, running+1)
	case st.active == "verification":
		if p := last.Pipeline; p > 0 {
			claim = fmt.Sprintf("ci:woodpecker/%d", p)
		} else {
			// the pipeline is not known yet: the Runner's pending pipeline uuid, never a derived one
			if !uuidPattern.MatchString(meta.PendingPipeline) {
				return bad("verification state %s without a pipeline number needs RecordMeta.PendingPipeline (a uuid), got %q", snap.State, meta.PendingPipeline)
			}
			claim = "ci:pending/" + meta.PendingPipeline
		}
	}
	rec["claimed_by"] = claim
	return rec, nil
}

var taskIDPattern = regexp.MustCompile(`^sc-[0-9]+$`)

var (
	gitSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	uuidPattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// executionKind is the ExecutionRef.kind of an execution: a reviewer reviews, any other execution dispatched by
// dispatch_auxiliary (a researcher, a planner) is auxiliary, the rest produces.
func executionKind(e Execution) string {
	switch {
	case e.Role == "reviewer":
		return "reviewing"
	case e.Role == "auxiliary" || e.Dispatch == "dispatch_auxiliary":
		return "auxiliary"
	}
	return "producing"
}

func executionState(outcome string) string {
	switch outcome {
	case "":
		return "RUNNING"
	case "succeeded":
		return "SUCCEEDED"
	case "cancelled":
		return "CANCELLED"
	}
	return "FAILED"
}

// RecordGeneration is the provenance row of one generation as the Runner knows it (task.schema.yaml Generation).
type RecordGeneration struct {
	Generation       int
	Kind             string // test or judge
	Status           string // active, superseded or completed
	StartCommit      string // 40 hex characters
	ACVersion        int
	StartedAt        string // RFC 3339
	SupersededReason string // test_change_approved, strengthened, respec or exemption_denied; "" unless superseded
}
