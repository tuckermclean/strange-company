package statemachine

import (
	"fmt"
	"testing"
)

// projWant is the human state of plan 29 section 14.13 for every state, with the snapshot variants of the two
// conditional entries: TEST_SPEC_CONFLICT awaiting human approval, and SPECIFIED with no execution dispatched yet.
// The label is the one of the table's humanProjection entry (contracts/state-machine.yaml).
var projWant = map[string]HumanState{
	"NEEDS_HUMAN": "Needs Human", "JUDGE_AWAITING_APPROVAL": "Needs Human", "ALREADY_SATISFIED": "Needs Human",
	"NEEDS_INVESTIGATION": "Needs Human",
	"BLOCKED":             "Blocked",
	"DONE":                "Done", "CANCELLED": "Done",
	"REVIEW": "Review", "AWAITING_MERGE": "Review",
	"TEST_AUTHORING": "In Progress", "RED_VERIFYING": "In Progress", "RED_VERIFIED": "In Progress",
	"TEST_NEEDS_WORK": "In Progress", "IMPLEMENTING": "In Progress", "GREEN_VERIFYING": "In Progress",
	"GREEN_VERIFIED": "In Progress", "CHANGES_REQUESTED": "In Progress", "JUDGE_AUTHORING": "In Progress",
	"JUDGE_VALIDATING": "In Progress", "JUDGE_APPROVED": "In Progress", "PRODUCING": "In Progress", "JUDGING": "In Progress",
}

var projLabel = map[HumanState]string{
	"Needs Human": "status/needs-human", "Blocked": "status/blocked", "Done": "status/done",
	"Review": "status/review", "Ready": "status/ready", "In Progress": "status/in-progress",
}

// 29-AC-9: Project returns the plan 29 section 14.13 human state (and the table's label) for all 24 states, in
// every mode the state exists in and for every snapshot variant, and never Backlog.
func TestProject_AllStates_29AC9(t *testing.T) {
	mdl := evContract(t)
	m := evMachine(t)
	if len(mdl.states) != 24 {
		t.Fatalf("the contract declares %d states, the criterion lists 24", len(mdl.states))
	}
	seen := map[string]bool{}
	for _, st := range mdl.states {
		for _, mode := range st.modes {
			for _, dispatched := range []bool{false, true} {
				for _, awaiting := range []bool{false, true} {
					snap := evSnap(mdl, st.name, mode)
					snap.ExecutionDispatched, snap.ConflictAwaitingApproval = dispatched, awaiting
					want, ok := projWant[st.name]
					switch st.name {
					case "SPECIFIED":
						want, ok = "Ready", true
						if dispatched {
							want = "In Progress"
						}
					case "TEST_SPEC_CONFLICT":
						want, ok = "In Progress", true
						if awaiting {
							want = "Needs Human"
						}
					}
					if !ok {
						t.Fatalf("test table has no expectation for state %s", st.name)
					}
					hs, label := Project(m, snap)
					name := fmt.Sprintf("%s/%s/dispatched=%v/awaiting=%v", st.name, mode, dispatched, awaiting)
					if hs != want || label != projLabel[want] {
						t.Errorf("%s: Project = (%q, %q), want (%q, %q)", name, hs, label, want, projLabel[want])
					}
					if hs == "Backlog" || label == "status/backlog" {
						t.Errorf("%s: Project returned Backlog, which only a human sets", name)
					}
					seen[st.name] = true
				}
			}
		}
	}
	if len(seen) != 24 {
		t.Errorf("%d distinct states checked, want 24", len(seen))
	}
}

// 29-AC-9: labels come from the table's humanProjection entries, not from constants in the code: a table whose
// Review entry carries another label projects REVIEW and AWAITING_MERGE to it; unknown states and a nil machine
// project to nothing (fail closed).
func TestProject_FollowsTableAndFailsClosed_29AC9(t *testing.T) {
	mdl := evContract(t)
	base := baseBytes(t)
	g, e := registries(t, parseMap(t, base))
	fx := fixture{Ops: []fixtureOp{{Op: "replace", Path: "spec/humanProjection/humanState=Review/label", Value: "status/under-review"}}}
	m, err := Load(mutate(t, base, fx), g, e, opts(t))
	if err != nil {
		t.Fatalf("Load of the relabelled table: %v", err)
	}
	for _, st := range []string{"REVIEW", "AWAITING_MERGE"} {
		if hs, label := Project(m, evSnap(mdl, st, "test-first")); hs != "Review" || label != "status/under-review" {
			t.Errorf("%s: Project = (%q, %q), want (Review, status/under-review)", st, hs, label)
		}
	}
	if hs, label := Project(m, evSnap(mdl, "DONE", "test-first")); hs != "Done" || label != "status/done" {
		t.Errorf("DONE: Project = (%q, %q), want (Done, status/done)", hs, label)
	}
	if hs, label := Project(evMachine(t), evSnap(mdl, "NO_SUCH_STATE", "test-first")); hs != "" || label != "" {
		t.Errorf("unknown state: Project = (%q, %q), want empty", hs, label)
	}
	if hs, label := Project(nil, evSnap(mdl, "DONE", "test-first")); hs != "" || label != "" {
		t.Errorf("nil machine: Project = (%q, %q), want empty", hs, label)
	}
}
