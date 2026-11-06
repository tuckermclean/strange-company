package fake_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"strange.company/sc/internal/provider"
	"strange.company/sc/internal/provider/fake"
)

func scenarioProvider(t *testing.T, scenario string) *fake.Provider {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fake-provider")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(scenario), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := fake.New(map[string]string{"SC_TEST_MODE": "1"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func wantCode(t *testing.T, op string, err error, code string) {
	t.Helper()
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Code != code {
		t.Errorf("%s error = %v, want a provider error with code %s", op, err, code)
	}
}

// 03-AC-5, 03-AC-7 (plan 03 section 22): a scenario file is parsed strictly. A misspelled key is SCENARIO_INVALID, not
// a silently ignored setting (a crashAfterorder typo would turn the orphan scenario into a plain run).
func TestScenario_UnknownKeyIsInvalid_03AC7(t *testing.T) {
	for _, c := range []struct{ name, yaml string }{
		{"misspelled orphan key", "orphan:\n  crashAfterorder: true\n"},
		{"unknown top-level key", "orfan:\n  crashAfterOrder: true\n"},
		{"unknown plan key", "plan:\n  extraResource: []\n"},
		{"unknown discovery key", "discovery:\n  resolver: [10.0.0.1]\n"},
		{"unknown key in an extra resource", "plan:\n  extraResources:\n    - type: x\n      name: y\n      actions: [create]\n      action: [create]\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			p := scenarioProvider(t, c.yaml)
			_, err := p.Plan(ctx, provider.Desired{Cluster: "lab", NodeCount: 1})
			wantCode(t, "Plan", err, "SCENARIO_INVALID")
			err = p.Apply(ctx, &provider.ProviderPlan{Nodes: []provider.Node{{Name: "node-01"}}})
			wantCode(t, "Apply", err, "SCENARIO_INVALID")
			_, err = p.Discover(ctx)
			wantCode(t, "Discover", err, "SCENARIO_INVALID")
		})
	}
}

// 03-AC-5: Apply with an invalid scenario creates nothing: the scenario is read before the counter is bumped.
func TestScenario_InvalidScenarioApplyCreatesNothing_03AC5(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fake-provider")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte("orphan:\n  crashAfterorder: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := fake.New(map[string]string{"SC_TEST_MODE": "1"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = p.Apply(context.Background(), &provider.ProviderPlan{Nodes: []provider.Node{{Name: "node-01"}}})
	if n, err := fake.CreateCount(dir); err != nil || n != 0 {
		t.Errorf("create counter after Apply with an invalid scenario = %d, %v; want 0", n, err)
	}
}

// 03-AC-5, 03-AC-7: Discover and Plan read one scenario document (one shared struct). A scenario that sets
// discovery.resolvers and plan.extraResources works for both: Discover reports the resolvers, Plan applies the extra
// resource and the guard refuses it. (A strict decoder over a per-method struct would reject the other's keys.)
func TestScenario_OneDocumentServesDiscoverAndPlan_03AC5(t *testing.T) {
	p := scenarioProvider(t, `discovery:
  resolvers: [192.0.2.53, 192.0.2.54]
plan:
  extraResources:
    - type: fake_unlisted_resource
      name: rogue
      actions: [create]
orphan:
  crashAfterOrder: false
`)
	ctx := context.Background()
	d, err := p.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(d.Resolvers) != 2 || d.Resolvers[0] != "192.0.2.53" || d.Resolvers[1] != "192.0.2.54" {
		t.Errorf("Discover resolvers = %v, want [192.0.2.53 192.0.2.54]", d.Resolvers)
	}
	_, err = p.Plan(ctx, provider.Desired{Cluster: "lab", NodeCount: 1})
	wantCode(t, "Plan", err, "GUARD_UNEXPECTED_RESOURCE")
}
