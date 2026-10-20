package statemachine

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// Decision D14 (S4 RED audit): the task-state guards are derived by the library from the Task, like the counter
// guards, and never taken from the base facts. 29-AC-11 (plan 29 sections 14.2, 14.5, 14.6, TDD-5, TDD-6, TDD-20):
//
//   - G_NO_IMPLEMENTER_DISPATCHED_IN_GENERATION: no implementer execution was dispatched in the current
//     generation, whatever its outcome; a new generation resets it;
//   - the conflict record: E_RECORD_CONFLICT opens it; E_RECORD_ADJUDICATION records the verdicts (StepInput.Verdict
//     of adjudication_recorded); the first adjudication is decisive, a later one can only tighten test_invalid to
//     test_valid; disagreeing adjudications force human approval whatever RetryPolicy.ResolutionApproval says;
//     with "adjudicator" a (non-disputed) decisive test_invalid sets test_change_approved; E_APPROVE_TEST_CHANGE
//     sets it; E_REJECT_CONFLICT closes the record. G_TEST_CHANGE_APPROVED and G_CONFLICT_REJECTED (the
//     adjudication found the test valid, T031) are answered from it, and Snapshot.ConflictAwaitingApproval says
//     that a decisive test_invalid awaits the human;
//   - G_STRENGTHEN_AUTHORIZED: a strengthen_tests decision (T016) is recorded for the current episode and cleared
//     when the episode ends (plan 29 section 14.5).
//
// Every fact check below runs with the base answering the opposite of what the library must say, so a guard
// taken from the base fails.

type d14 struct {
	t    *testing.T
	m    *Machine
	mdl  *evModel
	p    RetryPolicy
	task Task
	n    int
}

func newD14(t *testing.T, resolution string) *d14 {
	t.Helper()
	p := acPolicy
	p.ResolutionApproval = resolution
	return &d14{t: t, m: evMachine(t), mdl: evContract(t), p: p,
		task: Task{Snapshot: TaskSnapshot{Mode: "test-first", Attempts: Attempts{AutonomousMax: p.MaxAutonomousAttempts, TesterMax: p.MaxTesterAttempts,
			MeritByGeneration: map[string]int{}, ByClass: map[string]int{}}}}}
}

func (d *d14) spec(role string) StepInput {
	d.n++
	return StepInput{Dispatch: DispatchSpec{Role: role, Harness: "claude-code", Model: fmt.Sprintf("%s-model-%d", role, d.n), Profile: "std",
		StartCommit: fmt.Sprintf("c%d", d.n), ACVersion: 1, OrchestratorNotes: "notes"}}
}

func (d *d14) facts(vals map[string]string) evFacts {
	f := evPermissive(d.mdl)
	maps.Copy(f.v, vals)
	return f
}

// peek decides an event without advancing the task.
func (d *d14) peek(actor, event, role string, in StepInput, vals map[string]string) (Decision, Task) {
	d.t.Helper()
	dec, nt, err := Apply(d.m, d.task, Event{Name: event, Actor: actor, Role: role}, in, d.p, d.facts(vals))
	if err != nil {
		d.t.Fatalf("Apply(%s %s) from %s: %v", actor, event, d.task.Snapshot.State, err)
	}
	if (dec.Applied == nil) == (dec.Rejected == nil) {
		d.t.Fatalf("Apply(%s %s) from %s: the decision must hold exactly one of Applied and Rejected, got %+v", actor, event, d.task.Snapshot.State, dec)
	}
	if dec.Rejected != nil && !reflect.DeepEqual(nt, d.task) {
		d.t.Fatalf("Apply(%s %s): a Rejected event changed the task", actor, event)
	}
	return dec, nt
}

// ok applies an event that must fire transition id into state to.
func (d *d14) ok(actor, event, role string, in StepInput, id, to string) {
	d.t.Helper()
	dec, nt := d.peek(actor, event, role, in, nil)
	if dec.Applied == nil || dec.Applied.TransitionID != id || dec.Applied.To != to {
		d.t.Fatalf("%s %s from %s: %s, want %s -> %s", actor, event, d.task.Snapshot.State, evDescribe(dec), id, to)
	}
	d.task = nt
}

// refused checks that the event is rejected with GATE_NOT_SATISFIED and exactly the failed guards.
func (d *d14) refused(actor, event, role string, in StepInput, guards ...string) {
	d.t.Helper()
	dec, _ := d.peek(actor, event, role, in, nil)
	if dec.Rejected == nil {
		d.t.Errorf("%s %s from %s fired %s, want a rejection by %v", actor, event, d.task.Snapshot.State, evDescribe(dec), guards)
		return
	}
	got := slices.Clone(dec.Rejected.FailedGuards)
	want := slices.Clone(guards)
	sort.Strings(got)
	sort.Strings(want)
	if dec.Rejected.Code != "GATE_NOT_SATISFIED" || !slices.Equal(got, want) {
		d.t.Errorf("%s %s from %s: rejected %s with failed guards %v, want GATE_NOT_SATISFIED with %v", actor, event, d.task.Snapshot.State, dec.Rejected.Code, got, want)
	}
}

// accepted checks that the event fires transition id (without advancing).
func (d *d14) accepted(actor, event, role string, in StepInput, id string) {
	d.t.Helper()
	dec, _ := d.peek(actor, event, role, in, nil)
	if dec.Applied == nil || dec.Applied.TransitionID != id {
		d.t.Errorf("%s %s from %s: %s, want %s", actor, event, d.task.Snapshot.State, evDescribe(dec), id)
	}
}

// fact asks AccountingFacts for a boolean guard under a base that answers the opposite of want; the answer must
// be known and equal want.
func (d *d14) fact(guard string, want bool) {
	d.t.Helper()
	for _, baseVal := range []bool{want, !want} {
		base := evPermissive(d.mdl)
		base.b[guard] = baseVal
		f := AccountingFacts(d.task, d.p, StepInput{}, base)
		v, known := f.Bool(Event{Name: "dispatch_tester", Actor: "hermes", Role: "tester"}, guard)
		if !known || v != want {
			d.t.Errorf("AccountingFacts(%s in %s, base %v).%s = %v (known %v), want %v whatever the base says", d.task.Snapshot.Mode, d.task.Snapshot.State, baseVal, guard, v, known, want)
		}
	}
}

func (d *d14) toRedVerified() {
	d.t.Helper()
	d.ok("hermes", "task_created", "", StepInput{}, "T001", "SPECIFIED")
	d.ok("hermes", "dispatch_tester", "tester", d.spec("tester"), "T010", "TEST_AUTHORING")
	d.ok("runner", "tester_result_accepted", "", StepInput{}, "T014", "RED_VERIFYING")
	d.t.Helper()
	dec, nt := d.peek("ci", "red_classified", "", StepInput{}, map[string]string{"G_VERIFICATION_OUTCOME": "red_valid"})
	if dec.Applied == nil || dec.Applied.TransitionID != "T020" {
		d.t.Fatalf("red_classified red_valid: %s, want T020", evDescribe(dec))
	}
	d.task = nt
}

// toAlreadySatisfied: the RED passes already (T022).
func (d *d14) toAlreadySatisfied() {
	d.t.Helper()
	d.ok("hermes", "task_created", "", StepInput{}, "T001", "SPECIFIED")
	d.ok("hermes", "dispatch_tester", "tester", d.spec("tester"), "T010", "TEST_AUTHORING")
	d.ok("runner", "tester_result_accepted", "", StepInput{}, "T014", "RED_VERIFYING")
	d.redAlreadySatisfied()
}

func (d *d14) redAlreadySatisfied() {
	d.t.Helper()
	dec, nt := d.peek("ci", "red_classified", "", StepInput{}, map[string]string{"G_VERIFICATION_OUTCOME": "already_satisfied"})
	if dec.Applied == nil || dec.Applied.TransitionID != "T022" {
		d.t.Fatalf("red_classified already_satisfied: %s, want T022", evDescribe(dec))
	}
	d.task = nt
}

// toConflict: RED verified, an implementer dispatched, and it reports a test spec conflict (T080).
func (d *d14) toConflict() {
	d.t.Helper()
	d.toRedVerified()
	d.ok("hermes", "dispatch_implementer", "implementer", d.spec("implementer"), "T030", "IMPLEMENTING")
	d.ok("runner", "implementer_reported_conflict", "", StepInput{}, "T080", "TEST_SPEC_CONFLICT")
}

// adjudicate dispatches the adjudicator and records its verdict (T090, T082).
func (d *d14) adjudicate(verdict string) {
	d.t.Helper()
	d.ok("hermes", "dispatch_auxiliary", "reviewer", d.spec("reviewer"), "T090", "TEST_SPEC_CONFLICT")
	d.ok("runner", "adjudication_recorded", "", StepInput{Verdict: verdict}, "T082", "TEST_SPEC_CONFLICT")
}

// ---- G_NO_IMPLEMENTER_DISPATCHED_IN_GENERATION (29-AC-11 case 1, TDD-5)

// 29-AC-11 (TDD-5, plan 29 section 14.2): the guard is false from the first implementer dispatch of the
// generation on, whatever the outcome of that execution (running, blocked, GREEN failed), and true before.
func TestAccountingFacts_NoImplementerDispatched_29AC11_D14a(t *testing.T) {
	g := "G_NO_IMPLEMENTER_DISPATCHED_IN_GENERATION"
	d := newD14(t, "")
	d.toRedVerified()
	d.fact(g, true)
	d.ok("hermes", "dispatch_implementer", "implementer", d.spec("implementer"), "T030", "IMPLEMENTING")
	d.fact(g, false) // dispatched and running
	d.ok("runner", "execution_blocked", "", StepInput{}, "T107", "RED_VERIFIED")
	d.fact(g, false) // blocked: the implementer was still dispatched (case 1)

	// the strengthen decision of a human does not lift it: only the guard fails, then
	d.refused("hermes", "dispatch_tester", "tester", d.spec("tester"), "G_STRENGTHEN_AUTHORIZED", g)
	d.ok("human", "strengthen_tests", "", StepInput{}, "T016", "RED_VERIFIED")
	d.refused("hermes", "dispatch_tester", "tester", d.spec("tester"), g)
	d.refused("human", "dispatch_tester", "tester", d.spec("tester"), g)

	// a failed GREEN is an outcome too
	e := newD14(t, "")
	e.toRedVerified()
	e.ok("hermes", "dispatch_implementer", "implementer", e.spec("implementer"), "T030", "IMPLEMENTING")
	e.ok("runner", "implementer_result_accepted", "", StepInput{}, "T032", "GREEN_VERIFYING")
	dec, nt := e.peek("ci", "green_classified", "", StepInput{FailureSignature: "sig"}, map[string]string{"G_VERIFICATION_OUTCOME": "green_fail"})
	if dec.Applied == nil || dec.Applied.TransitionID != "T034" {
		t.Fatalf("green_fail: %s, want T034", evDescribe(dec))
	}
	e.task = nt
	e.fact(g, false)
}

// 29-AC-11: a new generation (the approved corrected test, T013) resets the guard; a dispatch of the next
// generation's implementer sets it again. Only implementer dispatches of the current generation count.
func TestAccountingFacts_NoImplementerDispatched_ResetsWithGeneration_29AC11_D14a(t *testing.T) {
	g := "G_NO_IMPLEMENTER_DISPATCHED_IN_GENERATION"
	d := newD14(t, "")
	d.toConflict()
	d.fact(g, false)
	d.ok("human", "approve_test_change", "", StepInput{}, "T083", "TEST_SPEC_CONFLICT")
	d.ok("hermes", "dispatch_tester", "tester", d.spec("tester"), "T013", "TEST_AUTHORING")
	if d.task.Snapshot.Generation != 2 {
		t.Fatalf("generation %d after T013, want 2", d.task.Snapshot.Generation)
	}
	d.fact(g, true)
	d.ok("runner", "tester_result_accepted", "", StepInput{}, "T014", "RED_VERIFYING")
	dec, nt := d.peek("ci", "red_classified", "", StepInput{}, map[string]string{"G_VERIFICATION_OUTCOME": "red_valid"})
	if dec.Applied == nil || dec.Applied.TransitionID != "T020" {
		t.Fatalf("red_valid: %s, want T020", evDescribe(dec))
	}
	d.task = nt
	d.fact(g, true)
	d.ok("hermes", "dispatch_implementer", "implementer", d.spec("implementer"), "T030", "IMPLEMENTING")
	d.fact(g, false)
}

// ---- G_STRENGTHEN_AUTHORIZED (29-AC-11 case 2, TDD-6, plan 29 sections 14.2 and 14.5)

// 29-AC-11 (TDD-6): from ALREADY_SATISFIED a tester dispatch is refused by G_STRENGTHEN_AUTHORIZED alone until a
// human decides /sc strengthen-tests (T016), then it fires T015. The decision belongs to the episode: it
// survives a researcher dispatch in the same stay, and it is spent by the dispatch, so a second
// ALREADY_SATISFIED needs a new decision.
func TestAccountingFacts_StrengthenAuthorized_AlreadySatisfied_29AC11_D14c(t *testing.T) {
	g := "G_STRENGTHEN_AUTHORIZED"
	d := newD14(t, "")
	d.toAlreadySatisfied()
	d.fact(g, false)
	d.refused("hermes", "dispatch_tester", "tester", d.spec("tester"), g)
	d.refused("human", "dispatch_tester", "tester", d.spec("tester"), g)
	d.ok("human", "strengthen_tests", "", StepInput{}, "T016", "ALREADY_SATISFIED")
	d.fact(g, true)
	// an investigation in the same stay keeps the decision
	d.ok("hermes", "dispatch_auxiliary", "researcher", d.spec("researcher"), "T090", "ALREADY_SATISFIED")
	d.ok("runner", "auxiliary_result_recorded", "", StepInput{}, "T091", "ALREADY_SATISFIED")
	d.fact(g, true)
	d.accepted("hermes", "dispatch_tester", "tester", d.spec("tester"), "T015")
	d.ok("hermes", "dispatch_tester", "tester", d.spec("tester"), "T015", "TEST_AUTHORING")
	d.fact(g, false) // the episode ended with the dispatch
	d.ok("runner", "tester_result_accepted", "", StepInput{}, "T014", "RED_VERIFYING")
	d.redAlreadySatisfied()
	d.fact(g, false)
	d.refused("hermes", "dispatch_tester", "tester", d.spec("tester"), g)
}

// 29-AC-11 (TDD-B3, plan 29 section 14.2): in RED_VERIFIED a strengthening dispatch (T012) needs the human
// decision, opens a new generation, and the decision is spent: the next RED_VERIFIED needs a new one.
func TestAccountingFacts_StrengthenAuthorized_RedVerified_29AC11_D14c(t *testing.T) {
	g := "G_STRENGTHEN_AUTHORIZED"
	d := newD14(t, "")
	d.toRedVerified()
	d.fact(g, false)
	d.refused("hermes", "dispatch_tester", "tester", d.spec("tester"), g)
	d.ok("human", "strengthen_tests", "", StepInput{}, "T016", "RED_VERIFIED")
	d.fact(g, true)
	d.ok("hermes", "dispatch_tester", "tester", d.spec("tester"), "T012", "TEST_AUTHORING")
	if d.task.Snapshot.Generation != 2 {
		t.Fatalf("generation %d after T012, want 2", d.task.Snapshot.Generation)
	}
	d.fact(g, false)
	d.ok("runner", "tester_result_accepted", "", StepInput{}, "T014", "RED_VERIFYING")
	dec, nt := d.peek("ci", "red_classified", "", StepInput{}, map[string]string{"G_VERIFICATION_OUTCOME": "red_valid"})
	if dec.Applied == nil || dec.Applied.TransitionID != "T020" {
		t.Fatalf("red_valid: %s, want T020", evDescribe(dec))
	}
	d.task = nt
	d.fact(g, false)
	d.refused("hermes", "dispatch_tester", "tester", d.spec("tester"), g)
}

// ---- the conflict record (29-AC-11 cases 3 and 4, TDD-20, plan 29 section 14.6)

type d14Case struct {
	name       string
	resolution string
	verdicts   []string
	approved   bool // G_TEST_CHANGE_APPROVED
	rejected   bool // G_CONFLICT_REJECTED: the effective adjudication is test_valid
	awaiting   bool // Snapshot.ConflictAwaitingApproval
}

var d14Cases = []d14Case{
	{"human, test_invalid awaits the human", "human", []string{"test_invalid"}, false, false, true},
	{"default policy is human", "", []string{"test_invalid"}, false, false, true},
	{"human, test_valid", "human", []string{"test_valid"}, false, true, false},
	{"human, spec_ambiguous", "human", []string{"spec_ambiguous"}, false, false, false},
	{"human, valid then invalid (29-AC-11 case 4): stays unapproved", "human", []string{"test_valid", "test_invalid"}, false, true, false},
	{"human, invalid then valid: tightened", "human", []string{"test_invalid", "test_valid"}, false, true, false},
	{"human, invalid twice", "human", []string{"test_invalid", "test_invalid"}, false, false, true},
	{"adjudicator, single test_invalid approves", "adjudicator", []string{"test_invalid"}, true, false, false},
	{"adjudicator, test_valid", "adjudicator", []string{"test_valid"}, false, true, false},
	{"adjudicator, spec_ambiguous then invalid: the first is decisive", "adjudicator", []string{"spec_ambiguous", "test_invalid"}, false, false, false},
	{"adjudicator, valid then invalid (case 4): not approved", "adjudicator", []string{"test_valid", "test_invalid"}, false, true, false},
	{"adjudicator, invalid then valid: disputed, the approval is withdrawn", "adjudicator", []string{"test_invalid", "test_valid"}, false, true, false},
	{"adjudicator, invalid twice (they agree)", "adjudicator", []string{"test_invalid", "test_invalid"}, true, false, false},
}

// 29-AC-11 (TDD-20): AccountingFacts and Advance keep the conflict record. G_TEST_CHANGE_APPROVED,
// G_CONFLICT_REJECTED and the awaiting-approval projection input follow the adjudications; the dispatches that
// those guards gate (T013 and T031) are refused or fired accordingly, with the base saying the opposite.
func TestConflictRecord_Adjudications_29AC11_D14b(t *testing.T) {
	for _, c := range d14Cases {
		t.Run(c.name, func(t *testing.T) {
			d := newD14(t, c.resolution)
			d.toConflict()
			d.fact("G_TEST_CHANGE_APPROVED", false) // open, nothing adjudicated yet
			d.fact("G_CONFLICT_REJECTED", false)
			for _, v := range c.verdicts {
				d.adjudicate(v)
			}
			d.fact("G_TEST_CHANGE_APPROVED", c.approved)
			d.fact("G_CONFLICT_REJECTED", c.rejected)
			if got := d.task.Snapshot.ConflictAwaitingApproval; got != c.awaiting {
				t.Errorf("Snapshot.ConflictAwaitingApproval = %v, want %v", got, c.awaiting)
			}
			if c.approved {
				d.accepted("hermes", "dispatch_tester", "tester", d.spec("tester"), "T013")
			} else {
				d.refused("hermes", "dispatch_tester", "tester", d.spec("tester"), "G_TEST_CHANGE_APPROVED")
			}
			if c.rejected {
				d.accepted("hermes", "dispatch_implementer", "implementer", d.spec("implementer"), "T031")
			} else {
				d.refused("hermes", "dispatch_implementer", "implementer", d.spec("implementer"), "G_CONFLICT_REJECTED")
			}
		})
	}
}

// 29-AC-11 (TDD-20, plan 29 section 14.6 item 2): disagreeing adjudications force human approval whatever the
// policy says, and the human approval (T083) then sets test_change_approved.
func TestConflictRecord_DisputedNeedsHuman_29AC11_D14b(t *testing.T) {
	d := newD14(t, "adjudicator")
	d.toConflict()
	d.adjudicate("test_invalid")
	d.fact("G_TEST_CHANGE_APPROVED", true) // the adjudicator approved, nobody disagreed yet
	d.adjudicate("test_valid")
	d.fact("G_TEST_CHANGE_APPROVED", false)
	d.refused("hermes", "dispatch_tester", "tester", d.spec("tester"), "G_TEST_CHANGE_APPROVED")
	d.ok("human", "approve_test_change", "", StepInput{}, "T083", "TEST_SPEC_CONFLICT")
	d.fact("G_TEST_CHANGE_APPROVED", true)
	d.ok("hermes", "dispatch_tester", "tester", d.spec("tester"), "T013", "TEST_AUTHORING")
}

// 29-AC-11 (plan 29 section 14.6 items 4 and 5): with resolutionApproval human a test_invalid adjudication
// notifies and waits; /sc accept-test-change (T083) sets test_change_approved and ends the wait, also without
// any adjudication; the dispatch of the corrected test is then T013.
func TestConflictRecord_HumanApproval_29AC11_D14b(t *testing.T) {
	d := newD14(t, "human")
	d.toConflict()
	d.adjudicate("test_invalid")
	d.fact("G_TEST_CHANGE_APPROVED", false)
	if !d.task.Snapshot.ConflictAwaitingApproval {
		t.Error("ConflictAwaitingApproval = false after a test_invalid adjudication under human approval")
	}
	d.ok("human", "approve_test_change", "", StepInput{}, "T083", "TEST_SPEC_CONFLICT")
	d.fact("G_TEST_CHANGE_APPROVED", true)
	if d.task.Snapshot.ConflictAwaitingApproval {
		t.Error("ConflictAwaitingApproval = true after the human approval")
	}
	d.ok("hermes", "dispatch_tester", "tester", d.spec("tester"), "T013", "TEST_AUTHORING")

	// without an adjudication
	e := newD14(t, "human")
	e.toConflict()
	e.fact("G_TEST_CHANGE_APPROVED", false)
	e.ok("human", "approve_test_change", "", StepInput{}, "T083", "TEST_SPEC_CONFLICT")
	e.fact("G_TEST_CHANGE_APPROVED", true)
}

// 29-AC-11: /sc reject-conflict (T084, E_REJECT_CONFLICT) closes the record: nothing stays approved or
// rejected, and the next conflict starts a fresh record.
func TestConflictRecord_RejectClosesAndNextConflictIsFresh_29AC11_D14b(t *testing.T) {
	d := newD14(t, "human")
	d.toConflict()
	d.adjudicate("test_valid")
	d.ok("human", "approve_test_change", "", StepInput{}, "T083", "TEST_SPEC_CONFLICT") // approved and valid, then rejected
	d.fact("G_TEST_CHANGE_APPROVED", true)
	d.ok("human", "reject_conflict", "", StepInput{}, "T084", "RED_VERIFIED")
	d.fact("G_TEST_CHANGE_APPROVED", false)
	d.fact("G_CONFLICT_REJECTED", false)
	if d.task.Snapshot.ConflictAwaitingApproval {
		t.Error("ConflictAwaitingApproval = true after the conflict was rejected")
	}
	d.ok("hermes", "dispatch_implementer", "implementer", d.spec("implementer"), "T030", "IMPLEMENTING")
	d.ok("runner", "implementer_reported_conflict", "", StepInput{}, "T080", "TEST_SPEC_CONFLICT")
	d.fact("G_TEST_CHANGE_APPROVED", false) // not carried over from the closed conflict
	d.fact("G_CONFLICT_REJECTED", false)
	d.refused("hermes", "dispatch_implementer", "implementer", d.spec("implementer"), "G_CONFLICT_REJECTED")
	d.adjudicate("test_invalid")
	d.fact("G_CONFLICT_REJECTED", false) // the old test_valid is gone
	d.fact("G_TEST_CHANGE_APPROVED", false)
}

// 29-AC-11: with no open conflict nothing is approved or rejected, whatever the base says.
func TestConflictRecord_NoConflict_29AC11_D14b(t *testing.T) {
	d := newD14(t, "adjudicator")
	d.toRedVerified()
	d.fact("G_TEST_CHANGE_APPROVED", false)
	d.fact("G_CONFLICT_REJECTED", false)
}

// 29-AC-11 (D11/D12 style): Advance does not trust a decision for a guard AccountingFacts answers from the task.
// A decision forged with permissive facts is refused when the task does not satisfy the guard, and the refusal
// names the guard.
func TestAdvance_RejectsForgedTaskStateGuards_29AC11_D14(t *testing.T) {
	forged := func(d *d14, actor, event, role string, in StepInput, guard string) {
		t.Helper()
		ev := Event{Name: event, Actor: actor, Role: role}
		dec, err := Fire(d.m, d.task.Snapshot, ev, d.facts(nil))
		if err != nil || dec.Applied == nil {
			t.Fatalf("setup: raw Fire of %s from %s = %s, %v", event, d.task.Snapshot.State, evDescribe(dec), err)
		}
		got, err := Advance(d.m, d.task, ev, dec, in, d.p)
		var fe *FireError
		if !errors.As(err, &fe) || fe.Code != AdvanceNotApplied || !strings.Contains(fe.Message, guard) {
			t.Errorf("Advance(%s %s %s) = %v, want %s naming %s", dec.Applied.TransitionID, event, d.task.Snapshot.State, err, AdvanceNotApplied, guard)
			return
		}
		if !reflect.DeepEqual(got, Task{}) {
			t.Errorf("Advance(%s): a non-zero Task came with the error", event)
		}
	}
	t.Run("T013 without an approval", func(t *testing.T) {
		d := newD14(t, "human")
		d.toConflict()
		d.adjudicate("test_invalid")
		forged(d, "hermes", "dispatch_tester", "tester", d.spec("tester"), "G_TEST_CHANGE_APPROVED")
	})
	t.Run("T013 after valid then invalid under adjudicator", func(t *testing.T) {
		d := newD14(t, "adjudicator")
		d.toConflict()
		d.adjudicate("test_valid")
		d.adjudicate("test_invalid")
		forged(d, "hermes", "dispatch_tester", "tester", d.spec("tester"), "G_TEST_CHANGE_APPROVED")
	})
	t.Run("T031 without a test_valid adjudication", func(t *testing.T) {
		d := newD14(t, "human")
		d.toConflict()
		d.adjudicate("test_invalid")
		forged(d, "hermes", "dispatch_implementer", "implementer", d.spec("implementer"), "G_CONFLICT_REJECTED")
	})
	t.Run("T015 without a strengthen decision", func(t *testing.T) {
		d := newD14(t, "human")
		d.toAlreadySatisfied()
		forged(d, "hermes", "dispatch_tester", "tester", d.spec("tester"), "G_STRENGTHEN_AUTHORIZED")
	})
	t.Run("T012 without a strengthen decision", func(t *testing.T) {
		d := newD14(t, "human")
		d.toRedVerified()
		forged(d, "hermes", "dispatch_tester", "tester", d.spec("tester"), "G_STRENGTHEN_AUTHORIZED")
	})
	t.Run("T012 after an implementer was dispatched", func(t *testing.T) {
		d := newD14(t, "human")
		d.toRedVerified()
		d.ok("hermes", "dispatch_implementer", "implementer", d.spec("implementer"), "T030", "IMPLEMENTING")
		d.ok("runner", "execution_blocked", "", StepInput{}, "T107", "RED_VERIFIED")
		d.ok("human", "strengthen_tests", "", StepInput{}, "T016", "RED_VERIFIED")
		forged(d, "hermes", "dispatch_tester", "tester", d.spec("tester"), "G_NO_IMPLEMENTER_DISPATCHED_IN_GENERATION")
	})
}

// 29-AC-11 / 29-AC-10: the replay of the recorded steps reproduces the conflict record and the strengthen
// decision, so a task read back from its history answers the guards the same way.
func TestReplay_ReproducesTaskStateGuards_29AC11_D14(t *testing.T) {
	d := newD14(t, "adjudicator")
	d.toConflict()
	d.adjudicate("test_invalid")
	d.ok("hermes", "dispatch_tester", "tester", d.spec("tester"), "T013", "TEST_AUTHORING")
	initial := Task{Snapshot: TaskSnapshot{Mode: "test-first", Attempts: Attempts{AutonomousMax: d.p.MaxAutonomousAttempts, TesterMax: d.p.MaxTesterAttempts,
		MeritByGeneration: map[string]int{}, ByClass: map[string]int{}}}}
	got, err := Replay(d.m, initial, d.task.History, d.p)
	if err != nil {
		t.Fatalf("Replay of a conflict that was adjudicated and approved: %v", err)
	}
	if !reflect.DeepEqual(got, d.task) {
		t.Errorf("Replay = %+v, want %+v", got, d.task)
	}
	// the same history under the human policy is refused: T013 would have lacked the approval
	h := d.p
	h.ResolutionApproval = "human"
	if _, err := Replay(d.m, initial, d.task.History, h); err == nil {
		t.Error("Replay of an adjudicator-approved history under the human policy succeeded, want it refused (T013 lacks the approval)")
	}
}
