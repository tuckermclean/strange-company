package statemachine

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing"
)

// Golden retry sequences of 29-AC-7 and the identical-replay specification of 29-AC-8. They drive the real
// contract step by step through Apply (AccountingFacts, Fire, Advance) with the policy values of acPolicy
// (see accounting_test.go). Deterministic; no clock, no global random source (D7, D8).

type acRun struct {
	t    *testing.T
	m    *Machine
	mdl  *evModel
	task Task
	n    int // dispatch counter, makes every dispatch distinct unless a test says otherwise
}

func newRun(t *testing.T, mode string) *acRun {
	t.Helper()
	return &acRun{t: t, m: evMachine(t), mdl: evContract(t),
		task: Task{Snapshot: TaskSnapshot{Mode: mode, Attempts: Attempts{AutonomousMax: acPolicy.MaxAutonomousAttempts, TesterMax: acPolicy.MaxTesterAttempts,
			MeritByGeneration: map[string]int{}, ByClass: map[string]int{}}}}}
}

func (r *acRun) a() Attempts { return r.task.Snapshot.Attempts }

func (r *acRun) facts(vals map[string]string) evFacts {
	f := evPermissive(r.mdl)
	maps.Copy(f.v, vals)
	return f
}

// try applies an event and returns the decision; the task advances only when it was applied.
func (r *acRun) try(actor, event, role string, in StepInput, vals map[string]string) Decision {
	r.t.Helper()
	d, nt, err := Apply(r.m, r.task, Event{Name: event, Actor: actor, Role: role}, in, acPolicy, r.facts(vals))
	if err != nil {
		r.t.Fatalf("Apply(%s %s) from %s: %v", actor, event, r.task.Snapshot.State, err)
	}
	if (d.Applied == nil) == (d.Rejected == nil) {
		r.t.Fatalf("Apply(%s %s) from %s: decision must hold exactly one of Applied and Rejected, got %+v", actor, event, r.task.Snapshot.State, d)
	}
	if d.Rejected != nil && !reflect.DeepEqual(nt, r.task) {
		r.t.Fatalf("Apply(%s %s): a Rejected event changed the task", actor, event)
	}
	r.task = nt
	return d
}

// ok applies an event that must fire transition id and leave the task in state to.
func (r *acRun) ok(actor, event, role string, in StepInput, vals map[string]string, id, to string) *Applied {
	r.t.Helper()
	from := r.task.Snapshot.State
	d := r.try(actor, event, role, in, vals)
	if d.Applied == nil {
		r.t.Fatalf("%s %s from %s: want Applied %s, got %s", actor, event, from, id, evDescribe(d))
	}
	if d.Applied.TransitionID != id || d.Applied.To != to || r.task.Snapshot.State != to {
		r.t.Fatalf("%s %s from %s: fired %s -> %s (task state %s), want %s -> %s", actor, event, from, d.Applied.TransitionID, d.Applied.To, r.task.Snapshot.State, id, to)
	}
	return d.Applied
}

func (r *acRun) spec(role string, i int) StepInput {
	return StepInput{Dispatch: DispatchSpec{Role: role, Harness: "claude-code", Model: fmt.Sprintf("%s-tier-%d", role, i), Profile: "std",
		StartCommit: fmt.Sprintf("c%d", i), ACVersion: 1, OrchestratorNotes: "notes"}}
}

// toRedVerified: creation, the tester dispatch, its accepted result and a valid RED (the "tester ok" prefix).
func (r *acRun) toRedVerified() {
	r.t.Helper()
	r.ok("hermes", "task_created", "", StepInput{}, nil, "T001", "SPECIFIED")
	r.ok("hermes", "dispatch_tester", "tester", r.spec("tester", 1), nil, "T010", "TEST_AUTHORING")
	r.ok("runner", "tester_result_accepted", "", StepInput{}, nil, "T014", "RED_VERIFYING")
	r.ok("ci", "red_classified", "", StepInput{}, map[string]string{"G_VERIFICATION_OUTCOME": "red_valid"}, "T020", "RED_VERIFIED")
}

// implRound: dispatch the implementer (tier i), accept its result, GREEN fails with signature sig.
func (r *acRun) implRound(i int, sig, wantFail string, wantTo string) {
	r.t.Helper()
	r.ok("hermes", "dispatch_implementer", "implementer", r.spec("implementer", i), nil, "T030", "IMPLEMENTING")
	r.ok("runner", "implementer_result_accepted", "", StepInput{}, nil, "T032", "GREEN_VERIFYING")
	r.ok("ci", "green_classified", "", StepInput{FailureSignature: sig}, map[string]string{"G_VERIFICATION_OUTCOME": "green_fail"}, wantFail, wantTo)
}

func (r *acRun) want(what string, got, want int) {
	r.t.Helper()
	if got != want {
		r.t.Errorf("%s = %d, want %d", what, got, want)
	}
}

func (r *acRun) checkExhausted() {
	r.t.Helper()
	s := r.task.Snapshot
	if s.State != "NEEDS_HUMAN" || r.task.NeedsHumanReason != "retries_exhausted" {
		r.t.Errorf("final state %s with reason %q, want NEEDS_HUMAN with retries_exhausted", s.State, r.task.NeedsHumanReason)
	}
	r.want(`merit_by_generation["implementer:g1"]`, s.Attempts.MeritByGeneration["implementer:g1"], 5)
	r.want("autonomous_used", s.Attempts.AutonomousUsed, 5)
	r.want("by_class.merit", s.Attempts.ByClass["merit"], 5)
	r.want("tester_used", s.Attempts.TesterUsed, 1)
	if s.ResumeState != "GREEN_VERIFYING" {
		r.t.Errorf("ResumeState = %q, want GREEN_VERIFYING (plan 29 section 14.9: a verification state resumes into itself)", s.ResumeState)
	}
}

// 29-AC-7: the golden sequence "tester ok, GREEN fail x5" ends in NEEDS_HUMAN with retries_exhausted after the
// fifth implementer dispatch, with merit_by_generation["implementer:g1"] = 5. Every dispatch changes the tier
// (a different model), so none is an identical replay; the first four GREEN failures return to RED_VERIFIED
// (T034), the fifth escalates (T035); then no sixth implementer dispatch is possible.
func TestRetryAccounting_GreenFailTimesFive_29AC7(t *testing.T) {
	r := newRun(t, "test-first")
	r.toRedVerified()
	r.want("tester_used after the tester dispatch", r.a().TesterUsed, 1)
	if r.task.Snapshot.Generation != 1 {
		t.Errorf("Generation = %d after the first tester dispatch, want 1", r.task.Snapshot.Generation)
	}
	for i := 1; i <= 4; i++ {
		r.implRound(i, fmt.Sprintf("sig-%d", i), "T034", "RED_VERIFIED")
		r.want(fmt.Sprintf("autonomous_used after dispatch %d", i), r.a().AutonomousUsed, i)
		r.want(fmt.Sprintf("merit_by_generation after failure %d", i), r.a().MeritByGeneration["implementer:g1"], i)
	}
	r.ok("hermes", "dispatch_implementer", "implementer", r.spec("implementer", 5), nil, "T030", "IMPLEMENTING")
	r.want("autonomous_used after the 5th implementer dispatch", r.a().AutonomousUsed, 5)
	r.ok("runner", "implementer_result_accepted", "", StepInput{}, nil, "T032", "GREEN_VERIFYING")
	a := r.ok("ci", "green_classified", "", StepInput{FailureSignature: "sig-5"}, map[string]string{"G_VERIFICATION_OUTCOME": "green_fail"}, "T035", "NEEDS_HUMAN")
	if a.ReasonCode != "retries_exhausted" {
		t.Errorf("T035 ReasonCode = %q, want retries_exhausted", a.ReasonCode)
	}
	r.checkExhausted()
	if len(r.task.Executions) != 6 {
		t.Errorf("%d executions recorded, want 6 (1 tester + 5 implementer)", len(r.task.Executions))
	}
	if len(r.task.History) != 4+5*3 {
		t.Errorf("%d history steps, want %d", len(r.task.History), 4+5*3)
	}
	// Nothing autonomous leaves NEEDS_HUMAN: a sixth dispatch is refused.
	if d := r.try("hermes", "dispatch_implementer", "implementer", r.spec("implementer", 6), nil); d.Applied != nil {
		t.Errorf("a dispatch from NEEDS_HUMAN fired %s", evDescribe(d))
	}
	// A human retry grants humanGrant (3) more attempts and resumes the verification state.
	r.ok("human", "retry", "", StepInput{}, nil, "T066", "GREEN_VERIFYING")
	r.want("autonomous_max after /sc retry", r.a().AutonomousMax, 8)
	r.want("autonomous_used after /sc retry", r.a().AutonomousUsed, 5)
	if r.task.NeedsHumanReason != "" {
		t.Errorf("NeedsHumanReason = %q after the resume, want it cleared", r.task.NeedsHumanReason)
	}
	// The history replays to the very same task.
	checkReplay(t, r)
}

func checkReplay(t *testing.T, r *acRun) {
	t.Helper()
	initial := Task{Snapshot: TaskSnapshot{Mode: r.task.Snapshot.Mode, Attempts: Attempts{AutonomousMax: acPolicy.MaxAutonomousAttempts, TesterMax: acPolicy.MaxTesterAttempts,
		MeritByGeneration: map[string]int{}, ByClass: map[string]int{}}}}
	got, err := Replay(r.m, initial, r.task.History, acPolicy)
	if err != nil {
		t.Errorf("Replay: %v", err)
		return
	}
	if !reflect.DeepEqual(got, r.task) {
		t.Errorf("Replay of the history gives\n%+v\nwant\n%+v", got, r.task)
	}
}

// 29-AC-7: the same sequence with two non-consecutive infrastructure failures (after the 2nd and 4th dispatch)
// ends identically and shows autonomous_used = 5 and by_class.infrastructure = 2: each failed execution is
// refunded, its requeue (T100) counts again, so the net attempts are unchanged; the accepted result that follows
// resets the consecutive count.
func TestRetryAccounting_InfrastructureRefunds_29AC7(t *testing.T) {
	r := newRun(t, "test-first")
	r.toRedVerified()
	infra := 0
	for i := 1; i <= 5; i++ {
		r.ok("hermes", "dispatch_implementer", "implementer", r.spec("implementer", i), nil, "T030", "IMPLEMENTING")
		if i == 2 || i == 4 {
			infra++
			before := len(r.task.Executions)
			r.ok("runner", "execution_failed", "", StepInput{FailureCode: "JOB_LOST"}, map[string]string{"G_FAILURE_CLASS": "infrastructure"}, "T100", "IMPLEMENTING")
			r.want("autonomous_used after the infrastructure failure of dispatch "+fmt.Sprint(i), r.a().AutonomousUsed, i)
			r.want("by_class.infrastructure", r.a().ByClass["infrastructure"], infra)
			r.want("consecutive_infrastructure", r.a().ConsecutiveInfrastructure, 1)
			r.want("by_class.merit untouched", r.a().ByClass["merit"], i-1)
			ex := r.task.Executions
			if len(ex) != before+1 || !ex[before-1].Refunded || ex[before-1].Outcome != "infrastructure" ||
				ex[before].Fingerprint != ex[before-1].Fingerprint || !ex[before].Counted || ex[before].Role != "implementer" || ex[before].Refunded {
				t.Errorf("executions after the requeue = %+v; want the failed execution refunded (outcome infrastructure) and a counted requeue with the same fingerprint appended", ex)
			}
		}
		r.ok("runner", "implementer_result_accepted", "", StepInput{}, nil, "T032", "GREEN_VERIFYING")
		r.want("consecutive_infrastructure after an accepted result", r.a().ConsecutiveInfrastructure, 0)
		to, id := "RED_VERIFIED", "T034"
		if i == 5 {
			to, id = "NEEDS_HUMAN", "T035"
		}
		r.ok("ci", "green_classified", "", StepInput{FailureSignature: fmt.Sprintf("sig-%d", i)}, map[string]string{"G_VERIFICATION_OUTCOME": "green_fail"}, id, to)
	}
	r.checkExhausted()
	r.want("by_class.infrastructure", r.a().ByClass["infrastructure"], 2)
	r.want("autonomous_used", r.a().AutonomousUsed, 5)
	checkReplay(t, r)
}

// 29-AC-7 (plan 29 sections 15.2, 16.2): two consecutive TIMED_OUT implementer executions end in NEEDS_HUMAN
// with infrastructure_exhausted. The first is requeued (T100), the second is T101; the timeouts never count as
// merit and the second execution's attempt is refunded, nothing is left consumed.
func TestRetryAccounting_ConsecutiveTimeouts_29AC7(t *testing.T) {
	r := newRun(t, "test-first")
	r.toRedVerified()
	r.ok("hermes", "dispatch_implementer", "implementer", r.spec("implementer", 1), nil, "T030", "IMPLEMENTING")
	infra := map[string]string{"G_FAILURE_CLASS": "infrastructure"}
	r.ok("runner", "execution_failed", "", StepInput{FailureCode: "TIMED_OUT"}, infra, "T100", "IMPLEMENTING")
	r.want("consecutive_timeouts after the first timeout", r.a().ConsecutiveTimeouts, 1)
	a := r.ok("runner", "execution_failed", "", StepInput{FailureCode: "TIMED_OUT"}, infra, "T101", "NEEDS_HUMAN")
	if a.ReasonCode != "infrastructure_exhausted" || r.task.NeedsHumanReason != "infrastructure_exhausted" {
		t.Errorf("reason = %q / %q, want infrastructure_exhausted", a.ReasonCode, r.task.NeedsHumanReason)
	}
	r.want("consecutive_timeouts", r.a().ConsecutiveTimeouts, 2)
	r.want("by_class.infrastructure", r.a().ByClass["infrastructure"], 2)
	r.want("by_class.merit", r.a().ByClass["merit"], 0)
	r.want("merit_by_generation", r.a().MeritByGeneration["implementer:g1"], 0)
	r.want("autonomous_used (refunded)", r.a().AutonomousUsed, 0)
	if r.task.Snapshot.ResumeState != "RED_VERIFIED" {
		t.Errorf("ResumeState = %q, want RED_VERIFIED (plan 29 section 14.9: the execution's return state)", r.task.Snapshot.ResumeState)
	}
	checkReplay(t, r)
}

// 29-AC-7: a timeout followed by another infrastructure failure is not "consecutive timeouts" (any other
// terminal outcome resets that counter), and the third consecutive infrastructure failure of any kind exhausts
// maxConsecutive 3 (G_INFRA_REQUEUE_ALLOWED is evaluated after counting the current failure).
func TestRetryAccounting_ConsecutiveInfrastructureCap_29AC7(t *testing.T) {
	r := newRun(t, "test-first")
	r.toRedVerified()
	r.ok("hermes", "dispatch_implementer", "implementer", r.spec("implementer", 1), nil, "T030", "IMPLEMENTING")
	infra := map[string]string{"G_FAILURE_CLASS": "infrastructure"}
	r.ok("runner", "execution_failed", "", StepInput{FailureCode: "TIMED_OUT"}, infra, "T100", "IMPLEMENTING")
	r.ok("runner", "execution_failed", "", StepInput{FailureCode: "JOB_LOST"}, infra, "T100", "IMPLEMENTING")
	r.want("consecutive_timeouts reset by another outcome", r.a().ConsecutiveTimeouts, 0)
	r.want("consecutive_infrastructure", r.a().ConsecutiveInfrastructure, 2)
	r.ok("runner", "execution_failed", "", StepInput{FailureCode: "IMAGE_PULL"}, infra, "T101", "NEEDS_HUMAN")
	r.want("by_class.infrastructure", r.a().ByClass["infrastructure"], 3)
	r.want("autonomous_used (refunded)", r.a().AutonomousUsed, 0)
}

// 29-AC-7: an EMERGENCY_STOP failure ends in BLOCKED with blocked.reason_code = emergency_stop and no attempt
// consumed (T105); a shared budget window stop is BLOCKED with budget_window_exhausted; a task-attributable
// budget failure is NEEDS_HUMAN (T106) and keeps its attempt. A human unblock resumes the dispatch-ready state
// and clears the block.
func TestRetryAccounting_EmergencyStop_29AC7(t *testing.T) {
	budget := map[string]string{"G_FAILURE_CLASS": "budget"}
	r := newRun(t, "test-first")
	r.toRedVerified()
	r.ok("hermes", "dispatch_implementer", "implementer", r.spec("implementer", 1), nil, "T030", "IMPLEMENTING")
	r.want("autonomous_used while running", r.a().AutonomousUsed, 1)
	r.ok("runner", "execution_failed", "", StepInput{FailureCode: "EMERGENCY_STOP"}, budget, "T105", "BLOCKED")
	if r.task.BlockedReason != "emergency_stop" {
		t.Errorf("blocked.reason_code = %q, want emergency_stop", r.task.BlockedReason)
	}
	r.want("autonomous_used (no attempt consumed)", r.a().AutonomousUsed, 0)
	r.want("by_class.budget", r.a().ByClass["budget"], 1)
	r.want("by_class.merit", r.a().ByClass["merit"], 0)
	if r.task.Snapshot.ResumeState != "RED_VERIFIED" {
		t.Errorf("ResumeState = %q, want RED_VERIFIED", r.task.Snapshot.ResumeState)
	}
	if r.task.NeedsHumanReason != "" {
		t.Errorf("NeedsHumanReason = %q for a BLOCKED task, want none", r.task.NeedsHumanReason)
	}
	for _, actor := range []string{"runner", "ci", "hermes"} {
		if d := r.try(actor, "unblock", "", StepInput{}, nil); d.Applied != nil {
			t.Errorf("%s unblocked a BLOCKED task: %s", actor, evDescribe(d))
		}
	}
	r.ok("human", "unblock", "", StepInput{}, nil, "T065", "RED_VERIFIED")
	if r.task.BlockedReason != "" {
		t.Errorf("blocked.reason_code = %q after the unblock, want it cleared", r.task.BlockedReason)
	}
	checkReplay(t, r)

	r = newRun(t, "test-first")
	r.toRedVerified()
	r.ok("hermes", "dispatch_implementer", "implementer", r.spec("implementer", 1), nil, "T030", "IMPLEMENTING")
	r.ok("runner", "execution_failed", "", StepInput{FailureCode: "BUDGET_SHARED_EXHAUSTED"}, budget, "T105", "BLOCKED")
	if r.task.BlockedReason != "budget_window_exhausted" {
		t.Errorf("blocked.reason_code = %q, want budget_window_exhausted", r.task.BlockedReason)
	}
	r.want("autonomous_used", r.a().AutonomousUsed, 0)

	r = newRun(t, "test-first")
	r.toRedVerified()
	r.ok("hermes", "dispatch_implementer", "implementer", r.spec("implementer", 1), nil, "T030", "IMPLEMENTING")
	a := r.ok("runner", "execution_failed", "", StepInput{FailureCode: "BUDGET_TASK_EXHAUSTED"}, budget, "T106", "NEEDS_HUMAN")
	if a.ReasonCode != "budget_exceeded" || r.task.NeedsHumanReason != "budget_exceeded" {
		t.Errorf("reason = %q / %q, want budget_exceeded", a.ReasonCode, r.task.NeedsHumanReason)
	}
	r.want("autonomous_used (a task budget failure keeps the attempt)", r.a().AutonomousUsed, 1)
}

// 29-AC-7 (plan 29 sections 14.2, 16.6): testers get 3 attempts. Each invalid RED is a tester merit failure
// (T021) and the 4th tester dispatch is refused at dispatch time with ATTEMPTS_EXHAUSTED naming
// G_ATTEMPTS_REMAIN (the tester counter, not the implementer one).
func TestRetryAccounting_TesterAttempts_29AC7(t *testing.T) {
	r := newRun(t, "test-first")
	r.ok("hermes", "task_created", "", StepInput{}, nil, "T001", "SPECIFIED")
	for i := 1; i <= 3; i++ {
		id := "T011"
		if i == 1 {
			id = "T010"
		}
		r.ok("hermes", "dispatch_tester", "tester", r.spec("tester", i), nil, id, "TEST_AUTHORING")
		r.want("tester_used", r.a().TesterUsed, i)
		r.ok("runner", "tester_result_accepted", "", StepInput{}, nil, "T014", "RED_VERIFYING")
		r.ok("ci", "red_classified", "", StepInput{FailureSignature: fmt.Sprintf("red-%d", i)}, map[string]string{"G_VERIFICATION_OUTCOME": "red_invalid"}, "T021", "TEST_NEEDS_WORK")
		r.want(`merit_by_generation["tester:g1"]`, r.a().MeritByGeneration["tester:g1"], i)
	}
	r.want("autonomous_used", r.a().AutonomousUsed, 0)
	d := r.try("hermes", "dispatch_tester", "tester", r.spec("tester", 4), nil)
	if d.Rejected == nil || d.Rejected.Code != CodeAttemptsExhaust || !slices.Contains(d.Rejected.FailedGuards, "G_ATTEMPTS_REMAIN") {
		t.Errorf("4th tester dispatch: want Rejected{ATTEMPTS_EXHAUSTED} naming G_ATTEMPTS_REMAIN, got %s", evDescribe(d))
	}
}

// ---- 29-AC-8

// afterOneFailure: RED_VERIFIED after one implementer dispatch d (dispatch index i) failed GREEN on merit with
// signature sig.
func (r *acRun) failedDispatch(in StepInput, sig string) {
	r.t.Helper()
	r.ok("hermes", "dispatch_implementer", "implementer", in, nil, "T030", "IMPLEMENTING")
	r.ok("runner", "implementer_result_accepted", "", StepInput{}, nil, "T032", "GREEN_VERIFYING")
	r.ok("ci", "green_classified", "", StepInput{FailureSignature: sig}, map[string]string{"G_VERIFICATION_OUTCOME": "green_fail"}, "T034", "RED_VERIFIED")
}

// 29-AC-8 (plan 29 sections 7.2, 16.4): a dispatch whose fingerprint equals the fingerprint of an earlier
// execution of the same role in the same generation that ended in a merit failure is rejected by
// G_NOT_IDENTICAL_REPLAY alone, which maps to ATTEMPTS_EXHAUSTED with details.allowed = [identical_replay]; the
// task does not change; the same dispatch with a different tier (model) is accepted.
func TestIdenticalReplay_SameFingerprintRejected_29AC8(t *testing.T) {
	r := newRun(t, "test-first")
	r.toRedVerified()
	d1 := r.spec("implementer", 1)
	r.failedDispatch(d1, "sig-a")
	d := r.try("hermes", "dispatch_implementer", "implementer", d1, nil)
	if d.Rejected == nil {
		t.Fatalf("identical replay: want Rejected, got %s", evDescribe(d))
	}
	if !slices.Equal(d.Rejected.FailedGuards, []string{"G_NOT_IDENTICAL_REPLAY"}) {
		t.Errorf("FailedGuards = %v, want [G_NOT_IDENTICAL_REPLAY]", d.Rejected.FailedGuards)
	}
	if d.Rejected.Code != CodeAttemptsExhaust {
		t.Errorf("Code = %s, want ATTEMPTS_EXHAUSTED (plan 29 section 7.2)", d.Rejected.Code)
	}
	if got := d.Rejected.Allowed(); !slices.Equal(got, []string{"identical_replay"}) {
		t.Errorf("Allowed() = %v, want [identical_replay]", got)
	}
	r.want("autonomous_used after the refused replay", r.a().AutonomousUsed, 1)
	// A different tier is accepted.
	higher := d1
	higher.Dispatch.Model = "implementer-tier-2"
	r.ok("hermes", "dispatch_implementer", "implementer", higher, nil, "T030", "IMPLEMENTING")
	r.want("autonomous_used", r.a().AutonomousUsed, 2)
}

// 29-AC-8: only a merit or policy failure makes a fingerprint a replay. An execution that succeeded or ended
// infrastructure-class (refunded) does not (infrastructure requeues are exempt, plan 29 section 16.4), and a
// failure of another generation does not. AccountingFacts is specified directly on constructed tasks. A dispatch
// without a role in its DispatchSpec cannot be fingerprinted and fails closed (false, known).
func TestIdenticalReplay_OnlyMeritOrPolicyFailuresCount_29AC8(t *testing.T) {
	mdl := evContract(t)
	d := DispatchSpec{Role: "implementer", Harness: "h", Model: "m", Profile: "p", StartCommit: "c", ACVersion: 1, OrchestratorNotes: "n"}
	fp := Fingerprint(d)
	ev := Event{Name: "dispatch_implementer", Actor: "hermes", Role: "implementer"}
	mk := func(gen int, outcome string) Task {
		task := acTask("RED_VERIFIED", Attempts{AutonomousMax: 5, TesterMax: 3})
		task.Snapshot.Generation = gen
		task.Executions = []Execution{{Role: "implementer", Generation: 1, Fingerprint: fp, Harness: "h", Model: "m", Profile: "p", NotesDigest: acSHA("n"), Counted: true, Outcome: outcome}}
		return task
	}
	for _, c := range []struct {
		name string
		task Task
		in   DispatchSpec
		want bool
	}{
		{"earlier merit failure", mk(1, "merit"), d, false},
		{"earlier policy failure", mk(1, "policy"), d, false},
		{"earlier success", mk(1, "succeeded"), d, true},
		{"earlier infrastructure failure", mk(1, "infrastructure"), d, true},
		{"still running", mk(1, ""), d, true},
		{"merit failure of an earlier generation", mk(2, "merit"), d, true},
		{"no dispatch role", mk(1, "merit"), DispatchSpec{}, false},
	} {
		f := acFacts(t, c.task, StepInput{Dispatch: c.in}, evPermissive(mdl))
		got, known := f.Bool(ev, "G_NOT_IDENTICAL_REPLAY")
		if !known || got != c.want {
			t.Errorf("%s: G_NOT_IDENTICAL_REPLAY = (%v, known %v), want (%v, known true)", c.name, got, known, c.want)
		}
	}
}

// 29-AC-8 / D9b: Allowed() is only set when G_NOT_IDENTICAL_REPLAY failed alone.
func TestRejected_Allowed_D9b(t *testing.T) {
	for _, c := range []struct {
		guards []string
		want   []string
	}{
		{[]string{"G_NOT_IDENTICAL_REPLAY"}, []string{"identical_replay"}},
		{[]string{"G_ATTEMPTS_REMAIN"}, nil},
		{[]string{"G_NOT_IDENTICAL_REPLAY", "G_RED_EVIDENCE_VALID"}, nil},
		{nil, nil},
	} {
		if got := (Rejected{Code: CodeAttemptsExhaust, FailedGuards: c.guards}).Allowed(); !slices.Equal(got, c.want) {
			t.Errorf("Allowed() of %v = %v, want %v", c.guards, got, c.want)
		}
	}
}

// 29-AC-8: two consecutive equal failure signatures followed by a dispatch with an unchanged (harness, model,
// profile, sha256(notes)) is an identical replay even though its fingerprint differs (here: a new start commit);
// one failure, differing signatures, or a changed setting do not make it one.
func TestIdenticalReplay_EqualSignaturesSameSettings_29AC8(t *testing.T) {
	same := func(r *acRun, i int) StepInput {
		in := r.spec("implementer", 1) // same model, harness, profile, notes
		in.Dispatch.StartCommit = fmt.Sprintf("commit-%d", i)
		return in
	}
	setup := func(sig1, sig2 string) *acRun {
		r := newRun(t, "test-first")
		r.toRedVerified()
		r.failedDispatch(same(r, 1), sig1)
		r.failedDispatch(same(r, 2), sig2)
		return r
	}
	// Two equal signatures, then the unchanged settings: rejected.
	r := setup("sig-x", "sig-x")
	d := r.try("hermes", "dispatch_implementer", "implementer", same(r, 3), nil)
	if d.Rejected == nil || !slices.Contains(d.Rejected.FailedGuards, "G_NOT_IDENTICAL_REPLAY") {
		t.Errorf("equal signatures, unchanged settings: want Rejected naming G_NOT_IDENTICAL_REPLAY, got %s", evDescribe(d))
	}
	// A changed setting (notes, model, profile, harness) is accepted.
	for name, mut := range map[string]func(*DispatchSpec){
		"notes": func(x *DispatchSpec) { x.OrchestratorNotes = "new guidance" }, "model": func(x *DispatchSpec) { x.Model = "bigger" },
		"profile": func(x *DispatchSpec) { x.Profile = "heavy" }, "harness": func(x *DispatchSpec) { x.Harness = "codex" },
	} {
		r := setup("sig-x", "sig-x")
		in := same(r, 3)
		mut(&in.Dispatch)
		if d := r.try("hermes", "dispatch_implementer", "implementer", in, nil); d.Applied == nil {
			t.Errorf("changed %s: want Applied, got %s", name, evDescribe(d))
		}
	}
	// Differing signatures: accepted.
	r = setup("sig-x", "sig-y")
	if d := r.try("hermes", "dispatch_implementer", "implementer", same(r, 3), nil); d.Applied == nil {
		t.Errorf("differing signatures, unchanged settings: want Applied, got %s", evDescribe(d))
	}
	// Only one failure so far: accepted (the rule needs the last two).
	r = newRun(t, "test-first")
	r.toRedVerified()
	r.failedDispatch(same(r, 1), "sig-x")
	if d := r.try("hermes", "dispatch_implementer", "implementer", same(r, 2), nil); d.Applied == nil {
		t.Errorf("one failure, unchanged settings: want Applied, got %s", evDescribe(d))
	}
	// An infrastructure failure in between does not break or extend the run of merit failures: the last two MERIT
	// failures still have equal signatures.
	r = newRun(t, "test-first")
	r.toRedVerified()
	r.failedDispatch(same(r, 1), "sig-x")
	r.ok("hermes", "dispatch_implementer", "implementer", same(r, 2), nil, "T030", "IMPLEMENTING")
	r.ok("runner", "execution_failed", "", StepInput{FailureCode: "JOB_LOST"}, map[string]string{"G_FAILURE_CLASS": "infrastructure"}, "T100", "IMPLEMENTING")
	r.ok("runner", "implementer_result_accepted", "", StepInput{}, nil, "T032", "GREEN_VERIFYING")
	r.ok("ci", "green_classified", "", StepInput{FailureSignature: "sig-x"}, map[string]string{"G_VERIFICATION_OUTCOME": "green_fail"}, "T034", "RED_VERIFIED")
	if d := r.try("hermes", "dispatch_implementer", "implementer", same(r, 3), nil); d.Rejected == nil || !slices.Contains(d.Rejected.FailedGuards, "G_NOT_IDENTICAL_REPLAY") {
		t.Errorf("two equal merit signatures around an infrastructure failure: want Rejected naming G_NOT_IDENTICAL_REPLAY, got %s", evDescribe(d))
	}
}

// 29-AC-8: an infrastructure requeue is exempt: T100 re-queues the same role, harness, model and profile
// without going through G_NOT_IDENTICAL_REPLAY (covered by TestRetryAccounting_InfrastructureRefunds_29AC7, which
// requeues twice with an unchanged fingerprint), and a later dispatch with the fingerprint of an
// infrastructure-failed execution is accepted.
func TestIdenticalReplay_InfrastructureFailureIsNoReplay_29AC8(t *testing.T) {
	r := newRun(t, "test-first")
	r.toRedVerified()
	d1 := r.spec("implementer", 1)
	r.ok("hermes", "dispatch_implementer", "implementer", d1, nil, "T030", "IMPLEMENTING")
	r.ok("runner", "execution_failed", "", StepInput{FailureCode: "TIMED_OUT"}, map[string]string{"G_FAILURE_CLASS": "infrastructure"}, "T100", "IMPLEMENTING")
	r.ok("runner", "execution_failed", "", StepInput{FailureCode: "TIMED_OUT"}, map[string]string{"G_FAILURE_CLASS": "infrastructure"}, "T101", "NEEDS_HUMAN")
	r.ok("human", "retry", "", StepInput{}, nil, "T066", "RED_VERIFIED")
	if d := r.try("hermes", "dispatch_implementer", "implementer", d1, nil); d.Applied == nil {
		t.Errorf("a dispatch with the fingerprint of infrastructure-failed executions: want Applied, got %s", evDescribe(d))
	}
}
