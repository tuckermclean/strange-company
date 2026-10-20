package statemachine

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

// Decision D11 (S3 review 1): identical replay fails closed on the event role (B1), Advance re-derives and
// verifies the decision (B2), AccountingFacts asks each base fact at most once (N1), BlockedReason follows the
// same failure code as the decision (N2) and merit_by_generation is keyed by the failed execution's role (N3).

// ---- B1

// 29-AC-8 (D11 B1): an empty Event.Role, or an Event.Role different from DispatchSpec.Role, is an unfingerprintable
// dispatch and fails closed as an identical replay, also when the dispatch differs from the failed one (a rotated
// Role changes the fingerprint and must not evade the replay rules).
func TestIdenticalReplay_EventRoleFailsClosed_29AC8_D11B1(t *testing.T) {
	wantReplay := func(t *testing.T, what string, d Decision) {
		t.Helper()
		if d.Rejected == nil {
			t.Errorf("%s: want Rejected (identical replay, or another fail-closed refusal), got %s", what, evDescribe(d))
		}
	}
	// rule 1 (same fingerprint) evaded by rotating the role of the spec while the event role is empty
	r := newRun(t, "test-first")
	r.toRedVerified()
	d1 := r.spec("implementer", 1)
	r.failedDispatch(d1, "sig-a")
	x := d1
	x.Dispatch.Role = "researcher"
	n0 := len(r.task.Executions)
	wantReplay(t, "Event.Role empty, DispatchSpec.Role researcher, otherwise identical", r.try("hermes", "dispatch_implementer", "", x, nil))
	wantReplay(t, "Event.Role empty, DispatchSpec.Role implementer", r.try("hermes", "dispatch_implementer", "", d1, nil))
	wantReplay(t, "Event.Role implementer, DispatchSpec.Role researcher", r.try("hermes", "dispatch_implementer", "implementer", x, nil))
	if n := len(r.task.Executions); n != n0 {
		t.Errorf("a refused dispatch recorded an execution: %d executions, want %d", n, n0)
	}

	// rule 2 (two equal signatures, same settings) evaded the same way
	r = newRun(t, "test-first")
	r.toRedVerified()
	s1 := r.spec("implementer", 1)
	s1.Dispatch.StartCommit = "a"
	r.failedDispatch(s1, "sig-x")
	s2 := s1
	s2.Dispatch.StartCommit = "b"
	r.failedDispatch(s2, "sig-x")
	s3 := s1
	s3.Dispatch.StartCommit = "c"
	wantReplay(t, "rule 2 control (Event.Role implementer)", r.try("hermes", "dispatch_implementer", "implementer", s3, nil))
	n0 = len(r.task.Executions)
	s3.Dispatch.Role = "planner"
	wantReplay(t, "rule 2, Event.Role empty, DispatchSpec.Role planner", r.try("hermes", "dispatch_implementer", "", s3, nil))
	if n := len(r.task.Executions); n != n0 {
		t.Errorf("a refused dispatch recorded an execution: %d executions, want %d", n, n0)
	}
}

// 29-AC-7 (D11 B1): the role of a recorded execution is Event.Role.
func TestExecutionRole_IsEventRole_29AC7_D11B1(t *testing.T) {
	r := newRun(t, "test-first")
	r.toRedVerified()
	r.ok("hermes", "dispatch_implementer", "implementer", r.spec("implementer", 1), nil, "T030", "IMPLEMENTING")
	for i, want := range []string{"tester", "implementer"} {
		if got := r.task.Executions[i].Role; got != want {
			t.Errorf("execution %d role = %q, want the Event.Role %q", i, got, want)
		}
	}
	// a role the event does not carry is never invented from the spec
	x := r.spec("implementer", 2)
	x.Dispatch.Role = "researcher"
	r2 := newRun(t, "test-first")
	r2.toRedVerified()
	n := len(r2.task.Executions)
	if d := r2.try("hermes", "dispatch_implementer", "", x, nil); d.Applied != nil {
		t.Errorf("a dispatch without Event.Role was applied: %s", evDescribe(d))
		if got := r2.task.Executions[n].Role; got != "" {
			t.Errorf("recorded execution role = %q, want Event.Role (empty)", got)
		}
	}
}

// ---- B2

func d11Forgeable(t *testing.T) (*Machine, Task, Event, StepInput, Applied) {
	t.Helper()
	m := evMachine(t)
	task := acTask("RED_VERIFIED", Attempts{AutonomousUsed: 1, AutonomousMax: 5, TesterUsed: 1, TesterMax: 3})
	task.Snapshot.ReturnState = "SPECIFIED"
	task.Executions = []Execution{{Role: "implementer", Generation: 1, Counted: true, Outcome: "merit", Signature: "s"}}
	ev := Event{Name: "dispatch_implementer", Actor: "hermes", Role: "implementer"}
	in := StepInput{Dispatch: DispatchSpec{Role: "implementer", Model: "m-new", Harness: "h"}}
	d, nt, err := Apply(m, task, ev, in, acPolicy, evPermissive(evContract(t)))
	if err != nil || d.Applied == nil {
		t.Fatalf("setup: Apply = %s, %v", evDescribe(d), err)
	}
	if _, err := Advance(m, task, ev, d, in, acPolicy); err != nil || nt.Snapshot.State != "IMPLEMENTING" {
		t.Fatalf("setup: the unforged decision must advance: %v", err)
	}
	return m, task, ev, in, *d.Applied
}

// 29-AC-7 (D11 B2): Advance re-derives the decision from the table; a forged Applied is an error and the zero
// Task. A wrong target, effects, failure class or reason code is SM_ADVANCE_NOT_APPLIED; a delta that differs
// from the recomputed one (negative, a grant, missing, surplus) is SM_ADVANCE_DELTA_INVALID.
func TestAdvance_RejectsForgedDecisions_29AC7_D11B2(t *testing.T) {
	m, task, ev, in, good := d11Forgeable(t)
	mod := func(f func(a *Applied)) Applied {
		a := cloneApplied(good)
		f(&a)
		return a
	}
	for _, c := range []struct {
		name, code string
		a          Applied
	}{
		{"to DONE", AdvanceNotApplied, mod(func(a *Applied) { a.To = "DONE" })},
		{"to AWAITING_MERGE", AdvanceNotApplied, mod(func(a *Applied) { a.To = "AWAITING_MERGE" })},
		{"effects dropped", AdvanceNotApplied, mod(func(a *Applied) { a.Effects = nil })},
		{"effects extended", AdvanceNotApplied, mod(func(a *Applied) { a.Effects = append(a.Effects, "E_CLOSE_TRACKING:completed") })},
		{"failure class", AdvanceNotApplied, mod(func(a *Applied) { a.FailureClass = "merit" })},
		{"reason code", AdvanceNotApplied, mod(func(a *Applied) { a.ReasonCode = "forged" })},
		{"refund delta", AdvanceDeltaInvalid, mod(func(a *Applied) { a.Deltas = map[string]int{"autonomous_used": -1} })},
		{"grant via generation", AdvanceDeltaInvalid, mod(func(a *Applied) { a.Deltas["generation"] += 5 })},
		{"missing delta", AdvanceDeltaInvalid, mod(func(a *Applied) { a.Deltas = nil })},
		{"surplus delta", AdvanceDeltaInvalid, mod(func(a *Applied) { a.Deltas["reviews_used"] = 1 })},
		{"double count", AdvanceDeltaInvalid, mod(func(a *Applied) { a.Deltas["autonomous_used"] = 2 })},
	} {
		a := c.a
		before := fmt.Sprintf("%+v", task)
		got, err := Advance(m, task, ev, Decision{Applied: &a}, in, acPolicy)
		var fe *FireError
		if !errors.As(err, &fe) || fe.Code != c.code {
			t.Errorf("%s: Advance error = %v, want a *FireError with code %s", c.name, err, c.code)
		}
		if !reflect.DeepEqual(got, Task{}) && err != nil {
			t.Errorf("%s: Advance returned a non-zero Task with an error", c.name)
		}
		if fmt.Sprintf("%+v", task) != before {
			t.Errorf("%s: Advance modified its argument", c.name)
		}
	}

	// the deltas of a failure depend on the failure code that the StepInput carries
	im := acTask("IMPLEMENTING", Attempts{AutonomousUsed: 1, AutonomousMax: 5, TesterMax: 3})
	im.Snapshot.ReturnState = "RED_VERIFIED"
	im.Executions = []Execution{{Role: "implementer", Generation: 1, Counted: true}}
	fev := Event{Name: "execution_failed", Actor: "runner"}
	base := evPermissive(evContract(t))
	base.v["G_FAILURE_CLASS"] = "infrastructure"
	fin := StepInput{FailureCode: "TIMED_OUT"}
	d, _, err := Apply(m, im, fev, fin, acPolicy, base)
	if err != nil || d.Applied == nil || d.Applied.Deltas["consecutive_timeouts"] != 1 {
		t.Fatalf("setup: Apply(execution_failed TIMED_OUT) = %s, %v", evDescribe(d), err)
	}
	a := cloneApplied(*d.Applied)
	delete(a.Deltas, "consecutive_timeouts")
	if _, err := Advance(m, im, fev, Decision{Applied: &a}, fin, acPolicy); err == nil {
		t.Error("a TIMED_OUT failure without its consecutive_timeouts delta was accepted")
	}
	b := cloneApplied(*d.Applied)
	if _, err := Advance(m, im, fev, Decision{Applied: &b}, StepInput{FailureCode: "JOB_LOST"}, acPolicy); err == nil {
		t.Error("TIMED_OUT deltas were accepted for a step whose failure code is JOB_LOST")
	}
}

// 29-AC-7 / 29-AC-4 P5 (D11 B2): Replay rejects a history whose step was forged.
func TestReplay_RejectsForgedHistory_29AC7_D11B2(t *testing.T) {
	r := newRun(t, "test-first")
	r.toRedVerified()
	init := Task{Snapshot: TaskSnapshot{Mode: "test-first", Attempts: Attempts{AutonomousMax: acPolicy.MaxAutonomousAttempts, TesterMax: acPolicy.MaxTesterAttempts,
		MeritByGeneration: map[string]int{}, ByClass: map[string]int{}}}}
	if _, err := Replay(r.m, init, r.task.History, acPolicy); err != nil {
		t.Fatalf("setup: the honest history must replay: %v", err)
	}
	ev := Event{Name: "dispatch_implementer", Actor: "hermes", Role: "implementer"}
	in := r.spec("implementer", 1)
	for name, a := range map[string]Applied{
		"T030 to AWAITING_MERGE": {TransitionID: "T030", From: "RED_VERIFIED", To: "AWAITING_MERGE"},
		"T030 to DONE":           {TransitionID: "T030", From: "RED_VERIFIED", To: "DONE"},
		"T030 with a grant":      {TransitionID: "T030", From: "RED_VERIFIED", To: "IMPLEMENTING", Deltas: map[string]int{"generation": 5}},
		"T030 with a refund":     {TransitionID: "T030", From: "RED_VERIFIED", To: "IMPLEMENTING", Deltas: map[string]int{"autonomous_used": -1}},
	} {
		h := slices.Clone(r.task.History)
		h = append(h, Step{Event: ev, Input: in, Applied: a})
		if got, err := Replay(r.m, init, h, acPolicy); err == nil {
			t.Errorf("%s: Replay accepted a forged last step and ended in %s", name, got.Snapshot.State)
		}
	}
}

// ---- N1, N2

// d11Flip answers G_FAILURE_CODE differently on every call and counts what it is asked.
type d11Flip struct {
	evFacts
	n map[string]int
}

func (f *d11Flip) Value(ev Event, g string) (string, bool) {
	f.n["v:"+g]++
	if g == "G_FAILURE_CODE" {
		return []string{"TIMED_OUT", "JOB_LOST", "START_TIMEOUT"}[(f.n["v:"+g]-1)%3], true
	}
	return f.evFacts.Value(ev, g)
}

func (f *d11Flip) Bool(ev Event, g string) (bool, bool) {
	f.n["b:"+g]++
	return f.evFacts.Bool(ev, g)
}

// 29-AC-7 (D11 N1): the base is asked each fact at most once per Fire, so a base that flip-flops cannot make the
// decision, the guards and the deltas disagree: the result follows its first answer (TIMED_OUT).
func TestAccountingFacts_BaseAskedOnce_29AC7_D11N1(t *testing.T) {
	m := evMachine(t)
	base := &d11Flip{evFacts: evPermissive(evContract(t)), n: map[string]int{}}
	base.v["G_FAILURE_CLASS"] = "infrastructure"
	task := acTask("IMPLEMENTING", Attempts{AutonomousUsed: 1, AutonomousMax: 5, TesterMax: 3})
	task.Snapshot.ReturnState = "RED_VERIFIED"
	task.Executions = []Execution{{Role: "implementer", Generation: 1, Counted: true}}
	d, nt, err := Apply(m, task, Event{Name: "execution_failed", Actor: "runner"}, StepInput{}, acPolicy, base)
	if err != nil || d.Applied == nil {
		t.Fatalf("Apply = %s, %v", evDescribe(d), err)
	}
	if got := nt.Snapshot.Attempts.ConsecutiveTimeouts; got != 1 {
		t.Errorf("consecutive_timeouts = %d, want 1 (the first answer TIMED_OUT decides)", got)
	}
	if got := nt.Snapshot.Attempts.ConsecutiveInfrastructure; got != 1 {
		t.Errorf("consecutive_infrastructure = %d, want 1", got)
	}
	for k, n := range base.n {
		if n > 1 {
			t.Errorf("the base was asked %q %d times in one Fire, want at most once", k, n)
		}
	}
	if base.n["v:G_FAILURE_CODE"] == 0 {
		t.Error("the base was never asked G_FAILURE_CODE (StepInput.FailureCode is empty)")
	}
}

// 29-AC-7 (D11 N2): an EMERGENCY_STOP supplied by the base Facts (StepInput.FailureCode empty) blocks the task
// with the reason emergency_stop, from the same failure code as the decision, and refunds the attempt.
func TestBlockedReason_FollowsBaseFailureCode_29AC7_D11N2(t *testing.T) {
	m := evMachine(t)
	for code, reason := range map[string]string{"EMERGENCY_STOP": "emergency_stop", "BUDGET_SHARED_EXHAUSTED": "budget_window_exhausted"} {
		base := evPermissive(evContract(t))
		base.v["G_FAILURE_CLASS"] = "budget"
		base.v["G_FAILURE_CODE"] = code
		task := acTask("IMPLEMENTING", Attempts{AutonomousUsed: 1, AutonomousMax: 5, TesterMax: 3})
		task.Snapshot.ReturnState = "RED_VERIFIED"
		task.Executions = []Execution{{Role: "implementer", Generation: 1, Counted: true}}
		d, nt, err := Apply(m, task, Event{Name: "execution_failed", Actor: "runner"}, StepInput{}, acPolicy, base)
		if err != nil || d.Applied == nil || nt.Snapshot.State != "BLOCKED" {
			t.Errorf("%s: Apply = %s, %v; want BLOCKED", code, evDescribe(d), err)
			continue
		}
		if nt.BlockedReason != reason {
			t.Errorf("%s: blocked.reason_code = %q, want %q", code, nt.BlockedReason, reason)
		}
		if nt.Snapshot.Attempts.AutonomousUsed != 0 {
			t.Errorf("%s: autonomous_used = %d, want the attempt refunded (0)", code, nt.Snapshot.Attempts.AutonomousUsed)
		}
	}
}

// ---- N3

// 29-AC-7 (D11 N3, plan 29 section 16.1, CD-09): a T102 worker merit failure increments
// merit_by_generation["<role>:g<n>"] for the roles reviewer, researcher and planner only, keyed by the role of
// the failed execution; implementer and tester worker failures count in by_class.merit alone.
func TestMeritByGeneration_WorkerFailureRoles_29AC7_D11N3(t *testing.T) {
	m := evMachine(t)
	for _, c := range []struct {
		state, mode, ret, role string
		want                   map[string]int
	}{
		{"REVIEW", "test-first", "GREEN_VERIFIED", "reviewer", map[string]int{"reviewer:g1": 1}},
		{"PRODUCING", "verification-first", "JUDGE_APPROVED", "researcher", map[string]int{"researcher:g1": 1}},
		{"PRODUCING", "verification-first", "JUDGE_APPROVED", "planner", map[string]int{"planner:g1": 1}},
		{"PRODUCING", "verification-first", "JUDGE_APPROVED", "implementer", map[string]int{}},
		{"IMPLEMENTING", "test-first", "RED_VERIFIED", "implementer", map[string]int{}},
		{"TEST_AUTHORING", "test-first", "SPECIFIED", "tester", map[string]int{}},
	} {
		name := c.state + "/" + c.role
		base := evPermissive(evContract(t))
		base.v["G_FAILURE_CLASS"] = "merit"
		task := acTask(c.state, Attempts{AutonomousUsed: 1, AutonomousMax: 5, TesterUsed: 1, TesterMax: 3, ReviewsUsed: 1})
		task.Snapshot.Mode, task.Snapshot.ReturnState = c.mode, c.ret
		task.Executions = []Execution{{Role: c.role, Generation: 1, Counted: c.state != "REVIEW"}}
		d, nt, err := Apply(m, task, Event{Name: "execution_failed", Actor: "runner"}, StepInput{FailureSignature: "s"}, acPolicy, base)
		if err != nil || d.Applied == nil || d.Applied.TransitionID != "T102" {
			t.Errorf("%s: Apply = %s, %v; want T102", name, evDescribe(d), err)
			continue
		}
		got := nt.Snapshot.Attempts.MeritByGeneration
		if len(got) != len(c.want) || (len(c.want) > 0 && !reflect.DeepEqual(got, c.want)) {
			t.Errorf("%s: merit_by_generation = %v, want %v", name, got, c.want)
		}
		if n := nt.Snapshot.Attempts.ByClass["merit"]; n != 1 {
			t.Errorf("%s: by_class.merit = %d, want 1", name, n)
		}
	}
}
