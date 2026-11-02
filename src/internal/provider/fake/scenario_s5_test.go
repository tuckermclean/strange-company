package fake_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"strange.company/sc/internal/provider"
)

func s5Scenario(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "test", "fixtures", "fake-provider", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 03-AC-8, 03-AC-10, 03-AC-11 (plan 03 section 22): the scenario knobs `capacity` (cpu, memoryBytes, diskBytes,
// diskClass) and `paid` + `machineClass` are part of the strictly decoded FakeProviderScenario: the fixtures that carry
// them are not SCENARIO_INVALID.
func TestScenario_CapacityAndPaidKnobsAreAccepted_03AC8(t *testing.T) {
	for _, name := range []string{"capacity-below-minimum", "capacity-tolerance", "capacity-small-disk", "paid"} {
		t.Run(name, func(t *testing.T) {
			p := scenarioProvider(t, s5Scenario(t, name))
			if _, err := p.Plan(context.Background(), provider.Desired{Cluster: "lab", NodeCount: 1}); err != nil {
				t.Errorf("Plan with the scenario %s: %v, want the scenario accepted", name, err)
			}
		})
	}
}

// 03-AC-11: Capabilities().paid follows the scenario (the lab refusal reads it); without the knob the fake is unpaid.
func TestCapabilities_PaidFollowsTheScenario_03AC11(t *testing.T) {
	if got := scenarioProvider(t, "discovery:\n  resolvers: [10.0.0.1]\n").Capabilities(); got.Paid || !got.CreatesNodes || got.Name != "fake" {
		t.Errorf("Capabilities() without the paid knob = %+v, want fake, unpaid, creating nodes", got)
	}
	if got := scenarioProvider(t, s5Scenario(t, "paid")).Capabilities(); !got.Paid || !got.CreatesNodes || got.Name != "fake" {
		t.Errorf("Capabilities() with paid: true = %+v, want fake, paid, creating nodes", got)
	}
}
