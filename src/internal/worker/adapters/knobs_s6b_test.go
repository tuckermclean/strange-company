package adapters_test

import (
	"testing"

	"strange.company/sc/internal/worker/adapters"
)

// S6b of F07, 28-R-5 (guard tests, they pass already): the four hostile knobs that sc-worker acts on are derived only
// for adapter fake, and every non-fake adapter of the packet schema derives none of them.
func TestFakeKnobs_HostileKnobsOnlyForAdapterFake_S6bN7(t *testing.T) {
	want := map[string]adapters.FakeKnobs{
		"multi-commit":       {SkipFold: true},
		"push-other-branch":  {PushExtraRefs: true},
		"not-descendant":     {SkipPrechecks: true, ParentOverride: "base"},
		"wrong-commit-claim": {ClaimCommitOverride: "start"},
	}
	for scenario, w := range want {
		t.Run("fake/"+scenario, func(t *testing.T) {
			got, err := adapters.DeriveFakeKnobs(withFakeScenario(t, "fake", scenario))
			if err != nil {
				t.Fatal(err)
			}
			if got != w {
				t.Errorf("knobs = %+v, want %+v", got, w)
			}
		})
		for _, adapter := range []string{"claude-code", "codex", "opencode"} {
			t.Run(adapter+"/"+scenario, func(t *testing.T) {
				got, err := adapters.DeriveFakeKnobs(withFakeScenario(t, adapter, scenario))
				if err != nil {
					t.Fatal(err)
				}
				if got != (adapters.FakeKnobs{}) {
					t.Errorf("knobs = %+v for adapter %s, want all off (28-R-5)", got, adapter)
				}
			})
		}
	}
}
