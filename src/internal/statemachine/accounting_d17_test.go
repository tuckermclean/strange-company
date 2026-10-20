package statemachine

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Decision D17 (S4 review non-blockers N1 to N4): the library derives more task-state guards from the Task and
// refuses an adjudication that has no adjudicator behind it. Every fact check runs with the base answering the
// opposite of what the library must say (d14.fact), so a guard taken from the base fails.

// ---- N1: G_NO_ACTIVE_EXECUTION

// 29-AC-5, 29-AC-11 (D17 N1, plan 29 section 6.2 and the guard text "No execution of this task is QUEUED, STARTING
// or RUNNING"): the guard is derived from Task.Executions (an execution with an empty Outcome is running), not
// taken from the base. A running researcher auxiliary execution blocks an implementer dispatch with ALREADY_RUNNING.
func TestAccountingFacts_NoActiveExecution_29AC11_D17N1(t *testing.T) {
	g := "G_NO_ACTIVE_EXECUTION"
	d := newD14(t, "")
	d.toRedVerified()
	d.fact(g, true)
	d.ok("hermes", "dispatch_auxiliary", "researcher", d.spec("researcher"), "T090", "RED_VERIFIED")
	d.fact(g, false)
	dec, _ := d.peek("hermes", "dispatch_implementer", "implementer", d.spec("implementer"), nil)
	if dec.Rejected == nil {
		t.Fatalf("dispatch_implementer while a researcher auxiliary execution runs fired %s, want a rejection", evDescribe(dec))
	}
	if dec.Rejected.Code != "ALREADY_RUNNING" || !slices.Equal(dec.Rejected.FailedGuards, []string{g}) {
		t.Errorf("rejected %s with %v, want ALREADY_RUNNING with [%s]", dec.Rejected.Code, dec.Rejected.FailedGuards, g)
	}
	d.ok("runner", "auxiliary_result_recorded", "", StepInput{}, "T091", "RED_VERIFIED")
	d.fact(g, true)
	d.accepted("hermes", "dispatch_implementer", "implementer", d.spec("implementer"), "T030")
	d.ok("hermes", "dispatch_implementer", "implementer", d.spec("implementer"), "T030", "IMPLEMENTING")
	d.fact(g, false) // the implementer runs
}

// 29-AC-11 (D17 N1): Advance does not trust a decision forged with permissive facts while an execution runs.
func TestAdvance_RejectsDispatchWhileAnExecutionRuns_29AC11_D17N1(t *testing.T) {
	d := newD14(t, "")
	d.toRedVerified()
	d.ok("hermes", "dispatch_auxiliary", "researcher", d.spec("researcher"), "T090", "RED_VERIFIED")
	d17Forged(t, d, "hermes", "dispatch_implementer", "implementer", d.spec("implementer"), "G_NO_ACTIVE_EXECUTION")
}

// d17Forged fires the event with raw permissive facts and checks that Advance refuses the decision with
// SM_ADVANCE_NOT_APPLIED, and that the message names the guard when one is given.
func d17Forged(t *testing.T, d *d14, actor, event, role string, in StepInput, guard string) {
	t.Helper()
	ev := Event{Name: event, Actor: actor, Role: role}
	dec, err := Fire(d.m, d.task.Snapshot, ev, d.facts(nil))
	if err != nil || dec.Applied == nil {
		t.Fatalf("setup: raw Fire of %s from %s = %s, %v", event, d.task.Snapshot.State, evDescribe(dec), err)
	}
	got, err := Advance(d.m, d.task, ev, dec, in, d.p)
	var fe *FireError
	if !errors.As(err, &fe) || fe.Code != AdvanceNotApplied || (guard != "" && !strings.Contains(fe.Message, guard)) {
		t.Errorf("Advance(%s %s from %s) = %v, want %s %s", dec.Applied.TransitionID, event, d.task.Snapshot.State, err, AdvanceNotApplied, guard)
		return
	}
	if !reflect.DeepEqual(got, Task{}) {
		t.Errorf("Advance(%s): a non-zero Task came with the error", event)
	}
}

// ---- N2: G_NO_PRODUCER_DISPATCHED_IN_GENERATION (T112) and G_NO_PRODUCTION_IN_GENERATION (T123)

// toJudgeApproved drives a verification-first task to JUDGE_APPROVED (auto approval).
func (d *d14) toJudgeApproved() {
	d.t.Helper()
	d.task.Snapshot.Mode = "verification-first"
	d.ok("hermes", "task_created", "", StepInput{}, "T001", "SPECIFIED")
	dec, nt := d.peek("hermes", "dispatch_tester", "tester", d.spec("tester"), map[string]string{"G_JUDGE_SOURCE": "artifact"})
	if dec.Applied == nil || dec.Applied.TransitionID != "T110" {
		d.t.Fatalf("dispatch_tester in SPECIFIED: %s, want T110", evDescribe(dec))
	}
	d.task = nt
	d.ok("runner", "tester_result_accepted", "", StepInput{}, "T114", "JUDGE_VALIDATING")
	dec, nt = d.peek("ci", "judge_classified", "", StepInput{}, map[string]string{"G_VERIFICATION_OUTCOME": "judge_valid", "G_APPROVAL": "auto"})
	if dec.Applied == nil || dec.Applied.TransitionID != "T116" {
		d.t.Fatalf("judge_classified judge_valid auto: %s, want T116", evDescribe(dec))
	}
	d.task = nt
}

// 29-AC-11 (D17 N2, plan 21 section 14.8 strengthening before production): G_NO_PRODUCER_DISPATCHED_IN_GENERATION
// is derived from the recorded executions of the current generation: false from the first producer dispatch
// (dispatch_producer, running or blocked) on, true before. The reviewer auxiliary of T090 is no producer
// (in verification-first a researcher dispatched in JUDGE_APPROVED is a producer: roleEvent maps it to dispatch_producer).
// After a blocked producer and a human /sc strengthen-tests, dispatch_tester (T112) is refused by that guard alone.
func TestAccountingFacts_NoProducerDispatched_29AC11_D17N2(t *testing.T) {
	g := "G_NO_PRODUCER_DISPATCHED_IN_GENERATION"
	d := newD14(t, "")
	d.toJudgeApproved()
	d.fact(g, true)
	d.ok("hermes", "dispatch_auxiliary", "reviewer", d.spec("reviewer"), "T090", "JUDGE_APPROVED")
	d.ok("runner", "auxiliary_result_recorded", "", StepInput{}, "T091", "JUDGE_APPROVED")
	d.fact(g, true) // an auxiliary execution (a read-only reviewer) is not a producer dispatch
	d.ok("hermes", "dispatch_producer", "implementer", d.spec("implementer"), "T124", "PRODUCING")
	d.fact(g, false)
	d.ok("runner", "execution_blocked", "", StepInput{}, "T107", "JUDGE_APPROVED")
	d.fact(g, false) // blocked: the producer was still dispatched
	d.ok("human", "strengthen_tests", "", StepInput{}, "T016", "JUDGE_APPROVED")
	dec, _ := d.peek("hermes", "dispatch_tester", "tester", d.spec("tester"), map[string]string{"G_JUDGE_SOURCE": "artifact"})
	if dec.Rejected == nil {
		t.Fatalf("dispatch_tester after a producer was dispatched fired %s, want a rejection (T112 needs %s)", evDescribe(dec), g)
	}
	if dec.Rejected.Code != "GATE_NOT_SATISFIED" || !slices.Equal(dec.Rejected.FailedGuards, []string{g}) {
		t.Errorf("rejected %s with %v, want GATE_NOT_SATISFIED with [%s]", dec.Rejected.Code, dec.Rejected.FailedGuards, g)
	}
	d17Forged(t, d, "hermes", "dispatch_tester", "tester", d.spec("tester"), g)
}

// 29-AC-11 (D17 N2): a producer dispatched with the researcher role (dispatch_producer admits implementer and
// researcher) counts as a producer dispatch.
func TestAccountingFacts_NoProducerDispatched_ResearcherProducer_29AC11_D17N2(t *testing.T) {
	g := "G_NO_PRODUCER_DISPATCHED_IN_GENERATION"
	d := newD14(t, "")
	d.toJudgeApproved()
	d.ok("hermes", "dispatch_producer", "researcher", d.spec("researcher"), "T124", "PRODUCING")
	d.fact(g, false)
}

// 29-AC-11 (D17 N2, plan 21 section 14.8): G_NO_PRODUCTION_IN_GENERATION (T123 /sc deny-exemption) is derived
// likewise: a human may deny the exemption before any production, and not after a producer was dispatched; the
// base cannot lift the refusal.
func TestAccountingFacts_NoProduction_29AC11_D17N2(t *testing.T) {
	g := "G_NO_PRODUCTION_IN_GENERATION"
	d := newD14(t, "")
	d.toJudgeApproved()
	d.fact(g, true)
	d.accepted("human", "deny_exemption", "", StepInput{}, "T123")
	d.ok("hermes", "dispatch_producer", "implementer", d.spec("implementer"), "T124", "PRODUCING")
	d.fact(g, false)
	d.ok("runner", "execution_blocked", "", StepInput{}, "T107", "JUDGE_APPROVED")
	d.fact(g, false)
	d.refused("human", "deny_exemption", "", StepInput{}, g)
	d17Forged(t, d, "human", "deny_exemption", "", StepInput{}, g)
}

// ---- N3: adjudication_recorded needs a running reviewer auxiliary execution, an open conflict and a known verdict

// d17Adjudicable drives the task into TEST_SPEC_CONFLICT with a running reviewer auxiliary execution.
func d17Adjudicable(t *testing.T, resolution string) *d14 {
	t.Helper()
	d := newD14(t, resolution)
	d.toConflict()
	d.ok("hermes", "dispatch_auxiliary", "reviewer", d.spec("reviewer"), "T090", "TEST_SPEC_CONFLICT")
	return d
}

// d17NotApplied checks that adjudication_recorded is not applied by Apply (an error SM_ADVANCE_NOT_APPLIED or a
// rejection) and that Advance refuses the decision a permissive Fire produced.
func d17NotApplied(t *testing.T, d *d14, verdict string) {
	t.Helper()
	in := StepInput{Verdict: verdict}
	dec, _, err := Apply(d.m, d.task, Event{Name: "adjudication_recorded", Actor: "runner"}, in, d.p, d.facts(nil))
	if dec.Applied != nil {
		t.Errorf("Apply(adjudication_recorded, verdict %q) applied %s, want it refused", verdict, evDescribe(dec))
	}
	var fe *FireError
	if err != nil && (!errors.As(err, &fe) || fe.Code != AdvanceNotApplied) {
		t.Errorf("Apply(adjudication_recorded, verdict %q) error = %v, want %s", verdict, err, AdvanceNotApplied)
	}
	if err == nil && dec.Rejected == nil {
		t.Errorf("Apply(adjudication_recorded, verdict %q) neither applied nor rejected: %+v", verdict, dec)
	}
	d17Forged(t, d, "runner", "adjudication_recorded", "", in, "")
}

// 29-AC-11 (D17 N3, TDD-20, plan 29 section 14.6): an adjudication needs the adjudicator behind it. A lone T082
// without T090 (no running execution) is refused, and under resolutionApproval adjudicator it does not set
// G_TEST_CHANGE_APPROVED.
func TestAdvance_AdjudicationWithoutAnExecution_29AC11_D17N3(t *testing.T) {
	for _, resolution := range []string{"human", "adjudicator"} {
		t.Run(resolution, func(t *testing.T) {
			d := newD14(t, resolution)
			d.toConflict()
			d17NotApplied(t, d, "test_invalid")
			d.fact("G_TEST_CHANGE_APPROVED", false)
			d.fact("G_CONFLICT_REJECTED", false)
			if d.task.Snapshot.ConflictAwaitingApproval {
				t.Error("ConflictAwaitingApproval = true: a refused adjudication left a trace")
			}
			d.refused("hermes", "dispatch_tester", "tester", d.spec("tester"), "G_TEST_CHANGE_APPROVED")
		})
	}
}

// 29-AC-11 (D17 N3): the running execution must be a reviewer (the adjudicator); a researcher auxiliary
// execution cannot record an adjudication, and neither can an execution that already ended.
func TestAdvance_AdjudicationNeedsAReviewer_29AC11_D17N3(t *testing.T) {
	d := newD14(t, "adjudicator")
	d.toConflict()
	d.ok("hermes", "dispatch_auxiliary", "researcher", d.spec("researcher"), "T090", "TEST_SPEC_CONFLICT")
	d17NotApplied(t, d, "test_invalid")
	d.fact("G_TEST_CHANGE_APPROVED", false)

	e := d17Adjudicable(t, "adjudicator")
	e.ok("runner", "adjudication_recorded", "", StepInput{Verdict: "test_invalid"}, "T082", "TEST_SPEC_CONFLICT")
	e.fact("G_TEST_CHANGE_APPROVED", true)
	d17NotApplied(t, e, "test_invalid") // the adjudicator's execution ended with its verdict
}

// 29-AC-11 (D17 N3): E_RECORD_ADJUDICATION never opens a conflict. A task in TEST_SPEC_CONFLICT whose record holds
// no open conflict (an adjudicator running) refuses the verdict.
func TestAdvance_AdjudicationNeedsAnOpenConflict_29AC11_D17N3(t *testing.T) {
	d := d17Adjudicable(t, "adjudicator")
	d.task.Conflict = ConflictRecord{}
	d17NotApplied(t, d, "test_invalid")
	d.fact("G_TEST_CHANGE_APPROVED", false)
	d.fact("G_CONFLICT_REJECTED", false)
}

// 29-AC-11 (D17 N3): an empty or unknown verdict is refused (it is not recorded as a disagreement), the three
// known ones are accepted.
func TestAdvance_AdjudicationNeedsAKnownVerdict_29AC11_D17N3(t *testing.T) {
	for _, v := range []string{"", "maybe", "TEST_VALID", "test_valid "} {
		t.Run("verdict "+v, func(t *testing.T) {
			d := d17Adjudicable(t, "adjudicator")
			d17NotApplied(t, d, v)
			d.fact("G_TEST_CHANGE_APPROVED", false)
			if got := len(d.task.Conflict.Verdicts); got != 0 {
				t.Errorf("%d verdicts recorded, want none", got)
			}
		})
	}
	for _, v := range []string{"test_valid", "test_invalid", "spec_ambiguous"} {
		t.Run("verdict "+v, func(t *testing.T) {
			d := d17Adjudicable(t, "human")
			d.ok("runner", "adjudication_recorded", "", StepInput{Verdict: v}, "T082", "TEST_SPEC_CONFLICT")
		})
	}
}

// ---- N4: a human approval is authoritative

// 29-AC-11 (D17 N4, plan 29 section 14.6): after a test_valid adjudication the human may still approve the test
// change (T083). The approval is authoritative: G_TEST_CHANGE_APPROVED holds, G_CONFLICT_REJECTED does not, so
// only T013 can fire and T031 cannot. Under both policies.
func TestConflictRecord_HumanApprovalOverridesTestValid_29AC11_D17N4(t *testing.T) {
	for _, resolution := range []string{"human", "adjudicator"} {
		t.Run(resolution, func(t *testing.T) {
			d := newD14(t, resolution)
			d.toConflict()
			d.adjudicate("test_valid")
			d.fact("G_CONFLICT_REJECTED", true) // before the human decides, the adjudication stands
			d.fact("G_TEST_CHANGE_APPROVED", false)
			d.ok("human", "approve_test_change", "", StepInput{}, "T083", "TEST_SPEC_CONFLICT")
			d.fact("G_TEST_CHANGE_APPROVED", true)
			d.fact("G_CONFLICT_REJECTED", false)
			d.refused("hermes", "dispatch_implementer", "implementer", d.spec("implementer"), "G_CONFLICT_REJECTED")
			d.accepted("hermes", "dispatch_tester", "tester", d.spec("tester"), "T013")
			d17Forged(t, d, "hermes", "dispatch_implementer", "implementer", d.spec("implementer"), "G_CONFLICT_REJECTED")
			d.ok("hermes", "dispatch_tester", "tester", d.spec("tester"), "T013", "TEST_AUTHORING")
		})
	}
}
