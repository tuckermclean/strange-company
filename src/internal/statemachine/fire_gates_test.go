package statemachine

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

// 29-AC-5: Fire(dispatch_implementer) from every state other than RED_VERIFIED, CHANGES_REQUESTED and
// TEST_SPEC_CONFLICT (with a rejected conflict) returns Rejected{GATE_NOT_SATISFIED}, for all four actor
// classes and both modes, with otherwise-permissive facts. Plan 29 section 7.2 maps only the "no active
// execution" and "attempts remain" failures to other codes, so a state without the transition is
// GATE_NOT_SATISFIED too. T030 and T031 exist in test-first only (verification-first dispatches a producer).
func TestFire_DispatchImplementerGate_29AC5(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	exempt := map[string]bool{"RED_VERIFIED": true, "CHANGES_REQUESTED": true, "TEST_SPEC_CONFLICT": true}
	perm := evPermissive(mdl)
	perm.b["G_CONFLICT_REJECTED"] = true
	checked := 0
	for _, st := range mdl.states {
		for _, mode := range st.modes {
			for _, actor := range evActors {
				if exempt[st.name] && mode == "test-first" && (actor == "hermes" || actor == "human") {
					continue // fires, see the positive cases below
				}
				d := evFire(t, m, evSnap(mdl, st.name, mode), Event{Name: "dispatch_implementer", Actor: actor, Role: "implementer"}, perm)
				if d.Rejected == nil || d.Rejected.Code != CodeGateNotSatisfied {
					t.Errorf("%s/%s/%s: want Rejected{GATE_NOT_SATISFIED}, got %s", st.name, mode, actor, evDescribe(d))
				}
				checked++
			}
		}
	}
	if checked < 24*4 {
		t.Errorf("only %d (state, mode, actor) combinations checked", checked)
	}
}

// 29-AC-5: the three states that may dispatch the implementer fire only with their gates: RED_VERIFIED and
// CHANGES_REQUESTED need G_RED_EVIDENCE_VALID, TEST_SPEC_CONFLICT needs the conflict rejected (and valid RED).
func TestFire_DispatchImplementerPositiveAndGates_29AC5(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	perm := evPermissive(mdl)
	for _, tc := range []struct{ state, wantID string }{
		{"RED_VERIFIED", "T030"}, {"CHANGES_REQUESTED", "T030"}, {"TEST_SPEC_CONFLICT", "T031"},
	} {
		for _, actor := range []string{"hermes", "human"} {
			snap := evSnap(mdl, tc.state, "test-first")
			ev := Event{Name: "dispatch_implementer", Actor: actor, Role: "implementer"}
			d := evFire(t, m, snap, ev, perm)
			if d.Applied == nil || d.Applied.TransitionID != tc.wantID || d.Applied.To != "IMPLEMENTING" {
				t.Errorf("%s/%s: want Applied %s -> IMPLEMENTING, got %s", tc.state, actor, tc.wantID, evDescribe(d))
			}
			// G_RED_EVIDENCE_VALID=false rejects (also from CHANGES_REQUESTED and TEST_SPEC_CONFLICT).
			noRed := perm.clone()
			noRed.b["G_RED_EVIDENCE_VALID"] = false
			d = evFire(t, m, snap, ev, noRed)
			if d.Rejected == nil || d.Rejected.Code != CodeGateNotSatisfied || !slices.Contains(d.Rejected.FailedGuards, "G_RED_EVIDENCE_VALID") {
				t.Errorf("%s/%s with G_RED_EVIDENCE_VALID=false: want Rejected{GATE_NOT_SATISFIED} naming it, got %s", tc.state, actor, evDescribe(d))
			}
			// A missing G_RED_EVIDENCE_VALID fact is false (fail closed).
			noFact := perm.clone()
			delete(noFact.b, "G_RED_EVIDENCE_VALID")
			if d = evFire(t, m, snap, ev, noFact); d.Rejected == nil {
				t.Errorf("%s/%s with G_RED_EVIDENCE_VALID unknown: want Rejected, got %s", tc.state, actor, evDescribe(d))
			}
		}
	}
	// TEST_SPEC_CONFLICT with a conflict that was not rejected: gated by G_CONFLICT_REJECTED.
	open := perm.clone()
	open.b["G_CONFLICT_REJECTED"] = false
	d := evFire(t, m, evSnap(mdl, "TEST_SPEC_CONFLICT", "test-first"), Event{Name: "dispatch_implementer", Actor: "hermes", Role: "implementer"}, open)
	if d.Rejected == nil || d.Rejected.Code != CodeGateNotSatisfied || !slices.Contains(d.Rejected.FailedGuards, "G_CONFLICT_REJECTED") {
		t.Errorf("TEST_SPEC_CONFLICT with an unrejected conflict: want Rejected{GATE_NOT_SATISFIED} naming G_CONFLICT_REJECTED, got %s", evDescribe(d))
	}
}

// 29-AC-5 (plan 29 section 7.2): when only G_NO_ACTIVE_EXECUTION fails the code is ALREADY_RUNNING; when only
// an attempts guard fails, ATTEMPTS_EXHAUSTED; any other combination is GATE_NOT_SATISFIED.
func TestFire_DispatchRejectionCodes_29AC5(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	perm := evPermissive(mdl)
	snap := evSnap(mdl, "RED_VERIFIED", "test-first")
	ev := Event{Name: "dispatch_implementer", Actor: "hermes", Role: "implementer"}
	for _, tc := range []struct {
		fail []string
		want string
	}{
		{[]string{"G_NO_ACTIVE_EXECUTION"}, CodeAlreadyRunning},
		{[]string{"G_ATTEMPTS_REMAIN"}, CodeAttemptsExhaust},
		{[]string{"G_NO_ACTIVE_EXECUTION", "G_ATTEMPTS_REMAIN"}, CodeGateNotSatisfied},
		{[]string{"G_NO_ACTIVE_EXECUTION", "G_DISPATCH_PREREQUISITES"}, CodeGateNotSatisfied},
		{[]string{"G_START_DESCENDS_FROM_RED"}, CodeGateNotSatisfied},
	} {
		f := perm.clone()
		for _, g := range tc.fail {
			f.b[g] = false
		}
		d := evFire(t, m, snap, ev, f)
		if d.Rejected == nil || d.Rejected.Code != tc.want {
			t.Errorf("failing %v: want Rejected{%s}, got %s", tc.fail, tc.want, evDescribe(d))
			continue
		}
		for _, g := range tc.fail {
			if !slices.Contains(d.Rejected.FailedGuards, g) {
				t.Errorf("failing %v: FailedGuards %v does not name %s", tc.fail, d.Rejected.FailedGuards, g)
			}
		}
	}
}

// 29-AC-6 (lesson L-4): for every transition whose from contains BLOCKED or NEEDS_HUMAN and whose to is not
// $same, firing it with actor classes runner, ci and hermes returns Rejected, from each held state it leaves,
// in every mode, with every fact true. The human actor, where the transition lists it, fires it (control).
func TestFire_HeldStatesRefuseNonHuman_29AC6(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	n, controls := 0, 0
	for _, tr := range mdl.transitions {
		if !tr.fromHeld || tr.toSame {
			continue
		}
		for _, mode := range tr.modes {
			for _, held := range []string{"BLOCKED", "NEEDS_HUMAN"} {
				if !slices.Contains(tr.from, held) {
					continue
				}
				for k := 0; k < evArgCount(tr); k++ {
					f := evSat(mdl, tr, k)
					snap := evSnap(mdl, held, mode)
					for _, actor := range []string{"runner", "ci", "hermes"} {
						d := evFire(t, m, snap, Event{Name: tr.event, Actor: actor}, f)
						if d.Rejected == nil {
							t.Errorf("%s from %s/%s as %s: want Rejected, got %s", tr.id, held, mode, actor, evDescribe(d))
						}
						n++
					}
					if slices.Contains(tr.actors, "human") {
						d := evFire(t, m, snap, Event{Name: tr.event, Actor: "human"}, f)
						if msg := evCheckApplied(d, tr, snap); msg != "" {
							t.Errorf("control %s from %s/%s as human: %s", tr.id, held, mode, msg)
						}
						controls++
					}
				}
			}
		}
	}
	if n == 0 || controls == 0 {
		t.Errorf("sweep covered %d refusals and %d controls", n, controls)
	}
}

// 29-AC-6 (property form of L-4): whatever transition's facts are all-true, a runner, ci or hermes event fired
// from BLOCKED or NEEDS_HUMAN either is rejected or stays in the held state ($same).
func TestFire_NonHumanNeverLeavesHeld_29AC6(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	applied := 0
	for _, tr := range mdl.transitions {
		for k := 0; k < evArgCount(tr); k++ {
			f := evSat(mdl, tr, k)
			for _, mode := range []string{"test-first", "verification-first"} {
				for _, held := range []string{"BLOCKED", "NEEDS_HUMAN"} {
					snap := evSnap(mdl, held, mode)
					for _, actor := range []string{"runner", "ci", "hermes"} {
						d := evFire(t, m, snap, Event{Name: tr.event, Actor: actor}, f)
						if d.Applied != nil {
							applied++
							if d.Applied.To != held {
								t.Errorf("%s %s/%s as %s left %s for %s (%s)", tr.event, held, mode, actor, held, d.Applied.To, d.Applied.TransitionID)
							}
						}
					}
				}
			}
		}
	}
	if applied == 0 {
		t.Error("no held-state self transition (T053, T056) fired as runner: the sweep proves nothing")
	}
}

// 29-AC-3 (plan 29 section 8.1, fail closed): a fact that is missing, or answered with known=false, makes its
// guard false; the failed guards of every candidate are reported in candidate id then listed order, without
// duplicates, and the implicit G_ACTOR_AUTHORIZED comes first.
func TestFire_MissingFactsFailClosed_29AC3(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	snap := evSnap(mdl, "SPECIFIED", "test-first")
	ev := Event{Name: "dispatch_tester", Actor: "hermes", Role: "tester"}
	want := []string{"G_ACTOR_AUTHORIZED", "G_DISPATCH_PREREQUISITES", "G_NO_ACTIVE_EXECUTION", "G_ATTEMPTS_REMAIN"}
	for name, facts := range map[string]Facts{"empty": evFacts{}, "known=false": evLiar{}} {
		d := evFire(t, m, snap, ev, facts)
		if d.Rejected == nil || d.Rejected.Code != CodeGateNotSatisfied || !slices.Equal(d.Rejected.FailedGuards, want) {
			t.Errorf("%s facts: want Rejected{GATE_NOT_SATISFIED %v}, got %s", name, want, evDescribe(d))
		}
	}
	// A transition without guards still needs the actor to be authorized (T016: strengthen_tests, human).
	d := evFire(t, m, evSnap(mdl, "ALREADY_SATISFIED", "test-first"), Event{Name: "strengthen_tests", Actor: "human"}, evFacts{})
	if d.Rejected == nil || !slices.Equal(d.Rejected.FailedGuards, []string{"G_ACTOR_AUTHORIZED"}) {
		t.Errorf("unauthenticated human: want Rejected naming only G_ACTOR_AUTHORIZED, got %s", evDescribe(d))
	}
	// Valued guards and negations: green_fail at GREEN_VERIFYING with the attempts fact missing fires neither
	// T034 (G_ATTEMPTS_REMAIN) nor T035 (G_ATTEMPTS_EXHAUSTED, the negation of a missing fact is false).
	f := evFacts{b: map[string]bool{"G_ACTOR_AUTHORIZED": true, "G_EVIDENCE_AUTHENTIC": true, "G_HEAD_UNCHANGED": true},
		v: map[string]string{"G_VERIFICATION_OUTCOME": "green_fail"}}
	d = evFire(t, m, evSnap(mdl, "GREEN_VERIFYING", "test-first"), Event{Name: "green_classified", Actor: "ci"}, f)
	wantF := []string{"G_VERIFICATION_OUTCOME", "G_ATTEMPTS_REMAIN", "G_ATTEMPTS_EXHAUSTED"}
	if d.Rejected == nil || !slices.Equal(d.Rejected.FailedGuards, wantF) {
		t.Errorf("green_fail without the attempts fact: want Rejected %v, got %s", wantF, evDescribe(d))
	}
	// With the fact known the negation pair is exclusive.
	f.b["G_ATTEMPTS_REMAIN"] = true
	if d = evFire(t, m, evSnap(mdl, "GREEN_VERIFYING", "test-first"), Event{Name: "green_classified", Actor: "ci"}, f); d.Applied == nil || d.Applied.TransitionID != "T034" || d.Applied.To != "RED_VERIFIED" {
		t.Errorf("attempts remain: want T034 -> RED_VERIFIED, got %s", evDescribe(d))
	}
	f.b["G_ATTEMPTS_REMAIN"] = false
	if d = evFire(t, m, evSnap(mdl, "GREEN_VERIFYING", "test-first"), Event{Name: "green_classified", Actor: "ci"}, f); d.Applied == nil || d.Applied.TransitionID != "T035" || d.Applied.To != "NEEDS_HUMAN" || d.Applied.ReasonCode != "retries_exhausted" {
		t.Errorf("attempts exhausted: want T035 -> NEEDS_HUMAN retries_exhausted, got %s", evDescribe(d))
	}
}

// 29-AC-3: negation guards are derived from their base guard: true exactly when the base fact is known false.
func TestGuards_NegationsDerivedFromBase_29AC3(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	// Each pair is exercised through a transition that lists the negation alone (see evDerived); firing it with
	// the base fact true must not fire it, with the base fact false must fire it, and missing must not fire it.
	for neg, base := range evDerived {
		var tr evT
		for _, c := range mdl.transitions {
			if slices.ContainsFunc(c.guards, func(x string) bool { return evIDOf(x) == neg }) {
				tr = c
				break
			}
		}
		if tr.id == "" {
			t.Fatalf("no transition lists %s", neg)
		}
		mode := tr.modes[0]
		snap := evSnap(mdl, evFromStates(mdl, tr, mode)[0], mode)
		ev := Event{Name: tr.event, Actor: tr.actors[0]}
		f := evSat(mdl, tr, 0) // base false: the negation passes
		d := evFire(t, m, snap, ev, f)
		if d.Applied == nil || d.Applied.TransitionID != tr.id {
			t.Errorf("%s: base %s=false: want %s to fire, got %s", neg, base, tr.id, evDescribe(d))
		}
		delete(f.b, base)
		d = evFire(t, m, snap, ev, f)
		if d.Applied != nil && d.Applied.TransitionID == tr.id {
			t.Errorf("%s: base %s unknown: %s fired (the negation of a missing fact must be false)", neg, base, tr.id)
		}
	}
}

// 29-AC-3: guard arguments reach the guard. A valued guard passes when the fact equals one of its
// comma-separated arguments exactly; a different, empty, prefixed or comma-joined value fails it.
func TestFire_GuardArgumentsMatchExactly_29AC3(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	snap := evSnap(mdl, "RED_VERIFYING", "test-first")
	ev := Event{Name: "red_classified", Actor: "ci"}
	for value, want := range map[string]string{
		"red_valid": "T020", "red_invalid": "T021", "already_satisfied": "T022",
		"baseline_failing": "T023", "partially_satisfied": "T023", "inconclusive": "T023",
		"": "", "red_valid_extra": "", "red_": "", "RED_VALID": "", "green_pass": "",
		"red_valid,red_invalid": "", "baseline_failing,inconclusive": "", " inconclusive": "",
	} {
		f := evPermissive(mdl)
		f.v["G_VERIFICATION_OUTCOME"] = value
		d := evFire(t, m, snap, ev, f)
		switch {
		case want != "" && (d.Applied == nil || d.Applied.TransitionID != want):
			t.Errorf("outcome %q: want %s, got %s", value, want, evDescribe(d))
		case want == "" && (d.Rejected == nil || !slices.Contains(d.Rejected.FailedGuards, "G_VERIFICATION_OUTCOME")):
			t.Errorf("outcome %q: want Rejected naming G_VERIFICATION_OUTCOME, got %s", value, evDescribe(d))
		}
	}
	// Execution failure code list (T105: EMERGENCY_STOP, BUDGET_SHARED_EXHAUSTED) against budget class.
	snap = evSnap(mdl, "IMPLEMENTING", "test-first")
	for code, want := range map[string]string{"EMERGENCY_STOP": "T105", "BUDGET_SHARED_EXHAUSTED": "T105", "BUDGET_EXCEEDED_TASK": "T106", "": "T106"} {
		f := evPermissive(mdl)
		f.v["G_FAILURE_CLASS"] = "budget"
		f.v["G_FAILURE_CODE"] = code
		d := evFire(t, m, snap, Event{Name: "execution_failed", Actor: "runner"}, f)
		if d.Applied == nil || d.Applied.TransitionID != want {
			t.Errorf("budget failure code %q: want %s, got %s", code, want, evDescribe(d))
		}
	}
}

// 29-AC-3 (plan 29 section 8.1 step 4): $return resolves to the snapshot's ReturnState (it must be one of the
// state's returnTargets), $resume to ResumeState (a non-held, non-terminal, non-execution state of the mode).
func TestFire_PseudoTargets_29AC3(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	f := evPermissive(mdl)
	f.v["G_FAILURE_CLASS"] = "merit"
	f.b["G_ATTEMPTS_REMAIN"] = true
	ret := Event{Name: "execution_failed", Actor: "runner"}
	for _, rt := range []string{"RED_VERIFIED", "CHANGES_REQUESTED", "TEST_SPEC_CONFLICT"} {
		snap := evSnap(mdl, "IMPLEMENTING", "test-first")
		snap.ReturnState = rt
		d := evFire(t, m, snap, ret, f)
		if d.Applied == nil || d.Applied.TransitionID != "T102" || d.Applied.To != rt {
			t.Errorf("IMPLEMENTING with ReturnState %s: want T102 -> %s, got %s", rt, rt, evDescribe(d))
		}
	}
	for _, bad := range []string{"", "DONE", "SPECIFIED", "NO_SUCH_STATE", "BLOCKED"} {
		snap := evSnap(mdl, "IMPLEMENTING", "test-first")
		snap.ReturnState = bad
		d, err := Fire(m, snap, ret, f)
		expectFireError(t, fmt.Sprintf("$return to %q", bad), d, err, FireReturnInvalid)
	}
	res := Event{Name: "unblock", Actor: "human"}
	rf := evPermissive(mdl)
	for _, rs := range []string{"SPECIFIED", "RED_VERIFIED", "TEST_NEEDS_WORK", "GREEN_VERIFIED", "CHANGES_REQUESTED", "AWAITING_MERGE"} {
		snap := evSnap(mdl, "BLOCKED", "test-first")
		snap.ResumeState = rs
		d := evFire(t, m, snap, res, rf)
		if d.Applied == nil || d.Applied.TransitionID != "T065" || d.Applied.To != rs {
			t.Errorf("unblock with ResumeState %s: want T065 -> %s, got %s", rs, rs, evDescribe(d))
		}
	}
	for _, bad := range []string{"", "BLOCKED", "NEEDS_HUMAN", "DONE", "CANCELLED", "IMPLEMENTING", "NO_SUCH_STATE"} {
		snap := evSnap(mdl, "BLOCKED", "test-first")
		snap.ResumeState = bad
		d, err := Fire(m, snap, res, rf)
		expectFireError(t, fmt.Sprintf("$resume to %q", bad), d, err, FireResumeInvalid)
	}
	// A resume state that does not exist in the task's mode is invalid (RED_VERIFIED is test-first only).
	snap := evSnap(mdl, "BLOCKED", "verification-first")
	snap.ResumeState = "RED_VERIFIED"
	d, err := Fire(m, snap, res, rf)
	expectFireError(t, "$resume to a state outside the mode", d, err, FireResumeInvalid)
}

func expectFireError(t *testing.T, what string, d Decision, err error, code string) {
	t.Helper()
	var fe *FireError
	switch {
	case err == nil:
		t.Errorf("%s: want error %s, got %s", what, code, evDescribe(d))
	case !errors.As(err, &fe) || fe.Code != code:
		t.Errorf("%s: error %v, want *FireError %s", what, err, code)
	case d.Applied != nil || d.Rejected != nil:
		t.Errorf("%s: an error must come with the zero Decision, got %s", what, evDescribe(d))
	}
}

// 29-AC-3 (fail closed, elevated path): malformed input is an error and a zero Decision, never a default pass;
// a well-formed but unmatched event is Rejected{GATE_NOT_SATISFIED}.
func TestFire_MalformedInputFailsClosed_29AC3(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	perm := evPermissive(mdl)
	good := evSnap(mdl, "RED_VERIFIED", "test-first")
	ev := Event{Name: "dispatch_implementer", Actor: "hermes", Role: "implementer"}

	d, err := Fire(nil, good, ev, perm)
	expectFireError(t, "nil machine", d, err, FireMachineNil)
	d, err = Fire(m, good, ev, nil)
	expectFireError(t, "nil facts", d, err, FireFactsNil)

	snap := good
	snap.Mode = "turbo-first"
	d, err = Fire(m, snap, ev, perm)
	expectFireError(t, "unknown mode", d, err, FireUnknownMode)
	snap.Mode = ""
	d, err = Fire(m, snap, ev, perm)
	expectFireError(t, "empty mode", d, err, FireUnknownMode)

	snap = good
	snap.State = "NO_SUCH_STATE"
	d, err = Fire(m, snap, ev, perm)
	expectFireError(t, "unknown state", d, err, FireUnknownState)
	snap = evSnap(mdl, "RED_VERIFYING", "verification-first")
	d, err = Fire(m, snap, Event{Name: "red_classified", Actor: "ci"}, perm)
	expectFireError(t, "state outside the task's mode", d, err, FireUnknownState)
	snap.State = "$none"
	d, err = Fire(m, snap, Event{Name: "task_created", Actor: "human"}, perm)
	expectFireError(t, "the literal $none as a state", d, err, FireUnknownState)

	d, err = Fire(m, good, Event{Name: "no_such_event", Actor: "hermes"}, perm)
	expectFireError(t, "unknown event", d, err, FireUnknownEvent)
	d, err = Fire(m, good, Event{Name: "", Actor: "hermes"}, perm)
	expectFireError(t, "empty event", d, err, FireUnknownEvent)
	d, err = Fire(m, good, Event{Name: "dispatch_implementer", Actor: "wizard"}, perm)
	expectFireError(t, "unknown actor class", d, err, FireUnknownActor)
	d, err = Fire(m, good, Event{Name: "dispatch_implementer", Actor: "human:alice"}, perm)
	expectFireError(t, "actor identity instead of class", d, err, FireUnknownActor)

	// Known inputs that match nothing are rejections, not errors.
	creation := TaskSnapshot{Mode: "test-first"} // State "" = the task does not exist yet
	for _, tc := range []struct {
		what string
		snap TaskSnapshot
		ev   Event
	}{
		{"creation snapshot with a non-creation event", creation, Event{Name: "dispatch_tester", Actor: "hermes"}},
		{"creation event on an existing task", good, Event{Name: "task_created", Actor: "hermes"}},
		{"event from a terminal state", evSnap(mdl, "DONE", "test-first"), Event{Name: "cancel_requested", Actor: "human"}},
		{"actor not listed by any candidate", good, Event{Name: "dispatch_implementer", Actor: "ci"}},
	} {
		d := evFire(t, m, tc.snap, tc.ev, perm)
		if d.Rejected == nil || d.Rejected.Code != CodeGateNotSatisfied {
			t.Errorf("%s: want Rejected{GATE_NOT_SATISFIED}, got %s", tc.what, evDescribe(d))
		}
	}
	// The catalogue's implicit guard names the actor mismatch.
	d = evFire(t, m, good, Event{Name: "dispatch_implementer", Actor: "ci"}, perm)
	if d.Rejected == nil || !slices.Contains(d.Rejected.FailedGuards, "G_ACTOR_AUTHORIZED") {
		t.Errorf("a class the candidates do not list: want Rejected naming G_ACTOR_AUTHORIZED, got %s", evDescribe(d))
	}
	// Creation: both modes, both listed actors, From is "$none".
	for _, mode := range []string{"test-first", "verification-first"} {
		for _, actor := range []string{"hermes", "human"} {
			d := evFire(t, m, TaskSnapshot{Mode: mode}, Event{Name: "task_created", Actor: actor}, perm)
			if d.Applied == nil || d.Applied.TransitionID != "T001" || d.Applied.From != "$none" || d.Applied.To != "SPECIFIED" {
				t.Errorf("task_created %s/%s: want T001 $none -> SPECIFIED, got %s", mode, actor, evDescribe(d))
			}
		}
	}
}

// Plan 29 section 7.1 NextAllowedRoles (no acceptance criterion of its own; supports 29-AC-5): the roles, in
// ascending order, for which a dispatch event by Hermes would currently fire. Roles map to events as in plan 29
// section 7.2 (tester -> dispatch_tester, implementer -> dispatch_implementer in test-first, reviewer in
// GREEN_VERIFIED -> dispatch_reviewer, researcher and planner and other reviewers -> dispatch_auxiliary) and a
// state's dispatchableRoles bound the answer.
func TestNextAllowedRoles_Plan29S7_1(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	perm := evPermissive(mdl)
	roles := func(r ...Role) []Role { return r }
	for _, tc := range []struct {
		state string
		facts evFacts
		want  []Role
	}{
		{"SPECIFIED", perm, roles("planner", "researcher", "tester")},
		{"RED_VERIFIED", perm, roles("implementer", "researcher", "reviewer", "tester")},
		{"CHANGES_REQUESTED", perm, roles("implementer", "researcher")},
		{"TEST_SPEC_CONFLICT", perm, roles("implementer", "researcher", "reviewer", "tester")},
		{"GREEN_VERIFIED", perm, roles("reviewer")},
		{"IMPLEMENTING", perm, nil},
		{"AWAITING_MERGE", perm, nil},
		{"DONE", perm, nil},
		{"RED_VERIFIED", evFacts{}, nil}, // fail closed: nothing known, nothing allowed
	} {
		got := NextAllowedRoles(m, evSnap(mdl, tc.state, "test-first"), tc.facts)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: NextAllowedRoles = %v, want %v", tc.state, got, tc.want)
		}
	}
	// Facts that gate one role remove only that role.
	noRed := perm.clone()
	noRed.b["G_RED_EVIDENCE_VALID"] = false
	if got, want := NextAllowedRoles(m, evSnap(mdl, "RED_VERIFIED", "test-first"), noRed), roles("researcher", "reviewer", "tester"); !slices.Equal(got, want) {
		t.Errorf("RED_VERIFIED without valid RED: NextAllowedRoles = %v, want %v", got, want)
	}
	busy := perm.clone()
	busy.b["G_NO_ACTIVE_EXECUTION"] = false
	if got := NextAllowedRoles(m, evSnap(mdl, "RED_VERIFIED", "test-first"), busy); len(got) != 0 {
		t.Errorf("an execution is active: NextAllowedRoles = %v, want none", got)
	}
	noAux := perm.clone()
	noAux.rb = map[string]bool{"researcher/G_ROLE_AUXILIARY_HERE": false}
	if got, want := NextAllowedRoles(m, evSnap(mdl, "RED_VERIFIED", "test-first"), noAux), roles("implementer", "reviewer", "tester"); !slices.Equal(got, want) {
		t.Errorf("researcher not auxiliary here: NextAllowedRoles = %v, want %v", got, want)
	}
	if got := NextAllowedRoles(nil, evSnap(mdl, "RED_VERIFIED", "test-first"), perm); len(got) != 0 {
		t.Errorf("nil machine: NextAllowedRoles = %v, want none", got)
	}
	if got := NextAllowedRoles(m, evSnap(mdl, "RED_VERIFIED", "test-first"), nil); len(got) != 0 {
		t.Errorf("nil facts: NextAllowedRoles = %v, want none", got)
	}
	unknown := evSnap(mdl, "NO_SUCH_STATE", "test-first")
	if got := NextAllowedRoles(m, unknown, perm); len(got) != 0 {
		t.Errorf("unknown state: NextAllowedRoles = %v, want none", got)
	}
}
