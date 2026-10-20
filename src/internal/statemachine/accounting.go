package statemachine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Retry accounting (plan 29 section 16, F05 slice S3). The types specify the accounting layer, the functions
// implement it. Everything here is pure (no I/O, no clock, no randomness) and fails closed. The layer sits beside Fire, which stays driven by raw Facts:
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
	// ResolutionApproval is spec.testSpecConflict.resolutionApproval: "human" (the default, also when empty) or
	// "adjudicator" (a decisive test_invalid adjudication itself sets test_change_approved; D14).
	//
	// SC-STUB(F05 29-AC-11)
	ResolutionApproval string
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
	// Verdict is the adjudicator verdict of an adjudication_recorded event: test_valid, test_invalid or
	// spec_ambiguous (plan 21 section 14.6; D14).
	//
	// SC-STUB(F05 29-AC-11)
	Verdict string
}

// Execution is one execution of the task as the accounting sees it. Outcome is "" while running, "succeeded"
// for an accepted result, "blocked" for a worker that reported itself blocked, "cancelled" for a cancelled
// execution, else the failure class (merit, infrastructure, policy, budget). Counted says the
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
	AdvanceDeltaInvalid  = "SM_ADVANCE_DELTA_INVALID"
)

func hexSHA256(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func sortedCopy(l []string) []string {
	out := make([]string, len(l))
	copy(out, l)
	sort.Strings(out)
	return out
}

// Fingerprint is F of plan 29 section 16.4: the lowercase hex sha256 of the canonical JSON (keys in ascending
// order, no insignificant whitespace) of {ac_version, context_refs (sorted), failure_signatures (sorted),
// harness, model, notes_sha256 (lowercase hex sha256 of the orchestrator notes), profile, role, start_commit}.
func Fingerprint(d DispatchSpec) string {
	// The struct fields are declared in ascending key order, which is the order encoding/json emits.
	v := struct {
		ACVersion         int      `json:"ac_version"`
		ContextRefs       []string `json:"context_refs"`
		FailureSignatures []string `json:"failure_signatures"`
		Harness           string   `json:"harness"`
		Model             string   `json:"model"`
		NotesSHA256       string   `json:"notes_sha256"`
		Profile           string   `json:"profile"`
		Role              string   `json:"role"`
		StartCommit       string   `json:"start_commit"`
	}{d.ACVersion, sortedCopy(d.ContextRefs), sortedCopy(d.FailureSignatures), d.Harness, d.Model, hexSHA256(d.OrchestratorNotes), d.Profile, d.Role, d.StartCommit}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "" // unreachable: strings and integers always encode
	}
	return hexSHA256(strings.TrimSuffix(buf.String(), "\n"))
}

// accountingFacts is the Facts of AccountingFacts.
type accountingFacts struct {
	t    Task
	p    RetryPolicy
	in   StepInput
	base Facts
	// The base is asked each fact at most once; the first answer decides (D11 N1).
	bools  map[string]boolAnswer
	values map[string]valueAnswer
}

// AccountingFacts answers G_ATTEMPTS_REMAIN, G_NOT_IDENTICAL_REPLAY, G_INFRA_REQUEUE_ALLOWED, G_REVIEWS_REMAIN
// and G_AUXILIARY_REMAIN (always known) from the task, the policy and the step input, answers G_FAILURE_CODE
// from in.FailureCode when that is set, and delegates every other guard to base (a nil base knows nothing).
func AccountingFacts(t Task, p RetryPolicy, in StepInput, base Facts) Facts {
	return &accountingFacts{t: t, p: p, in: in, base: base, bools: map[string]boolAnswer{}, values: map[string]valueAnswer{}}
}

// testerSide reports whether the event concerns the tester attempt counter (dispatch of a tester, a tester
// verification failure, or the failure of a tester execution).
func testerSide(ev Event, state string) bool {
	switch ev.Name {
	case "dispatch_tester", "red_classified", "judge_classified":
		return true
	case "execution_failed":
		return state == "TEST_AUTHORING" || state == "JUDGE_AUTHORING"
	}
	return false
}

func (f *accountingFacts) Bool(ev Event, guard string) (bool, bool) {
	a := f.t.Snapshot.Attempts
	switch guard {
	case "G_ATTEMPTS_REMAIN":
		if testerSide(ev, f.t.Snapshot.State) {
			return a.TesterUsed < a.TesterMax, true
		}
		return a.AutonomousUsed < a.AutonomousMax, true
	case "G_REVIEWS_REMAIN":
		return a.ReviewsUsed < f.p.MaxReviewExecutions, true
	case "G_AUXILIARY_REMAIN":
		return a.AuxiliaryUsed < f.p.MaxAuxiliaryExecutions, true
	case "G_NOT_IDENTICAL_REPLAY":
		return !f.identicalReplay(ev), true
	case "G_INFRA_REQUEUE_ALLOWED":
		// Evaluated after counting the current failure (plan 29 section 16.6).
		timeout := f.failureCode(ev) == "TIMED_OUT"
		ok := a.ConsecutiveInfrastructure+1 < f.p.MaxConsecutive && a.ByClass["infrastructure"]+1 < f.p.MaxTotal
		if timeout && a.ConsecutiveTimeouts+1 >= f.p.MaxConsecutiveTimeouts {
			ok = false
		}
		return ok, true
	}
	if f.base == nil {
		return false, false
	}
	if a, ok := f.bools[guard]; ok {
		return a.value, a.known
	}
	v, k := f.base.Bool(ev, guard)
	f.bools[guard] = boolAnswer{v, k}
	return v, k
}

func (f *accountingFacts) Value(ev Event, guard string) (string, bool) {
	if guard == "G_FAILURE_CODE" {
		if f.in.FailureCode != "" {
			return f.in.FailureCode, true
		}
		// A code the base does not know is "" everywhere (D12 B3): no refund, no timeout, nothing recorded.
		if v, k := f.baseValue(ev, guard); k {
			return v, true
		}
		return "", false
	}
	return f.baseValue(ev, guard)
}

func (f *accountingFacts) baseValue(ev Event, guard string) (string, bool) {
	if f.base == nil {
		return "", false
	}
	if a, ok := f.values[guard]; ok {
		return a.value, a.known
	}
	v, k := f.base.Value(ev, guard)
	f.values[guard] = valueAnswer{v, k}
	return v, k
}

// failureCode is the failure code of the step: StepInput.FailureCode, else the first answer of the base. It is
// the single source of the decision, the deltas and the recorded StepInput.
func (f *accountingFacts) failureCode(ev Event) string {
	v, _ := f.Value(ev, "G_FAILURE_CODE") // "" when the code is not known (D12 B3)
	return v
}

// identicalReplay is the rule of plan 29 section 16.4. A dispatch that cannot be fingerprinted (no role, or a
// role other than the event's, or an event without a role) fails closed: it counts as a replay.
func (f *accountingFacts) identicalReplay(ev Event) bool {
	d := f.in.Dispatch
	if d.Role == "" || ev.Role == "" || ev.Role != d.Role {
		return true
	}
	fp, gen := Fingerprint(d), f.t.Snapshot.Generation
	var ofRole []Execution
	for _, e := range f.t.Executions {
		if e.Role == d.Role && e.Generation == gen {
			ofRole = append(ofRole, e)
		}
	}
	var merit []Execution
	for _, e := range ofRole {
		if e.Fingerprint == fp && (e.Outcome == "merit" || e.Outcome == "policy") {
			return true
		}
		if e.Outcome == "merit" {
			merit = append(merit, e)
		}
	}
	if len(merit) < 2 || len(ofRole) == 0 {
		return false
	}
	if merit[len(merit)-1].Signature != merit[len(merit)-2].Signature {
		return false
	}
	last := ofRole[len(ofRole)-1]
	return last.Harness == d.Harness && last.Model == d.Model && last.Profile == d.Profile && last.NotesDigest == hexSHA256(d.OrchestratorNotes)
}

// Allowed is the MCP details.allowed list of the rejection (plan 29 section 7.2): ["identical_replay"] when
// G_NOT_IDENTICAL_REPLAY alone failed, else nil.
func (r Rejected) Allowed() []string {
	if len(r.FailedGuards) == 1 && r.FailedGuards[0] == "G_NOT_IDENTICAL_REPLAY" {
		return []string{"identical_replay"}
	}
	return nil
}

// ---- Applied.Deltas

// executionEnds are the events that end the task's active execution.
var executionEnds = map[string]bool{
	"tester_result_accepted": true, "implementer_result_accepted": true, "producer_result_accepted": true,
	"review_accepted": true, "review_changes_requested": true, "review_rejected": true,
	"implementer_reported_conflict": true, "execution_blocked": true, "execution_failed": true,
	"auxiliary_result_recorded": true,
}

// refundCodes are the failure codes of an execution whose attempt is given back (plan 29 section 16.1).
var refundCodes = map[string]bool{"EMERGENCY_STOP": true, "BUDGET_SHARED_EXHAUSTED": true}

// verifierSubject maps a verification state to the role whose work it judges.
var verifierSubject = map[string]string{
	"RED_VERIFYING": "tester", "JUDGE_VALIDATING": "tester", "GREEN_VERIFYING": "implementer", "JUDGING": "implementer",
}

func hasEffect(effects []string, id string) bool {
	for _, e := range effects {
		if idOf(e) == id {
			return true
		}
	}
	return false
}

// dispatchInto returns the role argument of E_CREATE_EXECUTION and countsAttempt of the first transition that
// dispatches into the state ("" when none does).
func (m *Machine) dispatchInto(name string) (role string, counts bool) {
	for i := range m.transitions {
		t := &m.transitions[i]
		if t.to == name && dispatchEvents[t.event] {
			if r := effectArg(t.effects, "E_CREATE_EXECUTION"); r != "" {
				return r, t.countsAttempt
			}
		}
	}
	return "", false
}

// attemptCounter is the delta key of the attempt counter consumed by an execution running in the state.
func (m *Machine) attemptCounter(name string) string {
	role, counts := m.dispatchInto(name)
	switch {
	case !counts:
		return ""
	case role == "tester":
		return "tester_used"
	}
	return "autonomous_used"
}

// subjectRole is the role whose work a failure event judges: "tester", "implementer" (implementer and
// producer), "reviewer" or "" when none.
func (m *Machine) subjectRole(event, from string) string {
	if event == "review_changes_requested" || event == "review_rejected" {
		return "implementer"
	}
	if st := m.findState(from); st != nil && st.active == "required" {
		switch r, _ := m.dispatchInto(from); r {
		case "tester":
			return "tester"
		case "implementer", "producer":
			return "implementer"
		case "reviewer":
			return "reviewer"
		}
		return ""
	}
	return verifierSubject[from]
}

// deltas derives the counter changes of the fired transition t from the snapshot, the event and the facts only.
//
// workerRole is the recorded role of the failed execution for an execution_failed event ("" when unknown). Only
// reviewer, researcher and planner worker merit failures key merit_by_generation (plan 29 section 16.1, CD-09);
// implementer and tester worker failures count in by_class.merit alone.
func (m *Machine) deltas(t *transition, cur *state, snap TaskSnapshot, ev Event, facts Facts, workerRole string, haveRole bool) map[string]int {
	d := map[string]int{}
	add := func(k string, n int) {
		if n == 0 {
			return
		}
		if d[k] += n; d[k] == 0 {
			delete(d, k)
		}
	}
	at := snap.Attempts
	resetConsecutive := func() {
		add("consecutive_infrastructure", -at.ConsecutiveInfrastructure)
		add("consecutive_timeouts", -at.ConsecutiveTimeouts)
	}
	refund := func() {
		if cur != nil && cur.active == "required" {
			if k := m.attemptCounter(cur.name); k != "" {
				add(k, -1)
			}
		}
	}
	if role := effectArg(t.effects, "E_CREATE_EXECUTION"); role != "" {
		switch {
		case t.countsAttempt && role == "tester":
			add("tester_used", 1)
		case t.countsAttempt:
			add("autonomous_used", 1)
		case role == "reviewer":
			add("reviews_used", 1)
		case role == "auxiliary":
			add("auxiliary_used", 1)
		}
		if snap.Generation == 0 {
			add("generation", 1)
		}
	}
	if hasEffect(t.effects, "E_START_GENERATION") || hasEffect(t.effects, "E_ARCHIVE_GENERATION") {
		add("generation", 1)
	}
	class := effectArg(t.effects, "E_COUNT_FAILURE")
	if class != "" {
		add("by_class."+class, 1)
		if class == "merit" {
			r := workerRole
			if ev.Name != "execution_failed" {
				r = m.subjectRole(ev.Name, snap.State)
			} else if !haveRole {
				// raw Fire knows no execution records: the role the state dispatches, kept only when it is a
				// merit role (plan 29 section 16.1, CD-09); Apply and Advance use the recorded role
				if r = m.subjectRole(ev.Name, snap.State); !meritRoles[r] {
					r = ""
				}
			} else if !meritRoles[r] {
				r = ""
			}
			if r != "" {
				add(fmt.Sprintf("merit_by_generation.%s:g%d", r, snap.Generation), 1)
			}
		}
	}
	if executionEnds[ev.Name] {
		failed := ev.Name == "execution_failed"
		code := ""
		if failed && (class == "infrastructure" || class == "budget") {
			if v, k := facts.Value(ev, "G_FAILURE_CODE"); k {
				code = v // a code that is not known counts as "" (D12 B3)
			}
		}
		if failed && class == "infrastructure" {
			add("consecutive_infrastructure", 1)
			if code == "TIMED_OUT" {
				add("consecutive_timeouts", 1)
			} else {
				add("consecutive_timeouts", -at.ConsecutiveTimeouts)
			}
			// T100 requeues the execution, which counts again: the refund and the new count cancel out.
			if !hasEffect(t.effects, "E_REQUEUE_EXECUTION") {
				refund()
			}
		} else {
			resetConsecutive()
			if failed && class == "budget" && refundCodes[code] {
				refund()
			}
		}
	}
	if hasEffect(t.effects, "E_CANCEL_ACTIVE_EXECUTION") {
		// Also applies to T062 escalate and T052 pr_merged; bounded, because leaving a held state needs a human (D11 N5).
		refund() // cancelling never consumes an attempt (plan 29 section 14.10)
	}
	if hasEffect(t.effects, "E_GRANT_ATTEMPTS") {
		// Advance also grants policy HumanGrant attempts, and raises tester_max when the tester budget is exhausted (D11 N4).
		resetConsecutive()
	}
	return d
}

// meritRoles are the roles whose worker merit failures count in merit_by_generation.
var meritRoles = map[string]bool{"reviewer": true, "researcher": true, "planner": true}

// runningRole is the recorded role of the task's active execution ("" when none).
func runningRole(t Task) string {
	if i := lastExecution(t.Executions, func(e Execution) bool { return e.Outcome == "" }); i >= 0 {
		return t.Executions[i].Role
	}
	return ""
}

// codeFacts answers G_FAILURE_CODE alone, from the failure code a step recorded.
type codeFacts struct{ code string }

func (c codeFacts) Bool(Event, string) (bool, bool) { return false, false }
func (c codeFacts) Value(_ Event, g string) (string, bool) {
	return c.code, g == "G_FAILURE_CODE" && c.code != ""
}

// ---- Advance

func cloneAttempts(a Attempts) Attempts {
	a.MeritByGeneration = cloneIntMap(a.MeritByGeneration)
	a.ByClass = cloneIntMap(a.ByClass)
	return a
}

func cloneIntMap(m map[string]int) map[string]int {
	if m == nil {
		return nil
	}
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneInput(in StepInput) StepInput {
	in.Dispatch.ContextRefs = slices.Clone(in.Dispatch.ContextRefs)
	in.Dispatch.FailureSignatures = slices.Clone(in.Dispatch.FailureSignatures)
	return in
}

func cloneApplied(a Applied) Applied {
	a.Effects = slices.Clone(a.Effects)
	a.Deltas = cloneIntMap(a.Deltas)
	return a
}

func cloneTask(t Task) Task {
	t.Snapshot.Attempts = cloneAttempts(t.Snapshot.Attempts)
	t.Executions = slices.Clone(t.Executions)
	if t.History != nil {
		h := make([]Step, len(t.History))
		for i, s := range t.History {
			h[i] = Step{Event: s.Event, Input: cloneInput(s.Input), Applied: cloneApplied(s.Applied)}
		}
		t.History = h
	}
	return t
}

func adverr(code, format string, a ...any) (Task, error) { return Task{}, ferr(code, format, a...) }

// applyDelta adds n to the counter named by key. Unknown keys and counters that would go negative are errors.
func applyDelta(a *Attempts, generation *int, key string, n int) error {
	counter := map[string]*int{
		"autonomous_used": &a.AutonomousUsed, "tester_used": &a.TesterUsed, "reviews_used": &a.ReviewsUsed,
		"auxiliary_used": &a.AuxiliaryUsed, "generation": generation,
		"consecutive_infrastructure": &a.ConsecutiveInfrastructure, "consecutive_timeouts": &a.ConsecutiveTimeouts,
	}[key]
	if counter == nil {
		prefix, name, ok := strings.Cut(key, ".")
		var mp *map[string]int
		switch {
		case ok && prefix == "by_class" && name != "":
			mp = &a.ByClass
		case ok && prefix == "merit_by_generation" && name != "":
			mp = &a.MeritByGeneration
		default:
			return fmt.Errorf("unknown delta key %q", key)
		}
		if *mp == nil {
			*mp = map[string]int{}
		}
		v := (*mp)[name] + n
		if v < 0 {
			return fmt.Errorf("delta %s=%d makes the counter negative", key, n)
		}
		(*mp)[name] = v
		return nil
	}
	if *counter+n < 0 {
		return fmt.Errorf("delta %s=%d makes the counter negative", key, n)
	}
	*counter += n
	return nil
}

func lastExecution(ex []Execution, match func(Execution) bool) int {
	for i := len(ex) - 1; i >= 0; i-- {
		if match(ex[i]) {
			return i
		}
	}
	return -1
}

func (m *Machine) transitionByID(id string) *transition {
	for i := range m.transitions {
		if m.transitions[i].id == id {
			return &m.transitions[i]
		}
	}
	return nil
}

// Advance applies an Applied decision to the task and returns the new task (the argument is never modified).
// It fails closed with a *FireError and the zero Task when the decision is not an Applied one (AdvanceNotApplied),
// does not leave the task's state (AdvanceStateMismatch), names a transition, event or state the table does not
// have (AdvanceNotApplied) or carries a delta that cannot be applied (AdvanceDeltaInvalid).
//
// Advance does not trust the decision (D11, D12). The event's actor must be a declared actor class and one of the
// transition's actors. Every guard of the transition that AccountingFacts answers from the task, the policy and
// the step input (attempts, reviews, auxiliary, identical replay, infrastructure requeue, G_FAILURE_CODE) must
// hold. No lower-id candidate for the same state, event, mode and actor may pass under those facts. Fail-closed
// rule of that shadowing check: a guard those facts cannot answer counts as passing only when the transition
// being advanced lists the same guard reference (the decision then vouches for it), else the candidate does not
// pass. A caller that fires with Fire and then calls Advance must set StepInput.FailureCode to the code the
// decision used, for an execution_failed event; Apply does so.
func Advance(m *Machine, t Task, ev Event, d Decision, in StepInput, p RetryPolicy) (Task, error) {
	if m == nil {
		return adverr(FireMachineNil, "the machine is nil")
	}
	if d.Applied == nil {
		return adverr(AdvanceNotApplied, "the decision is not an applied transition")
	}
	a := d.Applied
	from := t.Snapshot.State
	if from == "" {
		from = "$none"
	}
	if a.From != from {
		return adverr(AdvanceStateMismatch, "the decision leaves %q but the task is in %q", a.From, from)
	}
	tr := m.transitionByID(a.TransitionID)
	toSt := m.findState(a.To)
	if tr == nil || tr.event != ev.Name || toSt == nil || !has(toSt.modes, t.Snapshot.Mode) {
		return adverr(AdvanceNotApplied, "the decision (%s -> %s) does not match the table and the event %s", a.TransitionID, a.To, ev.Name)
	}
	fromSt := m.findState(t.Snapshot.State)
	if !has(m.actors, ev.Actor) || !has(tr.actors, ev.Actor) {
		return adverr(AdvanceNotApplied, "actor %q may not fire %s", ev.Actor, a.TransitionID)
	}
	// Re-derive the decision from the table (D11 B2): the target, effects, failure class and reason code are the
	// transition's, and the deltas are the recomputed ones under the failure code the step carries.
	if !has(tr.from, from) || !has(tr.modes, t.Snapshot.Mode) {
		return adverr(AdvanceNotApplied, "%s does not leave %s in mode %s", a.TransitionID, from, t.Snapshot.Mode)
	}
	if to, err := m.target(tr, fromSt, t.Snapshot); err != nil || to != a.To {
		return adverr(AdvanceNotApplied, "%s: the target of the table is not %s", a.TransitionID, a.To)
	}
	if !slices.Equal(a.Effects, tr.effects) || a.FailureClass != effectArg(tr.effects, "E_COUNT_FAILURE") || a.ReasonCode != effectArg(tr.effects, "E_NOTIFY_HUMAN") {
		return adverr(AdvanceNotApplied, "%s: the effects, failure class or reason code differ from the table", a.TransitionID)
	}
	if err := m.recheckGuards(tr, t, ev, in, p, from); err != nil {
		return Task{}, err
	}
	want := m.deltas(tr, fromSt, t.Snapshot, ev, codeFacts{in.FailureCode}, runningRole(t), true)
	if len(want) != len(a.Deltas) {
		return adverr(AdvanceDeltaInvalid, "%s: the deltas differ from the recomputed ones", a.TransitionID)
	}
	for k, v := range want {
		if got, ok := a.Deltas[k]; !ok || got != v {
			return adverr(AdvanceDeltaInvalid, "%s: delta %s differs from the recomputed one", a.TransitionID, k)
		}
	}
	n := cloneTask(t)
	s := &n.Snapshot
	keys := make([]string, 0, len(a.Deltas))
	for k := range a.Deltas {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := applyDelta(&s.Attempts, &s.Generation, k, a.Deltas[k]); err != nil {
			return adverr(AdvanceDeltaInvalid, "%s: %v", a.TransitionID, err)
		}
	}
	if from == "$none" {
		if s.Attempts.AutonomousMax == 0 {
			s.Attempts.AutonomousMax = p.MaxAutonomousAttempts
		}
		if s.Attempts.TesterMax == 0 {
			s.Attempts.TesterMax = p.MaxTesterAttempts
		}
	}
	if hasEffect(tr.effects, "E_GRANT_ATTEMPTS") && p.HumanGrant > 0 {
		at := &s.Attempts
		testerOut := at.TesterUsed >= at.TesterMax
		if testerOut {
			at.TesterMax += p.HumanGrant
		}
		if at.AutonomousUsed >= at.AutonomousMax || !testerOut {
			at.AutonomousMax += p.HumanGrant
		}
	}
	// Resume state (plan 29 section 14.9): the dispatch-ready state of an active execution, else the current state.
	if hasEffect(tr.effects, "E_RECORD_RESUME_STATE") {
		if fromSt != nil && fromSt.active == "required" {
			s.ResumeState = t.Snapshot.ReturnState
		} else {
			s.ResumeState = t.Snapshot.State
		}
	}
	switch {
	case toSt.active != "required":
		s.ReturnState = ""
	case fromSt != nil && fromSt.active != "required":
		s.ReturnState = t.Snapshot.State
	}
	s.State = a.To
	if hasEffect(tr.effects, "E_CREATE_EXECUTION") {
		s.ExecutionDispatched = true
	}
	if a.To == "NEEDS_HUMAN" {
		if from != "NEEDS_HUMAN" {
			n.NeedsHumanReason = a.ReasonCode
		}
	} else {
		n.NeedsHumanReason = ""
	}
	if a.To == "BLOCKED" {
		if from != "BLOCKED" {
			n.BlockedReason = "human_hold"
			if ev.Name == "execution_failed" {
				switch in.FailureCode {
				case "EMERGENCY_STOP":
					n.BlockedReason = "emergency_stop"
				case "BUDGET_SHARED_EXHAUSTED":
					n.BlockedReason = "budget_window_exhausted"
				}
			}
		}
	} else {
		n.BlockedReason = ""
	}
	m.recordExecutions(&n, tr, ev, a, in)
	n.History = append(n.History, Step{Event: ev, Input: cloneInput(in), Applied: cloneApplied(*a)})
	return n, nil
}

// guardAnswer evaluates one guard reference like guardHolds and also says whether the facts knew it.
func guardAnswer(ref string, ev Event, facts Facts) (holds, known bool) {
	id, args := idOf(ref), argsOf(ref)
	switch base, derived := derivedGuards[id]; {
	case valuedGuards[id]:
		v, k := facts.Value(ev, id)
		return k && v != "" && has(args, v), k
	case derived:
		v, k := facts.Bool(ev, base)
		return len(args) == 0 && k && !v, k
	case guardSet[id]:
		v, k := facts.Bool(ev, id)
		return len(args) == 0 && k && v, k
	}
	return false, true // an unknown identifier fails
}

// recheckGuards is the guard part of Advance (D12 B2): see the Advance comment.
func (m *Machine) recheckGuards(tr *transition, t Task, ev Event, in StepInput, p RetryPolicy, from string) error {
	facts := AccountingFacts(t, p, in, codeFacts{in.FailureCode})
	for _, ref := range tr.guards {
		if idOf(ref) == "G_ACTOR_AUTHORIZED" {
			continue
		}
		if holds, known := guardAnswer(ref, ev, facts); known && !holds {
			return ferr(AdvanceNotApplied, "%s: guard %s does not hold", tr.id, idOf(ref))
		}
	}
	for i := range m.transitions {
		c := &m.transitions[i]
		if c.id >= tr.id || c.event != ev.Name || !has(c.modes, t.Snapshot.Mode) || !has(c.from, from) || !has(c.actors, ev.Actor) {
			continue
		}
		passes := true
		for _, ref := range c.guards {
			if idOf(ref) == "G_ACTOR_AUTHORIZED" {
				continue
			}
			holds, known := guardAnswer(ref, ev, facts)
			if known && !holds || !known && !has(tr.guards, ref) {
				passes = false
				break
			}
		}
		if passes {
			return ferr(AdvanceNotApplied, "%s: the candidate %s would pass under the same facts", tr.id, c.id)
		}
	}
	return nil
}

// recordExecutions keeps the execution records of the task in step with the applied transition.
func (m *Machine) recordExecutions(n *Task, tr *transition, ev Event, a *Applied, in StepInput) {
	running := func(e Execution) bool { return e.Outcome == "" }
	refunded := func(e Execution) bool {
		k := m.attemptCounter(a.From)
		return e.Counted && k != "" && a.Deltas[k] < 0
	}
	if executionEnds[ev.Name] {
		if i := lastExecution(n.Executions, running); i >= 0 {
			e := &n.Executions[i]
			switch ev.Name {
			case "execution_failed":
				e.Outcome, e.Signature = a.FailureClass, in.FailureSignature
				e.Refunded = refunded(*e)
				if hasEffect(tr.effects, "E_REQUEUE_EXECUTION") {
					e.Refunded = e.Counted
					requeued := *e
					requeued.Generation = n.Snapshot.Generation
					requeued.Outcome, requeued.Signature, requeued.Refunded = "", "", false
					n.Executions = append(n.Executions, requeued)
				}
			case "execution_blocked":
				e.Outcome = "blocked"
			default:
				e.Outcome = "succeeded"
			}
		}
	}
	if hasEffect(tr.effects, "E_CANCEL_ACTIVE_EXECUTION") {
		if i := lastExecution(n.Executions, running); i >= 0 {
			e := &n.Executions[i]
			e.Outcome, e.Refunded = "cancelled", refunded(*e)
		}
	}
	// A verification or review judgment that fails the work of an execution that had been accepted.
	if a.FailureClass == "merit" || a.FailureClass == "policy" {
		if ev.Name != "execution_failed" && ev.Name != "verification_infra_failed" {
			role := m.subjectRole(ev.Name, a.From)
			i := lastExecution(n.Executions, func(e Execution) bool { return e.Counted && (e.Role == "tester") == (role == "tester") })
			if (role == "tester" || role == "implementer") && i >= 0 && n.Executions[i].Outcome == "succeeded" {
				n.Executions[i].Outcome, n.Executions[i].Signature = a.FailureClass, in.FailureSignature
			}
		}
	}
	if arg := effectArg(tr.effects, "E_CREATE_EXECUTION"); arg != "" {
		role := ev.Role // the recorded role is the Event.Role (D11 B1)
		if role == "" {
			role = arg
		}
		n.Executions = append(n.Executions, Execution{
			Role: role, Generation: n.Snapshot.Generation, Fingerprint: Fingerprint(in.Dispatch), Harness: in.Dispatch.Harness,
			Model: in.Dispatch.Model, Profile: in.Dispatch.Profile, NotesDigest: hexSHA256(in.Dispatch.OrchestratorNotes), Counted: tr.countsAttempt,
		})
	}
}

// Apply is AccountingFacts, Fire and Advance in one call. A Rejected decision leaves the task unchanged; a
// malformed input is an error and the zero Decision and Task.
func Apply(m *Machine, t Task, ev Event, in StepInput, p RetryPolicy, base Facts) (Decision, Task, error) {
	af := AccountingFacts(t, p, in, base).(*accountingFacts)
	d, err := Fire(m, t.Snapshot, ev, af)
	if err != nil {
		return Decision{}, Task{}, err
	}
	if d.Applied == nil {
		return d, t, nil
	}
	// The step records the failure code the decision used and the role-keyed merit counter of the task.
	if ev.Name == "execution_failed" {
		in.FailureCode = af.failureCode(ev)
		for k := range d.Applied.Deltas {
			if strings.HasPrefix(k, "merit_by_generation.") {
				delete(d.Applied.Deltas, k)
			}
		}
		if r := runningRole(t); d.Applied.FailureClass == "merit" && meritRoles[r] {
			d.Applied.Deltas[fmt.Sprintf("merit_by_generation.%s:g%d", r, t.Snapshot.Generation)]++
		}
	}
	n, err := Advance(m, t, ev, d, in, p)
	if err != nil {
		return Decision{}, Task{}, err
	}
	return d, n, nil
}

// Replay folds the recorded steps over the initial task with Advance (under the same policy).
func Replay(m *Machine, initial Task, steps []Step, p RetryPolicy) (Task, error) {
	t := cloneTask(initial)
	for i, st := range steps {
		a := cloneApplied(st.Applied)
		n, err := Advance(m, t, st.Event, Decision{Applied: &a}, st.Input, p)
		if err != nil {
			if fe, ok := err.(*FireError); ok {
				return Task{}, ferr(fe.Code, "step %d: %s", i, fe.Message)
			}
			return Task{}, err
		}
		t = n
	}
	return t, nil
}
