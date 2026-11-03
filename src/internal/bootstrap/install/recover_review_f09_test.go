package install_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F09 slice S8 attempt 2, 03-AC-12 (plan 03 section 20.1, plan 50 sections 7.1, 14.7, 14.9): review findings on the
// recover fencing step.
//
// Seams the tests fix:
//   - the plan names the step `recover.fence` (plan 50 section 14.9), not `recover.fence-nodes`;
//   - the fake scenario gains `fence: {fail: true}`: Fence returns an error without touching the state (a crash after
//     the checkpoint of facts.fenced_nodes);
//   - a Probe error that is not a provider `unavailable` (here SCENARIO_INVALID from an unreadable scenario) is not
//     "unreachable": the step fails with that error, exit other than 5, proposes no fencing.

const outageFenceFails = "regions:\n  configured: fake-2\n  unavailable: [fake-1]\nfence:\n  fail: true\n"

func (i *inst) writeScenario(doc string) {
	i.t.Helper()
	write(i.t, i.fakeProviderDir()+"/scenario.yaml", doc, 0o600)
}

// stateNames are the node names of the fake's managed state (<dir>/state.json written by the fake).
func (i *inst) stateNames(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(i.fakeProviderDir(), "state.json"))
	if err != nil {
		t.Fatalf("fake state: %v", err)
	}
	var st struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("fake state: %v", err)
	}
	names := map[string]bool{}
	for _, n := range st.Nodes {
		names[n.Name] = true
	}
	return names
}

// 03-AC-12 (crash safety): Fence fails after facts.fenced_nodes was checkpointed; the region then answers again. The
// rerun must not leave node-01 in facts.fenced_nodes while it is still a live managed node.
func TestRecoverFencing_RerunDropsStaleFencedFactOfLiveNode_03AC12(t *testing.T) {
	t.Parallel()
	i := fenceInstalled(t)
	i.writeScenario(outageFenceFails)
	code, out := i.recoverRun(clusterName+" 1", nil)
	if code == 0 {
		t.Fatalf("setup: the run with a failing Fence succeeded:\n%s", out)
	}
	fenced := fenceFacts(t, i).Facts.FencedNodes
	if len(fenced) != 1 || !i.stateNames(t)["node-01"] {
		t.Fatalf("setup: want node-01 in facts.fenced_nodes and still in provider state; facts=%+v\n%s", fenced, out)
	}
	i.writeScenario("regions:\n  configured: fake-2\n")
	i.recoverRun(clusterName+" 1", nil)
	live := i.stateNames(t)
	for _, f := range fenceFacts(t, i).Facts.FencedNodes {
		for name := range live {
			if f.Address == `fake_node.node["`+name+`"]` {
				t.Errorf("03-AC-12: facts.fenced_nodes lists %s, which is still a managed node in provider state", f.Address)
			}
		}
	}
}

// 03-AC-12: a Probe error that is not "unavailable" is not an unreachable node: the step fails with that error (not
// exit 5), proposes no fencing and fences nothing.
func TestRecoverFencing_NonUnavailableProbeErrorFailsWithoutFencing_03AC12(t *testing.T) {
	t.Parallel()
	i := fenceInstalled(t)
	i.writeScenario("regions:\n  configured: fake-2\n  unavailable: [fake-1]\nbogusKey: true\n")
	code, out := i.recoverRun(clusterName+" 1", nil)
	if code == 0 || code == 5 {
		t.Errorf("03-AC-12: exit %d, want a failure other than the confirmation exit 5:\n%s", code, out)
	}
	for _, bad := range []string{"UNREACHABLE NODES", "FENCE  node-01", "FENCE_CONFIRMATION"} {
		if strings.Contains(out, bad) {
			t.Errorf("03-AC-12: the output proposes fencing (%q):\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "SCENARIO_INVALID") {
		t.Errorf("03-AC-12: the error of the probe is not reported:\n%s", out)
	}
	if got := fenceFacts(t, i).Facts.FencedNodes; len(got) != 0 {
		t.Errorf("03-AC-12: facts.fenced_nodes = %+v, want none", got)
	}
}

// 03-AC-12 (plan 50 section 14.7): the typed text matches exactly; surrounding whitespace is a mismatch, as for
// SC_CONFIRM_PLAN.
func TestRecoverFencing_ConfirmationWithSurroundingWhitespaceIsMismatch_03AC12(t *testing.T) {
	t.Parallel()
	i := fenceInstalled(t)
	code, out := i.recoverRun(" "+clusterName+" 1 ", nil)
	requireExit(t, code, 5, out)
	if !strings.Contains(out, "FENCE_CONFIRMATION_MISMATCH") {
		t.Errorf("03-AC-12: no FENCE_CONFIRMATION_MISMATCH:\n%s", out)
	}
	if got := fenceFacts(t, i).Facts.FencedNodes; len(got) != 0 {
		t.Errorf("03-AC-12: facts.fenced_nodes = %+v, want none", got)
	}
	if !i.stateNames(t)["node-01"] {
		t.Errorf("03-AC-12: node-01 was fenced despite the mismatch")
	}
}

// 03-AC-12 (plan 50 section 7.1): --confirm-fence-nodes is accepted and fences like the environment variable.
func TestRecoverFencing_ConfirmFenceNodesFlagFences_03AC12(t *testing.T) {
	t.Parallel()
	i := fenceInstalled(t)
	code, out := i.recoverRun("", nil, "--confirm-fence-nodes", clusterName+" 1")
	requireExit(t, code, 5, out) // stops at the unconfirmed plan, like the env path
	if !strings.Contains(out, "PLAN_CONFIRMATION_MISSING") {
		t.Errorf("03-AC-12: the run did not reach the plan confirmation:\n%s", out)
	}
	if got := fenceFacts(t, i).Facts.FencedNodes; len(got) != 1 || got[0].Address != `fake_node.node["node-01"]` {
		t.Errorf("03-AC-12: facts.fenced_nodes = %+v, want node-01", got)
	}
	if c, ok := pvRead(t, i).confirmation("fence-nodes"); !ok || c.Method != "flag" {
		t.Errorf("03-AC-12: fence-nodes confirmation = %+v (found %v), want method flag", c, ok)
	}
}

// 03-AC-12 (plan 50 section 14.9): the fencing step is named recover.fence in the journal and for SC_TEST_STOP_AFTER.
func TestRecoverFencing_StepIsNamedRecoverFence_03AC12(t *testing.T) {
	t.Parallel()
	i := fenceInstalled(t)
	code, out := i.recoverRun(clusterName+" 1", stopAfter("recover.fence"))
	requireExit(t, code, 0, out)
	j := pvRead(t, i)
	if s, ok := j.stepIn("recover", "recover.fence"); !ok || s.Status != "succeeded" {
		t.Errorf("03-AC-12: journal step recover.fence = %+v (found %v), want succeeded", s, ok)
	}
	if _, ok := j.stepIn("recover", "recover.fence-nodes"); ok {
		t.Errorf("03-AC-12: journal still has the step recover.fence-nodes")
	}
	if _, ok := j.stepIn("recover", "plan.compute-plan"); ok {
		t.Errorf("03-AC-12: the run did not stop after recover.fence")
	}
}
