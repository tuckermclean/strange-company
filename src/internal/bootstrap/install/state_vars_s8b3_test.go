package install

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/provider"
	"strange.company/sc/internal/provider/fake"
)

// S7 review N5 (F08 S8b3): the compute module of the production state wiring gets TF_VAR_nodes, the node name to internal
// IP map of the provider inventory, so PlanCopy compares the state copy with the nodes that exist instead of counting a
// destroy of every node. The test is in the install package because sess.stateVars is unexported.
func TestStateVars_ComputeCarriesNodesOfTheInventory_S8b3N5(t *testing.T) {
	t.Parallel()
	env := map[string]string{"SC_TEST_MODE": "1"}
	p, err := fake.New(env, filepath.Join(t.TempDir(), "fake-provider"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	plan, err := p.Plan(ctx, provider.Desired{Cluster: "lab-one", NodeCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	s := &sess{cluster: "lab-one", env: env, prov: p}
	var nodes string
	for _, v := range s.stateVars("compute") {
		if strings.HasPrefix(v, "TF_VAR_nodes=") {
			nodes = strings.TrimPrefix(v, "TF_VAR_nodes=")
		}
	}
	if nodes == "" {
		t.Fatalf("stateVars(compute) = %v: no TF_VAR_nodes", s.stateVars("compute"))
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(nodes), &got); err != nil {
		t.Fatalf("TF_VAR_nodes is not a JSON object: %v: %s", err, nodes)
	}
	want := map[string]string{"node-01": "10.99.0.11", "node-02": "10.99.0.12"}
	if len(got) != len(want) || got["node-01"] != want["node-01"] || got["node-02"] != want["node-02"] {
		t.Errorf("TF_VAR_nodes = %v, want %v", got, want)
	}
}
