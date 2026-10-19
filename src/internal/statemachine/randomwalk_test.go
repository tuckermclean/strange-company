package statemachine

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// 29-AC-4 (plan 29 section 22, property tests): seeded random walks per mode with random events, actors and
// facts, through the accounting layer (Apply) so that the counters are real. The five properties are checked
// at every step (the fifth at the end of each walk):
//
//	P1 no walk leaves the declared state set (of its mode);
//	P2 no non-human actor ever leaves BLOCKED or NEEDS_HUMAN;
//	P3 no IMPLEMENTING entry without G_RED_EVIDENCE_VALID true (asked and answered true and known);
//	P4 autonomous_used <= autonomous_max (and tester_used <= tester_max, and no counter below zero);
//	P5 replaying the recorded history reproduces the final task.
//
// Beyond the five, a Rejected event leaves the task unchanged and Apply never errors on a well-formed event.
//
// The flag -seed takes N or A..B; the default 1..10000 runs the full ten thousand walks per mode, so a plain
// `make test` covers 29-AC-4 (decision D3). A failing seed is reported as `go test -run TestRandomWalks -seed N`.
// Randomness is a local PCG per walk (D7); nothing reads the clock (D8).

var seedFlag = flag.String("seed", "1..10000", "random walk seeds for TestRandomWalks: N or A..B (default the full range)")

func parseSeeds(s string) (lo, hi uint64, err error) {
	a, b, isRange := strings.Cut(strings.TrimSpace(s), "..")
	if lo, err = strconv.ParseUint(a, 10, 64); err != nil {
		return 0, 0, fmt.Errorf("bad -seed %q: %v", s, err)
	}
	hi = lo
	if isRange {
		if hi, err = strconv.ParseUint(b, 10, 64); err != nil {
			return 0, 0, fmt.Errorf("bad -seed %q: %v", s, err)
		}
	}
	if hi < lo {
		return 0, 0, fmt.Errorf("bad -seed %q: the range is empty", s)
	}
	return lo, hi, nil
}

// 29-AC-4: the -seed flag syntax (N or A..B) and its default.
func TestSeedFlag_29AC4(t *testing.T) {
	for _, c := range []struct {
		in     string
		lo, hi uint64
		bad    bool
	}{
		{"7", 7, 7, false}, {"1..10000", 1, 10000, false}, {"3..9", 3, 9, false}, {" 5 ", 5, 5, false},
		{"9..3", 0, 0, true}, {"", 0, 0, true}, {"x", 0, 0, true}, {"1..", 0, 0, true}, {"..4", 0, 0, true}, {"-1", 0, 0, true},
	} {
		lo, hi, err := parseSeeds(c.in)
		if c.bad != (err != nil) || (!c.bad && (lo != c.lo || hi != c.hi)) {
			t.Errorf("parseSeeds(%q) = %d, %d, %v; want %d, %d, bad=%v", c.in, lo, hi, err, c.lo, c.hi, c.bad)
		}
	}
	if f := flag.Lookup("seed"); f == nil || f.DefValue != "1..10000" {
		t.Errorf("the -seed flag must exist with the default 1..10000 (the full range per mode), got %+v", f)
	}
}

// rwFacts is lazily memoized random Facts for one step: each guard is drawn when first asked and keeps its
// answer, and asked records what Fire consulted. wantTrue / wantFalse / wantV steer the facts towards the chosen
// transition so that walks get deep.
type rwFacts struct {
	rng       *rand.Rand
	pool      map[string][]string
	wantTrue  map[string]bool
	wantFalse map[string]bool
	wantV     map[string]string
	b         map[string][2]bool
	v         map[string]rwV
}

type rwV struct {
	v     string
	known bool
}

func (f *rwFacts) Bool(_ Event, g string) (bool, bool) {
	if a, ok := f.b[g]; ok {
		return a[0], a[1]
	}
	p := 0.5
	switch {
	case f.wantTrue[g]:
		p = 0.93
	case f.wantFalse[g]:
		p = 0.07
	}
	a := [2]bool{f.rng.Float64() < p, f.rng.Float64() < 0.97}
	f.b[g] = a
	return a[0], a[1]
}

func (f *rwFacts) Value(_ Event, g string) (string, bool) {
	if a, ok := f.v[g]; ok {
		return a.v, a.known
	}
	a := rwV{known: f.rng.Float64() < 0.97}
	if w := f.wantV[g]; w != "" && f.rng.Float64() < 0.85 {
		a.v = w
	} else if p := f.pool[g]; len(p) > 0 {
		a.v = p[f.rng.IntN(len(p))]
	}
	f.v[g] = a
	return a.v, a.known
}

// rwPools collects, per valued guard, the arguments the contract uses, plus values no guard accepts.
func rwPools(mdl *evModel) map[string][]string {
	pool := map[string][]string{}
	for _, tr := range mdl.transitions {
		for _, g := range tr.guards {
			if id := evIDOf(g); evValued[id] {
				for _, a := range evArgsOf(g) {
					if !slices.Contains(pool[id], a) {
						pool[id] = append(pool[id], a)
					}
				}
			}
		}
	}
	for id := range evValued {
		pool[id] = append(pool[id], "no-such-value")
	}
	return pool
}

var (
	rwCodes      = []string{"TIMED_OUT", "JOB_LOST", "START_TIMEOUT", "EMERGENCY_STOP", "BUDGET_SHARED_EXHAUSTED", "BUDGET_TASK_EXHAUSTED", "HARNESS_GAVE_UP", ""}
	rwSigs       = []string{"sig-a", "sig-b", "sig-c"}
	rwModels     = []string{"m1", "m2", "m3"}
	rwRoles      = []string{"researcher", "planner", "tester", "implementer", "reviewer"}
	rwDispatches = map[string]string{"dispatch_tester": "tester", "dispatch_implementer": "implementer", "dispatch_reviewer": "reviewer"}
)

func rwPolicy(seed uint64) RetryPolicy {
	p := acPolicy
	switch seed % 3 {
	case 0:
		p.MaxAutonomousAttempts, p.MaxTesterAttempts, p.MaxReviewExecutions, p.MaxAuxiliaryExecutions = 2, 2, 1, 1
		p.MaxConsecutive, p.MaxTotal, p.HumanGrant = 2, 3, 1
	case 1:
		p.MaxAutonomousAttempts, p.MaxTesterAttempts, p.MaxReviewExecutions, p.MaxAuxiliaryExecutions = 3, 2, 2, 2
	}
	return p
}

func rwInitial(mode string, p RetryPolicy) Task {
	return Task{Snapshot: TaskSnapshot{Mode: mode, Attempts: Attempts{AutonomousMax: p.MaxAutonomousAttempts, TesterMax: p.MaxTesterAttempts,
		MeritByGeneration: map[string]int{}, ByClass: map[string]int{}}}}
}

type rwStats struct{ implementing, held, heldNonHuman, attemptRefusals, replayRefusals, applied int }

// rwWalk runs one walk and returns the first violation ("" when none).
func rwWalk(m *Machine, mdl *evModel, pool map[string][]string, mode string, seed uint64, st *rwStats) string {
	stream := uint64(1)
	if mode == "verification-first" {
		stream = 2
	}
	rng := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15*stream))
	pol := rwPolicy(seed)
	task := rwInitial(mode, pol)
	const steps = 20
	for i := 0; i < steps; i++ {
		from := task.Snapshot.State
		if from == "" {
			from = "$none"
		}
		var cands []evT
		for _, tr := range mdl.transitions {
			if slices.Contains(tr.from, from) && slices.Contains(tr.modes, mode) {
				cands = append(cands, tr)
			}
		}
		rf := &rwFacts{rng: rng, pool: pool, wantTrue: map[string]bool{"G_ACTOR_AUTHORIZED": true}, wantFalse: map[string]bool{}, wantV: map[string]string{},
			b: map[string][2]bool{}, v: map[string]rwV{}}
		var name, actor string
		if len(cands) > 0 && rng.Float64() < 0.9 {
			tr := cands[rng.IntN(len(cands))]
			name, actor = tr.event, tr.actors[rng.IntN(len(tr.actors))]
			if rng.Float64() < 0.15 {
				actor = evActors[rng.IntN(len(evActors))]
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
		} else {
			name, actor = mdl.events[rng.IntN(len(mdl.events))], evActors[rng.IntN(len(evActors))]
		}
		ev := Event{Name: name, Actor: actor}
		in := StepInput{FailureSignature: rwSigs[rng.IntN(len(rwSigs))], FailureCode: rwCodes[rng.IntN(len(rwCodes))]}
		if evDispatchEvents[name] {
			role := rwDispatches[name]
			switch name {
			case "dispatch_producer":
				role = []string{"implementer", "researcher"}[rng.IntN(2)]
			case "dispatch_auxiliary":
				role = rwRoles[rng.IntN(len(rwRoles))]
			}
			if rng.Float64() < 0.1 {
				role = rwRoles[rng.IntN(len(rwRoles))]
			}
			ev.Role = role
			in.Dispatch = DispatchSpec{Role: role, Harness: "h", Model: rwModels[rng.IntN(len(rwModels))], Profile: "p",
				StartCommit: fmt.Sprintf("c%d", rng.IntN(2)), ACVersion: 1, OrchestratorNotes: fmt.Sprintf("n%d", rng.IntN(2))}
		}
		where := fmt.Sprintf("seed %d mode %s step %d (%s %s by %s)", seed, mode, i, from, name, actor)
		d, nt, err := Apply(m, task, ev, in, pol, rf)
		if err != nil {
			return where + ": Apply error: " + err.Error()
		}
		if (d.Applied == nil) == (d.Rejected == nil) {
			return where + ": decision must hold exactly one of Applied and Rejected, got " + evDescribe(d)
		}
		if d.Rejected != nil {
			if !reflect.DeepEqual(nt, task) {
				return where + ": a Rejected event changed the task"
			}
			if slices.Contains(d.Rejected.FailedGuards, "G_ATTEMPTS_REMAIN") {
				st.attemptRefusals++
			}
			if slices.Contains(d.Rejected.FailedGuards, "G_NOT_IDENTICAL_REPLAY") {
				st.replayRefusals++
			}
			continue
		}
		st.applied++
		to := nt.Snapshot.State
		// P1
		if s, ok := mdl.byName[to]; !ok || !s.inMode(mode) || to != d.Applied.To || d.Applied.From != from {
			return where + fmt.Sprintf(": left the declared state set: %s -> %q (Applied %+v)", from, to, *d.Applied)
		}
		// P2
		if from == "BLOCKED" || from == "NEEDS_HUMAN" {
			st.held++
			if actor != "human" {
				st.heldNonHuman++
				if to != from {
					return where + fmt.Sprintf(": the non-human actor %s left %s for %s", actor, from, to)
				}
			}
		}
		// P3
		if to == "IMPLEMENTING" && from != "IMPLEMENTING" {
			st.implementing++
			if v, ok := rf.b["G_RED_EVIDENCE_VALID"]; !ok || !v[0] || !v[1] {
				return where + fmt.Sprintf(": entered IMPLEMENTING without G_RED_EVIDENCE_VALID true (asked %v, answer %v)", ok, v)
			}
		}
		// P4
		a := nt.Snapshot.Attempts
		if a.AutonomousUsed < 0 || a.AutonomousUsed > a.AutonomousMax || a.TesterUsed < 0 || a.TesterUsed > a.TesterMax {
			return where + fmt.Sprintf(": counters out of bounds: autonomous %d/%d tester %d/%d", a.AutonomousUsed, a.AutonomousMax, a.TesterUsed, a.TesterMax)
		}
		task = nt
	}
	// P5
	got, err := Replay(m, rwInitial(mode, pol), task.History, pol)
	if err != nil {
		return fmt.Sprintf("seed %d mode %s: Replay error: %v", seed, mode, err)
	}
	if !reflect.DeepEqual(got, task) {
		return fmt.Sprintf("seed %d mode %s: replaying %d history steps gives state %q, want %q (or different counters)", seed, mode, len(task.History), got.Snapshot.State, task.Snapshot.State)
	}
	if n := len(task.History); n > 0 && task.History[n-1].Applied.To != task.Snapshot.State {
		return fmt.Sprintf("seed %d mode %s: the last history step ends in %q but the task is in %q", seed, mode, task.History[n-1].Applied.To, task.Snapshot.State)
	}
	return ""
}

// 29-AC-4: TestRandomWalks. Zero violations of the five properties of plan 29 section 22 over the seeds of
// -seed, in both modes.
func TestRandomWalks_29AC4(t *testing.T) {
	lo, hi, err := parseSeeds(*seedFlag)
	if err != nil {
		t.Fatal(err)
	}
	m := evMachine(t)
	mdl := evContract(t)
	pool := rwPools(mdl)
	var violations []string
	stats := map[string]*rwStats{"test-first": {}, "verification-first": {}}
	for _, mode := range modes {
		for seed := lo; seed <= hi; seed++ {
			if v := rwWalk(m, mdl, pool, mode, seed, stats[mode]); v != "" {
				violations = append(violations, v)
				if len(violations) >= 5 {
					t.Fatalf("%d violations (stopped at 5); reproduce with `go test -run TestRandomWalks -seed N`:\n%s", len(violations), strings.Join(violations, "\n"))
				}
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("violations; reproduce with `go test -run TestRandomWalks -seed N`:\n%s", strings.Join(violations, "\n"))
	}
	// The walks must be non-vacuous when the whole range ran: they enter IMPLEMENTING (test-first), sit in the
	// held states and try to leave them as non-human actors, and the accounting refuses dispatches.
	if hi-lo+1 >= 1000 {
		for _, mode := range modes {
			s := stats[mode]
			if s.applied == 0 || s.held == 0 || s.heldNonHuman == 0 || s.attemptRefusals == 0 || (mode == "test-first" && s.implementing == 0) {
				t.Errorf("%s: the walks are vacuous: %+v", mode, *s)
			}
		}
	}
}
