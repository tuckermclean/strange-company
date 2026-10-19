package statemachine

import (
	"maps"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"
)

// D9: hardening of Fire found in the S2 review (plan 29 sections 7.2 and 8.1). Each test below fails on the
// S2 implementation. (e), the documentation that a panicking Facts propagates with nothing applied, has no test.

// d9Patched loads the contract with one valid change: T090 (dispatch_auxiliary from RED_VERIFIED and seven
// other states, actors hermes and human) is limited to hermes and a human-only copy T199 is added, so the group
// (RED_VERIFIED, dispatch_auxiliary, test-first) has candidates with different actor lists.
func d9Patched(t *testing.T) *Machine {
	t.Helper()
	doc := parseMap(t, baseBytes(t))
	spec := doc["spec"].(map[string]any)
	trs := spec["transitions"].([]any)
	var copyOf map[string]any
	for _, x := range trs {
		if tm := x.(map[string]any); tm["id"] == "T090" {
			copyOf = map[string]any{}
			maps.Copy(copyOf, tm)
			tm["actors"] = []any{"hermes"}
		}
	}
	if copyOf == nil {
		t.Fatal("T090 not found in the contract")
	}
	copyOf["id"], copyOf["actors"], copyOf["humanCommand"] = "T199", []any{"human"}, "/sc escalate"
	spec["transitions"] = append(trs, copyOf)
	b, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	g, e := registries(t, doc)
	m, err := Load(b, g, e, opts(t))
	if err != nil || m == nil {
		t.Fatalf("the patched table must be valid, Load: %v", err)
	}
	return m
}

// D9a (plan 29 section 8.1 step 1): candidates are filtered by actor class before any guard is evaluated, and
// the rejection code and failed guards are computed from the actor-filtered candidates only. A human-only
// candidate in the group must not pollute the Hermes rejection (T199 would add G_ACTOR_AUTHORIZED and turn
// ATTEMPTS_EXHAUSTED into GATE_NOT_SATISFIED); when only the actor filter empties the set the rejection names
// G_ACTOR_AUTHORIZED alone, whatever else is false.
func TestFire_ActorFilterBeforeGuards_D9a(t *testing.T) {
	mdl := evContract(t)
	snap := evSnap(mdl, "RED_VERIFIED", "test-first")
	perm := evPermissive(mdl)
	noAux := perm.clone()
	noAux.b["G_AUXILIARY_REMAIN"] = false
	for name, m := range map[string]*Machine{"patched table": d9Patched(t), "contract": evMachine(t)} {
		hermes := Event{Name: "dispatch_auxiliary", Actor: "hermes", Role: "researcher"}
		d := evFire(t, m, snap, hermes, noAux)
		if d.Rejected == nil || d.Rejected.Code != CodeAttemptsExhaust || !slices.Equal(d.Rejected.FailedGuards, []string{"G_AUXILIARY_REMAIN"}) {
			t.Errorf("%s: hermes with G_AUXILIARY_REMAIN=false: want Rejected{ATTEMPTS_EXHAUSTED [G_AUXILIARY_REMAIN]}, got %s", name, evDescribe(d))
		}
		if d := evFire(t, m, snap, hermes, perm); d.Applied == nil || d.Applied.TransitionID != "T090" {
			t.Errorf("%s: hermes with permissive facts: want Applied T090, got %s", name, evDescribe(d))
		}
		// No candidate lists the actor class: Rejected naming G_ACTOR_AUTHORIZED only, other facts are not consulted.
		ci := Event{Name: "dispatch_auxiliary", Actor: "ci", Role: "researcher"}
		d = evFire(t, m, snap, ci, noAux)
		if d.Rejected == nil || d.Rejected.Code != CodeGateNotSatisfied || !slices.Equal(d.Rejected.FailedGuards, []string{"G_ACTOR_AUTHORIZED"}) {
			t.Errorf("%s: ci with G_AUXILIARY_REMAIN=false: want Rejected{GATE_NOT_SATISFIED [G_ACTOR_AUTHORIZED]}, got %s", name, evDescribe(d))
		}
	}
	m := d9Patched(t)
	human := Event{Name: "dispatch_auxiliary", Actor: "human", Role: "researcher"}
	if d := evFire(t, m, snap, human, perm); d.Applied == nil || d.Applied.TransitionID != "T199" {
		t.Errorf("patched table, human with permissive facts: want Applied T199 (T090 is filtered out), got %s", evDescribe(d))
	}
	d := evFire(t, m, snap, human, noAux)
	if d.Rejected == nil || d.Rejected.Code != CodeAttemptsExhaust || !slices.Equal(d.Rejected.FailedGuards, []string{"G_AUXILIARY_REMAIN"}) {
		t.Errorf("patched table, human with G_AUXILIARY_REMAIN=false: want Rejected{ATTEMPTS_EXHAUSTED [G_AUXILIARY_REMAIN]}, got %s", evDescribe(d))
	}
	// A listed actor whose G_ACTOR_AUTHORIZED fact is false is rejected by that guard (the guard is still evaluated).
	denied := perm.clone()
	denied.b["G_ACTOR_AUTHORIZED"] = false
	d = evFire(t, m, snap, Event{Name: "dispatch_auxiliary", Actor: "hermes", Role: "researcher"}, denied)
	if d.Rejected == nil || !slices.Contains(d.Rejected.FailedGuards, "G_ACTOR_AUTHORIZED") {
		t.Errorf("hermes with G_ACTOR_AUTHORIZED=false: want Rejected naming it, got %s", evDescribe(d))
	}
}

// D9b (plan 29 section 7.2): G_NOT_IDENTICAL_REPLAY failing alone on a dispatch is ATTEMPTS_EXHAUSTED with
// details.allowed = [identical_replay]; combined with another failed gate it is GATE_NOT_SATISFIED.
func TestFire_IdenticalReplayCode_D9b(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	snap := evSnap(mdl, "RED_VERIFIED", "test-first")
	ev := Event{Name: "dispatch_implementer", Actor: "hermes", Role: "implementer"}
	f := evPermissive(mdl)
	f.b["G_NOT_IDENTICAL_REPLAY"] = false
	d := evFire(t, m, snap, ev, f)
	if d.Rejected == nil || d.Rejected.Code != CodeAttemptsExhaust || !slices.Equal(d.Rejected.FailedGuards, []string{"G_NOT_IDENTICAL_REPLAY"}) {
		t.Fatalf("G_NOT_IDENTICAL_REPLAY failing alone: want Rejected{ATTEMPTS_EXHAUSTED [G_NOT_IDENTICAL_REPLAY]}, got %s", evDescribe(d))
	}
	if got := d.Rejected.Allowed(); !slices.Equal(got, []string{"identical_replay"}) {
		t.Errorf("Allowed() = %v, want [identical_replay]", got)
	}
	f.b["G_RED_EVIDENCE_VALID"] = false
	d = evFire(t, m, snap, ev, f)
	if d.Rejected == nil || d.Rejected.Code != CodeGateNotSatisfied || d.Rejected.Allowed() != nil {
		t.Errorf("replay together with another failed gate: want Rejected{GATE_NOT_SATISFIED} and no allowed list, got %s", evDescribe(d))
	}
}

// d9Counting answers like base, except that every repeated question about the same guard gets the opposite
// answer, and counts the questions. first holds the first answers.
type d9Counting struct {
	base   evFacts
	nb, nv map[string]int
	fb     map[string][2]bool
	fv     map[string]rwV
}

func newD9Counting(base evFacts) *d9Counting {
	return &d9Counting{base: base, nb: map[string]int{}, nv: map[string]int{}, fb: map[string][2]bool{}, fv: map[string]rwV{}}
}

func (c *d9Counting) Bool(ev Event, g string) (bool, bool) {
	v, known := c.base.Bool(ev, g)
	c.nb[g]++
	if c.nb[g] == 1 {
		c.fb[g] = [2]bool{v, known}
		return v, known
	}
	return !v, known
}

func (c *d9Counting) Value(ev Event, g string) (string, bool) {
	v, known := c.base.Value(ev, g)
	c.nv[g]++
	if c.nv[g] == 1 {
		c.fv[g] = rwV{v, known}
		return v, known
	}
	return v + "-changed", known
}

// D9c: each fact is asked of Facts at most once per Fire call (the answer is cached), so Facts that answer
// differently on repeated questions cannot produce a decision that no single fact set yields: the decision
// equals the one for the first answers.
func TestFire_AsksEachFactOnce_D9c(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	pool := rwPools(mdl)
	type sc struct {
		state, event, actor, role string
	}
	scenarios := []sc{
		{"IMPLEMENTING", "execution_failed", "runner", ""}, {"TEST_AUTHORING", "execution_failed", "runner", ""},
		{"GREEN_VERIFYING", "green_classified", "ci", ""}, {"RED_VERIFYING", "red_classified", "ci", ""},
		{"RED_VERIFIED", "dispatch_implementer", "hermes", "implementer"}, {"REVIEW", "review_changes_requested", "runner", ""},
		{"RED_VERIFIED", "dispatch_auxiliary", "hermes", "researcher"}, {"NEEDS_HUMAN", "retry", "human", ""},
		{"GREEN_VERIFIED", "dispatch_reviewer", "hermes", "reviewer"},
	}
	checked := 0
	for seed := uint64(1); seed <= 40; seed++ {
		rng := rand.New(rand.NewPCG(seed, 0xd9c))
		for _, s := range scenarios {
			base := evFacts{b: map[string]bool{}, v: map[string]string{}, rb: map[string]bool{}}
			for _, g := range evBaseGuards(mdl) {
				if rng.Float64() < 0.95 {
					base.b[g] = rng.Float64() < 0.7
				}
			}
			for g, p := range pool {
				if rng.Float64() < 0.95 {
					base.v[g] = p[rng.IntN(len(p))]
				}
			}
			snap := evSnap(mdl, s.state, "test-first")
			ev := Event{Name: s.event, Actor: s.actor, Role: s.role}
			cf := newD9Counting(base)
			got, err := Fire(m, snap, ev, cf)
			if err != nil {
				t.Fatalf("seed %d %s/%s: Fire: %v", seed, s.state, s.event, err)
			}
			for g, n := range cf.nb {
				if n > 1 {
					t.Errorf("seed %d %s/%s: boolean fact %s was asked %d times, want at most once", seed, s.state, s.event, g, n)
				}
			}
			for g, n := range cf.nv {
				if n > 1 {
					t.Errorf("seed %d %s/%s: valued fact %s was asked %d times, want at most once", seed, s.state, s.event, g, n)
				}
			}
			first := evFacts{b: map[string]bool{}, v: map[string]string{}, rb: map[string]bool{}}
			for g, a := range cf.fb {
				if a[1] {
					first.b[g] = a[0]
				}
			}
			for g, a := range cf.fv {
				if a.known {
					first.v[g] = a.v
				}
			}
			want, err := Fire(m, snap, ev, first)
			if err != nil {
				t.Fatalf("seed %d %s/%s: Fire with the first answers: %v", seed, s.state, s.event, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("seed %d %s/%s: with changing answers Fire decided %s, with the first answers %s", seed, s.state, s.event, evDescribe(got), evDescribe(want))
			}
			checked++
		}
	}
	if checked != 40*len(scenarios) {
		t.Errorf("checked %d scenarios", checked)
	}
}

// d9RoleEvent is the independent reading of plan 29 section 7.2: the event an agent_start of the role makes.
func d9RoleEvent(role, state, mode string) string {
	switch {
	case role == "tester":
		return "dispatch_tester"
	case role == "implementer" && mode == "test-first":
		return "dispatch_implementer"
	case (role == "implementer" || role == "researcher") && mode == "verification-first" &&
		(state == "JUDGE_APPROVED" || state == "CHANGES_REQUESTED" || state == "TEST_SPEC_CONFLICT"):
		return "dispatch_producer"
	case role == "reviewer" && state == "GREEN_VERIFIED":
		return "dispatch_reviewer"
	}
	return "dispatch_auxiliary"
}

// D9d (plan 29 section 7.2): for a dispatch event, a non-empty Event.Role must map to that event and be one of
// the state's dispatchableRoles, else Fire rejects, whatever the facts say. (An empty Role is not judged; the
// transition table fixtures fire dispatches without one.)
func TestFire_DispatchRoleChecks_D9d(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	perm := evPermissive(mdl)
	perm.b["G_CONFLICT_REJECTED"] = true
	rejected := 0
	for _, st := range mdl.states {
		for _, mode := range st.modes {
			for _, role := range rwRoles {
				for ev := range evDispatchEvents {
					if ev == d9RoleEvent(role, st.name, mode) && slices.Contains(st.dispatchable, role) {
						continue
					}
					for _, actor := range []string{"hermes", "human"} {
						d := evFire(t, m, evSnap(mdl, st.name, mode), Event{Name: ev, Actor: actor, Role: role}, perm)
						if d.Rejected == nil {
							t.Errorf("%s/%s: %s by %s with role %s fired (%s), want Rejected: the role does not map to the event or is not dispatchable here",
								st.name, mode, ev, actor, role, evDescribe(d))
						} else {
							rejected++
						}
					}
				}
			}
		}
	}
	if rejected < 500 {
		t.Errorf("only %d mismatching (state, mode, role, event) cases were checked", rejected)
	}
	// Controls: the matching role fires, and so does an empty role (not judged).
	snap := evSnap(mdl, "RED_VERIFIED", "test-first")
	for _, role := range []string{"implementer", ""} {
		d := evFire(t, m, snap, Event{Name: "dispatch_implementer", Actor: "hermes", Role: role}, perm)
		if d.Applied == nil || d.Applied.TransitionID != "T030" {
			t.Errorf("dispatch_implementer with role %q from RED_VERIFIED: want Applied T030, got %s", role, evDescribe(d))
		}
	}
	// The mismatch is reported like a failed gate, never as a code that invites a retry.
	d := evFire(t, m, snap, Event{Name: "dispatch_implementer", Actor: "hermes", Role: "reviewer"}, perm)
	if d.Rejected == nil || d.Rejected.Code != CodeGateNotSatisfied {
		t.Errorf("dispatch_implementer with role reviewer: want Rejected{GATE_NOT_SATISFIED}, got %s", evDescribe(d))
	}
}
