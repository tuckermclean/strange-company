package statemachine

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
)

// This file holds the shared test model of the evaluation slice (F05 S2): an independent reading of
// contracts/state-machine.yaml (so the expected values do not come from the code under test), the table-driven
// fake Facts and the fixture builders. No test here reads the clock or a global random source (D7, D8).

// evT is one transition of the contract.
type evT struct {
	id, to, event                    string
	from, actors, modes              []string
	guards, effects                  []string
	humanCommand                     string
	humanOnly, toSame, toReturn      bool
	toResume, fromHeld, fromCreation bool
}

// evS is one state of the contract.
type evS struct {
	name, active  string
	modes         []string
	returnTargets []string
	dispatchable  []string
	terminal      bool
}

type evModel struct {
	transitions []evT // ascending id
	states      []evS // contract order
	byName      map[string]evS
	guards      []string // sorted catalogue ids
	effects     []string // sorted catalogue ids
	events      []string // sorted catalogue names
	projection  []map[string]any
}

var evActors = []string{"runner", "ci", "hermes", "human"}

// evValued are the guards whose fact is a value compared with the guard's arguments.
var evValued = map[string]bool{
	"G_VERIFICATION_OUTCOME": true, "G_FAILURE_CLASS": true, "G_FAILURE_CODE": true, "G_REFUSAL_CODE": true,
	"G_REVIEW_VERDICT": true, "G_JUDGE_SOURCE": true, "G_APPROVAL": true,
}

// evDerived maps a guard that the catalogue defines as the negation of another to that base guard: Facts answer
// only the base guard; the negation is true exactly when the base fact is known and false (fail closed).
var evDerived = map[string]string{
	"G_ATTEMPTS_EXHAUSTED":         "G_ATTEMPTS_REMAIN",
	"G_INFRA_RETRIGGERS_EXHAUSTED": "G_INFRA_RETRIGGERS_REMAIN",
	"G_CONFLICTS_EXHAUSTED":        "G_CONFLICTS_REMAIN",
	"G_MERGE_PROVENANCE_FAILED":    "G_MERGE_PROVENANCE_VERIFIED",
	"G_INFRA_REQUEUE_EXHAUSTED":    "G_INFRA_REQUEUE_ALLOWED",
	"G_BLOCKED_RESULTS_EXHAUSTED":  "G_BLOCKED_RESULTS_REMAIN",
	"G_NO_MERGEABLE_CHANGE":        "G_HAS_MERGEABLE_CHANGE",
	"G_JUDGE_NONDETERMINISTIC":     "G_JUDGE_DETERMINISTIC",
}

var evDispatchEvents = map[string]bool{
	"dispatch_tester": true, "dispatch_implementer": true, "dispatch_producer": true,
	"dispatch_reviewer": true, "dispatch_auxiliary": true,
}

func evStrs(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, e := range l {
		out = append(out, fmt.Sprint(e))
	}
	return out
}

func evKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// evContract parses contracts/state-machine.yaml independently of Load.
func evContract(t testing.TB) *evModel {
	t.Helper()
	doc := parseMap(t, baseBytes(t))
	spec := doc["spec"].(map[string]any)
	mdl := &evModel{byName: map[string]evS{}}
	for _, s := range spec["states"].([]any) {
		sm := s.(map[string]any)
		st := evS{name: fmt.Sprint(sm["name"]), active: fmt.Sprint(sm["activeExecution"]), modes: evStrs(sm["modes"]),
			returnTargets: evStrs(sm["returnTargets"]), dispatchable: evStrs(sm["dispatchableRoles"])}
		st.terminal, _ = sm["terminal"].(bool)
		mdl.states = append(mdl.states, st)
		mdl.byName[st.name] = st
	}
	for _, x := range spec["transitions"].([]any) {
		tm := x.(map[string]any)
		tr := evT{id: fmt.Sprint(tm["id"]), to: fmt.Sprint(tm["to"]), event: fmt.Sprint(tm["event"]), from: evStrs(tm["from"]),
			actors: evStrs(tm["actors"]), modes: evStrs(tm["modes"]), guards: evStrs(tm["guards"]), effects: evStrs(tm["effects"])}
		tr.humanCommand, _ = tm["humanCommand"].(string)
		tr.humanOnly = len(tr.actors) == 1 && tr.actors[0] == "human"
		tr.toSame, tr.toReturn, tr.toResume = tr.to == "$same", tr.to == "$return", tr.to == "$resume"
		tr.fromCreation = slices.Contains(tr.from, "$none")
		tr.fromHeld = slices.Contains(tr.from, "BLOCKED") || slices.Contains(tr.from, "NEEDS_HUMAN")
		mdl.transitions = append(mdl.transitions, tr)
	}
	sort.Slice(mdl.transitions, func(i, j int) bool { return mdl.transitions[i].id < mdl.transitions[j].id })
	mdl.guards = evKeys(spec["guards"].(map[string]any))
	mdl.effects = evKeys(spec["effects"].(map[string]any))
	mdl.events = evKeys(spec["events"].(map[string]any))
	for _, p := range spec["humanProjection"].([]any) {
		mdl.projection = append(mdl.projection, p.(map[string]any))
	}
	return mdl
}

// evMachine loads the real contract with identifier-membership registries (so evaluation tests do not depend on
// the real registry types, which have their own tests).
func evMachine(t testing.TB) *Machine {
	t.Helper()
	m, err := loadBase(t)
	if err != nil || m == nil {
		t.Fatalf("Load(contracts/state-machine.yaml) failed: %v", err)
	}
	return m
}

func evIDOf(ref string) string { id, _, _ := strings.Cut(ref, ":"); return id }
func evArgsOf(ref string) []string {
	_, a, ok := strings.Cut(ref, ":")
	if !ok {
		return nil
	}
	return strings.Split(a, ",")
}

// evEffectArg returns the argument of the first effect with the given id ("" and false when absent).
func evEffectArg(effects []string, id string) (string, bool) {
	for _, e := range effects {
		if evIDOf(e) == id {
			a := evArgsOf(e)
			if len(a) == 0 {
				return "", true
			}
			return a[0], true
		}
	}
	return "", false
}

func (s evS) inMode(mode string) bool { return slices.Contains(s.modes, mode) }

// evFacts is the table-driven fake Facts. b answers boolean guards, v valued guards; rb overrides a boolean
// guard for one role as "<role>/<guard>". A key that is absent is a missing fact.
type evFacts struct {
	b  map[string]bool
	v  map[string]string
	rb map[string]bool
}

func (f evFacts) Bool(ev Event, guard string) (bool, bool) {
	if v, ok := f.rb[ev.Role+"/"+guard]; ok && ev.Role != "" {
		return v, true
	}
	v, ok := f.b[guard]
	return v, ok
}

func (f evFacts) Value(ev Event, guard string) (string, bool) {
	v, ok := f.v[guard]
	return v, ok
}

// evLiar answers a value together with known=false for everything: fail-closed evaluation must ignore it.
type evLiar struct{}

func (evLiar) Bool(Event, string) (bool, bool)    { return true, false }
func (evLiar) Value(Event, string) (string, bool) { return "red_valid", false }

func (f evFacts) clone() evFacts {
	c := evFacts{b: map[string]bool{}, v: map[string]string{}, rb: map[string]bool{}}
	for k, x := range f.b {
		c.b[k] = x
	}
	for k, x := range f.v {
		c.v[k] = x
	}
	for k, x := range f.rb {
		c.rb[k] = x
	}
	return c
}

// evBaseGuards are the boolean guards whose facts Facts answers: everything except valued and derived guards.
func evBaseGuards(mdl *evModel) []string {
	var out []string
	for _, g := range mdl.guards {
		if _, d := evDerived[g]; !evValued[g] && !d {
			out = append(out, g)
		}
	}
	return out
}

// evPermissive answers true for every base guard and no value.
func evPermissive(mdl *evModel) evFacts {
	f := evFacts{b: map[string]bool{}, v: map[string]string{}, rb: map[string]bool{}}
	for _, g := range evBaseGuards(mdl) {
		f.b[g] = true
	}
	return f
}

// evSat returns facts under which every guard of tr passes (argIdx selects the argument of valued guards) and
// every other valued guard is missing. Negation guards pass by making their base guard false.
func evSat(mdl *evModel, tr evT, argIdx int) evFacts {
	f := evPermissive(mdl)
	for _, g := range tr.guards {
		id, args := evIDOf(g), evArgsOf(g)
		switch {
		case evValued[id]:
			f.v[id] = args[argIdx%len(args)]
		case evDerived[id] != "":
			f.b[evDerived[id]] = false
		}
	}
	return f
}

// evArgCount is the number of argument choices of tr: the longest comma list of a valued guard (else 1).
func evArgCount(tr evT) int {
	n := 1
	for _, g := range tr.guards {
		if evValued[evIDOf(g)] {
			n = max(n, len(evArgsOf(g)))
		}
	}
	return n
}

// evSnap builds a snapshot in state/mode. ReturnState is the last return target that exists in the mode;
// ResumeState is TEST_NEEDS_WORK (a legal resume target in both modes); the two never coincide.
func evSnap(mdl *evModel, state, mode string) TaskSnapshot {
	snap := TaskSnapshot{Mode: mode, State: state, ResumeState: "TEST_NEEDS_WORK", Generation: 1,
		Attempts: Attempts{AutonomousMax: 5, TesterMax: 3, MeritByGeneration: map[string]int{}, ByClass: map[string]int{}}}
	if st, ok := mdl.byName[state]; ok {
		for _, rt := range st.returnTargets {
			if mdl.byName[rt].inMode(mode) {
				snap.ReturnState = rt
			}
		}
	}
	return snap
}

// evFromStates returns the concrete states a transition leaves ("" stands for creation) that exist in mode.
func evFromStates(mdl *evModel, tr evT, mode string) []string {
	var out []string
	for _, f := range tr.from {
		if f == "$none" {
			out = append(out, "")
			continue
		}
		if mdl.byName[f].inMode(mode) {
			out = append(out, f)
		}
	}
	return out
}

// evWant resolves the expected target of tr fired from snap.
func evWant(tr evT, snap TaskSnapshot) string {
	switch {
	case tr.toSame:
		return snap.State
	case tr.toReturn:
		return snap.ReturnState
	case tr.toResume:
		return snap.ResumeState
	}
	return tr.to
}

// evFire fires and demands a well-formed decision: no error and exactly one of Applied or Rejected.
func evFire(t testing.TB, m *Machine, snap TaskSnapshot, ev Event, facts Facts) Decision {
	t.Helper()
	d, err := Fire(m, snap, ev, facts)
	if err != nil {
		t.Fatalf("Fire(%s %s/%s %s) error: %v", ev.Name, snap.State, snap.Mode, ev.Actor, err)
	}
	if (d.Applied == nil) == (d.Rejected == nil) {
		t.Fatalf("Fire(%s %s/%s %s): decision must hold exactly one of Applied and Rejected, got %+v", ev.Name, snap.State, snap.Mode, ev.Actor, d)
	}
	return d
}

func evDescribe(d Decision) string {
	switch {
	case d.Applied != nil:
		return fmt.Sprintf("Applied{%s %s->%s}", d.Applied.TransitionID, d.Applied.From, d.Applied.To)
	case d.Rejected != nil:
		return fmt.Sprintf("Rejected{%s %v}", d.Rejected.Code, d.Rejected.FailedGuards)
	}
	return "empty decision"
}

// evCheckApplied compares an Applied decision with the contract's transition (id, from, target, effects,
// failure class, reason code).
func evCheckApplied(d Decision, tr evT, snap TaskSnapshot) string {
	if d.Applied == nil {
		return "want Applied " + tr.id + ", got " + evDescribe(d)
	}
	a := d.Applied
	from := snap.State
	if from == "" {
		from = "$none"
	}
	if a.TransitionID != tr.id {
		return fmt.Sprintf("fired %s, want %s", a.TransitionID, tr.id)
	}
	if a.From != from {
		return fmt.Sprintf("%s: From = %q, want %q", tr.id, a.From, from)
	}
	if want := evWant(tr, snap); a.To != want {
		return fmt.Sprintf("%s: To = %q, want %q", tr.id, a.To, want)
	}
	if !slices.Equal(a.Effects, tr.effects) {
		return fmt.Sprintf("%s: Effects = %v, want %v", tr.id, a.Effects, tr.effects)
	}
	wantClass, _ := evEffectArg(tr.effects, "E_COUNT_FAILURE")
	if a.FailureClass != wantClass {
		return fmt.Sprintf("%s: FailureClass = %q, want %q", tr.id, a.FailureClass, wantClass)
	}
	if reason, ok := evEffectArg(tr.effects, "E_NOTIFY_HUMAN"); ok && a.ReasonCode != reason {
		return fmt.Sprintf("%s: ReasonCode = %q, want %q", tr.id, a.ReasonCode, reason)
	}
	return ""
}
