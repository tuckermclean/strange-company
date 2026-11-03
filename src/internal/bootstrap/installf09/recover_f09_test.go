package installf09_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"strange.company/sc/internal/provider/fake"
)

// F09 slice S8, 03-AC-12 (plan 03 section 20.1, DR-12; plan 50 sections 7.1 and 14.9): recover fences the compute nodes
// whose provider API does not answer, then computes a compute plan that creates replacement nodes with new names in the
// configured region. Only the compute fencing and the compute plan of the recover pipeline are specified here; restore
// is plan 45.
//
// Seams the tests fix (the implementer follows them):
//   - the fake scenario gains `regions: {configured: <region>, unavailable: [<region>...]}` (fixture recover-region-outage.yaml);
//     without a scenario the fake creates nodes in the region fake-1, records the region of every node in its state and
//     a node in an unavailable region fails Inventory;
//   - `./bootstrap recover [--non-interactive]` runs the install state's journal through a recover pipeline of the
//     steps `recover.fence-nodes` (before plan.compute-plan) and `plan.compute-plan`, `plan.confirm`; SC_TEST_STOP_AFTER
//     names a step as for install;
//   - the non-interactive confirmation of fence-nodes is the environment variable SC_CONFIRM_FENCE_NODES (flag form
//     --confirm-fence-nodes), whose value is the text typed after the kind: "<cluster> <count>" (plan 50 section 7.1
//     lists no flag for it); journal confirmation kind fence-nodes, method flag;
//   - the fenced node leaves the fake's state (Inventory no longer lists it) and is recorded as
//     facts.fenced_nodes[{address: fake_node.node["node-01"], provider_id, fenced_at}] in the journal;
//   - the footprint title names the region, as plan 03 section 14.7 shows ("... (fake, region fake-2)").

const fenceCreateNode02 = `(?m)^\s+CREATE\s+node-02\b`

type fenceJournal struct {
	Facts struct {
		FencedNodes []struct {
			Address    string `json:"address"`
			ProviderID string `json:"provider_id"`
			FencedAt   string `json:"fenced_at"`
		} `json:"fenced_nodes"`
	} `json:"facts"`
}

func fenceFacts(t testing.TB, i *inst) fenceJournal {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(i.repo, ".bootstrap", "journal.json"))
	if err != nil {
		t.Fatalf("no local journal: %v", err)
	}
	var j fenceJournal
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatalf("journal does not parse: %v", err)
	}
	return j
}

// fenceInstalled provisions node-01 in fake-1 and then moves the scenario to the regional outage.
func fenceInstalled(t *testing.T) *inst {
	t.Helper()
	i := newInst(t)
	code, out := s5Provision(t, i, nil)
	requireExit(t, code, 0, out)
	if n := i.createCount(); n != 1 {
		t.Fatalf("setup: create counter = %d, want 1", n)
	}
	i.useScenario("recover-region-outage")
	return i
}

func (i *inst) recoverRun(confirmFence string, extra map[string]string, args ...string) (int, string) {
	env := i.unattendedEnv()
	delete(env, "SC_CONFIRM_PLAN")
	env = merge(env, stopAfter("plan.confirm"), extra)
	if confirmFence != "" {
		env["SC_CONFIRM_FENCE_NODES"] = confirmFence
	}
	return i.run("", env, i.envOnlyArgs(append([]string{"--non-interactive", "recover"}, args...)...))
}

// 03-AC-12: without the fence-nodes confirmation a non-interactive recover exits 5, names the nodes it would fence,
// fences nothing, records no fenced_nodes and creates nothing.
func TestRecoverFencing_NonInteractiveWithoutConfirmationExits5_03AC12(t *testing.T) {
	t.Parallel()
	i := fenceInstalled(t)
	code, out := i.recoverRun("", nil)
	requireExit(t, code, 5, out)
	for _, want := range []string{"fence-nodes", "node-01"} {
		if !strings.Contains(out, want) {
			t.Errorf("03-AC-12: output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "PLAN_CONFIRMATION_MISSING") || regexp.MustCompile(fenceCreateNode02).MatchString(out) {
		t.Errorf("03-AC-12: the run went on to the compute plan without the fence-nodes confirmation:\n%s", out)
	}
	if got := fenceFacts(t, i).Facts.FencedNodes; len(got) != 0 {
		t.Errorf("03-AC-12: facts.fenced_nodes = %+v, want none", got)
	}
	if n := i.createCount(); n != 1 {
		t.Errorf("03-AC-12: create counter = %d, want it unchanged at 1", n)
	}
	if c, ok := pvRead(t, i).confirmation("fence-nodes"); ok {
		t.Errorf("03-AC-12: a fence-nodes confirmation was recorded: %+v", c)
	}
}

// 03-AC-12: a typed count that does not match the number of unreachable nodes is a mismatch (exit 5) and fences nothing.
func TestRecoverFencing_WrongCountExits5_03AC12(t *testing.T) {
	t.Parallel()
	i := fenceInstalled(t)
	code, out := i.recoverRun(clusterName+" 2", nil)
	requireExit(t, code, 5, out)
	if got := fenceFacts(t, i).Facts.FencedNodes; len(got) != 0 {
		t.Errorf("03-AC-12: facts.fenced_nodes = %+v after a wrong count, want none", got)
	}
	if n := i.createCount(); n != 1 {
		t.Errorf("03-AC-12: create counter = %d, want 1", n)
	}
}

// 03-AC-12: with `fence-nodes <cluster> 1` the journal lists node-01 in facts.fenced_nodes, the compute plan creates
// node-02 (a new name) in fake-2, the guard passes (no GUARD_ code, the footprint is printed) and the run stops
// at the plan confirmation; with the right D8 it ends successfully. Nothing is applied.
func TestRecoverFencing_ConfirmedFencesAndPlansReplacement_03AC12(t *testing.T) {
	t.Parallel()
	i := fenceInstalled(t)
	code, out := i.recoverRun(clusterName+" 1", nil)
	requireExit(t, code, 5, out) // the plan itself is not confirmed yet
	if !strings.Contains(out, "PLAN_CONFIRMATION_MISSING") {
		t.Errorf("03-AC-12: the run did not reach the plan confirmation:\n%s", out)
	}
	if strings.Contains(out, "GUARD_") {
		t.Errorf("03-AC-12: guard verdict is not pass:\n%s", out)
	}
	if !regexp.MustCompile(fenceCreateNode02).MatchString(out) {
		t.Errorf("03-AC-12: the footprint does not list CREATE node-02:\n%s", out)
	}
	if regexp.MustCompile(`(?m)^\s+CREATE\s+node-01\b`).MatchString(out) {
		t.Errorf("03-AC-12: the plan reuses the name node-01:\n%s", out)
	}
	if !regexp.MustCompile(`(?m)^(PAID )?FOOTPRINT.*\(fake, region fake-2\)`).MatchString(out) {
		t.Errorf("03-AC-12: the footprint title does not name the region fake-2:\n%s", out)
	}
	fenced := fenceFacts(t, i).Facts.FencedNodes
	if len(fenced) != 1 {
		t.Fatalf("03-AC-12: facts.fenced_nodes = %+v, want exactly node-01", fenced)
	}
	f := fenced[0]
	if f.Address != `fake_node.node["node-01"]` || f.ProviderID == "" {
		t.Errorf("03-AC-12: fenced node = %+v, want address fake_node.node[\"node-01\"] and a provider_id", f)
	}
	parseTime(t, f.FencedAt)
	if c, ok := pvRead(t, i).confirmation("fence-nodes"); !ok || c.Method != "flag" {
		t.Errorf("03-AC-12: fence-nodes confirmation = %+v (found %v), want method flag", c, ok)
	}
	if n := i.createCount(); n != 1 {
		t.Errorf("03-AC-12: create counter = %d, want 1 (the plan applies nothing)", n)
	}
	// node-01 left the fake's state: its inventory no longer lists it.
	p, err := fake.New(map[string]string{"SC_TEST_MODE": "1"}, i.fakeProviderDir())
	if err != nil {
		t.Fatal(err)
	}
	inv, err := p.Inventory(context.Background())
	if err != nil {
		t.Fatalf("Inventory after fencing: %v", err)
	}
	for _, n := range inv.Nodes {
		if n.Name == "node-01" {
			t.Errorf("03-AC-12: node-01 is still in the fake state after fencing")
		}
	}

	m := s5Digest.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("03-AC-12: no plan digest printed:\n%s", out)
	}
	code, out = i.recoverRun(clusterName+" 1", nil, "--confirm-plan", m[1][:8])
	requireExit(t, code, 0, out)
}
