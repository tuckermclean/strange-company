package fake_test

import (
	"context"
	"testing"

	"strange.company/sc/internal/provider"
)

// 03-AC-5, 03-AC-7: a scenario file is one document of kind FakeProviderScenario; a second document, another kind or
// another apiVersion is SCENARIO_INVALID, not silently ignored.
func TestScenario_DocumentIdentityIsStrict_03AC7(t *testing.T) {
	for _, c := range []struct{ name, yaml string }{
		{"second document", "discovery:\n  resolvers: [10.0.0.1]\n---\norphan:\n  crashAfterOrder: true\n"},
		{"wrong kind", "kind: SomethingElse\n"},
		{"wrong apiVersion", "apiVersion: strange.company/v2\n"},
		{"right kind, wrong apiVersion", "apiVersion: example.com/v1\nkind: FakeProviderScenario\n"},
		{"wrong kind, right apiVersion", "apiVersion: strange.company/v1alpha1\nkind: Other\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			p := scenarioProvider(t, c.yaml)
			_, err := p.Plan(ctx, provider.Desired{Cluster: "lab", NodeCount: 1})
			wantCode(t, "Plan", err, "SCENARIO_INVALID")
			_, err = p.Discover(ctx)
			wantCode(t, "Discover", err, "SCENARIO_INVALID")
		})
	}
}

// 03-AC-5, 03-AC-7: the identity keys are optional, but when right they are accepted.
func TestScenario_CorrectIdentityIsAccepted_03AC7(t *testing.T) {
	p := scenarioProvider(t, "apiVersion: strange.company/v1alpha1\nkind: FakeProviderScenario\ndiscovery:\n  resolvers: [10.0.0.1]\n")
	if _, err := p.Discover(context.Background()); err != nil {
		t.Errorf("Discover with a correct scenario identity: %v", err)
	}
}
