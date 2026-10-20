package statemachine

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// 29-AC-4 (D11 B3): steered random walks. The unsteered walks of randomwalk_test.go rarely reach the dispatch /
// fail loop with the counters at their maxima, so property 4 (autonomous_used <= autonomous_max at every
// dispatch) could hold vacuously. These walks start at RED_VERIFIED (test-first) or JUDGE_APPROVED
// (verification-first), bias towards dispatches, execution failures, failing verifications and human resumes,
// check the same five properties as rwWalk and assert that the run was not vacuous: an implementer (test-first)
// and a producer (verification-first) dispatch was refused by G_ATTEMPTS_REMAIN with autonomous_used ==
// autonomous_max, and an identical replay was refused.

var steeredSeedFlag = flag.String("seed-steered", "1..2000", "seeds of TestRandomWalksSteered_29AC4: N or A..B")

type rwSteerStats struct {
	attemptRefusals, replayRefusals, dispatches, applied int
}

// rwSteerStart brings a fresh task to RED_VERIFIED (test-first) or JUDGE_APPROVED (verification-first).
func rwSteerStart(m *Machine, mdl *evModel, mode string, pol RetryPolicy) (Task, error) {
	task := rwInitial(mode, pol)
	step := func(actor, event, role string, in StepInput, vals map[string]string) error {
		f := evPermissive(mdl)
		for k, v := range vals {
			f.v[k] = v
		}
		d, nt, err := Apply(m, task, Event{Name: event, Actor: actor, Role: role}, in, pol, f)
		if err != nil || d.Applied == nil {
			return fmt.Errorf("start: %s: %s, %v", event, evDescribe(d), err)
		}
		task = nt
		return nil
	}
	if err := step("hermes", "task_created", "", StepInput{}, nil); err != nil {
		return task, err
	}
	if mode == "verification-first" {
		return task, step("runner", "judge_bound", "", StepInput{}, map[string]string{"G_JUDGE_SOURCE": "acceptance-criteria", "G_APPROVAL": "auto"})
	}
	spec := StepInput{Dispatch: DispatchSpec{Role: "tester", Harness: "h", Model: "t1", Profile: "p", StartCommit: "c0", ACVersion: 1}}
	for _, s := range []struct {
		actor, event, role string
		in                 StepInput
		vals               map[string]string
	}{
		{"hermes", "dispatch_tester", "tester", spec, nil},
		{"runner", "tester_result_accepted", "", StepInput{}, nil},
		{"ci", "red_classified", "", StepInput{}, map[string]string{"G_VERIFICATION_OUTCOME": "red_valid"}},
	} {
		if err := step(s.actor, s.event, s.role, s.in, s.vals); err != nil {
			return task, err
		}
	}
	return task, nil
}

var rwSteerBias = map[string][]string{
	"RED_VERIFIED": {"dispatch_implementer"}, "CHANGES_REQUESTED": {"dispatch_implementer", "dispatch_producer"},
	"TEST_SPEC_CONFLICT": {"dispatch_implementer", "dispatch_producer"}, "JUDGE_APPROVED": {"dispatch_producer"},
	"IMPLEMENTING":    {"execution_failed", "execution_failed", "implementer_result_accepted"},
	"PRODUCING":       {"execution_failed", "execution_failed", "producer_result_accepted"},
	"GREEN_VERIFYING": {"green_classified"}, "JUDGING": {"judging_classified"},
	"NEEDS_HUMAN": {"unblock", "retry", "respec"}, "BLOCKED": {"unblock", "block"},
}

func rwSteerWalk(m *Machine, mdl *evModel, pool map[string][]string, mode string, seed uint64, st *rwSteerStats) string {
	stream := uint64(11)
	if mode == "verification-first" {
		stream = 12
	}
	rng := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15*stream))
	pol := rwPolicy(seed)
	task, err := rwSteerStart(m, mdl, mode, pol)
	if err != nil {
		return fmt.Sprintf("seed %d mode %s: %v", seed, mode, err)
	}
	start := task
	const steps = 40
	for i := 0; i < steps; i++ {
		from := task.Snapshot.State
		var cands []evT
		for _, tr := range mdl.transitions {
			if slices.Contains(tr.from, from) && slices.Contains(tr.modes, mode) {
				cands = append(cands, tr)
			}
		}
		rf := &rwFacts{rng: rng, pool: pool, wantTrue: map[string]bool{"G_ACTOR_AUTHORIZED": true}, wantFalse: map[string]bool{}, wantV: map[string]string{},
			b: map[string][2]bool{}, v: map[string]rwV{}}
		var biased []evT
		for _, tr := range cands {
			if slices.Contains(rwSteerBias[from], tr.event) {
				biased = append(biased, tr)
			}
		}
		var name, actor string
		switch {
		case len(biased) > 0 && rng.Float64() < 0.85:
			// pick the event by its weight in the bias list, then a transition for it
			ev := rwSteerBias[from][rng.IntN(len(rwSteerBias[from]))]
			var of []evT
			for _, tr := range biased {
				if tr.event == ev {
					of = append(of, tr)
				}
			}
			if len(of) == 0 {
				of = biased
			}
			tr := of[rng.IntN(len(of))]
			name, actor = tr.event, tr.actors[rng.IntN(len(tr.actors))]
			if from == "NEEDS_HUMAN" || from == "BLOCKED" {
				actor = "human"
			}
			for _, g := range tr.guards {
				id, args := evIDOf(g), evArgsOf(g)
				switch base, derived := evDerived[id]; {
				case evValued[id]:
					rf.wantV[id] = args[rng.IntN(len(args))]
				case derived:
					rf.wantFalse[base] = true
				default:
					rf.wantTrue[id] = true
				}
			}
			switch name {
			case "green_classified":
				rf.v["G_VERIFICATION_OUTCOME"] = rwV{"green_fail", true}
			case "judging_classified":
				rf.v["G_VERIFICATION_OUTCOME"] = rwV{"judging_fail", true}
			}
			for _, g := range []string{"G_DISPATCH_PREREQUISITES", "G_NO_ACTIVE_EXECUTION", "G_RED_EVIDENCE_VALID", "G_ACTOR_AUTHORIZED"} {
				rf.wantTrue[g] = true
			}
		case len(cands) > 0 && rng.Float64() < 0.9:
			tr := cands[rng.IntN(len(cands))]
			name, actor = tr.event, tr.actors[rng.IntN(len(tr.actors))]
			for _, g := range tr.guards {
				if id := evIDOf(g); evValued[id] {
					rf.wantV[id] = evArgsOf(g)[rng.IntN(len(evArgsOf(g)))]
				} else if _, derived := evDerived[id]; !derived {
					rf.wantTrue[id] = true
				}
			}
		default:
			name, actor = mdl.events[rng.IntN(len(mdl.events))], evActors[rng.IntN(len(evActors))]
		}
		ev := Event{Name: name, Actor: actor}
		in := StepInput{FailureSignature: rwSigs[rng.IntN(len(rwSigs))], FailureCode: rwCodes[rng.IntN(len(rwCodes))]}
		if name == "execution_failed" && rng.Float64() < 0.6 {
			rf.v["G_FAILURE_CLASS"] = rwV{[]string{"merit", "merit", "merit", "infrastructure", "policy"}[rng.IntN(5)], true}
		}
		if evDispatchEvents[name] {
			role := rwDispatches[name]
			if name == "dispatch_producer" {
				role = []string{"implementer", "researcher"}[rng.IntN(2)]
			}
			ev.Role = role
			in.Dispatch = DispatchSpec{Role: role, Harness: "h", Model: rwModels[rng.IntN(2)], Profile: "p", StartCommit: "c0", ACVersion: 1, OrchestratorNotes: "n"}
		}
		where := fmt.Sprintf("seed %d mode %s step %d (%s %s by %s)", seed, mode, i, from, name, actor)
		before := task.Snapshot.Attempts
		d, nt, err := Apply(m, task, ev, in, pol, rf)
		if err != nil {
			return where + ": Apply error: " + err.Error()
		}
		if (d.Applied == nil) == (d.Rejected == nil) {
			return where + ": decision must hold exactly one of Applied and Rejected, got " + evDescribe(d)
		}
		isDispatch := name == "dispatch_implementer" || name == "dispatch_producer"
		if d.Rejected != nil {
			if !reflect.DeepEqual(nt, task) {
				return where + ": a Rejected event changed the task"
			}
			if isDispatch && slices.Contains(d.Rejected.FailedGuards, "G_ATTEMPTS_REMAIN") && before.AutonomousUsed == before.AutonomousMax {
				st.attemptRefusals++
			}
			if isDispatch && slices.Contains(d.Rejected.FailedGuards, "G_NOT_IDENTICAL_REPLAY") {
				st.replayRefusals++
			}
			continue
		}
		st.applied++
		if isDispatch {
			st.dispatches++
			if before.AutonomousUsed >= before.AutonomousMax {
				return where + fmt.Sprintf(": dispatched with autonomous_used %d >= autonomous_max %d", before.AutonomousUsed, before.AutonomousMax)
			}
		}
		to := nt.Snapshot.State
		if s, ok := mdl.byName[to]; !ok || !s.inMode(mode) || to != d.Applied.To || d.Applied.From != from { // P1
			return where + fmt.Sprintf(": left the declared state set: %s -> %q (Applied %+v)", from, to, *d.Applied)
		}
		if (from == "BLOCKED" || from == "NEEDS_HUMAN") && actor != "human" && to != from { // P2
			return where + fmt.Sprintf(": the non-human actor %s left %s for %s", actor, from, to)
		}
		if to == "IMPLEMENTING" && from != "IMPLEMENTING" { // P3
			if v, ok := rf.b["G_RED_EVIDENCE_VALID"]; !ok || !v[0] || !v[1] {
				return where + ": entered IMPLEMENTING without G_RED_EVIDENCE_VALID true"
			}
		}
		a := nt.Snapshot.Attempts // P4
		if a.AutonomousUsed < 0 || a.AutonomousUsed > a.AutonomousMax || a.TesterUsed < 0 || a.TesterUsed > a.TesterMax {
			return where + fmt.Sprintf(": counters out of bounds: autonomous %d/%d tester %d/%d", a.AutonomousUsed, a.AutonomousMax, a.TesterUsed, a.TesterMax)
		}
		task = nt
	}
	// P5: the walk starts from the prepared task, so replay the steps after the prefix.
	got, err := Replay(m, start, task.History[len(start.History):], pol)
	if err != nil {
		return fmt.Sprintf("seed %d mode %s: Replay error: %v", seed, mode, err)
	}
	if !reflect.DeepEqual(got, task) {
		return fmt.Sprintf("seed %d mode %s: replaying the history gives state %q, want %q (or different counters)", seed, mode, got.Snapshot.State, task.Snapshot.State)
	}
	return ""
}

// 29-AC-4 (D11 B3): the steered walks hold the five properties and are not vacuous.
func TestRandomWalksSteered_29AC4(t *testing.T) {
	lo, hi, err := parseSeeds(*steeredSeedFlag)
	if err != nil {
		t.Fatal(err)
	}
	m, mdl := evMachine(t), evContract(t)
	pool := rwPools(mdl)
	stats := map[string]*rwSteerStats{"test-first": {}, "verification-first": {}}
	var violations []string
	for _, mode := range modes {
		for seed := lo; seed <= hi; seed++ {
			if v := rwSteerWalk(m, mdl, pool, mode, seed, stats[mode]); v != "" {
				violations = append(violations, v)
				if len(violations) >= 5 {
					t.Fatalf("%d violations (stopped at 5); reproduce with `go test -run TestRandomWalksSteered -seed-steered N`:\n%s", len(violations), strings.Join(violations, "\n"))
				}
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("violations; reproduce with `go test -run TestRandomWalksSteered -seed-steered N`:\n%s", strings.Join(violations, "\n"))
	}
	if hi-lo+1 >= 500 {
		for _, mode := range modes {
			s := stats[mode]
			t.Logf("%s: %+v", mode, *s)
			if s.dispatches == 0 {
				t.Errorf("%s: no dispatch was applied: %+v", mode, *s)
			}
			if s.attemptRefusals == 0 {
				t.Errorf("%s: no implementer/producer dispatch was refused by G_ATTEMPTS_REMAIN with autonomous_used == autonomous_max: %+v", mode, *s)
			}
			if s.replayRefusals == 0 {
				t.Errorf("%s: no dispatch was refused as an identical replay: %+v", mode, *s)
			}
		}
	}
}
