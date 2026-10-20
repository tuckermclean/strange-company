package statemachine

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
}

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
//
// SC-STUB(F05 29-AC-10)
func Record(m *Machine, t Task, meta RecordMeta) (map[string]any, error) { return nil, nil }
