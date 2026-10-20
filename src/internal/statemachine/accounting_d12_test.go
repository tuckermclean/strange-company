package statemachine

import (
	"errors"
	"reflect"
	"slices"
	"testing"
)

// Decision D12 (S3 review 2): Advance and Replay reject an event whose actor may not fire the transition (B1),
// Advance re-evaluates the guards of the transition that AccountingFacts can answer and rejects a step under
// which a lower-id candidate would pass (B2), and a failure code answered with known=false counts as "" (B3).

func d12Init(mode string) Task {
	return Task{Snapshot: TaskSnapshot{Mode: mode, Attempts: Attempts{AutonomousMax: acPolicy.MaxAutonomousAttempts, TesterMax: acPolicy.MaxTesterAttempts,
		MeritByGeneration: map[string]int{}, ByClass: map[string]int{}}}}
}

func d12WantNotApplied(t *testing.T, what string, got Task, err error) {
	t.Helper()
	var fe *FireError
	if !errors.As(err, &fe) || fe.Code != AdvanceNotApplied {
		t.Errorf("%s: error = %v, want a *FireError with code %s", what, err, AdvanceNotApplied)
		return
	}
	if !reflect.DeepEqual(got, Task{}) {
		t.Errorf("%s: a non-zero Task came with the error", what)
	}
}

// d12Held builds an honest task held in NEEDS_HUMAN (GREEN fails five times) or BLOCKED (emergency stop), and the
// human step that leaves it.
func d12Held(t *testing.T, state string) (*acRun, Event, Decision) {
	t.Helper()
	r := newRun(t, "test-first")
	r.toRedVerified()
	var ev Event
	switch state {
	case "NEEDS_HUMAN":
		for i := 1; i <= 4; i++ {
			r.implRound(i, "sig", "T034", "RED_VERIFIED")
		}
		r.ok("hermes", "dispatch_implementer", "implementer", r.spec("implementer", 5), nil, "T030", "IMPLEMENTING")
		r.ok("runner", "implementer_result_accepted", "", StepInput{}, nil, "T032", "GREEN_VERIFYING")
		r.ok("ci", "green_classified", "", StepInput{FailureSignature: "sig"}, map[string]string{"G_VERIFICATION_OUTCOME": "green_fail"}, "T035", "NEEDS_HUMAN")
		ev = Event{Name: "retry", Actor: "human"}
	case "BLOCKED":
		r.ok("hermes", "dispatch_implementer", "implementer", r.spec("implementer", 1), nil, "T030", "IMPLEMENTING")
		r.ok("runner", "execution_failed", "", StepInput{FailureCode: "EMERGENCY_STOP"}, map[string]string{"G_FAILURE_CLASS": "budget"}, "T105", "BLOCKED")
		ev = Event{Name: "unblock", Actor: "human"}
	}
	d, _, err := Apply(r.m, r.task, ev, StepInput{}, acPolicy, r.facts(nil))
	if err != nil || d.Applied == nil {
		t.Fatalf("setup: the human %s from %s = %s, %v", ev.Name, state, evDescribe(d), err)
	}
	return r, ev, d
}

// 29-AC-7 / 29-AC-4 P5 (D12 B1): only a declared actor of the transition may leave a held state.
func TestAdvanceReplay_RejectActorOutsideTransition_29AC7_D12B1(t *testing.T) {
	for _, state := range []string{"NEEDS_HUMAN", "BLOCKED"} {
		r, hev, d := d12Held(t, state)
		// with the human the same step is accepted
		if _, err := Advance(r.m, r.task, hev, d, StepInput{}, acPolicy); err != nil {
			t.Fatalf("%s: the human step must be accepted: %v", state, err)
		}
		h := append(slices.Clone(r.task.History), Step{Event: hev, Applied: cloneApplied(*d.Applied)})
		if _, err := Replay(r.m, d12Init("test-first"), h, acPolicy); err != nil {
			t.Fatalf("%s: the honest history with the human step must replay: %v", state, err)
		}
		for _, actor := range []string{"runner", "hermes", "ci", "nonsense"} {
			forged := Event{Name: hev.Name, Actor: actor}
			a := cloneApplied(*d.Applied)
			got, err := Advance(r.m, r.task, forged, Decision{Applied: &a}, StepInput{}, acPolicy)
			d12WantNotApplied(t, "Advance "+state+" "+hev.Name+" by "+actor, got, err)
			fh := append(slices.Clone(r.task.History), Step{Event: forged, Applied: cloneApplied(*d.Applied)})
			rgot, rerr := Replay(r.m, d12Init("test-first"), fh, acPolicy)
			if rerr == nil {
				t.Errorf("Replay accepted %s %s by %s and ended in %s", state, hev.Name, actor, rgot.Snapshot.State)
			}
		}
	}
}

// ---- B2

func d12Dispatch(t *testing.T, task Task) (*Machine, Event, StepInput) {
	t.Helper()
	return evMachine(t), Event{Name: "dispatch_implementer", Actor: "hermes", Role: "implementer"},
		StepInput{Dispatch: DispatchSpec{Role: "implementer", Harness: "h", Model: "m-new"}}
}

// d12Forgery checks that Advance and Replay (from the same initial task) refuse the step.
func d12Forgery(t *testing.T, name string, m *Machine, task Task, ev Event, in StepInput, a Applied) {
	t.Helper()
	ac := cloneApplied(a)
	got, err := Advance(m, task, ev, Decision{Applied: &ac}, in, acPolicy)
	d12WantNotApplied(t, "Advance "+name, got, err)
	rgot, rerr := Replay(m, task, []Step{{Event: ev, Input: in, Applied: cloneApplied(a)}}, acPolicy)
	if rerr == nil {
		t.Errorf("Replay accepted the forged step %s and ended in %s", name, rgot.Snapshot.State)
	}
}

// 29-AC-7 (D12 B2): T030 with no attempt left is refused (G_ATTEMPTS_REMAIN).
func TestAdvance_RejectsDispatchWithoutAttempts_29AC7_D12B2(t *testing.T) {
	m, ev, in := d12Dispatch(t, Task{})
	twin := acTask("RED_VERIFIED", Attempts{AutonomousUsed: 1, AutonomousMax: 5, TesterUsed: 1, TesterMax: 3})
	d, _, err := Apply(m, twin, ev, in, acPolicy, evPermissive(evContract(t)))
	if err != nil || d.Applied == nil {
		t.Fatalf("setup: Apply = %s, %v", evDescribe(d), err)
	}
	spent := acTask("RED_VERIFIED", Attempts{AutonomousUsed: 5, AutonomousMax: 5, TesterUsed: 1, TesterMax: 3})
	d12Forgery(t, "T030 with autonomous_used == autonomous_max", m, spent, ev, in, *d.Applied)
	over := acTask("RED_VERIFIED", Attempts{AutonomousUsed: 7, AutonomousMax: 5, TesterUsed: 1, TesterMax: 3})
	d12Forgery(t, "T030 with autonomous_used > autonomous_max", m, over, ev, in, *d.Applied)
	if _, err := Advance(m, twin, ev, Decision{Applied: d.Applied}, in, acPolicy); err != nil {
		t.Errorf("the honest dispatch must still advance: %v", err)
	}
}

// 29-AC-8 (D12 B2): T030 whose fingerprint equals an earlier merit-failed execution is refused.
func TestAdvance_RejectsIdenticalReplay_29AC8_D12B2(t *testing.T) {
	m, ev, in := d12Dispatch(t, Task{})
	twin := acTask("RED_VERIFIED", Attempts{AutonomousUsed: 1, AutonomousMax: 5, TesterUsed: 1, TesterMax: 3})
	d, _, err := Apply(m, twin, ev, in, acPolicy, evPermissive(evContract(t)))
	if err != nil || d.Applied == nil {
		t.Fatalf("setup: Apply = %s, %v", evDescribe(d), err)
	}
	replay := acTask("RED_VERIFIED", Attempts{AutonomousUsed: 1, AutonomousMax: 5, TesterUsed: 1, TesterMax: 3})
	replay.Executions = []Execution{{Role: "implementer", Generation: 1, Counted: true, Outcome: "merit", Signature: "s", Fingerprint: Fingerprint(in.Dispatch)}}
	d12Forgery(t, "T030 with the fingerprint of a merit-failed execution", m, replay, ev, in, *d.Applied)
}

// 29-AC-7 (D12 B2): a forged T100 repeated past infrastructure maxConsecutive is refused.
func TestAdvance_RejectsRequeuePastMaxConsecutive_29AC7_D12B2(t *testing.T) {
	m := evMachine(t)
	fev := Event{Name: "execution_failed", Actor: "runner"}
	fin := StepInput{FailureCode: "JOB_LOST"}
	base := evPermissive(evContract(t))
	base.v["G_FAILURE_CLASS"] = "infrastructure"
	mk := func(ci int) Task {
		task := acTask("IMPLEMENTING", Attempts{AutonomousUsed: 1, AutonomousMax: 5, TesterMax: 3, ConsecutiveInfrastructure: ci, ByClass: map[string]int{"infrastructure": ci}})
		task.Snapshot.ReturnState = "RED_VERIFIED"
		task.Executions = []Execution{{Role: "implementer", Generation: 1, Counted: true}}
		return task
	}
	d, _, err := Apply(m, mk(0), fev, fin, acPolicy, base)
	if err != nil || d.Applied == nil || d.Applied.TransitionID != "T100" {
		t.Fatalf("setup: Apply = %s, %v", evDescribe(d), err)
	}
	d12Forgery(t, "T100 with 2 consecutive infrastructure failures already counted", m, mk(2), fev, fin, *d.Applied)
	d12Forgery(t, "T100 with 3 consecutive infrastructure failures already counted", m, mk(3), fev, fin, *d.Applied)
	// repeated: the third consecutive T100 is refused
	task := mk(0)
	var history []Step
	for i := 1; i <= 5; i++ {
		a := cloneApplied(*d.Applied)
		n, err := Advance(m, task, fev, Decision{Applied: &a}, fin, acPolicy)
		if i <= 2 && err != nil {
			t.Fatalf("T100 number %d must be accepted: %v", i, err)
		}
		if i == 3 {
			d12WantNotApplied(t, "the third consecutive forged T100", n, err)
		}
		if err != nil {
			break
		}
		history = append(history, Step{Event: fev, Input: fin, Applied: cloneApplied(*d.Applied)})
		task = n
	}
	if _, err := Replay(m, mk(0), append(history, Step{Event: fev, Input: fin, Applied: cloneApplied(*d.Applied)}), acPolicy); err == nil {
		t.Error("Replay accepted a history with a third consecutive T100")
	}
}

// 29-AC-7 (D12 B2): a T106 step whose input code was changed to EMERGENCY_STOP, with the matching refund deltas,
// is refused: T105 (the lower id) would win under the same facts.
func TestAdvance_RejectsShadowedCandidate_29AC7_D12B2(t *testing.T) {
	m := evMachine(t)
	fev := Event{Name: "execution_failed", Actor: "runner"}
	task := acTask("IMPLEMENTING", Attempts{AutonomousUsed: 2, AutonomousMax: 5, TesterMax: 3})
	task.Snapshot.ReturnState = "RED_VERIFIED"
	task.Executions = []Execution{{Role: "implementer", Generation: 1, Counted: true}}
	base := evPermissive(evContract(t))
	base.v["G_FAILURE_CLASS"] = "budget"
	honest := StepInput{FailureCode: "BUDGET_TASK_EXHAUSTED"}
	d, _, err := Apply(m, task, fev, honest, acPolicy, base)
	if err != nil || d.Applied == nil || d.Applied.TransitionID != "T106" {
		t.Fatalf("setup: Apply = %s, %v", evDescribe(d), err)
	}
	if _, err := Advance(m, task, fev, Decision{Applied: d.Applied}, honest, acPolicy); err != nil {
		t.Fatalf("the honest T106 must advance: %v", err)
	}
	for _, code := range []string{"EMERGENCY_STOP", "BUDGET_SHARED_EXHAUSTED"} {
		forged := cloneApplied(*d.Applied)
		forged.Deltas = map[string]int{"by_class.budget": 1, "autonomous_used": -1}
		d12Forgery(t, "T106 with the code "+code, m, task, fev, StepInput{FailureCode: code}, forged)
	}
}

// ---- B3

// d12Unknown answers G_FAILURE_CODE with a value it does not know (known=false).
type d12Unknown struct {
	evFacts
	code string
}

func (u d12Unknown) Value(ev Event, g string) (string, bool) {
	if g == "G_FAILURE_CODE" {
		return u.code, false
	}
	return u.evFacts.Value(ev, g)
}

// 29-AC-7 (D12 B3): a failure code the base answers with known=false is "" everywhere: it does not refund, does
// not count as a timeout, and the recorded StepInput carries no code, so Replay reproduces the same task.
func TestApply_UnknownFailureCodeIsEmpty_29AC7_D12B3(t *testing.T) {
	for _, c := range []struct{ class, code, to string }{
		{"budget", "EMERGENCY_STOP", "NEEDS_HUMAN"},
		{"budget", "BUDGET_SHARED_EXHAUSTED", "NEEDS_HUMAN"},
		{"infrastructure", "TIMED_OUT", "IMPLEMENTING"},
	} {
		r := newRun(t, "test-first")
		r.toRedVerified()
		r.ok("hermes", "dispatch_implementer", "implementer", r.spec("implementer", 1), nil, "T030", "IMPLEMENTING")
		perm := r.facts(map[string]string{"G_FAILURE_CLASS": c.class})
		delete(perm.v, "G_FAILURE_CODE")
		before := r.a()
		d, n, err := Apply(r.m, r.task, Event{Name: "execution_failed", Actor: "runner"}, StepInput{}, acPolicy, d12Unknown{perm, c.code})
		if err != nil || d.Applied == nil {
			t.Fatalf("%s/%s: Apply = %s, %v", c.class, c.code, evDescribe(d), err)
		}
		name := c.class + "/" + c.code
		if c.class == "budget" {
			if d.Applied.TransitionID != "T106" || n.Snapshot.State != c.to {
				t.Errorf("%s: fired %s into %s, want T106 into %s", name, d.Applied.TransitionID, n.Snapshot.State, c.to)
			}
			if got := n.Snapshot.Attempts.AutonomousUsed; got != before.AutonomousUsed {
				t.Errorf("%s: autonomous_used = %d, want %d (an unknown code does not refund)", name, got, before.AutonomousUsed)
			}
			if d.Applied.Deltas["autonomous_used"] != 0 {
				t.Errorf("%s: Deltas = %v, want no autonomous_used refund", name, d.Applied.Deltas)
			}
			if i := len(n.Executions) - 1; n.Executions[i].Refunded {
				t.Errorf("%s: the execution is marked Refunded", name)
			}
			if n.BlockedReason != "" {
				t.Errorf("%s: BlockedReason = %q", name, n.BlockedReason)
			}
		} else {
			if got := n.Snapshot.Attempts.ConsecutiveTimeouts; got != 0 {
				t.Errorf("%s: consecutive_timeouts = %d, want 0 (an unknown TIMED_OUT is not a timeout)", name, got)
			}
			if d.Applied.Deltas["consecutive_timeouts"] != 0 {
				t.Errorf("%s: Deltas = %v, want no consecutive_timeouts", name, d.Applied.Deltas)
			}
		}
		if got := n.History[len(n.History)-1].Input.FailureCode; got != "" {
			t.Errorf("%s: recorded StepInput.FailureCode = %q, want \"\"", name, got)
		}
		replayed, err := Replay(r.m, d12Init("test-first"), n.History, acPolicy)
		if err != nil {
			t.Errorf("%s: Replay: %v", name, err)
		} else if !reflect.DeepEqual(replayed, n) {
			t.Errorf("%s: Replay gives\n%+v\nwant\n%+v", name, replayed, n)
		}
	}
}
