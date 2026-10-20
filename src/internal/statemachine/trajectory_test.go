package statemachine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"strange.company/sc/internal/contracts"
	"strange.company/sc/internal/statemachine/trajectory"
)

// The library trajectory runner (plan 21 sections 22.2 and 22.3, plan 29 sections 14 and 22, F05 slice S4).
//
// Every test/e2e/tdd/<test>.yaml is replayed against contracts/state-machine.yaml through Apply with a
// table-driven fake Facts, step by step: the state after each event, the fired transition id and the reason are
// compared with the file; a Rejected event leaves the task unchanged. After every applied step the task is
// serialized with Record and validated against contracts/task.schema.yaml (29-AC-10), together with the record
// invariants of plan 29 section 6.2 that the library can check. The file format is documented in
// test/e2e/tdd/README.md; the live runner (C10) reads the same files and ignores nothing but its own live: fields.
//
// Time comes from the files (events[].at, the fake clock of the trajectory), never from the process clock (D8).
// `make test-tdd-library` runs the tests named TestTrajectories_* (21-AC-20).

func trajectoryDir(t testing.TB) string { return filepath.Join(repoRoot(t), "test", "e2e", "tdd") }

// plan21Suite are the trajectories of plan 21 section 22.2 (the SPEC section 106 rows and the additional
// organizational tests) that are state-machine trajectories, plus the 29-AC-11 and 29-AC-7 trajectories. The
// exclusions are listed in test/e2e/tdd/README.md.
var plan21Suite = []string{
	"tdd-gate-implementer-before-red", "tdd-red-broken-test", "tdd-red-compile-error-then-stub", "tdd-already-satisfied",
	"tdd-tamper-delete", "tdd-tamper-weaken", "tdd-tamper-golden", "tdd-tamper-helper", "tdd-tamper-monkeypatch",
	"tdd-tamper-nested-module", "tdd-phase-conditional", "tdd-tamper-skip", "tdd-tamper-discovery", "tdd-tamper-env-detect",
	"tdd-happy-path", "tdd-test-spec-conflict", "tdd-conflict-rejected", "tdd-provenance-survives-retry",
	"tdd-exemption-not-in-policy", "tdd-exemption-auto-docs", "tdd-exemption-human-approval",
	"tdd-strengthen-after-blocked-refused", "tdd-already-satisfied-needs-human", "tdd-forged-trailer-commit",
	"tdd-unapproved-test-change", "tdd-protected-path-edit", "tdd-pipeline-config-edit", "tdd-merge-without-provenance",
	"tdd-human-command-authz", "tdd-exhaustion",
	// 29-AC-11 (bypass trajectories TDD-5/6/9/13/20) that have no row of their own in plan 21 section 22.2
	"tdd-review-truncated-input", "tdd-adjudication-tighten-only", "tdd-external-commit-hermes-refused",
	// 29-AC-7 golden retry sequences
	"tdd-retry-infra-refunds", "tdd-retry-consecutive-timeouts", "tdd-retry-emergency-stop",
}

// 21-AC-20: the suite of plan 21 section 22.2 is present as trajectory files and every file loads.
func TestTrajectories_Suite_21AC20(t *testing.T) {
	all, err := trajectory.Load(trajectoryDir(t))
	if err != nil {
		t.Fatalf("Load(test/e2e/tdd): %v", err)
	}
	have := map[string]bool{}
	for _, tr := range all {
		have[tr.Name] = true
	}
	for _, name := range plan21Suite {
		if !have[name] {
			t.Errorf("test/e2e/tdd/%s.yaml is missing", name)
		}
	}
	if len(all) < len(plan21Suite) {
		t.Errorf("%d trajectory files, want at least %d", len(all), len(plan21Suite))
	}
	for _, tr := range all {
		if len(tr.Refs) == 0 {
			t.Errorf("%s: refs is empty (name the plan row or acceptance criterion it specifies)", tr.File)
		}
	}
}

// ---- the fake Facts

// trajectoryFacts builds the table-driven fake facts of one step: every base boolean guard true, no valued fact,
// then the step's overrides. A key is a guard of the catalogue (boolean or valued), or "<role>/<guard>" for a
// per-role boolean. Unknown guards and values of the wrong kind fail the trajectory, they are never ignored.
func trajectoryFacts(t *testing.T, mdl *evModel, facts map[string]any) evFacts {
	t.Helper()
	f := evPermissive(mdl)
	keys := make([]string, 0, len(facts))
	for k := range facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		role, guard := "", k
		if r, g, ok := strings.Cut(k, "/"); ok {
			role, guard = r, g
		}
		if !slices.Contains(mdl.guards, guard) {
			t.Fatalf("fact %q: %q is not a guard of the table", k, guard)
		}
		switch v := facts[k].(type) {
		case bool:
			if evValued[guard] {
				t.Fatalf("fact %q: %s is a valued guard, give it a string", k, guard)
			}
			if role != "" {
				f.rb[role+"/"+guard] = v
			} else {
				f.b[guard] = v
			}
		case string:
			if !evValued[guard] || role != "" {
				t.Fatalf("fact %q: only valued guards take a string", k)
			}
			f.v[guard] = v
		default:
			t.Fatalf("fact %q: %v is neither a boolean nor a string", k, facts[k])
		}
	}
	return f
}

// ---- the record

func defaultRecordFields(mode string) map[string]any {
	const sha = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	verification := map[string]any{"mode": mode, "decided_at": "2026-10-13T12:00:00Z", "policy_revision": sha}
	if mode == "verification-first" {
		verification["exemption"] = map[string]any{"requested_class": "documentation-only", "justification": "documentation only change, no behavior",
			"status": "granted", "approval": "auto"}
	}
	return map[string]any{
		"task_id": "sc-1", "title": "Trajectory task", "objective": "Task driven by a trajectory file.", "repository": "projects/example",
		"project": "example", "source": map[string]any{"kind": "hermes"}, "base_ref": "main", "target_branch": "main",
		"base_commit": "0123456789abcdef0123456789abcdef01234567", "parent_task_id": nil,
		"acceptance_criteria": map[string]any{"current_version": 1, "versions": []any{map[string]any{
			"task_id": "sc-1", "version": 1, "status": "frozen", "created_at": "2026-10-13T12:00:00Z", "created_by": "hermes", "frozen_at": "2026-10-13T12:00:01Z",
			"criteria": []any{map[string]any{"id": "AC-1", "text": "The behavior holds.", "kind": "change"}},
			"digest":   "sha256:0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b"}}},
		"verification": verification, "branch": nil,
		"policy": map[string]any{"revision": sha, "commit": "89abcdef0123456789abcdef0123456789abcdef", "repo_policy_blob": nil},
	}
}

func trajectoryActor(class string, pipeline int) string {
	switch class {
	case "ci":
		return fmt.Sprintf("ci:woodpecker/%d", max(pipeline, 1))
	case "human":
		return "human:test-human"
	}
	return class
}

// generic round-trips a record through JSON, the shape every consumer sees.
func generic(t testing.TB, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("a record must serialize to JSON: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	return out
}

var (
	taskSchemaOnce *jsonschema.Schema
)

func taskSchema(t testing.TB) *jsonschema.Schema {
	t.Helper()
	if taskSchemaOnce == nil {
		all, err := contracts.CompileSchemas(contractsDir(t))
		if err != nil || all["task"] == nil {
			t.Fatalf("CompileSchemas(contracts): task schema %v, err %v", all["task"], err)
		}
		taskSchemaOnce = all["task"]
	}
	return taskSchemaOnce
}

// validateRecord checks rec against task.schema.yaml. The record is converted through the repository's own YAML
// loader (JSON is YAML) so the schema sees the same value shapes as sc-contracts.
func validateRecord(t testing.TB, rec map[string]any) error {
	t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("record does not serialize: %v", err)
	}
	doc, err := contracts.ParseYAML(b)
	if err != nil {
		t.Fatalf("record does not parse: %v", err)
	}
	return taskSchema(t).Validate(doc)
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func asList(v any) []any         { l, _ := v.([]any); return l }
func asStr(v any) string         { s, _ := v.(string); return s }
func asInt(v any) int            { f, _ := v.(float64); return int(f) }

// checkRecord checks the record invariants of plan 29 section 6.2 and the field mapping of the task.
func checkRecord(t *testing.T, m *Machine, task Task, rec map[string]any, where string) {
	t.Helper()
	bad := func(f string, a ...any) { t.Errorf("%s: record: %s", where, fmt.Sprintf(f, a...)) }
	s := task.Snapshot
	if asStr(rec["state"]) != s.State || m.findState(s.State) == nil {
		bad("state %v, task is in %s", rec["state"], s.State)
	}
	hist := asList(rec["history"])
	if len(hist) != len(task.History) || len(hist) == 0 {
		bad("history has %d rows, task has %d steps", len(hist), len(task.History))
		return
	}
	prev := ""
	for i, h := range hist {
		row := asMap(h)
		if asInt(row["seq"]) != i+1 {
			bad("history[%d].seq = %v, want %d (contiguous from 1)", i, row["seq"], i+1)
		}
		if m.findState(asStr(row["to_state"])) == nil {
			bad("history[%d].to_state %v is not a state of the table", i, row["to_state"])
		}
		step := task.History[i]
		if asStr(row["transition_id"]) != step.Applied.TransitionID || asStr(row["event"]) != step.Event.Name ||
			asStr(row["to_state"]) != step.Applied.To || asStr(row["actor_class"]) != step.Event.Actor {
			bad("history[%d] = %v does not match the applied step %+v", i, row, step.Applied)
		}
		if i == 0 && row["from_state"] != nil {
			bad("history[0].from_state = %v, want null (creation)", row["from_state"])
		}
		if i > 0 && asStr(row["from_state"]) != prev {
			bad("history[%d].from_state = %v, want %s (the previous to_state)", i, row["from_state"], prev)
		}
		if asInt(row["state_machine_revision"]) != Revision(m) {
			bad("history[%d].state_machine_revision = %v, want %d", i, row["state_machine_revision"], Revision(m))
		}
		prev = asStr(row["to_state"])
	}
	if prev != s.State {
		bad("history[-1].to_state = %s, want the state %s", prev, s.State)
	}
	if asInt(rec["version"]) != len(task.History) || asInt(rec["state_machine_revision"]) != Revision(m) {
		bad("version %v / state_machine_revision %v, want %d / %d", rec["version"], rec["state_machine_revision"], len(task.History), Revision(m))
	}
	if hs, _ := Project(m, s); asStr(rec["human_state"]) != string(hs) {
		bad("human_state %v, Project says %s", rec["human_state"], hs)
	}
	held := s.State == "NEEDS_HUMAN" || s.State == "BLOCKED"
	if _, ok := rec["needs_human"]; ok != (s.State == "NEEDS_HUMAN") {
		bad("needs_human present = %v in state %s", ok, s.State)
	}
	if _, ok := rec["blocked"]; ok != (s.State == "BLOCKED") {
		bad("blocked present = %v in state %s", ok, s.State)
	}
	if _, ok := rec["resume_state"]; ok != held {
		bad("resume_state present = %v in state %s", ok, s.State)
	}
	if held {
		rs := m.findState(asStr(rec["resume_state"]))
		if asStr(rec["resume_state"]) != s.ResumeState || rs == nil || isHeld(rs.name) || rs.terminal || rs.active == "required" {
			bad("resume_state %v (task %q) must be a dispatch-ready or verification state", rec["resume_state"], s.ResumeState)
		}
	}
	if nh := asMap(rec["needs_human"]); nh != nil && asStr(nh["reason"]) != task.NeedsHumanReason {
		bad("needs_human.reason %v, want %q", nh["reason"], task.NeedsHumanReason)
	}
	if b := asMap(rec["blocked"]); b != nil && asStr(b["reason_code"]) != task.BlockedReason {
		bad("blocked.reason_code %v, want %q", b["reason_code"], task.BlockedReason)
	}
	st := m.findState(s.State)
	_, closed := rec["closed_at"]
	if closed != (st != nil && st.terminal) {
		bad("closed_at present = %v in state %s", closed, s.State)
	}
	nRunning := 0
	for _, e := range task.Executions {
		if e.Outcome == "" {
			nRunning++
		}
	}
	if nRunning > 1 {
		bad("%d executions are non-terminal, want at most one (plan 29 section 6.2): %+v", nRunning, task.Executions)
	}
	running := nRunning > 0
	claim := asStr(rec["claimed_by"])
	switch {
	case running && !strings.HasPrefix(claim, "execution:"):
		bad("claimed_by = %v with a running execution, want execution:<id>", rec["claimed_by"])
	case !running && st != nil && st.active == "verification" && !strings.HasPrefix(claim, "ci:"):
		bad("claimed_by = %v in verification state %s, want ci:...", rec["claimed_by"], s.State)
	case !running && (st == nil || st.active != "verification") && rec["claimed_by"] != nil:
		bad("claimed_by = %v with nothing running and no pending verification", rec["claimed_by"])
	}
	if st != nil && st.active == "required" && !running {
		bad("state %s requires an execution but none is running", s.State)
	}
	a, want := asMap(rec["attempts"]), s.Attempts
	for k, v := range map[string]int{"autonomous_used": want.AutonomousUsed, "autonomous_max": want.AutonomousMax, "tester_used": want.TesterUsed,
		"tester_max": want.TesterMax, "consecutive_infrastructure": want.ConsecutiveInfrastructure, "consecutive_timeouts": want.ConsecutiveTimeouts,
		"reviews_used": want.ReviewsUsed, "auxiliary_used": want.AuxiliaryUsed} {
		if asInt(a[k]) != v {
			bad("attempts.%s = %v, want %d", k, a[k], v)
		}
	}
	for _, c := range []string{"merit", "infrastructure", "policy", "budget"} {
		if asInt(asMap(a["by_class"])[c]) != want.ByClass[c] {
			bad("attempts.by_class.%s = %v, want %d", c, asMap(a["by_class"])[c], want.ByClass[c])
		}
	}
	for k, v := range want.MeritByGeneration {
		if asInt(asMap(a["merit_by_generation"])[k]) != v {
			bad("attempts.merit_by_generation[%s] = %v, want %d", k, asMap(a["merit_by_generation"])[k], v)
		}
	}
	if tdd := asMap(rec["tdd"]); tdd == nil || asInt(tdd["current_generation"]) != s.Generation || tdd["required"] != (s.Mode == "test-first") {
		bad("tdd = %v, want current_generation %d and required %v", rec["tdd"], s.Generation, s.Mode == "test-first")
	}
	if ce := asMap(rec["current_execution"]); len(task.Executions) > 0 {
		last := task.Executions[len(task.Executions)-1]
		if ce == nil || asStr(ce["role"]) != last.Role || asStr(ce["harness"]) != last.Harness || asStr(ce["profile"]) != last.Profile {
			bad("current_execution = %v, want the latest execution %+v", rec["current_execution"], last)
		}
	} else if ce != nil {
		bad("current_execution = %v but the task has no execution", ce)
	}
}

// ---- the runner

// trajectoryReason is the reason of an applied step: the NEEDS_HUMAN reason code of the transition, the BLOCKED
// reason code when the step parks the task, else "".
func trajectoryReason(d Decision, task Task) string {
	switch {
	case d.Rejected != nil:
		return d.Rejected.Code
	case d.Applied.ReasonCode != "":
		return d.Applied.ReasonCode
	case d.Applied.To == "BLOCKED" && d.Applied.From != "BLOCKED":
		return task.BlockedReason
	}
	return ""
}

func trajectoryInput(tr trajectory.Trajectory, i int) StepInput {
	e := tr.Events[i]
	in := StepInput{FailureCode: e.FailureCode, FailureSignature: e.FailureSignature}
	if v, ok := e.Live["verdict"].(string); ok && e.Event == "adjudication_recorded" {
		in.Verdict = v // the adjudicator verdict reaches the library as StepInput.Verdict (D14)
	}
	switch d := e.Dispatch; {
	case d != nil:
		in.Dispatch = DispatchSpec{Role: e.Role, Harness: d.Harness, Model: d.Model, Profile: d.Profile, StartCommit: d.StartCommit, ACVersion: d.ACVersion,
			OrchestratorNotes: d.Notes, ContextRefs: d.ContextRefs, FailureSignatures: d.FailureSignatures}
	case strings.HasPrefix(e.Event, "dispatch_"):
		// no dispatch given: every dispatch differs from the earlier ones (a new model id and start commit)
		in.Dispatch = DispatchSpec{Role: e.Role, Harness: "claude-code", Model: fmt.Sprintf("model-%d", i+1), Profile: "std",
			StartCommit: fmt.Sprintf("commit-%d", i+1), ACVersion: 1, OrchestratorNotes: "notes"}
	}
	return in
}

// runTrajectory replays one trajectory file and checks every expectation; it returns the final task.
func runTrajectory(t *testing.T, m *Machine, tr trajectory.Trajectory) Task {
	t.Helper()
	mdl := evContract(t)
	task := Task{Snapshot: TaskSnapshot{Mode: tr.Mode, Attempts: Attempts{AutonomousMax: acPolicy.MaxAutonomousAttempts, TesterMax: acPolicy.MaxTesterAttempts,
		MeritByGeneration: map[string]int{}, ByClass: map[string]int{}}}}
	fields := defaultRecordFields(tr.Mode)
	for k, v := range tr.Task {
		fields[k] = v
	}
	var steps []RecordStep
	tiers := map[string]string{}
	var lastAt time.Time
	for i, e := range tr.Events {
		where := fmt.Sprintf("%s event %d (%s by %s)", tr.Name, i+1, e.Event, e.Actor)
		at, err := time.Parse(time.RFC3339, e.At)
		if err != nil || at.Before(lastAt) {
			t.Fatalf("%s: at %q must be RFC 3339 and not before the previous event", where, e.At)
		}
		lastAt = at
		in := trajectoryInput(tr, i)
		before := task
		d, next, err := Apply(m, task, Event{Name: e.Event, Actor: e.Actor, Role: e.Role}, in, acPolicy, trajectoryFacts(t, mdl, e.Facts))
		if err != nil {
			t.Fatalf("%s: Apply: %v", where, err)
		}
		if (d.Applied == nil) == (d.Rejected == nil) {
			t.Fatalf("%s: the decision must hold exactly one of Applied and Rejected, got %+v", where, d)
		}
		gotTr := "rejected"
		if d.Applied != nil {
			gotTr = d.Applied.TransitionID
			task = next
			tiers[in.Dispatch.Model] = "standard"
			steps = append(steps, RecordStep{At: e.At, Actor: trajectoryActor(e.Actor, e.Pipeline), Reason: firstNonEmpty(e.Reason, e.Event+" "+gotTr),
				EventKey: fmt.Sprintf("internal:%s-%d", tr.Name, i+1), Pipeline: e.Pipeline})
		} else if fmt.Sprint(next) != fmt.Sprint(before) {
			t.Errorf("%s: a Rejected event changed the task", where)
		}
		if gotTr != tr.ExpectedTransitions[i] {
			t.Errorf("%s: transition %s (%s), want %s", where, gotTr, evDescribe(d), tr.ExpectedTransitions[i])
		}
		if task.Snapshot.State != tr.ExpectedStates[i] {
			t.Errorf("%s: state %s, want %s", where, task.Snapshot.State, tr.ExpectedStates[i])
		}
		if r := trajectoryReason(d, task); r != tr.ExpectedReasons[i] {
			t.Errorf("%s: reason %q, want %q", where, r, tr.ExpectedReasons[i])
		}
		if tr.ExpectedFailedGuards != nil && tr.ExpectedFailedGuards[i] != nil {
			var got []string
			if d.Rejected != nil {
				got = slices.Clone(d.Rejected.FailedGuards)
			}
			want := slices.Clone(tr.ExpectedFailedGuards[i])
			sort.Strings(got)
			sort.Strings(want)
			if !slices.Equal(got, want) {
				t.Errorf("%s: failed guards %v, want %v", where, got, want)
			}
		}
		if t.Failed() {
			t.FailNow() // a diverged trajectory cannot be followed further
		}
		if d.Applied == nil {
			continue
		}
		rec, err := Record(m, task, RecordMeta{Fields: fields, Steps: steps, ModelTiers: tiers})
		if err != nil || rec == nil {
			t.Fatalf("%s: Record: %v", where, err)
		}
		g := generic(t, rec)
		if err := validateRecord(t, g); err != nil {
			t.Errorf("%s: the serialized record does not validate against task.schema.yaml: %v", where, err)
		}
		checkRecord(t, m, task, g, where)
	}
	for k, want := range tr.ExpectedAttempts {
		if got, ok := attemptValue(task.Snapshot.Attempts, k); !ok || got != want {
			t.Errorf("%s: final attempts.%s = %d (known %v), want %d", tr.Name, k, got, ok, want)
		}
	}
	return task
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// attemptValue reads a counter by the dotted key of expected_attempts (the schema's Attempts names, with
// by_class.<class> and merit_by_generation.<role>:g<n>).
func attemptValue(a Attempts, key string) (int, bool) {
	switch key {
	case "autonomous_used":
		return a.AutonomousUsed, true
	case "autonomous_max":
		return a.AutonomousMax, true
	case "tester_used":
		return a.TesterUsed, true
	case "tester_max":
		return a.TesterMax, true
	case "consecutive_infrastructure":
		return a.ConsecutiveInfrastructure, true
	case "consecutive_timeouts":
		return a.ConsecutiveTimeouts, true
	case "reviews_used":
		return a.ReviewsUsed, true
	case "auxiliary_used":
		return a.AuxiliaryUsed, true
	}
	if c, ok := strings.CutPrefix(key, "by_class."); ok {
		return a.ByClass[c], true
	}
	if c, ok := strings.CutPrefix(key, "merit_by_generation."); ok {
		return a.MeritByGeneration[c], true
	}
	return 0, false
}

// 21-AC-20, 29-AC-10, 29-AC-11, 29-AC-7: every trajectory file replays against the library with fake facts, and
// every record serialized after a step validates against task.schema.yaml. It fails when the directory holds no
// trajectory or a file is malformed.
func TestTrajectories_Replay_21AC20(t *testing.T) {
	all, err := trajectory.Load(trajectoryDir(t))
	if err != nil {
		t.Fatalf("Load(test/e2e/tdd): %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no trajectory files")
	}
	m := evMachine(t)
	for _, tr := range all {
		t.Run(tr.Name, func(t *testing.T) { runTrajectory(t, m, tr) })
	}
}

// 29-AC-10: the golden task records of the trajectory tests: for the happy path the record after every step
// validates (the runner above), and its last record is the schema's AWAITING_MERGE / DONE shape.
func TestTrajectories_HappyPathRecords_29AC10(t *testing.T) {
	all, err := trajectory.Load(trajectoryDir(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	m := evMachine(t)
	for _, tr := range all {
		if tr.Name != "tdd-happy-path" {
			continue
		}
		task := runTrajectory(t, m, tr)
		if task.Snapshot.State != "DONE" {
			t.Fatalf("tdd-happy-path ends in %s, want DONE", task.Snapshot.State)
		}
		var steps []RecordStep
		tiers := map[string]string{}
		for i, s := range task.History {
			steps = append(steps, RecordStep{At: tr.Events[i].At, Actor: trajectoryActor(s.Event.Actor, tr.Events[i].Pipeline), Reason: "step " + s.Applied.TransitionID,
				EventKey: fmt.Sprintf("internal:golden-%d", i+1), Pipeline: tr.Events[i].Pipeline})
			tiers[s.Input.Dispatch.Model] = "standard"
		}
		rec, err := Record(m, task, RecordMeta{Fields: defaultRecordFields(tr.Mode), Steps: steps, ModelTiers: tiers})
		if err != nil {
			t.Fatalf("Record: %v", err)
		}
		g := generic(t, rec)
		if err := validateRecord(t, g); err != nil {
			t.Fatalf("golden record: %v", err)
		}
		var ids []string
		for _, h := range asList(g["history"]) {
			ids = append(ids, asStr(asMap(h)["transition_id"]))
		}
		want := []string{"T001", "T010", "T014", "T020", "T030", "T032", "T033", "T040", "T041", "T050"}
		if !slices.Equal(ids, want) {
			t.Errorf("happy-path history = %v, want %v (plan 21 section 22.2)", ids, want)
		}
		if g["human_state"] != "Done" || g["closed_at"] == nil {
			t.Errorf("human_state %v closed_at %v, want Done and a closed_at", g["human_state"], g["closed_at"])
		}
		return
	}
	t.Fatal("tdd-happy-path.yaml not found")
}

// byName loads the trajectories by name.
func byName(t *testing.T) map[string]trajectory.Trajectory {
	t.Helper()
	all, err := trajectory.Load(trajectoryDir(t))
	if err != nil {
		t.Fatalf("Load(test/e2e/tdd): %v", err)
	}
	out := map[string]trajectory.Trajectory{}
	for _, tr := range all {
		out[tr.Name] = tr
	}
	return out
}

// trNext finds the first event of the given name and actor after index from (-1 when absent).
func trNext(tr trajectory.Trajectory, from int, event, actor string) int {
	for i := from + 1; i < len(tr.Events); i++ {
		if tr.Events[i].Event == event && tr.Events[i].Actor == actor {
			return i
		}
	}
	return -1
}

// trLast finds the last event of the given name and actor (-1 when absent).
func trLast(tr trajectory.Trajectory, event, actor string) int {
	for i := len(tr.Events) - 1; i >= 0; i-- {
		if tr.Events[i].Event == event && tr.Events[i].Actor == actor {
			return i
		}
	}
	return -1
}

// 29-AC-11: the five bypass trajectories keep the assertions the criterion names, so editing a file cannot
// silently drop them; the replay above checks that the library produces them.
func TestTrajectories_BypassAssertions_29AC11(t *testing.T) {
	trs := byName(t)
	rejectedWith := func(t *testing.T, tr trajectory.Trajectory, i int, code string, guards ...string) {
		t.Helper()
		if i < 0 || tr.ExpectedTransitions[i] != "rejected" || tr.ExpectedReasons[i] != code {
			t.Errorf("%s: event %d must be rejected with %s", tr.Name, i+1, code)
			return
		}
		for _, g := range guards {
			if tr.ExpectedFailedGuards == nil || !slices.Contains(tr.ExpectedFailedGuards[i], g) {
				t.Errorf("%s: event %d must list the failed guard %s", tr.Name, i+1, g)
			}
		}
	}
	t.Run("strengthen-after-blocked-refused", func(t *testing.T) { // TDD-5
		tr := trs["tdd-strengthen-after-blocked-refused"]
		impl := trNext(tr, -1, "dispatch_implementer", "hermes")
		blocked := trNext(tr, impl, "execution_blocked", "runner")
		refused := trNext(tr, blocked, "dispatch_tester", "hermes")
		if impl < 0 || blocked < 0 || refused < 0 || tr.ExpectedTransitions[impl] != "T030" || tr.ExpectedTransitions[blocked] != "T107" ||
			tr.ExpectedStates[blocked] != "RED_VERIFIED" {
			t.Fatalf("%s: want dispatch_implementer (T030), then execution_blocked (T107, back in RED_VERIFIED), then dispatch_tester", tr.Name)
		}
		rejectedWith(t, tr, refused, "GATE_NOT_SATISFIED", "G_NO_IMPLEMENTER_DISPATCHED_IN_GENERATION")
		if tr.ExpectedStates[refused] != "RED_VERIFIED" {
			t.Errorf("%s: the refused dispatch must leave the task in RED_VERIFIED, got %s", tr.Name, tr.ExpectedStates[refused])
		}
		// even after a human /sc strengthen-tests the dispatch stays refused, by that guard alone
		human := trNext(tr, refused, "strengthen_tests", "human")
		again := trLast(tr, "dispatch_tester", "hermes")
		if human < 0 || again < human {
			t.Fatalf("%s: want a human strengthen_tests and then a second refused dispatch_tester", tr.Name)
		}
		rejectedWith(t, tr, again, "GATE_NOT_SATISFIED", "G_NO_IMPLEMENTER_DISPATCHED_IN_GENERATION")
		if g := tr.ExpectedFailedGuards[again]; !slices.Equal(g, []string{"G_NO_IMPLEMENTER_DISPATCHED_IN_GENERATION"}) {
			t.Errorf("%s: after the human authorization only G_NO_IMPLEMENTER_DISPATCHED_IN_GENERATION may fail, got %v", tr.Name, g)
		}
	})
	t.Run("already-satisfied-needs-human", func(t *testing.T) { // TDD-6
		tr := trs["tdd-already-satisfied-needs-human"]
		at := slices.Index(tr.ExpectedStates, "ALREADY_SATISFIED")
		first := trNext(tr, at, "dispatch_tester", "hermes")
		human := trNext(tr, first, "strengthen_tests", "human")
		again := trNext(tr, human, "dispatch_tester", "hermes")
		if at < 0 || first < 0 || human < 0 || again < 0 {
			t.Fatalf("%s: want dispatch_tester from ALREADY_SATISFIED, then /sc strengthen-tests, then dispatch_tester", tr.Name)
		}
		rejectedWith(t, tr, first, "GATE_NOT_SATISFIED", "G_STRENGTHEN_AUTHORIZED")
		if tr.ExpectedTransitions[human] != "T016" || tr.ExpectedTransitions[again] != "T015" || tr.ExpectedStates[again] != "TEST_AUTHORING" {
			t.Errorf("%s: want T016 then T015 into TEST_AUTHORING", tr.Name)
		}
	})
	t.Run("review-truncated-input", func(t *testing.T) { // TDD-13
		tr := trs["tdd-review-truncated-input"]
		i := trNext(tr, -1, "review_accepted", "runner")
		if i < 0 || tr.ExpectedTransitions[i] != "T043" || tr.ExpectedStates[i] != "NEEDS_HUMAN" || tr.ExpectedReasons[i] != "needs_investigation" ||
			tr.Events[i].Facts["G_REVIEW_INPUT_COMPLETE"] != false {
			t.Errorf("%s: review_accepted with G_REVIEW_INPUT_COMPLETE=false must fire T043 into NEEDS_HUMAN (needs_investigation)", tr.Name)
		}
	})
	t.Run("adjudication-tighten-only", func(t *testing.T) { // TDD-20
		tr := trs["tdd-adjudication-tighten-only"]
		a1 := trNext(tr, -1, "adjudication_recorded", "runner")
		a2 := trNext(tr, a1, "adjudication_recorded", "runner")
		dispatch := trNext(tr, a2, "dispatch_tester", "hermes")
		if a1 < 0 || a2 < 0 || dispatch < 0 || tr.ExpectedTransitions[a1] != "T082" || tr.ExpectedTransitions[a2] != "T082" ||
			tr.ExpectedStates[a2] != "TEST_SPEC_CONFLICT" {
			t.Fatalf("%s: want two adjudications (T082, task stays in TEST_SPEC_CONFLICT) and then dispatch_tester", tr.Name)
		}
		rejectedWith(t, tr, dispatch, "GATE_NOT_SATISFIED", "G_TEST_CHANGE_APPROVED")
		if i := trNext(tr, -1, "approve_test_change", "human"); i >= 0 {
			t.Errorf("%s: no human approval may occur, test_change_approved must stay unset", tr.Name)
		}
	})
	t.Run("external-commit-hermes-refused", func(t *testing.T) { // TDD-9
		tr := trs["tdd-external-commit-hermes-refused"]
		h := trNext(tr, -1, "accept_external_commit", "hermes")
		human := trNext(tr, h, "accept_external_commit", "human")
		if h < 0 || tr.ExpectedStates[h] != "NEEDS_HUMAN" || human < 0 {
			t.Fatalf("%s: want accept_external_commit by hermes in NEEDS_HUMAN, then by a human", tr.Name)
		}
		rejectedWith(t, tr, h, "GATE_NOT_SATISFIED", "G_ACTOR_AUTHORIZED")
		if tr.ExpectedTransitions[human] != "T072" || tr.ExpectedStates[human] != "GREEN_VERIFYING" {
			t.Errorf("%s: the human accept_external_commit must follow and fire T072 into GREEN_VERIFYING", tr.Name)
		}
	})
}

// 29-AC-11 (D14): the task-state guards are derived by the library from the task, so no trajectory event may
// supply them as a fake fact (with or without a role prefix).
func TestTrajectories_NoTaskStateFacts_29AC11_D14(t *testing.T) {
	all, err := trajectory.Load(trajectoryDir(t))
	if err != nil || len(all) == 0 {
		t.Fatalf("Load(test/e2e/tdd) = %d trajectories, %v", len(all), err)
	}
	derived := []string{"G_NO_IMPLEMENTER_DISPATCHED_IN_GENERATION", "G_TEST_CHANGE_APPROVED", "G_STRENGTHEN_AUTHORIZED", "G_CONFLICT_REJECTED"}
	for _, tr := range all {
		for i, e := range tr.Events {
			for k := range e.Facts {
				_, g, found := strings.Cut(k, "/")
				if !found {
					g = k
				}
				if slices.Contains(derived, g) {
					t.Errorf("%s event %d (%s): the fact %s is derived by the library (D14), the file must not supply it", tr.Name, i+1, e.Event, k)
				}
			}
		}
	}
}

// 21-AC-20: `make test-tdd-library` exists and runs the trajectory tests of this package.
func TestMakeTarget_TestTddLibrary_21AC20(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(repoRoot(t), "mk", "*.mk"))
	mk, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	all := bytes.NewBuffer(mk)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all.WriteString("\n")
		all.Write(b)
	}
	re := regexp.MustCompile(`(?m)^test-tdd-library:[^\n]*\n((?:[\t][^\n]*\n?)+)`)
	m := re.FindStringSubmatch(all.String())
	if m == nil {
		t.Fatal("no `test-tdd-library:` target with a recipe in the Makefile or mk/*.mk")
	}
	if !strings.Contains(m[1], "go test") || !strings.Contains(m[1], "TestTrajectories_") || !strings.Contains(m[1], "internal/statemachine") {
		t.Errorf("the test-tdd-library recipe must run `go test` of ./internal/statemachine with -run TestTrajectories_, got:\n%s", m[1])
	}
	if !regexp.MustCompile(`(?m)^\.PHONY:[^\n]*\btest-tdd-library\b`).MatchString(all.String()) {
		t.Error("test-tdd-library is not declared .PHONY")
	}
}
