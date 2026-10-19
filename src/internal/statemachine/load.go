// Package statemachine is the pure task state-machine library (plan 29, F05).
package statemachine

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/contracts"
)

// Error codes returned by Load and Check (plan 29 section 8.2).
const (
	CodeSchemaInvalid          = "SM_SCHEMA_INVALID"
	CodeSchemaUnavailable      = "SM_SCHEMA_UNAVAILABLE"
	CodeUnreadable             = "SM_UNREADABLE"
	CodeYAMLInvalid            = "SM_YAML_INVALID"
	CodeYAMLAnchor             = "SM_YAML_ANCHOR"
	CodeStateUndeclared        = "SM_STATE_UNDECLARED"
	CodeReturnTargetUndeclared = "SM_RETURN_TARGET_UNDECLARED"
	CodeProjectionStateUndecl  = "SM_PROJECTION_STATE_UNDECLARED"
	CodeInitialStateInvalid    = "SM_INITIAL_STATE_INVALID"
	CodeTerminalMismatch       = "SM_TERMINAL_MISMATCH"
	CodeGuardUndeclared        = "SM_GUARD_UNDECLARED"
	CodeEffectUndeclared       = "SM_EFFECT_UNDECLARED"
	CodeGuardUnknown           = "SM_GUARD_UNKNOWN"
	CodeEffectUnknown          = "SM_EFFECT_UNKNOWN"
	CodeEventUndeclared        = "SM_EVENT_UNDECLARED"
	CodeTransitionIDDuplicate  = "SM_TRANSITION_ID_DUPLICATE"
	CodeUnreachable            = "SM_UNREACHABLE"
	CodeNonTerminating         = "SM_NON_TERMINATING"
	CodeTerminalHasOutgoing    = "SM_TERMINAL_HAS_OUTGOING"
	CodeReturnFromNonExecution = "SM_RETURN_FROM_NON_EXECUTION"
	CodeHeldExitNotHuman       = "SM_HELD_EXIT_NOT_HUMAN"
	CodeHumanTargetsExecution  = "SM_HUMAN_TARGETS_EXECUTION"
	CodeProjectionUncovered    = "SM_PROJECTION_UNCOVERED"
	CodeStallEscalateInvalid   = "SM_STALL_ESCALATE_INVALID"
	CodeStallNoTransition      = "SM_STALL_NO_TRANSITION"
	CodeHumanCommandMissing    = "SM_HUMAN_COMMAND_MISSING"
	CodeOutcomeIncomplete      = "SM_OUTCOME_INCOMPLETE"
	CodeFailureClassIncomplete = "SM_FAILURE_CLASS_INCOMPLETE"
	CodeOutcomeGuardInvalid    = "SM_OUTCOME_GUARD_INVALID"
	CodeCreationInvalid        = "SM_CREATION_INVALID"
	CodeResumeFromNonHeld      = "SM_RESUME_FROM_NON_HELD"
	CodeModeMismatch           = "SM_MODE_MISMATCH"
)

// GuardRegistry knows every guard identifier the library implements (the part after ":" is not part of the id).
type GuardRegistry interface{ HasGuard(id string) bool }

// EffectRegistry knows every effect identifier the library implements.
type EffectRegistry interface{ HasEffect(id string) bool }

// Options configures Load and Check. ContractsDir holds state-machine.schema.yaml (the single source).
type Options struct{ ContractsDir string }

// LoadError is a load failure: Code is one of the Code constants; Mode is set for per-mode failures (graph, coverage, stall, mode-mismatch, guard and creation errors).
type LoadError struct {
	Code    string
	Pointer string
	Mode    string
	File    string // set by Check: the table path, rendered before the mode
	Message string
}

// Machine is a loaded, validated transition table.
type Machine struct {
	revision    int
	initial     string
	states      []state
	transitions []transition
	report      Report
}

type state struct {
	name          string
	modes         []string
	terminal      bool
	active        string
	returnTargets []string
	escalateTo    string
}

type transition struct {
	id           string
	from         []string
	to           string
	event        string
	actors       []string
	modes        []string
	guards       []string
	effects      []string
	humanCommand string
}

// ModeReport is the reachability and termination result for one verification mode.
type ModeReport struct {
	Mode           string
	States         int
	Reachable      int
	Unreachable    []string
	NonTerminating []string
}

// Report holds one ModeReport per mode, in table order.
type Report struct{ Modes []ModeReport }

// Error renders the failure as `<CODE>[:<json-pointer>] [<file>: ][mode <mode>: ]<message>`.
func (e *LoadError) Error() string {
	if e == nil {
		return ""
	}
	s := e.Code
	if e.Pointer != "" {
		s += ":" + e.Pointer
	}
	s += " "
	if e.File != "" {
		s += e.File + ": "
	}
	if e.Mode != "" {
		s += "mode " + e.Mode + ": "
	}
	return s + e.Message
}

func fail(code, ptr, format string, a ...any) *LoadError {
	return &LoadError{Code: code, Pointer: ptr, Message: fmt.Sprintf(format, a...)}
}

// Revision returns metadata.revision of the loaded table; 0 for a nil Machine.
func Revision(m *Machine) int {
	if m == nil {
		return 0
	}
	return m.revision
}

// Report returns the per-mode reachability and termination report; the zero Report for a nil Machine.
func (m *Machine) Report() Report {
	if m == nil {
		return Report{}
	}
	return m.report
}

// outcomes lists, per classification event, the outcomes every state that handles the event must cover
// (plan 29 section 8.2).
var outcomes = []struct {
	event string
	list  []string
}{
	{"red_classified", []string{"red_valid", "red_invalid", "already_satisfied", "partially_satisfied", "baseline_failing", "inconclusive"}},
	{"green_classified", []string{"green_pass", "green_fail", "tamper_detected"}},
	{"judge_classified", []string{"judge_valid", "judge_invalid", "already_satisfied", "partially_satisfied", "baseline_failing", "inconclusive"}},
	{"judging_classified", []string{"judging_pass", "judging_fail", "tamper_detected"}},
}

var failureClasses = []string{"merit", "infrastructure", "policy", "budget"}

// Load validates a transition table: YAML, schema, declarations, catalogue membership, registry membership,
// per-transition rules and graph rules, in that order; the first failure is returned as a *LoadError. The
// schema is compiled from opts.ContractsDir (the single source); nil registries reject every identifier.
func Load(table []byte, guards GuardRegistry, effects EffectRegistry, opts Options) (*Machine, error) {
	if msg, bad := findAnchor(table); bad {
		return nil, fail(CodeYAMLAnchor, "", "%s", msg)
	}
	doc, err := contracts.ParseYAML(table)
	if err != nil {
		return nil, fail(CodeYAMLInvalid, "", "%s", oneLine(err.Error()))
	}
	schemas, err := contracts.CompileSchemas(opts.ContractsDir)
	if err != nil {
		return nil, fail(CodeSchemaUnavailable, "", "%s", oneLine(err.Error()))
	}
	sch := schemas["state-machine"]
	if sch == nil {
		return nil, fail(CodeSchemaUnavailable, "", "state-machine.schema.yaml not found in %q", opts.ContractsDir)
	}
	if err := sch.Validate(doc); err != nil {
		return nil, schemaError(err)
	}
	m, ble := build(doc)
	if ble != nil {
		return nil, ble
	}
	root, _ := doc.(map[string]any)
	spec, _ := root["spec"].(map[string]any)
	if le := declarations(m, spec); le != nil {
		return nil, le
	}
	if le := membership(m, spec, guards, effects); le != nil {
		return nil, le
	}
	if le := transitionRules(m); le != nil {
		return nil, le
	}
	modes := strs(spec["modes"])
	if le := creation(m, modes); le != nil {
		return nil, le
	}
	if le := coverage(m); le != nil {
		return nil, le
	}
	var le *LoadError
	m.report, le = graph(m, modes)
	if le != nil {
		return nil, le
	}
	// Every reachable-or-not required state must be an execution_failed source; checked after the graph rules so
	// an unreachable island reports SM_UNREACHABLE first.
	if le := coverEvent(m, m.stateIndex(), "execution_failed", "G_FAILURE_CLASS", failureClasses, CodeFailureClassIncomplete, "failure class", true); le != nil {
		return nil, le
	}
	return m, nil
}

func schemaError(err error) *LoadError {
	msg := oneLine(err.Error())
	p := ""
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		for len(ve.Causes) > 0 {
			ve = ve.Causes[0]
		}
		if len(ve.InstanceLocation) > 0 {
			parts := make([]any, len(ve.InstanceLocation))
			for i, x := range ve.InstanceLocation {
				parts[i] = x
			}
			p = ptr(parts...)
		}
	}
	return fail(CodeSchemaInvalid, p, "%s", msg)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func findAnchor(b []byte) (string, bool) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			return "", false // syntax errors are reported by the parser
		}
		if msg, bad := walkAnchor(&n); bad {
			return msg, true
		}
	}
}

func walkAnchor(n *yaml.Node) (string, bool) {
	if n.Kind == yaml.AliasNode {
		return fmt.Sprintf("line %d: YAML aliases are not allowed", n.Line), true
	}
	if n.Anchor != "" {
		return fmt.Sprintf("line %d: YAML anchors are not allowed (&%s)", n.Line, n.Anchor), true
	}
	for _, c := range n.Content {
		if msg, bad := walkAnchor(c); bad {
			return msg, true
		}
	}
	return "", false
}

func strs(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, e := range l {
		s, _ := e.(string)
		out = append(out, s)
	}
	return out
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func list(v any) []map[string]any {
	l, _ := v.([]any)
	out := make([]map[string]any, 0, len(l))
	for _, e := range l {
		m, _ := e.(map[string]any)
		out = append(out, m)
	}
	return out
}

// build extracts the typed table from a schema-valid document.
func build(doc any) (*Machine, *LoadError) {
	root, ok := doc.(map[string]any)
	if !ok {
		return nil, fail(CodeSchemaInvalid, "", "document is not a mapping")
	}
	meta, _ := root["metadata"].(map[string]any)
	spec, _ := root["spec"].(map[string]any)
	if meta == nil || spec == nil {
		return nil, fail(CodeSchemaInvalid, "", "metadata or spec missing")
	}
	rev, ok := meta["revision"].(int)
	if !ok || rev < 1 {
		return nil, fail(CodeSchemaInvalid, "/metadata/revision", "revision is not an integer >= 1")
	}
	m := &Machine{revision: rev, initial: str(spec, "initialState")}
	for _, s := range list(spec["states"]) {
		st := state{name: str(s, "name"), modes: strs(s["modes"]), active: str(s, "activeExecution"), returnTargets: strs(s["returnTargets"])}
		st.terminal, _ = s["terminal"].(bool)
		if sm, ok := s["stall"].(map[string]any); ok {
			st.escalateTo = str(sm, "escalateTo")
		}
		m.states = append(m.states, st)
	}
	for _, t := range list(spec["transitions"]) {
		m.transitions = append(m.transitions, transition{
			id: str(t, "id"), from: strs(t["from"]), to: str(t, "to"), event: str(t, "event"), actors: strs(t["actors"]),
			modes: strs(t["modes"]), guards: strs(t["guards"]), effects: strs(t["effects"]), humanCommand: str(t, "humanCommand"),
		})
	}
	return m, nil
}

func (m *Machine) stateIndex() map[string]int {
	idx := make(map[string]int, len(m.states))
	for i, s := range m.states {
		idx[s.name] = i
	}
	return idx
}

func ptr(parts ...any) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteByte('/')
		switch v := p.(type) {
		case int:
			b.WriteString(strconv.Itoa(v))
		case string:
			b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(v))
		}
	}
	return b.String()
}

func has(l []string, s string) bool {
	for _, e := range l {
		if e == s {
			return true
		}
	}
	return false
}

func idOf(ref string) string {
	id, _, _ := strings.Cut(ref, ":")
	return id
}

func argsOf(ref string) []string {
	_, a, ok := strings.Cut(ref, ":")
	if !ok {
		return nil
	}
	return strings.Split(a, ",")
}

// declarations checks every state named anywhere, the initial and terminal states, stall targets, projection
// coverage and transition id uniqueness.
func declarations(m *Machine, spec map[string]any) *LoadError {
	idx := m.stateIndex()
	if len(idx) != len(m.states) {
		seen := map[string]bool{}
		for i, s := range m.states {
			if seen[s.name] {
				return fail(CodeSchemaInvalid, ptr("spec", "states", i, "name"), "state %s is declared twice", s.name)
			}
			seen[s.name] = true
		}
	}
	if i, ok := idx[m.initial]; !ok {
		return fail(CodeInitialStateInvalid, "/spec/initialState", "initialState %s is not a declared state", m.initial)
	} else if m.states[i].terminal {
		return fail(CodeInitialStateInvalid, "/spec/initialState", "initialState %s is a terminal state", m.initial)
	}
	listed := map[string]bool{}
	for i, name := range strs(spec["terminalStates"]) {
		si, ok := idx[name]
		if !ok {
			return fail(CodeStateUndeclared, ptr("spec", "terminalStates", i), "terminal state %s is not declared", name)
		}
		if !m.states[si].terminal {
			return fail(CodeTerminalMismatch, ptr("spec", "terminalStates", i), "%s is listed in terminalStates but its terminal flag is false", name)
		}
		listed[name] = true
	}
	for i, s := range m.states {
		if s.terminal && !listed[s.name] {
			return fail(CodeTerminalMismatch, ptr("spec", "states", i, "terminal"), "terminal state %s is not listed in terminalStates", s.name)
		}
	}
	for i, s := range m.states {
		for j, rt := range s.returnTargets {
			if _, ok := idx[rt]; !ok {
				return fail(CodeReturnTargetUndeclared, ptr("spec", "states", i, "returnTargets", j), "%s returnTargets names undeclared state %s", s.name, rt)
			}
		}
	}
	seenID := map[string]bool{}
	for i, t := range m.transitions {
		if seenID[t.id] {
			return fail(CodeTransitionIDDuplicate, ptr("spec", "transitions", i, "id"), "transition id %s is used twice", t.id)
		}
		seenID[t.id] = true
		for j, f := range t.from {
			if f == "$none" {
				continue
			}
			if _, ok := idx[f]; !ok {
				return fail(CodeStateUndeclared, ptr("spec", "transitions", i, "from", j), "%s: source state %s is not declared", t.id, f)
			}
		}
		if !strings.HasPrefix(t.to, "$") {
			if _, ok := idx[t.to]; !ok {
				return fail(CodeStateUndeclared, ptr("spec", "transitions", i, "to"), "%s: target state %s is not declared", t.id, t.to)
			}
		}
	}
	for i, p := range list(spec["humanProjection"]) {
		for j, name := range strs(p["states"]) {
			if _, ok := idx[name]; !ok {
				return fail(CodeProjectionStateUndecl, ptr("spec", "humanProjection", i, "states", j), "projection names undeclared state %s", name)
			}
		}
	}
	covered := map[string]bool{}
	for _, p := range list(spec["humanProjection"]) {
		if _, cond := p["when"]; cond {
			continue
		}
		for _, name := range strs(p["states"]) {
			covered[name] = true
		}
	}
	for _, s := range m.states {
		if !covered[s.name] {
			return fail(CodeProjectionUncovered, "/spec/humanProjection", "state %s is not covered by a humanProjection entry without a when condition", s.name)
		}
	}
	for i, s := range m.states {
		if s.escalateTo == "" {
			continue
		}
		ti, ok := idx[s.escalateTo]
		if !ok {
			return fail(CodeStallEscalateInvalid, ptr("spec", "states", i, "stall", "escalateTo"), "%s: stall.escalateTo %s is not declared", s.name, s.escalateTo)
		}
		if m.states[ti].terminal {
			return fail(CodeStallEscalateInvalid, ptr("spec", "states", i, "stall", "escalateTo"), "%s: stall.escalateTo %s is a terminal state", s.name, s.escalateTo)
		}
		for _, mode := range s.modes {
			found := false
			for _, t := range m.transitions {
				if t.event == "stall_timeout" && t.to == s.escalateTo && has(t.from, s.name) && appliesIn(t, s, mode) && has(t.actors, "runner") {
					found = true
				}
			}
			if !found {
				le := fail(CodeStallNoTransition, ptr("spec", "states", i, "stall"), "%s has stall.escalateTo %s but no runner stall_timeout transition from it to that state", s.name, s.escalateTo)
				le.Mode = mode
				return le
			}
		}
	}
	return nil
}

// membership checks catalogue declarations (guards, effects, events) and then the registries.
func membership(m *Machine, spec map[string]any, guards GuardRegistry, effects EffectRegistry) *LoadError {
	gcat, _ := spec["guards"].(map[string]any)
	ecat, _ := spec["effects"].(map[string]any)
	evcat, _ := spec["events"].(map[string]any)
	for i, t := range m.transitions {
		for j, g := range t.guards {
			if _, ok := gcat[idOf(g)]; !ok {
				return fail(CodeGuardUndeclared, ptr("spec", "transitions", i, "guards", j), "%s: guard %s is not declared in spec.guards", t.id, idOf(g))
			}
		}
		for j, e := range t.effects {
			if _, ok := ecat[idOf(e)]; !ok {
				return fail(CodeEffectUndeclared, ptr("spec", "transitions", i, "effects", j), "%s: effect %s is not declared in spec.effects", t.id, idOf(e))
			}
		}
		if _, ok := evcat[t.event]; !ok {
			return fail(CodeEventUndeclared, ptr("spec", "transitions", i, "event"), "%s: event %s is not declared in spec.events", t.id, t.event)
		}
	}
	for j, e := range strs(spec["implicitEffects"]) {
		if _, ok := ecat[idOf(e)]; !ok {
			return fail(CodeEffectUndeclared, ptr("spec", "implicitEffects", j), "implicit effect %s is not declared in spec.effects", idOf(e))
		}
	}
	for _, id := range sortedKeys(gcat) {
		if guards == nil || !guards.HasGuard(id) {
			return fail(CodeGuardUnknown, ptr("spec", "guards", id), "guard %s has no implementation in the guard registry", id)
		}
	}
	for _, id := range sortedKeys(ecat) {
		if effects == nil || !effects.HasEffect(id) {
			return fail(CodeEffectUnknown, ptr("spec", "effects", id), "effect %s has no implementation in the effect registry", id)
		}
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func isHeld(name string) bool { return name == "BLOCKED" || name == "NEEDS_HUMAN" }

func humanOnly(t transition) bool { return len(t.actors) == 1 && t.actors[0] == "human" }

// transitionRules applies the per-transition invariants.
func transitionRules(m *Machine) *LoadError {
	idx := m.stateIndex()
	for i, t := range m.transitions {
		p := func(f string) string { return ptr("spec", "transitions", i, f) }
		if le := guardArgs(t, p); le != nil {
			return le
		}
		if has(t.from, "$none") && (len(t.from) != 1 || t.to != m.initial) {
			return fail(CodeCreationInvalid, p("from"), "%s: a $none transition must have from [$none] and target initialState %s", t.id, m.initial)
		}
		if t.to == "$resume" {
			for _, f := range t.from {
				if !isHeld(f) {
					return fail(CodeResumeFromNonHeld, p("from"), "%s: $resume from %s, which is not BLOCKED or NEEDS_HUMAN", t.id, f)
				}
			}
		}
		if le := modeRules(m, idx, t, p); le != nil {
			return le
		}
		for _, f := range t.from {
			if f == "$none" {
				continue
			}
			s := m.states[idx[f]]
			if s.terminal {
				return fail(CodeTerminalHasOutgoing, p("from"), "%s leaves terminal state %s", t.id, f)
			}
			if t.to == "$return" && (s.active != "required" || len(s.returnTargets) == 0) {
				return fail(CodeReturnFromNonExecution, p("to"), "%s uses $return from %s, which is not an activeExecution required state with returnTargets", t.id, f)
			}
		}
		if t.to != "$same" {
			for _, f := range t.from {
				if isHeld(f) && !humanOnly(t) {
					return fail(CodeHeldExitNotHuman, p("actors"), "%s leaves %s with actors %v; only human may", t.id, f, t.actors)
				}
			}
		}
		if humanOnly(t) {
			if !strings.HasPrefix(t.to, "$") && m.states[idx[t.to]].active == "required" {
				return fail(CodeHumanTargetsExecution, p("to"), "%s is human-only and targets %s (activeExecution required)", t.id, t.to)
			}
			if t.humanCommand == "" && t.event != "pr_closed_by_human" {
				return fail(CodeHumanCommandMissing, p("humanCommand"), "%s is human-only and has no humanCommand", t.id)
			}
		}
	}
	return nil
}

// guardArgs validates the arguments of the outcome and failure-class guards of one transition.
func guardArgs(t transition, p func(string) string) *LoadError {
	bad := func(gp, format string, a ...any) *LoadError {
		le := fail(CodeOutcomeGuardInvalid, gp, format, a...)
		if len(t.modes) == 1 {
			le.Mode = t.modes[0]
		}
		return le
	}
	_, isPhase := phaseOf(t.event)
	for _, spec := range []struct {
		guard    string
		want     []string
		required bool
	}{{"G_VERIFICATION_OUTCOME", outcomesFor(t.event), isPhase}, {"G_FAILURE_CLASS", failureClasses, t.event == "execution_failed"}} {
		n := 0
		for j, g := range t.guards {
			if idOf(g) != spec.guard {
				continue
			}
			n++
			gp := p("guards") + "/" + strconv.Itoa(j)
			if !spec.required {
				continue // strayGuards rejects it after coverage
			}
			if n > 1 {
				return bad(gp, "%s: more than one %s guard", t.id, spec.guard)
			}
			args := argsOf(g)
			if len(args) == 0 {
				return bad(gp, "%s: %s has no argument", t.id, spec.guard)
			}
			for _, a := range args {
				if a == "" || !has(spec.want, a) {
					return bad(gp, "%s: %s argument %q is empty or not valid for event %s", t.id, spec.guard, a, t.event)
				}
			}
		}
		if n == 0 && spec.required {
			return bad(p("guards"), "%s: event %s requires exactly one %s guard", t.id, t.event, spec.guard)
		}
	}
	return nil
}

func phaseOf(event string) ([]string, bool) {
	for _, o := range outcomes {
		if o.event == event {
			return o.list, true
		}
	}
	return nil, false
}

// creation checks that every mode has a $none creation transition.
func creation(m *Machine, modes []string) *LoadError {
	for _, mode := range modes {
		found := false
		for _, t := range m.transitions {
			if has(t.from, "$none") && has(t.modes, mode) {
				found = true
			}
		}
		if !found {
			le := fail(CodeCreationInvalid, "/spec/transitions", "no $none creation transition applies in mode %s", mode)
			le.Mode = mode
			return le
		}
	}
	return nil
}

func outcomesFor(event string) []string {
	l, _ := phaseOf(event)
	return l
}

// modeRules checks that a transition's from and to states exist in the modes it declares.
func modeRules(m *Machine, idx map[string]int, t transition, p func(string) string) *LoadError {
	hasFrom := false
	for _, f := range t.from {
		if f == "$none" {
			continue
		}
		hasFrom = true
		ok := false
		for _, mode := range t.modes {
			ok = ok || has(m.states[idx[f]].modes, mode)
		}
		if !ok {
			return fail(CodeModeMismatch, p("from"), "%s: source state %s exists in none of the transition's modes %v", t.id, f, t.modes)
		}
	}
	if !hasFrom || strings.HasPrefix(t.to, "$") {
		return nil
	}
	for _, mode := range t.modes {
		for _, f := range t.from {
			if f != "$none" && has(m.states[idx[f]].modes, mode) && !has(m.states[idx[t.to]].modes, mode) {
				le := fail(CodeModeMismatch, p("to"), "%s: target %s does not exist in mode %s where source %s does", t.id, t.to, mode, f)
				le.Mode = mode
				return le
			}
		}
	}
	return nil
}

// appliesIn reports whether the transition applies to the mode and the state belongs to it.
func appliesIn(t transition, s state, mode string) bool {
	return has(t.modes, mode) && has(s.modes, mode)
}

// coverage checks outcome and failure-class completeness for every (state, mode) handling the event.
func coverage(m *Machine) *LoadError {
	idx := m.stateIndex()
	for i, s := range m.states {
		if s.active == "verification" && phaseEvent[s.name] == "" {
			return fail(CodeOutcomeIncomplete, ptr("spec", "states", i, "activeExecution"), "%s is an activeExecution verification state with no phase event", s.name)
		}
	}
	for _, o := range outcomes {
		if le := coverEvent(m, idx, o.event, "G_VERIFICATION_OUTCOME", o.list, CodeOutcomeIncomplete, "outcome", false); le != nil {
			return le
		}
	}
	if le := coverEvent(m, idx, "execution_failed", "G_FAILURE_CLASS", failureClasses, CodeFailureClassIncomplete, "failure class", false); le != nil {
		return le
	}
	return strayGuards(m)
}

// strayGuards rejects outcome and failure-class guards on events they do not classify. It runs after the
// coverage rules so a table that re-labels a phase event reports the missing coverage first.
func strayGuards(m *Machine) *LoadError {
	for i, t := range m.transitions {
		_, isPhase := phaseOf(t.event)
		for j, g := range t.guards {
			id := idOf(g)
			if (id == "G_VERIFICATION_OUTCOME" && !isPhase) || (id == "G_FAILURE_CLASS" && t.event != "execution_failed") {
				le := fail(CodeOutcomeGuardInvalid, ptr("spec", "transitions", i, "guards", j), "%s: %s is not allowed on event %s", t.id, id, t.event)
				if len(t.modes) == 1 {
					le.Mode = t.modes[0]
				}
				return le
			}
		}
	}
	return nil
}

// phaseEvent is the classification event each activeExecution verification state must handle.
var phaseEvent = map[string]string{
	"RED_VERIFYING": "red_classified", "GREEN_VERIFYING": "green_classified",
	"JUDGE_VALIDATING": "judge_classified", "JUDGING": "judging_classified",
}

func coverEvent(m *Machine, idx map[string]int, event, guard string, want []string, code, what string, strict bool) *LoadError {
	for _, s := range m.states {
		for _, mode := range s.modes {
			var cands []transition
			for _, t := range m.transitions {
				if t.event == event && has(t.from, s.name) && appliesIn(t, s, mode) {
					cands = append(cands, t)
				}
			}
			if len(cands) == 0 {
				if (s.active == "verification" && phaseEvent[s.name] == event) || (strict && s.active == "required" && event == "execution_failed") {
					le := fail(code, "/spec/transitions", "%s has no %s transition in %s", s.name, event, mode)
					le.Mode = mode
					return le
				}
				continue
			}
			var classes []string
			for _, t := range cands {
				for _, a := range t.actors {
					if !has(classes, a) {
						classes = append(classes, a)
					}
				}
			}
			for _, class := range classes {
				got := map[string]bool{}
				for _, t := range cands {
					if !has(t.actors, class) {
						continue
					}
					for _, g := range t.guards {
						if idOf(g) == guard {
							for _, a := range argsOf(g) {
								got[a] = true
							}
						}
					}
				}
				for _, w := range want {
					if !got[w] {
						le := fail(code, "/spec/transitions", "%s in %s (%s) has no %s transition covering %s %s", event, s.name, mode, class, what, w)
						le.Mode = mode
						return le
					}
				}
			}
		}
	}
	return nil
}

// graph computes reachability from initialState and termination per mode.
func graph(m *Machine, modes []string) (Report, *LoadError) {
	var rep Report
	idx := m.stateIndex()
	for _, mode := range modes {
		inMode := map[string]bool{}
		for _, s := range m.states {
			if has(s.modes, mode) {
				inMode[s.name] = true
			}
		}
		fwd := map[string][]string{}
		rev := map[string][]string{}
		edge := func(a, b string) {
			if inMode[a] && inMode[b] {
				fwd[a] = append(fwd[a], b)
				rev[b] = append(rev[b], a)
			}
		}
		for _, t := range m.transitions {
			if !has(t.modes, mode) {
				continue
			}
			for _, f := range t.from {
				if f == "$none" || !inMode[f] {
					continue
				}
				switch t.to {
				case "$same":
					edge(f, f)
				case "$return":
					for _, r := range m.states[idx[f]].returnTargets {
						edge(f, r)
					}
				case "$resume":
					for _, s := range m.states {
						if !isHeld(s.name) && !s.terminal && s.active != "required" {
							edge(f, s.name)
						}
					}
				default:
					edge(f, t.to)
				}
			}
		}
		seen := bfs(m.initial, inMode, fwd)
		terminals := map[string]bool{}
		for _, s := range m.states {
			if inMode[s.name] && s.terminal {
				terminals[s.name] = true
			}
		}
		canEnd := map[string]bool{}
		for name := range terminals {
			for k := range bfs(name, inMode, rev) {
				canEnd[k] = true
			}
		}
		mr := ModeReport{Mode: mode}
		for _, s := range m.states {
			if !inMode[s.name] {
				continue
			}
			mr.States++
			if seen[s.name] {
				mr.Reachable++
			} else {
				mr.Unreachable = append(mr.Unreachable, s.name)
			}
			if !s.terminal && !canEnd[s.name] {
				mr.NonTerminating = append(mr.NonTerminating, s.name)
			}
		}
		rep.Modes = append(rep.Modes, mr)
		if len(mr.Unreachable) > 0 {
			le := fail(CodeUnreachable, "/spec/states", "states unreachable from %s: %s", m.initial, strings.Join(mr.Unreachable, ", "))
			le.Mode = mode
			return rep, le
		}
		if len(mr.NonTerminating) > 0 {
			le := fail(CodeNonTerminating, "/spec/states", "states with no path to a terminal state: %s", strings.Join(mr.NonTerminating, ", "))
			le.Mode = mode
			return rep, le
		}
	}
	return rep, nil
}

func bfs(start string, inMode map[string]bool, adj map[string][]string) map[string]bool {
	seen := map[string]bool{}
	if !inMode[start] {
		return seen
	}
	seen[start] = true
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, n := range adj[cur] {
			if !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	return seen
}

// catalogueRegistry accepts exactly the identifiers the table's own catalogues declare. Check uses it until the
// guard and effect implementations exist (plan 29 section 7.1, S2); Load then runs against the real registries.
type catalogueRegistry map[string]bool

func (r catalogueRegistry) HasGuard(id string) bool  { return r[id] }
func (r catalogueRegistry) HasEffect(id string) bool { return r[id] }

// Check loads the table at tablePath and writes the per-mode reachability report to w. It is the Go entry point
// of `make state-machine-check`; any load failure is returned as a *LoadError.
func Check(w io.Writer, tablePath string, opts Options) error {
	b, err := os.ReadFile(tablePath)
	if err != nil {
		return fail(CodeUnreadable, "", "%s: %s", tablePath, oneLine(err.Error()))
	}
	g, e := catalogueRegistry{}, catalogueRegistry{}
	if doc, perr := contracts.ParseYAML(b); perr == nil {
		root, _ := doc.(map[string]any)
		spec, _ := root["spec"].(map[string]any)
		gc, _ := spec["guards"].(map[string]any)
		ec, _ := spec["effects"].(map[string]any)
		for k := range gc {
			g[k] = true
		}
		for k := range ec {
			e[k] = true
		}
	}
	m, err := Load(b, g, e, opts)
	if err != nil {
		var le *LoadError
		if errors.As(err, &le) {
			c := *le
			c.File = tablePath
			return &c
		}
		return err
	}
	fmt.Fprintf(w, "state machine %s revision %d: schema valid, invariants hold\n", tablePath, m.revision)
	for _, mr := range m.report.Modes {
		fmt.Fprintf(w, "mode %s: states=%d reachable=%d unreachable=%d non-terminating=%d\n",
			mr.Mode, mr.States, mr.Reachable, len(mr.Unreachable), len(mr.NonTerminating))
	}
	return nil
}
