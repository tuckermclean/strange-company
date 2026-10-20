package statemachine

import (
	"slices"
	"testing"
)

// D13 (S3 review 3 non-blockers): Advance repeats Fire's dispatch-role check and treats G_FAILURE_CODE as known
// and equal to StepInput.FailureCode for execution_failed. No clock and no global random source (D7, D8).

// d13Applied builds the decision of transition id exactly as Fire would for the task, event and failure code.
func d13Applied(t *testing.T, m *Machine, id string, task Task, ev Event, code, role string) Applied {
	t.Helper()
	tr := m.transitionByID(id)
	if tr == nil {
		t.Fatalf("no transition %s", id)
	}
	from := task.Snapshot.State
	to, err := m.target(tr, m.findState(from), task.Snapshot)
	if err != nil {
		t.Fatalf("target of %s: %v", id, err)
	}
	return Applied{TransitionID: id, From: from, To: to, Effects: slices.Clone(tr.effects), FailureClass: effectArg(tr.effects, "E_COUNT_FAILURE"),
		ReasonCode: effectArg(tr.effects, "E_NOTIFY_HUMAN"),
		Deltas:     m.deltas(tr, m.findState(from), task.Snapshot, ev, codeFacts{code}, role, true)}
}

// 29-AC-7 (D13a, D9d in Advance): a dispatch whose non-empty Event.Role does not map to the event, or is not one
// of the state's dispatchableRoles, is refused by Advance and Replay with SM_ADVANCE_NOT_APPLIED; the honest role
// still advances.
func TestAdvance_RejectsForgedDispatchRole_29AC7_D13a(t *testing.T) {
	m := evMachine(t)
	for _, c := range []struct {
		name, state, event, id, role string
		ok                           bool
	}{
		{"implementer dispatch relabelled reviewer", "RED_VERIFIED", "dispatch_implementer", "T030", "reviewer", false},
		{"implementer dispatch relabelled tester", "RED_VERIFIED", "dispatch_implementer", "T030", "tester", false},
		{"implementer dispatch relabelled researcher", "RED_VERIFIED", "dispatch_implementer", "T030", "researcher", false},
		{"implementer dispatch with an unknown role", "RED_VERIFIED", "dispatch_implementer", "T030", "bogus", false},
		{"honest implementer dispatch", "RED_VERIFIED", "dispatch_implementer", "T030", "implementer", true},
		{"reviewer dispatch relabelled implementer", "GREEN_VERIFIED", "dispatch_reviewer", "T040", "implementer", false},
		{"honest reviewer dispatch", "GREEN_VERIFIED", "dispatch_reviewer", "T040", "reviewer", true},
		{"tester dispatch relabelled implementer", "TEST_NEEDS_WORK", "dispatch_tester", "T011", "implementer", false},
		{"honest tester dispatch", "TEST_NEEDS_WORK", "dispatch_tester", "T011", "tester", true},
		{"auxiliary dispatch of a role the state does not allow", "RED_VERIFIED", "dispatch_auxiliary", "T090", "planner", false},
		{"auxiliary dispatch of an allowed role", "RED_VERIFIED", "dispatch_auxiliary", "T090", "researcher", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			task := acTask(c.state, Attempts{AutonomousUsed: 1, AutonomousMax: 5, TesterUsed: 1, TesterMax: 3})
			task.Snapshot.ReturnState = ""
			ev := Event{Name: c.event, Actor: "hermes", Role: c.role}
			in := StepInput{Dispatch: DispatchSpec{Role: c.role, Harness: "h", Model: "m-new", Profile: "std", StartCommit: "c1", ACVersion: 1}}
			a := d13Applied(t, m, c.id, task, ev, "", "")
			if c.ok {
				ac := cloneApplied(a)
				got, err := Advance(m, task, ev, Decision{Applied: &ac}, in, acPolicy)
				if err != nil || got.Snapshot.State != a.To {
					t.Errorf("the honest dispatch must advance: state %s, err %v", got.Snapshot.State, err)
				}
				return
			}
			d12Forgery(t, c.name, m, task, ev, in, a)
		})
	}
}

// 29-AC-7 (D13b): in Advance's guard re-check an execution_failed event carries a known G_FAILURE_CODE equal to
// StepInput.FailureCode. A forged T105 (budget class, code guard EMERGENCY_STOP or BUDGET_SHARED_EXHAUSTED) with
// an empty code is refused, so is one with any other code; the honest codes advance; T100 (no code guard) with an
// empty code still advances.
func TestAdvance_RejectsForgedFailureCode_29AC7_D13b(t *testing.T) {
	m := evMachine(t)
	fev := Event{Name: "execution_failed", Actor: "runner"}
	mk := func() Task {
		task := acTask("IMPLEMENTING", Attempts{AutonomousUsed: 2, AutonomousMax: 5, TesterUsed: 1, TesterMax: 3})
		task.Snapshot.ReturnState = "RED_VERIFIED"
		task.Executions = []Execution{{Role: "implementer", Generation: 1, Counted: true}}
		return task
	}
	for _, code := range []string{"", "JOB_LOST", "TIMED_OUT", "emergency_stop"} {
		task := mk()
		a := d13Applied(t, m, "T105", task, fev, code, "implementer")
		d12Forgery(t, "T105 with failure code "+code, m, task, fev, StepInput{FailureCode: code}, a)
	}
	for _, code := range []string{"EMERGENCY_STOP", "BUDGET_SHARED_EXHAUSTED"} {
		task := mk()
		a := d13Applied(t, m, "T105", task, fev, code, "implementer")
		got, err := Advance(m, task, fev, Decision{Applied: &a}, StepInput{FailureCode: code}, acPolicy)
		if err != nil || got.Snapshot.State != "BLOCKED" {
			t.Errorf("honest T105 with %s: state %s, err %v; want BLOCKED", code, got.Snapshot.State, err)
		}
	}
	task := mk()
	a := d13Applied(t, m, "T100", task, fev, "", "implementer")
	if got, err := Advance(m, task, fev, Decision{Applied: &a}, StepInput{}, acPolicy); err != nil || got.Snapshot.State != "IMPLEMENTING" {
		t.Errorf("T100 with an empty failure code must still advance: state %s, err %v", got.Snapshot.State, err)
	}
}
