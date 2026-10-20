package statemachine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/contracts"
)

// 29-AC-10: the task record serializer and task.schema.yaml. No clock, no global random source (D7, D8).

// recordHistory builds a two-step history (creation, tester dispatch) with the honest Apply.
func recordHistory(t *testing.T) (*Machine, Task, RecordMeta) {
	t.Helper()
	m, mdl := evMachine(t), evContract(t)
	task := Task{Snapshot: TaskSnapshot{Mode: "test-first", Attempts: Attempts{AutonomousMax: 5, TesterMax: 3, MeritByGeneration: map[string]int{}, ByClass: map[string]int{}}}}
	meta := RecordMeta{Fields: defaultRecordFields("test-first"), ModelTiers: map[string]string{"m-tester": "standard"}}
	for i, s := range []struct {
		ev   Event
		in   StepInput
		at   string
		pipe int
	}{
		{Event{Name: "task_created", Actor: "hermes"}, StepInput{}, "2026-10-13T12:00:00Z", 0},
		{Event{Name: "dispatch_tester", Actor: "hermes", Role: "tester"},
			StepInput{Dispatch: DispatchSpec{Role: "tester", Harness: "claude-code", Model: "m-tester", Profile: "std", StartCommit: "c1", ACVersion: 1}}, "2026-10-13T12:00:01Z", 0},
	} {
		d, next, err := Apply(m, task, s.ev, s.in, acPolicy, evPermissive(mdl))
		if err != nil || d.Applied == nil {
			t.Fatalf("setup step %d: %s, %v", i, evDescribe(d), err)
		}
		task = next
		meta.Steps = append(meta.Steps, RecordStep{At: s.at, Actor: "hermes", Reason: "step " + d.Applied.TransitionID, EventKey: "internal:setup-" + string(rune('a'+i)), Pipeline: s.pipe})
	}
	return m, task, meta
}

// 29-AC-10: Record serializes a task with an active execution; the record validates and maps the library state.
func TestRecord_ValidatesAndMapsTheTask_29AC10(t *testing.T) {
	m, task, meta := recordHistory(t)
	rec, err := Record(m, task, meta)
	if err != nil || rec == nil {
		t.Fatalf("Record = %v, %v", rec, err)
	}
	g := generic(t, rec)
	if err := validateRecord(t, g); err != nil {
		t.Fatalf("record does not validate against task.schema.yaml: %v", err)
	}
	checkRecord(t, m, task, g, "setup")
	if g["state"] != "TEST_AUTHORING" || g["human_state"] != "In Progress" || g["version"] != float64(2) {
		t.Errorf("state %v human_state %v version %v, want TEST_AUTHORING, In Progress, 2", g["state"], g["human_state"], g["version"])
	}
	if claim := asStr(g["claimed_by"]); !strings.HasPrefix(claim, "execution:sc-1-x") {
		t.Errorf("claimed_by = %q, want execution:sc-1-x<n> (the running tester)", claim)
	}
	if g["created_at"] != "2026-10-13T12:00:00Z" || g["updated_at"] != "2026-10-13T12:00:01Z" || g["state_entered_at"] != "2026-10-13T12:00:01Z" {
		t.Errorf("created_at %v updated_at %v state_entered_at %v: the times come from the step metas", g["created_at"], g["updated_at"], g["state_entered_at"])
	}
	if got := asMap(asList(g["history"])[0]); got["from_state"] != nil || got["transition_id"] != "T001" || got["event"] != "task_created" {
		t.Errorf("history[0] = %v, want the creation row (from_state null, T001)", got)
	}
}

// 29-AC-10: Record fails closed on inconsistent input and never invents a record.
func TestRecord_FailsClosed_29AC10(t *testing.T) {
	m, task, meta := recordHistory(t)
	if rec, err := Record(nil, task, meta); err == nil || rec != nil {
		t.Errorf("Record with a nil machine = %v, %v; want an error and no record", rec, err)
	}
	short := meta
	short.Steps = meta.Steps[:1]
	if rec, err := Record(m, task, short); err == nil || rec != nil {
		t.Errorf("Record with fewer step metas than history steps = %v, %v; want an error", rec, err)
	}
	if rec, err := Record(m, Task{Snapshot: task.Snapshot}, RecordMeta{Fields: meta.Fields}); err == nil || rec != nil {
		t.Errorf("Record of a task without history = %v, %v; want an error (task.schema.yaml needs history)", rec, err)
	}
	noTier := meta
	noTier.ModelTiers = nil
	if rec, err := Record(m, task, noTier); err == nil || rec != nil {
		t.Errorf("Record with an execution whose model has no tier = %v, %v; want an error", rec, err)
	}
	bad := task
	bad.Snapshot.State = "IMPLEMENTING"
	if rec, err := Record(m, bad, meta); err == nil || rec != nil {
		t.Errorf("Record of a task whose state is not the last history state = %v, %v; want an error", rec, err)
	}
}

// 29-AC-10: every x-invalid-examples entry of task.schema.yaml fails validation, and the schema's own example
// validates. (The schema half passes without implementation work: it holds the contract, not the library.)
func TestTaskSchema_InvalidExamplesFail_29AC10(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(contractsDir(t), "task.schema.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := contracts.ParseYAML(b)
	if err != nil {
		t.Fatal(err)
	}
	schema := asAnyMap(t, doc)
	sch := taskSchema(t)
	invalid, _ := schema["x-invalid-examples"].([]any)
	if len(invalid) < 4 {
		t.Fatalf("x-invalid-examples has %d entries, want at least 4", len(invalid))
	}
	for i, x := range invalid {
		e := asAnyMap(t, x)
		if err := sch.Validate(e["value"]); err == nil {
			t.Errorf("x-invalid-examples[%d] (%v) validates, want a failure", i, e["reason"])
		}
	}
	for i, ex := range schema["examples"].([]any) {
		if err := sch.Validate(ex); err != nil {
			t.Errorf("examples[%d] does not validate: %v", i, err)
		}
	}
}

func asAnyMap(t testing.TB, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%T is not a map", v)
	}
	return m
}

var _ = errors.New
