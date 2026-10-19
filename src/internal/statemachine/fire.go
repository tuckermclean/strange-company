package statemachine

import (
	"fmt"
	"sort"
)

// This file implements the evaluation API of plan 29 section 7.1 (F05 slice S2): Fire (section 8.1), NextAllowedRoles
// and Project. Everything here is pure: no I/O, no clock, no randomness. It runs on the elevated path and fails
// closed: unknown identifiers, missing facts and malformed input never pass.

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

// Error renders the code and message.
func (e *FireError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + " " + e.Message
}

func ferr(code, format string, a ...any) error {
	return &FireError{Code: code, Message: fmt.Sprintf(format, a...)}
}

// Role is a dispatchable role (researcher, planner, tester, implementer, reviewer).
type Role string

// HumanState is a human projection state (plan 29 section 14.13).
type HumanState string

// dispatchEvents are the events of MCP agent_start (plan 29 section 7.2); only they map a failed gate to
// ALREADY_RUNNING or ATTEMPTS_EXHAUSTED.
var dispatchEvents = map[string]bool{
	"dispatch_tester": true, "dispatch_implementer": true, "dispatch_producer": true,
	"dispatch_reviewer": true, "dispatch_auxiliary": true,
}

// attemptGuards are the guards that map to ATTEMPTS_EXHAUSTED when they alone fail.
var attemptGuards = map[string]bool{"G_ATTEMPTS_REMAIN": true, "G_REVIEWS_REMAIN": true, "G_AUXILIARY_REMAIN": true, "G_NOT_IDENTICAL_REPLAY": true}

// Fire evaluates ev against snap (plan 29 section 8.1). Candidates are the transitions whose from contains the
// snapshot state ("$none" for a task that does not exist yet), whose event matches and whose modes contain the
// task's mode, narrowed to those that list the actor class (step 1); when only that narrowing empties the set the
// event is Rejected naming G_ACTOR_AUTHORIZED alone. For a dispatch event a non-empty Event.Role must map to the
// event (section 7.2) and be one of the state's dispatchableRoles, else the event is Rejected. The remaining
// candidates are taken in ascending id order; every guard of every candidate is evaluated (the implicit
// G_ACTOR_AUTHORIZED first); the first candidate whose guards all pass fires, with its counter deltas
// (Applied.Deltas). Otherwise the event is Rejected, with the code and the failed guards computed from the
// actor-filtered candidates only. Each fact is asked of facts at most once per call (the answer is cached), so
// facts that answer inconsistently cannot produce a decision that no single fact set yields. Malformed input is a
// *FireError and the zero Decision. Fire reads nothing but its arguments and never modifies them. A Facts
// implementation that panics propagates the panic out of Fire; nothing has been applied, because Fire applies
// nothing (Advance does).
func Fire(m *Machine, snap TaskSnapshot, ev Event, facts Facts) (Decision, error) {
	if m == nil {
		return Decision{}, ferr(FireMachineNil, "the machine is nil")
	}
	if facts == nil {
		return Decision{}, ferr(FireFactsNil, "the facts are nil")
	}
	if !has(m.modes, snap.Mode) {
		return Decision{}, ferr(FireUnknownMode, "mode %q is not a mode of the table", snap.Mode)
	}
	var cur *state
	if snap.State != "" {
		if cur = m.findState(snap.State); cur == nil || !has(cur.modes, snap.Mode) {
			return Decision{}, ferr(FireUnknownState, "state %q does not exist in mode %s", snap.State, snap.Mode)
		}
	}
	if !m.events[ev.Name] {
		return Decision{}, ferr(FireUnknownEvent, "event %q is not declared by the table", ev.Name)
	}
	if !has(m.actors, ev.Actor) {
		return Decision{}, ferr(FireUnknownActor, "actor class %q is not one of %v", ev.Actor, m.actors)
	}
	from := snap.State
	if cur == nil {
		from = "$none"
	}
	var cands []*transition
	listed := false
	for i := range m.transitions {
		t := &m.transitions[i]
		if t.event == ev.Name && has(t.modes, snap.Mode) && has(t.from, from) {
			listed = true
			if has(t.actors, ev.Actor) {
				cands = append(cands, t)
			}
		}
	}
	if listed && len(cands) == 0 {
		return Decision{Rejected: &Rejected{Code: CodeGateNotSatisfied, FailedGuards: []string{"G_ACTOR_AUTHORIZED"}}}, nil
	}
	if dispatchEvents[ev.Name] && ev.Role != "" && (cur == nil || roleEvent(ev.Role, snap.State, snap.Mode) != ev.Name || !has(cur.dispatchable, ev.Role)) {
		return Decision{Rejected: &Rejected{Code: CodeGateNotSatisfied, FailedGuards: []string{"G_DISPATCH_PREREQUISITES"}}}, nil
	}
	facts = newFactCache(facts)
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].id < cands[j].id })
	var failed []string
	var winner *transition
	for _, t := range cands {
		bad := failedGuards(t, ev, facts)
		if len(bad) == 0 {
			winner = t
			break
		}
		for _, g := range bad {
			if !has(failed, g) {
				failed = append(failed, g)
			}
		}
	}
	if winner == nil {
		return Decision{Rejected: &Rejected{Code: rejectionCode(ev.Name, failed), FailedGuards: failed}}, nil
	}
	to, err := m.target(winner, cur, snap)
	if err != nil {
		return Decision{}, err
	}
	a := &Applied{TransitionID: winner.id, From: from, To: to, Effects: append([]string(nil), winner.effects...)}
	a.FailureClass = effectArg(winner.effects, "E_COUNT_FAILURE")
	a.ReasonCode = effectArg(winner.effects, "E_NOTIFY_HUMAN")
	a.Deltas = m.deltas(winner, cur, snap, ev, facts)
	return Decision{Applied: a}, nil
}

// factCache answers each fact of the wrapped Facts at most once.
type factCache struct {
	f Facts
	b map[string]boolAnswer
	v map[string]valueAnswer
}

type boolAnswer struct{ value, known bool }

type valueAnswer struct {
	value string
	known bool
}

func newFactCache(f Facts) *factCache {
	return &factCache{f: f, b: map[string]boolAnswer{}, v: map[string]valueAnswer{}}
}

func (c *factCache) Bool(ev Event, guard string) (bool, bool) {
	if a, ok := c.b[guard]; ok {
		return a.value, a.known
	}
	v, k := c.f.Bool(ev, guard)
	c.b[guard] = boolAnswer{v, k}
	return v, k
}

func (c *factCache) Value(ev Event, guard string) (string, bool) {
	if a, ok := c.v[guard]; ok {
		return a.value, a.known
	}
	v, k := c.f.Value(ev, guard)
	c.v[guard] = valueAnswer{v, k}
	return v, k
}

// failedGuards evaluates every guard of t (implicit G_ACTOR_AUTHORIZED first) and returns the failed ids in order.
func failedGuards(t *transition, ev Event, facts Facts) []string {
	var bad []string
	add := func(id string) {
		if !has(bad, id) {
			bad = append(bad, id)
		}
	}
	if v, known := facts.Bool(ev, "G_ACTOR_AUTHORIZED"); !has(t.actors, ev.Actor) || !known || !v {
		add("G_ACTOR_AUTHORIZED")
	}
	for _, ref := range t.guards {
		if !guardHolds(ref, ev, facts) {
			add(idOf(ref))
		}
	}
	return bad
}

// guardHolds evaluates one guard reference. A missing fact (known=false), an unknown identifier and an argument
// on a boolean guard all evaluate false.
func guardHolds(ref string, ev Event, facts Facts) bool {
	id, args := idOf(ref), argsOf(ref)
	switch base, derived := derivedGuards[id]; {
	case valuedGuards[id]:
		v, known := facts.Value(ev, id)
		return known && v != "" && has(args, v)
	case derived:
		v, known := facts.Bool(ev, base)
		return len(args) == 0 && known && !v
	case guardSet[id]:
		v, known := facts.Bool(ev, id)
		return len(args) == 0 && known && v
	}
	return false
}

func rejectionCode(event string, failed []string) string {
	if !dispatchEvents[event] || len(failed) == 0 {
		return CodeGateNotSatisfied
	}
	if len(failed) == 1 && failed[0] == "G_NO_ACTIVE_EXECUTION" {
		return CodeAlreadyRunning
	}
	for _, g := range failed {
		if !attemptGuards[g] {
			return CodeGateNotSatisfied
		}
	}
	return CodeAttemptsExhaust
}

// effectArg returns the first argument of the first effect with the given id ("" when absent).
func effectArg(effects []string, id string) string {
	for _, e := range effects {
		if idOf(e) == id {
			if a := argsOf(e); len(a) > 0 {
				return a[0]
			}
		}
	}
	return ""
}

func (m *Machine) findState(name string) *state {
	for i := range m.states {
		if m.states[i].name == name {
			return &m.states[i]
		}
	}
	return nil
}

// target resolves the transition's target: $same is the snapshot state, $return the snapshot's ReturnState (one
// of the state's returnTargets that exists in the mode) and $resume the recorded ResumeState (a declared,
// non-held, non-terminal, non-execution state of the mode).
func (m *Machine) target(t *transition, cur *state, snap TaskSnapshot) (string, error) {
	switch t.to {
	case "$same":
		return snap.State, nil
	case "$return":
		rs := m.findState(snap.ReturnState)
		if cur == nil || rs == nil || !has(cur.returnTargets, snap.ReturnState) || !has(rs.modes, snap.Mode) {
			return "", ferr(FireReturnInvalid, "%s: ReturnState %q is not a return target of %s in mode %s", t.id, snap.ReturnState, snap.State, snap.Mode)
		}
		return snap.ReturnState, nil
	case "$resume":
		rs := m.findState(snap.ResumeState)
		if rs == nil || isHeld(rs.name) || rs.terminal || rs.active == "required" || !has(rs.modes, snap.Mode) {
			return "", ferr(FireResumeInvalid, "%s: ResumeState %q is not a resumable state in mode %s", t.id, snap.ResumeState, snap.Mode)
		}
		return snap.ResumeState, nil
	}
	return t.to, nil
}

// roleEvent maps an agent_start of the role to its dispatch event (plan 29 section 7.2): tester to
// dispatch_tester, implementer to dispatch_implementer in test-first, implementer and researcher to
// dispatch_producer in verification-first from JUDGE_APPROVED, CHANGES_REQUESTED and TEST_SPEC_CONFLICT,
// reviewer from GREEN_VERIFIED to dispatch_reviewer, and every other role and state to dispatch_auxiliary.
// (The producerRoles check is the G_PRODUCER_ROLE_ALLOWED fact.)
func roleEvent(role, state, mode string) string {
	switch {
	case role == "tester":
		return "dispatch_tester"
	case role == "implementer" && mode == "test-first":
		return "dispatch_implementer"
	case (role == "implementer" || role == "researcher") && mode == "verification-first" &&
		(state == "JUDGE_APPROVED" || state == "CHANGES_REQUESTED" || state == "TEST_SPEC_CONFLICT"):
		return "dispatch_producer"
	case role == "reviewer" && state == "GREEN_VERIFIED":
		return "dispatch_reviewer"
	}
	return "dispatch_auxiliary"
}

// NextAllowedRoles returns, in ascending order, the roles of the state's dispatchableRoles for which a dispatch
// event by Hermes would currently fire. A nil machine or facts, an unknown mode or state yield none.
func NextAllowedRoles(m *Machine, snap TaskSnapshot, facts Facts) []Role {
	if m == nil || facts == nil || snap.State == "" {
		return nil
	}
	st := m.findState(snap.State)
	if st == nil || !has(st.modes, snap.Mode) {
		return nil
	}
	var out []Role
	for _, role := range sorted(st.dispatchable) {
		d, err := Fire(m, snap, Event{Name: roleEvent(role, snap.State, snap.Mode), Actor: "hermes", Role: role}, facts)
		if err == nil && d.Applied != nil {
			out = append(out, Role(role))
		}
	}
	return out
}

// projection is one humanProjection entry of the table.
type projection struct {
	humanState, label, when string
	states                  []string
	priority                int
}

// Project returns the human state and status label of the task (plan 29 section 14.13): the entry of the table's
// humanProjection with the lowest priority number that lists the state and whose condition holds. Conditions are
// open_conflict.awaiting_human_approval and no_execution_dispatched; an unknown condition never holds. A nil
// machine or a state that does not exist in the task's mode projects to nothing.
func Project(m *Machine, snap TaskSnapshot) (HumanState, string) {
	if m == nil {
		return "", ""
	}
	st := m.findState(snap.State)
	if st == nil || !has(st.modes, snap.Mode) {
		return "", ""
	}
	for _, p := range m.projection {
		if !has(p.states, snap.State) {
			continue
		}
		switch p.when {
		case "":
		case "open_conflict.awaiting_human_approval":
			if !snap.ConflictAwaitingApproval {
				continue
			}
		case "no_execution_dispatched":
			if snap.ExecutionDispatched {
				continue
			}
		default:
			continue
		}
		return HumanState(p.humanState), p.label
	}
	return "", ""
}
