package statemachine

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// Decision D17 N5 (29-AC-10): Record writes no value it cannot know. The counters come from the task, the kind
// of an execution from its dispatch, and what the library cannot derive (generation rows, the pending pipeline
// uuid) from RecordMeta; a missing value is an error, never a fabricated or empty placeholder.

const (
	d17SHA1 = "0123456789abcdef0123456789abcdef01234567"
	d17SHA2 = "89abcdef0123456789abcdef0123456789abcdef"
	d17UUID = "123e4567-e89b-42d3-a456-426614174000"
)

// recordGenerations is the generation rows the Runner would hand to Record for the task: one per generation, the
// older ones superseded.
func recordGenerations(task Task, at string) []RecordGeneration {
	kind := "test"
	if task.Snapshot.Mode == "verification-first" {
		kind = "judge"
	}
	var out []RecordGeneration
	for g := 1; g <= task.Snapshot.Generation; g++ {
		row := RecordGeneration{Generation: g, Kind: kind, Status: "active", StartCommit: d17SHA1, ACVersion: 1, StartedAt: at}
		if g < task.Snapshot.Generation {
			row.Status, row.SupersededReason = "superseded", "strengthened"
		}
		out = append(out, row)
	}
	return out
}

// withRecordExtras adds the generation rows and the pending pipeline uuid to a meta that has the steps.
func withRecordExtras(task Task, meta RecordMeta) RecordMeta {
	if len(meta.Steps) == 0 {
		return meta
	}
	meta.Generations = recordGenerations(task, meta.Steps[0].At)
	meta.PendingPipeline = d17UUID
	return meta
}

// d17Meta builds the meta of the d14 task's history (pipeline numbers from pipes by step index).
func d17Meta(d *d14, pipes map[int]int) RecordMeta {
	meta := RecordMeta{Fields: defaultRecordFields(d.task.Snapshot.Mode), ModelTiers: map[string]string{}}
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for i, s := range d.task.History {
		meta.Steps = append(meta.Steps, RecordStep{At: at.Add(time.Duration(i) * time.Second).Format(time.RFC3339), Actor: trajectoryActor(s.Event.Actor, pipes[i]),
			Reason: "step " + s.Applied.TransitionID, EventKey: fmt.Sprintf("internal:d17-%d", i+1), Pipeline: pipes[i]})
		meta.ModelTiers[s.Input.Dispatch.Model] = "standard"
	}
	return withRecordExtras(d.task, meta)
}

func d17Record(t *testing.T, d *d14, meta RecordMeta) map[string]any {
	t.Helper()
	rec, err := Record(d.m, d.task, meta)
	if err != nil || rec == nil {
		t.Fatalf("Record = %v, %v", rec, err)
	}
	g := generic(t, rec)
	if err := validateRecord(t, g); err != nil {
		t.Fatalf("the record does not validate against task.schema.yaml: %v", err)
	}
	return g
}

// 29-AC-10 (D17 N5): attempts.blocked_results and attempts.conflicts_raised are the task's counters, not zeros.
func TestRecord_BlockedResultsAndConflictsRaisedFromTheTask_29AC10_D17N5(t *testing.T) {
	m, task, meta := recordHistory(t)
	task.Snapshot.Attempts.BlockedResults, task.Snapshot.Attempts.ConflictsRaised = 2, 1
	rec, err := Record(m, task, meta)
	if err != nil || rec == nil {
		t.Fatalf("Record = %v, %v", rec, err)
	}
	g := generic(t, rec)
	if err := validateRecord(t, g); err != nil {
		t.Fatalf("the record does not validate: %v", err)
	}
	a := asMap(g["attempts"])
	if asInt(a["blocked_results"]) != 2 || asInt(a["conflicts_raised"]) != 1 {
		t.Errorf("attempts.blocked_results = %v, conflicts_raised = %v, want 2 and 1 (the task's counters)", a["blocked_results"], a["conflicts_raised"])
	}
}

// 29-AC-10 (D17 N5, task.schema.yaml ExecutionRef.kind): a researcher dispatched as an auxiliary execution
// (dispatch_auxiliary, T090) has kind auxiliary; the same role dispatched as the producer of a verification-first
// task (dispatch_producer) is producing.
func TestRecord_ResearcherAuxiliaryExecutionKind_29AC10_D17N5(t *testing.T) {
	t.Run("auxiliary", func(t *testing.T) {
		d := newD14(t, "")
		d.toRedVerified()
		d.ok("hermes", "dispatch_auxiliary", "researcher", d.spec("researcher"), "T090", "RED_VERIFIED")
		g := d17Record(t, d, d17Meta(d, nil))
		ce := asMap(g["current_execution"])
		if ce["role"] != "researcher" || ce["kind"] != "auxiliary" || ce["state"] != "RUNNING" {
			t.Errorf("current_execution = %v, want the running researcher with kind auxiliary", ce)
		}
	})
	t.Run("producer", func(t *testing.T) {
		d := newD14(t, "")
		d.toJudgeApproved()
		d.ok("hermes", "dispatch_producer", "researcher", d.spec("researcher"), "T124", "PRODUCING")
		g := d17Record(t, d, d17Meta(d, nil))
		if ce := asMap(g["current_execution"]); ce["role"] != "researcher" || ce["kind"] != "producing" {
			t.Errorf("current_execution = %v, want the researcher producer with kind producing", ce)
		}
	})
}

// 29-AC-10 (D17 N5, plan 29 section 6.1): tdd.generations has one row per generation, from RecordMeta.Generations,
// and Record fails closed when a row is missing or the rows do not number the generations 1..n.
func TestRecord_GenerationRowsFromMeta_29AC10_D17N5(t *testing.T) {
	d := newD14(t, "human")
	d.toConflict()
	d.ok("human", "approve_test_change", "", StepInput{}, "T083", "TEST_SPEC_CONFLICT")
	d.ok("hermes", "dispatch_tester", "tester", d.spec("tester"), "T013", "TEST_AUTHORING")
	if d.task.Snapshot.Generation != 2 {
		t.Fatalf("generation %d after T013, want 2", d.task.Snapshot.Generation)
	}
	meta := d17Meta(d, nil)
	meta.Generations = []RecordGeneration{
		{Generation: 1, Kind: "test", Status: "superseded", StartCommit: d17SHA1, ACVersion: 1, StartedAt: "2026-10-13T12:00:01Z", SupersededReason: "test_change_approved"},
		{Generation: 2, Kind: "test", Status: "active", StartCommit: d17SHA2, ACVersion: 1, StartedAt: "2026-10-13T12:00:09Z"},
	}
	g := d17Record(t, d, meta)
	rows := asList(asMap(g["tdd"])["generations"])
	if len(rows) != 2 {
		t.Fatalf("tdd.generations has %d rows, want 2 (one per generation)", len(rows))
	}
	for i, want := range meta.Generations {
		row := asMap(rows[i])
		got := RecordGeneration{Generation: asInt(row["generation"]), Kind: asStr(row["kind"]), Status: asStr(row["status"]), StartCommit: asStr(row["start_commit"]),
			ACVersion: asInt(row["ac_version"]), StartedAt: asStr(row["started_at"]), SupersededReason: asStr(row["superseded_reason"])}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("tdd.generations[%d] = %+v, want %+v (the meta row)", i, got, want)
		}
		if _, ok := row["verifications"].([]any); !ok {
			t.Errorf("tdd.generations[%d].verifications = %v, want a list (task.schema.yaml requires it)", i, row["verifications"])
		}
	}
	for name, gens := range map[string][]RecordGeneration{
		"no rows":           nil,
		"one row of two":    meta.Generations[:1],
		"rows 2 and 1":      {meta.Generations[1], meta.Generations[0]},
		"rows 1 and 3":      {meta.Generations[0], {Generation: 3, Kind: "test", Status: "active", StartCommit: d17SHA2, ACVersion: 1, StartedAt: "2026-10-13T12:00:09Z"}},
		"three rows of two": append(append([]RecordGeneration{}, meta.Generations...), RecordGeneration{Generation: 3, Kind: "test", Status: "active", StartCommit: d17SHA2, ACVersion: 1, StartedAt: "2026-10-13T12:00:09Z"}),
	} {
		bad := meta
		bad.Generations = gens
		if rec, err := Record(d.m, d.task, bad); err == nil || rec != nil {
			t.Errorf("Record with %s = %v, %v; want an error and no record", name, rec, err)
		}
	}
}

// 29-AC-10 (D17 N5): a verification state whose step has no pipeline number is claimed by the pending pipeline of
// RecordMeta.PendingPipeline (claimed_by ci:pending/<uuid>); without it Record fails closed. A known pipeline number
// needs no uuid. No uuid is derived from the task.
func TestRecord_PendingPipelineFromMeta_29AC10_D17N5(t *testing.T) {
	d := newD14(t, "")
	d.ok("hermes", "task_created", "", StepInput{}, "T001", "SPECIFIED")
	d.ok("hermes", "dispatch_tester", "tester", d.spec("tester"), "T010", "TEST_AUTHORING")
	d.ok("runner", "tester_result_accepted", "", StepInput{}, "T014", "RED_VERIFYING")
	meta := d17Meta(d, nil) // the last step has no pipeline number
	g := d17Record(t, d, meta)
	if got, want := asStr(g["claimed_by"]), "ci:pending/"+d17UUID; got != want {
		t.Errorf("claimed_by = %q, want %q (the uuid of RecordMeta.PendingPipeline)", got, want)
	}
	other := meta
	other.PendingPipeline = "00000000-0000-4000-8000-000000000042"
	if got := asStr(generic(t, mustRecord(t, d, other))["claimed_by"]); got != "ci:pending/"+other.PendingPipeline {
		t.Errorf("claimed_by = %q, want the uuid of the meta whatever the task is", got)
	}
	missing := meta
	missing.PendingPipeline = ""
	if rec, err := Record(d.m, d.task, missing); err == nil || rec != nil {
		t.Errorf("Record in a verification state without a pipeline number and without PendingPipeline = %v, %v; want an error", rec, err)
	}
	known := d17Meta(d, map[int]int{2: 412})
	known.PendingPipeline = ""
	if got := asStr(generic(t, mustRecord(t, d, known))["claimed_by"]); got != "ci:woodpecker/412" {
		t.Errorf("claimed_by = %q with pipeline 412, want ci:woodpecker/412", got)
	}
}

func mustRecord(t *testing.T, d *d14, meta RecordMeta) map[string]any {
	t.Helper()
	rec, err := Record(d.m, d.task, meta)
	if err != nil || rec == nil {
		t.Fatalf("Record = %v, %v", rec, err)
	}
	return rec
}
