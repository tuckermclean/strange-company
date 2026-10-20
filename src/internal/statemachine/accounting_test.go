package statemachine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
)

// This file specifies the library-owned retry accounting layer of F05 slice S3 (plan 29 sections 15.2 and 16):
// Fingerprint (16.4), AccountingFacts, Applied.Deltas, Advance. No test here reads the clock or a global random
// source (D7, D8).
//
// Policy values used throughout (spec.retry of the platform policy and the schema example,
// contracts/verification.schema.yaml): maxAutonomousAttempts 5, maxTesterAttempts 3, maxReviewExecutions 3,
// maxAuxiliaryExecutions 8, maxBlockedResults 2, infrastructure maxConsecutive 3, maxTotal 8,
// maxConsecutiveTimeouts 2, policy maxViolations 0, humanGrant 3.
//
// Delta keys (Applied.Deltas, additive integers relative to the snapshot, zero deltas omitted, a reset is
// minus the current value): autonomous_used, tester_used, reviews_used, auxiliary_used, generation,
// consecutive_infrastructure, consecutive_timeouts, by_class.<merit|infrastructure|policy|budget>,
// merit_by_generation.<role>:g<generation>.

var acPolicy = RetryPolicy{
	MaxAutonomousAttempts: 5, MaxTesterAttempts: 3, MaxReviewExecutions: 3, MaxAuxiliaryExecutions: 8, MaxBlockedResults: 2,
	MaxConsecutive: 3, MaxTotal: 8, MaxConsecutiveTimeouts: 2, MaxPolicyViolations: 0, HumanGrant: 3,
}

func acSHA(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// 29-AC-8 (plan 29 section 16.4): the dispatch fingerprint is the sha256 of the canonical JSON of the listed
// fields; context refs and failure signatures are sorted, so their order does not matter.
func TestFingerprint_CanonicalJSON_29AC8(t *testing.T) {
	d := DispatchSpec{Role: "implementer", Harness: "claude-code", Model: "m-1", Profile: "std", StartCommit: "abc123", ACVersion: 2,
		OrchestratorNotes: "try again", ContextRefs: []string{"b", "a"}, FailureSignatures: []string{"s2", "s1"}}
	want := acSHA(`{"ac_version":2,"context_refs":["a","b"],"failure_signatures":["s1","s2"],"harness":"claude-code","model":"m-1",` +
		`"notes_sha256":"` + acSHA("try again") + `","profile":"std","role":"implementer","start_commit":"abc123"}`)
	if got := Fingerprint(d); got != want {
		t.Errorf("Fingerprint = %q, want %q", got, want)
	}
	if got, want := Fingerprint(DispatchSpec{Role: "tester"}), acSHA(`{"ac_version":0,"context_refs":[],"failure_signatures":[],"harness":"",`+
		`"model":"","notes_sha256":"`+acSHA("")+`","profile":"","role":"tester","start_commit":""}`); got != want {
		t.Errorf("Fingerprint of a sparse spec = %q, want %q (empty lists are [])", got, want)
	}
	swapped := d
	swapped.ContextRefs, swapped.FailureSignatures = []string{"a", "b"}, []string{"s1", "s2"}
	if Fingerprint(swapped) != Fingerprint(d) {
		t.Error("Fingerprint depends on the order of context_refs or failure_signatures")
	}
	if Fingerprint(d) != Fingerprint(d) {
		t.Error("Fingerprint is not deterministic")
	}
	for name, mut := range map[string]func(*DispatchSpec){
		"role": func(x *DispatchSpec) { x.Role = "producer" }, "harness": func(x *DispatchSpec) { x.Harness = "codex" },
		"model": func(x *DispatchSpec) { x.Model = "m-2" }, "profile": func(x *DispatchSpec) { x.Profile = "big" },
		"start_commit": func(x *DispatchSpec) { x.StartCommit = "def456" }, "ac_version": func(x *DispatchSpec) { x.ACVersion = 3 },
		"notes": func(x *DispatchSpec) { x.OrchestratorNotes = "other" }, "context_refs": func(x *DispatchSpec) { x.ContextRefs = []string{"a"} },
		"failure_signatures": func(x *DispatchSpec) { x.FailureSignatures = []string{"s1"} },
	} {
		x := d
		mut(&x)
		if Fingerprint(x) == Fingerprint(d) {
			t.Errorf("changing %s does not change the fingerprint", name)
		}
	}
	if !slices.Equal(d.ContextRefs, []string{"b", "a"}) {
		t.Error("Fingerprint sorted its argument in place")
	}
}

func acTask(state string, a Attempts) Task {
	if a.MeritByGeneration == nil {
		a.MeritByGeneration = map[string]int{}
	}
	if a.ByClass == nil {
		a.ByClass = map[string]int{}
	}
	return Task{Snapshot: TaskSnapshot{Mode: "test-first", State: state, Generation: 1, Attempts: a}}
}

func acFacts(t testing.TB, task Task, in StepInput, base Facts) Facts {
	t.Helper()
	f := AccountingFacts(task, acPolicy, in, base)
	if f == nil {
		t.Fatal("AccountingFacts returned nil")
	}
	return f
}

// 29-AC-7 (plan 29 sections 16.1, 16.6 and the catalogue text of the counter guards): AccountingFacts answers
// the counter guards from the snapshot counters (the snapshot's maxima, which grants raise) and the policy caps.
// Attempts are checked before the dispatch (used < max) and for failure events with the dispatch already counted
// (used < max still means "an attempt remains", so the fifth failure of five exhausts). The infrastructure
// requeue guard is evaluated after counting the current failure, so the second consecutive TIMED_OUT (cap 2)
// and the third consecutive infrastructure failure (cap 3) exhaust it.
func TestAccountingFacts_CounterGuards_29AC7(t *testing.T) {
	mdl := evContract(t)
	base := evPermissive(mdl)
	type tc struct {
		name, state, event, role, code, guard string
		a                                     Attempts
		want                                  bool
	}
	at := func(used, max, tUsed, tMax int) Attempts {
		return Attempts{AutonomousUsed: used, AutonomousMax: max, TesterUsed: tUsed, TesterMax: tMax}
	}
	cases := []tc{
		{"impl dispatch 4/5", "RED_VERIFIED", "dispatch_implementer", "implementer", "", "G_ATTEMPTS_REMAIN", at(4, 5, 3, 3), true},
		{"impl dispatch 5/5", "RED_VERIFIED", "dispatch_implementer", "implementer", "", "G_ATTEMPTS_REMAIN", at(5, 5, 0, 3), false},
		{"impl dispatch uses the snapshot max (grant)", "RED_VERIFIED", "dispatch_implementer", "implementer", "", "G_ATTEMPTS_REMAIN", at(6, 8, 0, 3), true},
		{"tester dispatch 2/3", "TEST_NEEDS_WORK", "dispatch_tester", "tester", "", "G_ATTEMPTS_REMAIN", at(5, 5, 2, 3), true},
		{"tester dispatch 3/3", "TEST_NEEDS_WORK", "dispatch_tester", "tester", "", "G_ATTEMPTS_REMAIN", at(0, 5, 3, 3), false},
		{"green failure with 4/5 used", "GREEN_VERIFYING", "green_classified", "", "", "G_ATTEMPTS_REMAIN", at(4, 5, 1, 3), true},
		{"green failure with 5/5 used", "GREEN_VERIFYING", "green_classified", "", "", "G_ATTEMPTS_REMAIN", at(5, 5, 1, 3), false},
		{"review changes with 5/5 used", "REVIEW", "review_changes_requested", "", "", "G_ATTEMPTS_REMAIN", at(5, 5, 1, 3), false},
		{"tester execution failure 2/3", "TEST_AUTHORING", "execution_failed", "", "", "G_ATTEMPTS_REMAIN", at(5, 5, 2, 3), true},
		{"tester execution failure 3/3", "TEST_AUTHORING", "execution_failed", "", "", "G_ATTEMPTS_REMAIN", at(0, 5, 3, 3), false},
		{"implementer execution failure 5/5", "IMPLEMENTING", "execution_failed", "", "", "G_ATTEMPTS_REMAIN", at(5, 5, 0, 3), false},
		{"reviews 2/3", "GREEN_VERIFIED", "dispatch_reviewer", "reviewer", "", "G_REVIEWS_REMAIN", Attempts{ReviewsUsed: 2, AutonomousMax: 5, TesterMax: 3}, true},
		{"reviews 3/3", "GREEN_VERIFIED", "dispatch_reviewer", "reviewer", "", "G_REVIEWS_REMAIN", Attempts{ReviewsUsed: 3, AutonomousMax: 5, TesterMax: 3}, false},
		{"auxiliary 7/8", "RED_VERIFIED", "dispatch_auxiliary", "researcher", "", "G_AUXILIARY_REMAIN", Attempts{AuxiliaryUsed: 7, AutonomousMax: 5, TesterMax: 3}, true},
		{"auxiliary 8/8", "RED_VERIFIED", "dispatch_auxiliary", "researcher", "", "G_AUXILIARY_REMAIN", Attempts{AuxiliaryUsed: 8, AutonomousMax: 5, TesterMax: 3}, false},
		{"requeue after 1 infra failure", "IMPLEMENTING", "execution_failed", "", "JOB_LOST", "G_INFRA_REQUEUE_ALLOWED", Attempts{ConsecutiveInfrastructure: 0, AutonomousMax: 5, TesterMax: 3}, true},
		{"requeue after 2 consecutive infra failures", "IMPLEMENTING", "execution_failed", "", "JOB_LOST", "G_INFRA_REQUEUE_ALLOWED", Attempts{ConsecutiveInfrastructure: 1, AutonomousMax: 5, TesterMax: 3}, true},
		{"third consecutive infra failure exhausts", "IMPLEMENTING", "execution_failed", "", "JOB_LOST", "G_INFRA_REQUEUE_ALLOWED", Attempts{ConsecutiveInfrastructure: 2, AutonomousMax: 5, TesterMax: 3}, false},
		{"total 6 of 8 still requeues", "IMPLEMENTING", "execution_failed", "", "JOB_LOST", "G_INFRA_REQUEUE_ALLOWED", Attempts{ByClass: map[string]int{"infrastructure": 6}, AutonomousMax: 5, TesterMax: 3}, true},
		{"total 7 of 8 exhausts", "IMPLEMENTING", "execution_failed", "", "JOB_LOST", "G_INFRA_REQUEUE_ALLOWED", Attempts{ByClass: map[string]int{"infrastructure": 7}, AutonomousMax: 5, TesterMax: 3}, false},
		{"first timeout requeues", "IMPLEMENTING", "execution_failed", "", "TIMED_OUT", "G_INFRA_REQUEUE_ALLOWED", Attempts{ConsecutiveInfrastructure: 0, ConsecutiveTimeouts: 0, AutonomousMax: 5, TesterMax: 3}, true},
		{"second consecutive timeout exhausts", "IMPLEMENTING", "execution_failed", "", "TIMED_OUT", "G_INFRA_REQUEUE_ALLOWED", Attempts{ConsecutiveInfrastructure: 1, ConsecutiveTimeouts: 1, AutonomousMax: 5, TesterMax: 3}, false},
		{"a non-timeout after a timeout requeues", "IMPLEMENTING", "execution_failed", "", "JOB_LOST", "G_INFRA_REQUEUE_ALLOWED", Attempts{ConsecutiveInfrastructure: 1, ConsecutiveTimeouts: 1, AutonomousMax: 5, TesterMax: 3}, true},
	}
	for _, c := range cases {
		task := acTask(c.state, c.a)
		// The base facts say the opposite for the owned guards: the accounting layer must not delegate them.
		b := base.clone()
		b.b[c.guard] = !c.want
		f := acFacts(t, task, StepInput{FailureCode: c.code}, b)
		got, known := f.Bool(Event{Name: c.event, Actor: "hermes", Role: c.role}, c.guard)
		if !known || got != c.want {
			t.Errorf("%s: %s = (%v, known %v), want (%v, known true)", c.name, c.guard, got, known, c.want)
		}
	}
}

// 29-AC-7: everything the accounting layer does not own is delegated to the base facts unchanged (known and
// unknown answers, boolean and valued); G_FAILURE_CODE is answered from the step input when it is set.
func TestAccountingFacts_DelegatesTheRest_29AC7(t *testing.T) {
	mdl := evContract(t)
	b := evPermissive(mdl)
	b.b["G_RED_EVIDENCE_VALID"] = false
	delete(b.b, "G_HEAD_UNCHANGED")
	b.v["G_VERIFICATION_OUTCOME"] = "green_fail"
	b.v["G_FAILURE_CODE"] = "FROM_BASE"
	f := acFacts(t, acTask("RED_VERIFIED", Attempts{AutonomousMax: 5, TesterMax: 3}), StepInput{}, b)
	ev := Event{Name: "dispatch_implementer", Actor: "hermes", Role: "implementer"}
	if v, k := f.Bool(ev, "G_RED_EVIDENCE_VALID"); !k || v {
		t.Errorf("G_RED_EVIDENCE_VALID = (%v, %v), want the base answer (false, known)", v, k)
	}
	if _, k := f.Bool(ev, "G_HEAD_UNCHANGED"); k {
		t.Error("an unknown base fact must stay unknown")
	}
	if v, k := f.Value(ev, "G_VERIFICATION_OUTCOME"); !k || v != "green_fail" {
		t.Errorf("G_VERIFICATION_OUTCOME = (%q, %v), want the base answer", v, k)
	}
	if v, k := f.Value(ev, "G_FAILURE_CODE"); !k || v != "FROM_BASE" {
		t.Errorf("G_FAILURE_CODE without step input code = (%q, %v), want the base answer", v, k)
	}
	f = acFacts(t, acTask("IMPLEMENTING", Attempts{AutonomousMax: 5, TesterMax: 3}), StepInput{FailureCode: "TIMED_OUT"}, b)
	if v, k := f.Value(Event{Name: "execution_failed", Actor: "runner"}, "G_FAILURE_CODE"); !k || v != "TIMED_OUT" {
		t.Errorf("G_FAILURE_CODE = (%q, %v), want the step input code TIMED_OUT", v, k)
	}
}

// acRawFire fires with the raw facts (the Facts of S2) so that the deltas of a transition are specified on their own.
func acRawFire(t testing.TB, m *Machine, snap TaskSnapshot, ev Event, f evFacts) *Applied {
	t.Helper()
	d, err := Fire(m, snap, ev, f)
	if err != nil {
		t.Fatalf("Fire(%s in %s): %v", ev.Name, snap.State, err)
	}
	if d.Applied == nil {
		t.Fatalf("Fire(%s in %s): want Applied, got %s", ev.Name, snap.State, evDescribe(d))
	}
	return d.Applied
}

// 29-AC-7 (plan 29 sections 7.1, 16.1): Applied carries the counter deltas of the fired transition, derived from
// the snapshot, the event and the facts only. Dispatch counts one attempt of the dispatched role class (tester
// or implementer/producer); reviewer and auxiliary dispatches count reviews_used and auxiliary_used; every
// E_COUNT_FAILURE adds one to by_class.<class>, a merit failure also to merit_by_generation of the failing role
// in the snapshot generation; an infrastructure ending adds consecutive_infrastructure (and a TIMED_OUT code
// consecutive_timeouts), any other ending resets both; an execution that ends infrastructure-class or with code
// EMERGENCY_STOP / BUDGET_SHARED_EXHAUSTED refunds its attempt (T100 requeues the same role, which counts again,
// so T100 nets to zero attempts; T101 and T105 keep the refund).
func TestFire_AppliedDeltas_29AC7(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	perm := evPermissive(mdl)
	with := func(f evFacts, v map[string]string, b map[string]bool) evFacts {
		f = f.clone()
		maps.Copy(f.v, v)
		maps.Copy(f.b, b)
		return f
	}
	snapWith := func(state string, edit func(*TaskSnapshot)) TaskSnapshot {
		s := evSnap(mdl, state, "test-first")
		if edit != nil {
			edit(&s)
		}
		return s
	}
	type tc struct {
		name string
		snap TaskSnapshot
		ev   Event
		f    evFacts
		id   string
		want map[string]int
	}
	cls := func(c, code string) map[string]string {
		return map[string]string{"G_FAILURE_CLASS": c, "G_FAILURE_CODE": code}
	}
	hermes := func(name, role string) Event { return Event{Name: name, Actor: "hermes", Role: role} }
	runner := func(name string) Event { return Event{Name: name, Actor: "runner"} }
	cases := []tc{
		{"T030 implementer dispatch", snapWith("RED_VERIFIED", nil), hermes("dispatch_implementer", "implementer"), perm, "T030", map[string]int{"autonomous_used": 1}},
		{"T010 first tester dispatch starts generation 1", snapWith("SPECIFIED", func(s *TaskSnapshot) { s.Generation = 0 }), hermes("dispatch_tester", "tester"), perm, "T010", map[string]int{"tester_used": 1, "generation": 1}},
		{"T011 tester retry", snapWith("TEST_NEEDS_WORK", nil), hermes("dispatch_tester", "tester"), perm, "T011", map[string]int{"tester_used": 1}},
		{"T040 reviewer dispatch", snapWith("GREEN_VERIFIED", nil), hermes("dispatch_reviewer", "reviewer"), perm, "T040", map[string]int{"reviews_used": 1}},
		{"T090 auxiliary dispatch", snapWith("RED_VERIFIED", nil), hermes("dispatch_auxiliary", "researcher"), perm, "T090", map[string]int{"auxiliary_used": 1}},
		{"T034 green_fail", snapWith("GREEN_VERIFYING", nil), Event{Name: "green_classified", Actor: "ci"}, with(perm, map[string]string{"G_VERIFICATION_OUTCOME": "green_fail"}, nil), "T034",
			map[string]int{"by_class.merit": 1, "merit_by_generation.implementer:g1": 1}},
		{"T035 green_fail exhausted (the failure is counted)", snapWith("GREEN_VERIFYING", func(s *TaskSnapshot) { s.Generation = 2 }), Event{Name: "green_classified", Actor: "ci"},
			with(perm, map[string]string{"G_VERIFICATION_OUTCOME": "green_fail"}, map[string]bool{"G_ATTEMPTS_REMAIN": false}), "T035",
			map[string]int{"by_class.merit": 1, "merit_by_generation.implementer:g2": 1}},
		{"T021 red_invalid", snapWith("RED_VERIFYING", nil), Event{Name: "red_classified", Actor: "ci"}, with(perm, map[string]string{"G_VERIFICATION_OUTCOME": "red_invalid"}, nil), "T021",
			map[string]int{"by_class.merit": 1, "merit_by_generation.tester:g1": 1}},
		{"T102 merit execution failure", snapWith("IMPLEMENTING", nil), runner("execution_failed"), with(perm, cls("merit", "HARNESS_GAVE_UP"), nil), "T102",
			map[string]int{"by_class.merit": 1}},
		{"T100 infrastructure requeue resets the timeouts", snapWith("IMPLEMENTING", func(s *TaskSnapshot) { s.Attempts.ConsecutiveTimeouts = 1 }), runner("execution_failed"),
			with(perm, cls("infrastructure", "JOB_LOST"), nil), "T100", map[string]int{"by_class.infrastructure": 1, "consecutive_infrastructure": 1, "consecutive_timeouts": -1}},
		{"T100 TIMED_OUT", snapWith("IMPLEMENTING", nil), runner("execution_failed"), with(perm, cls("infrastructure", "TIMED_OUT"), nil), "T100",
			map[string]int{"by_class.infrastructure": 1, "consecutive_infrastructure": 1, "consecutive_timeouts": 1}},
		{"T101 exhausted keeps the tester refund", snapWith("TEST_AUTHORING", func(s *TaskSnapshot) { s.Attempts.ConsecutiveInfrastructure, s.Attempts.ConsecutiveTimeouts = 1, 1 }), runner("execution_failed"),
			with(perm, cls("infrastructure", "TIMED_OUT"), map[string]bool{"G_INFRA_REQUEUE_ALLOWED": false}), "T101",
			map[string]int{"by_class.infrastructure": 1, "consecutive_infrastructure": 1, "consecutive_timeouts": 1, "tester_used": -1}},
		{"T105 emergency stop refunds and resets", snapWith("IMPLEMENTING", func(s *TaskSnapshot) { s.Attempts.ConsecutiveInfrastructure, s.Attempts.ConsecutiveTimeouts = 2, 1 }), runner("execution_failed"),
			with(perm, cls("budget", "EMERGENCY_STOP"), nil), "T105", map[string]int{"by_class.budget": 1, "autonomous_used": -1, "consecutive_infrastructure": -2, "consecutive_timeouts": -1}},
		{"T106 task budget does not refund", snapWith("IMPLEMENTING", nil), runner("execution_failed"), with(perm, cls("budget", "BUDGET_TASK_EXHAUSTED"), nil), "T106",
			map[string]int{"by_class.budget": 1}},
		{"T032 accepted result resets the consecutive infrastructure count", snapWith("IMPLEMENTING", func(s *TaskSnapshot) { s.Attempts.ConsecutiveInfrastructure = 1 }), runner("implementer_result_accepted"), perm, "T032",
			map[string]int{"consecutive_infrastructure": -1}},
		{"T014 no counters", snapWith("TEST_AUTHORING", nil), runner("tester_result_accepted"), perm, "T014", nil},
	}
	for _, c := range cases {
		a := acRawFire(t, m, c.snap, c.ev, c.f)
		if a.TransitionID != c.id {
			t.Errorf("%s: fired %s, want %s", c.name, a.TransitionID, c.id)
			continue
		}
		want := c.want
		if want == nil {
			want = map[string]int{}
		}
		got := a.Deltas
		if got == nil {
			got = map[string]int{}
		}
		if !maps.Equal(got, want) {
			t.Errorf("%s: Deltas = %v, want %v", c.name, got, want)
		}
	}
	// The input snapshot is never modified by Fire.
	s := snapWith("RED_VERIFIED", nil)
	before := fmt.Sprintf("%+v", s)
	acRawFire(t, m, s, hermes("dispatch_implementer", "implementer"), perm)
	if after := fmt.Sprintf("%+v", s); after != before {
		t.Errorf("Fire modified the snapshot: %s -> %s", before, after)
	}
}

// 29-AC-7: Advance rejects what it cannot apply (a Rejected or empty decision, a decision whose From is not
// the task's state) with a *FireError and returns the zero Task; it never modifies its argument.
func TestAdvance_FailsClosed_29AC7(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	perm := evPermissive(mdl)
	task := acTask("RED_VERIFIED", Attempts{AutonomousMax: 5, TesterMax: 3})
	task.Snapshot.ReturnState = "SPECIFIED"
	ev := Event{Name: "dispatch_implementer", Actor: "hermes", Role: "implementer"}
	in := StepInput{Dispatch: DispatchSpec{Role: "implementer", Model: "m1"}}
	d, err := Fire(m, task.Snapshot, ev, perm)
	if err != nil || d.Applied == nil {
		t.Fatalf("setup: Fire = %s, %v", evDescribe(d), err)
	}
	for name, dec := range map[string]Decision{
		"rejected":   {Rejected: &Rejected{Code: CodeGateNotSatisfied}},
		"empty":      {},
		"wrong from": {Applied: &Applied{TransitionID: "T030", From: "SPECIFIED", To: "IMPLEMENTING", Deltas: map[string]int{"autonomous_used": 1}}},
	} {
		before := fmt.Sprintf("%+v", task)
		got, err := Advance(m, task, ev, dec, in, acPolicy)
		var fe *FireError
		wantCode := AdvanceNotApplied
		if name == "wrong from" {
			wantCode = AdvanceStateMismatch
		}
		if !errors.As(err, &fe) || fe.Code != wantCode {
			t.Errorf("%s: Advance error = %v, want a *FireError with code %s", name, err, wantCode)
		}
		if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", Task{}) {
			t.Errorf("%s: Advance returned a non-zero Task with an error: %+v", name, got)
		}
		if after := fmt.Sprintf("%+v", task); after != before {
			t.Errorf("%s: Advance modified its argument", name)
		}
	}
	before := fmt.Sprintf("%+v", task)
	got, err := Advance(m, task, ev, d, in, acPolicy)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if after := fmt.Sprintf("%+v", task); after != before {
		t.Errorf("Advance modified its argument: %s -> %s", before, after)
	}
	s := got.Snapshot
	if s.State != "IMPLEMENTING" || s.ReturnState != "RED_VERIFIED" || s.Attempts.AutonomousUsed != 1 {
		t.Errorf("after T030: state %s, ReturnState %s, autonomous_used %d; want IMPLEMENTING, RED_VERIFIED (the dispatch-ready state), 1",
			s.State, s.ReturnState, s.Attempts.AutonomousUsed)
	}
	if len(got.Executions) != 1 || got.Executions[0].Role != "implementer" || !got.Executions[0].Counted || got.Executions[0].Fingerprint != Fingerprint(in.Dispatch) || got.Executions[0].Generation != 1 {
		t.Errorf("Executions = %+v, want one counted implementer execution of generation 1 with the dispatch fingerprint", got.Executions)
	}
	if len(got.History) != 1 || got.History[0].Event != ev || got.History[0].Applied.TransitionID != "T030" {
		t.Errorf("History = %+v, want the one applied step", got.History)
	}
}
