package store

import "testing"

// Every artifact type this package declares must be storable.
//
// The two lists this guards used to be a constant block and a hand-written map
// that had to agree. They stopped agreeing: run-log arrived in 0.12.0 and
// model-exchange in 0.15.1, neither reached the map, and every write of either
// was rejected. The steps treat a failed artifact write as non-fatal -- rightly,
// a card is worth more than a row of evidence -- so nothing surfaced. Two
// shipped features had never stored a single byte.
//
// This test is the second pair of eyes: a new constant that is not in
// AllArtifactTypes fails here rather than in production, silently, months later.
func TestEveryDeclaredArtifactTypeIsStorable(t *testing.T) {
	declared := []string{
		ArtifactSpec,
		ArtifactAmbiguityReport,
		ArtifactImplementationPlan,
		ArtifactTestMapping,
		ArtifactTestOutput,
		ArtifactRunLog,
		ArtifactModelExchange,
		ArtifactDiff,
		ArtifactCompilerOutput,
		ArtifactLinterOutput,
		ArtifactSecurityOutput,
		ArtifactReview,
		ArtifactCostReport,
		ArtifactFailureSummary,
		ArtifactHumanDecision,
	}

	for _, tp := range declared {
		if !artifactTypes[tp] {
			t.Errorf("artifact type %q is declared but cannot be stored; add it to AllArtifactTypes", tp)
		}
	}

	if len(declared) != len(AllArtifactTypes) {
		t.Errorf("AllArtifactTypes has %d entries and this test knows %d; one of them is missing a type",
			len(AllArtifactTypes), len(declared))
	}
}

// The two types whose absence went unnoticed for four releases, named so a
// future edit cannot quietly drop them again.
func TestTheTranscriptTypesAreStorable(t *testing.T) {
	for _, tp := range []string{ArtifactRunLog, ArtifactModelExchange} {
		if !artifactTypes[tp] {
			t.Errorf("%q cannot be stored; the raw discourse this system promises has nowhere to go", tp)
		}
	}
}
