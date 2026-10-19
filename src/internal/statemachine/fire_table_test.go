package statemachine

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// evTableCase is one fixture of the transition table test: a (snapshot, event, facts) triple and what must
// result. A transition fixture expects Applied with the transition's id, target, effects, failure class and
// reason code. A guard fixture makes exactly one guard fail and expects Rejected naming it, or, where another
// candidate then fires, the fallback transition recorded in evGuardFallback.
type evTableCase struct {
	name     string
	snap     TaskSnapshot
	ev       Event
	facts    evFacts
	tr       evT    // the transition under test (transition fixtures)
	guard    string // the failing guard (guard fixtures)
	fallback string // expected fallback transition id, "" when Rejected is expected
}

// evGuardFallback lists the guards whose "only this guard fails" fixture is answered by another candidate of the
// same (state, event, actor, mode) instead of a rejection: negation pairs flip to their sibling transition and
// the rest fall through to a later catch-all candidate. Computed by hand from plan 29 section 8.1 and the table.
var evGuardFallback = map[string]string{
	"G_ATTEMPTS_EXHAUSTED":         "T034", // T035 -> T034 (GREEN_VERIFYING, green_fail)
	"G_INFRA_RETRIGGERS_REMAIN":    "T025", // T024 -> T025
	"G_INFRA_RETRIGGERS_EXHAUSTED": "T024", // T025 -> T024
	"G_REVIEWED_COMMIT_IS_HEAD":    "T043", // T041 -> T043 (review_accepted catch-all)
	"G_HAS_MERGEABLE_CHANGE":       "T043",
	"G_PROVENANCE_COMPLETE":        "T043",
	"G_REVIEW_INPUT_COMPLETE":      "T043",
	"G_NO_MERGEABLE_CHANGE":        "T041", // T042 -> T041 (verification-first, HAS_MERGEABLE_CHANGE then true)
	"G_CHILDREN_TERMINAL":          "T043", // T042 -> T043
	"G_MERGE_PROVENANCE_VERIFIED":  "T051", // T050 -> T051
	"G_MERGE_PROVENANCE_FAILED":    "T050", // T051 -> T050
	"G_CONFLICTS_REMAIN":           "T081", // T080 -> T081
	"G_CONFLICTS_EXHAUSTED":        "T080", // T081 -> T080
	"G_FAILURE_CODE":               "T106", // T105 -> T106 (budget class, other code)
	"G_INFRA_REQUEUE_ALLOWED":      "T101", // T100 -> T101
	"G_INFRA_REQUEUE_EXHAUSTED":    "T100", // T101 -> T100
	"G_BLOCKED_RESULTS_REMAIN":     "T108", // T107 -> T108
	"G_BLOCKED_RESULTS_EXHAUSTED":  "T107", // T108 -> T107
	"G_JUDGE_DETERMINISTIC":        "T115", // T114 -> T115
	"G_JUDGE_NONDETERMINISTIC":     "T114", // T115 -> T114
}

// evTransitionCases builds the transition fixtures: for every transition id, every source state that exists in
// a mode of the transition, every mode, every actor class it lists and every argument of its valued guards, the
// facts under which all of its guards pass (only T043 needs an override: the catch-all must not be preempted by
// the accepting T041). The transition must fire with the contract's target and effects.
func evTransitionCases(t testing.TB, mdl *evModel) []evTableCase {
	var out []evTableCase
	for _, tr := range mdl.transitions {
		for _, mode := range tr.modes {
			for _, from := range evFromStates(mdl, tr, mode) {
				for _, actor := range tr.actors {
					for k := 0; k < evArgCount(tr); k++ {
						f := evSat(mdl, tr, k)
						if tr.id == "T043" {
							f.b["G_REVIEW_INPUT_COMPLETE"] = false // T041 would otherwise accept first
						}
						name := fmt.Sprintf("%s/%s/%s/%s/arg%d", tr.id, from, mode, actor, k)
						out = append(out, evTableCase{name: name, snap: evSnap(mdl, from, mode),
							ev: Event{Name: tr.event, Actor: actor}, facts: f, tr: tr})
					}
				}
			}
		}
	}
	if len(out) < len(mdl.transitions) {
		t.Fatalf("only %d transition fixtures for %d transitions", len(out), len(mdl.transitions))
	}
	return out
}

// evGuardCases builds one fixture per catalogue guard id: the first transition (by id) that lists the guard
// (G_ACTOR_AUTHORIZED is implicit on every transition: the first transition overall), all of that transition's
// guards passing except the one under test.
func evGuardCases(t testing.TB, mdl *evModel) []evTableCase {
	var out []evTableCase
	for _, g := range mdl.guards {
		var tr evT
		found := false
		for _, c := range mdl.transitions {
			if g == "G_ACTOR_AUTHORIZED" || slices.ContainsFunc(c.guards, func(x string) bool { return evIDOf(x) == g }) {
				tr, found = c, true
				break
			}
		}
		if !found {
			t.Fatalf("guard %s is listed by no transition", g)
		}
		mode := ""
		for _, m := range tr.modes {
			if len(evFromStates(mdl, tr, m)) > 0 {
				mode = m
				break
			}
		}
		state := evFromStates(mdl, tr, mode)[0]
		f := evSat(mdl, tr, 0)
		switch {
		case evValued[g]:
			f.v[g] = "__other__"
		case evDerived[g] != "":
			f.b[evDerived[g]] = true
		default:
			f.b[g] = false
		}
		out = append(out, evTableCase{name: fmt.Sprintf("%s/%s", g, tr.id), snap: evSnap(mdl, state, mode),
			ev: Event{Name: tr.event, Actor: tr.actors[0]}, facts: f, tr: tr, guard: g, fallback: evGuardFallback[g]})
	}
	return out
}

// evRunCase fires one fixture and returns "" when the outcome is the expected one, else a message.
func evRunCase(m *Machine, c evTableCase) string {
	d, err := Fire(m, c.snap, c.ev, c.facts)
	if err != nil {
		return "Fire error: " + err.Error()
	}
	if (d.Applied == nil) == (d.Rejected == nil) {
		return "decision must hold exactly one of Applied and Rejected, got " + evDescribe(d)
	}
	if c.guard == "" {
		return evCheckApplied(d, c.tr, c.snap)
	}
	if c.fallback != "" {
		if d.Applied == nil || d.Applied.TransitionID != c.fallback {
			return fmt.Sprintf("guard %s fails: want fallback %s, got %s", c.guard, c.fallback, evDescribe(d))
		}
		return ""
	}
	if d.Rejected == nil {
		return fmt.Sprintf("guard %s fails alone: want Rejected naming it, got %s", c.guard, evDescribe(d))
	}
	if !slices.Contains(d.Rejected.FailedGuards, c.guard) {
		return fmt.Sprintf("Rejected.FailedGuards = %v does not name %s", d.Rejected.FailedGuards, c.guard)
	}
	if d.Rejected.Code == "" {
		return "Rejected.Code is empty"
	}
	if evDispatchEvents[c.ev.Name] {
		want := CodeGateNotSatisfied
		switch c.guard {
		case "G_NO_ACTIVE_EXECUTION":
			want = CodeAlreadyRunning // plan 29 section 7.2
		case "G_ATTEMPTS_REMAIN", "G_REVIEWS_REMAIN", "G_AUXILIARY_REMAIN":
			want = CodeAttemptsExhaust
		case "G_NOT_IDENTICAL_REPLAY":
			return "" // the replay code and its details are slice S3 (29-AC-8)
		}
		if d.Rejected.Code != want {
			return fmt.Sprintf("Rejected.Code = %s, want %s", d.Rejected.Code, want)
		}
	}
	return ""
}

// 29-AC-3: for EVERY transition id of contracts/state-machine.yaml a fixture (state, mode, event, actor, facts)
// fires that transition with its target (pseudo-targets resolved), effects, failure class and reason code, and
// for EVERY guard id a fixture where only that guard fails is rejected naming the guard (or answered by the
// recorded fallback transition).
func TestTransitionTable(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	t.Run("transitions", func(t *testing.T) {
		for _, c := range evTransitionCases(t, mdl) {
			t.Run(c.name, func(t *testing.T) {
				if msg := evRunCase(m, c); msg != "" {
					t.Error(msg)
				}
			})
		}
	})
	t.Run("guards", func(t *testing.T) {
		for _, c := range evGuardCases(t, mdl) {
			t.Run(c.name, func(t *testing.T) {
				if msg := evRunCase(m, c); msg != "" {
					t.Error(msg)
				}
			})
		}
	})
}

// evCoverage counts the transition ids that fired and the guard ids whose fixture met its expectation.
func evCoverage(t testing.TB, mdl *evModel, m *Machine) (tFired, gDone map[string]bool) {
	tFired, gDone = map[string]bool{}, map[string]bool{}
	for _, c := range evTransitionCases(t, mdl) {
		if evRunCase(m, c) == "" {
			tFired[c.tr.id] = true
		}
	}
	for _, c := range evGuardCases(t, mdl) {
		if evRunCase(m, c) == "" {
			gDone[c.guard] = true
		}
	}
	return tFired, gDone
}

// 29-AC-3: the entry point of `make state-machine-coverage` (GREEN adds the target to mk/statemachine.mk; it
// runs `go test ./internal/statemachine -run TestTransitionCoverage_29AC3 -v` and so shows the line below).
// The exercised sets come from Fire's own answers: a transition id counts when a fixture fired it, a guard id
// counts when its fixture was rejected naming it or answered by its fallback. The test fails below 100%.
func TestTransitionCoverage_29AC3(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	tFired, gDone := evCoverage(t, mdl, m)
	line := fmt.Sprintf("state-machine coverage: transitions %d/%d guards %d/%d", len(tFired), len(mdl.transitions), len(gDone), len(mdl.guards))
	fmt.Println(line)
	if len(tFired) != len(mdl.transitions) || len(gDone) != len(mdl.guards) {
		var missT, missG []string
		for _, tr := range mdl.transitions {
			if !tFired[tr.id] {
				missT = append(missT, tr.id)
			}
		}
		for _, g := range mdl.guards {
			if !gDone[g] {
				missG = append(missG, g)
			}
		}
		sort.Strings(missT)
		t.Fatalf("%s\nnot exercised: transitions %d (first %v), guards %d (first %v)", line, len(missT), head(missT), len(missG), head(missG))
	}
	if !strings.HasSuffix(line, fmt.Sprintf("guards %d/%d", len(mdl.guards), len(mdl.guards))) {
		t.Fatalf("malformed coverage line %q", line)
	}
}

func head(s []string) []string {
	if len(s) > 5 {
		return s[:5]
	}
	return s
}

// 29-AC-3: Fire is deterministic (equal inputs, equal Decision), does not modify its snapshot, and returns
// effect lists that cannot corrupt the machine when the caller edits them.
func TestFire_DeterministicPureNoAliasing_29AC3(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	tr := mdl.transitions[0]
	for _, c := range evTransitionCases(t, mdl) {
		if c.tr.id == "T034" {
			tr = c.tr
			snap := c.snap
			snap.Attempts.MeritByGeneration = map[string]int{"implementer:g1": 2}
			before := snap.Attempts.MeritByGeneration["implementer:g1"]
			d1 := evFire(t, m, snap, c.ev, c.facts.clone())
			for i := 0; i < 20; i++ {
				d2 := evFire(t, m, snap, c.ev, c.facts.clone())
				if !reflect.DeepEqual(d1, d2) {
					t.Fatalf("run %d: %s differs from %s", i, evDescribe(d2), evDescribe(d1))
				}
			}
			if snap.Attempts.MeritByGeneration["implementer:g1"] != before || len(snap.Attempts.MeritByGeneration) != 1 {
				t.Fatalf("Fire modified the snapshot: %v", snap.Attempts.MeritByGeneration)
			}
			if d1.Applied == nil || len(d1.Applied.Effects) == 0 {
				t.Fatalf("T034: no effects in %s", evDescribe(d1))
			}
			d1.Applied.Effects[0] = "E_TAMPERED"
			d3 := evFire(t, m, snap, c.ev, c.facts.clone())
			if msg := evCheckApplied(d3, tr, snap); msg != "" {
				t.Fatalf("editing a returned Effects slice changed the machine: %s", msg)
			}
			return
		}
	}
	t.Fatal("no fixture for T034")
}
