package verify

import (
	"testing"
)

// ClassifyGreen (plan 22 section 14.7). Each test names the rule directories it iterates under testdata/green; no
// test walks testdata/green as a whole. Inputs outside the contracts are error cases only (no classified document is
// produced for them, so no schema assertion applies); every other fixture is validated against green-result.schema.

// TestGreenBaseline_InputsAreValid_22AC3 proves the baseline every fixture overlays is valid under the contracts it
// claims to follow: the manifest against the manifest validator, the classified document against green-result.
func TestGreenBaseline_InputsAreValid_22AC3(t *testing.T) {
	fx := g5BaseFixture(t)
	if err := validateManifestSchema(fx.Manifest); err != nil {
		t.Errorf("baseline manifest: %v", err)
	}
	in := g5InputOf(t, fx)
	cl := GreenClassification{Outcome: "green_pass", Reasons: []string{}, ComputedBy: "runner", ComputedAt: in.ComputedAt, PolicyRevision: in.PolicyRevision, VerifierLibVersion: "0.1.0"}
	if err := g5Validate(t, in.Observed, in.Integrity, cl); err != nil {
		t.Errorf("baseline green-result does not validate: %v", err)
	}
}

// 22-AC-3 (fixture per G-rule; S6 adds completeness and 100x determinism over these).
func TestClassifyGreen_Identity_G0_22AC3(t *testing.T) { g5RunRule(t, "ok", "G0") }

func TestClassifyGreen_Descent_G1_22AC3(t *testing.T) { g5RunRule(t, "G1") }

// 22-AC-27 (G2 foreign commits) and the manifest, discovery and static findings of section 14.6.
func TestClassifyGreen_Integrity_G2_22AC27(t *testing.T) { g5RunRule(t, "G2") }

func TestClassifyGreen_Suites_G3_22AC3(t *testing.T) { g5RunRule(t, "G3") }

func TestClassifyGreen_ProtectedTests_G4_22AC3(t *testing.T) { g5RunRule(t, "G4", "G4c") }

// 22-AC-23 (TDD-2): the isolation run.
func TestClassifyGreen_IsolationRun_G4b_22AC23(t *testing.T) { g5RunRule(t, "G4b") }

func TestClassifyGreen_OtherTests_G5_22AC3(t *testing.T) { g5RunRule(t, "G5") }

// 22-AC-25 (TDD-4): full-suite comparison.
func TestClassifyGreen_FullSuiteComparison_G5b_22AC25(t *testing.T) { g5RunRule(t, "G5b") }

func TestClassifyGreen_ChecksStubsTruncation_G6G7G8_22AC3(t *testing.T) {
	g5RunRule(t, "G6", "G7", "G8")
}

// 22-AC-24 (GREEN part): RED replay.
func TestClassifyGreen_RedReplay_G9_22AC24(t *testing.T) { g5RunRule(t, "G9") }

// Rules run in table order; every failing rule contributes its reasons; the first failing rule decides the outcome.
func TestClassifyGreen_RuleOrderAndAccumulation_22AC3(t *testing.T) {
	fx := g5BaseFixture(t)
	fx.Integrity.HeadDescendsFromRed = false
	fx.Integrity.StubsRemaining = []string{"pkg/impl.go: F"}
	fx.Observed.Checks = []CheckRun{{Name: "lint", ExitCode: 1, DurationMs: 1, Required: true}}
	in := g5InputOf(t, fx)
	got, err := ClassifyGreen(in)
	if err != nil {
		t.Fatalf("ClassifyGreen: %v", err)
	}
	if got.Outcome != "tamper_detected" {
		t.Errorf("outcome = %q, want tamper_detected (G1 fails first)", got.Outcome)
	}
	want := []string{"not_descendant_of_red", "required_check_failed", "stub_not_replaced"}
	if jsonOf(got.Reasons) != jsonOf(want) {
		t.Errorf("reasons = %v, want %v (rule order G1, G6, G7)", got.Reasons, want)
	}
}

// The classification is a pure function of its inputs: repeated calls give identical bytes, and the input is not
// modified.
func TestClassifyGreen_PureAndDeterministic_22AC3(t *testing.T) {
	fx := g5BaseFixture(t)
	fx.Integrity.ForeignCommits = []string{"1111111111111111111111111111111111111111"}
	fx.Observed.Checks = []CheckRun{{Name: "lint", ExitCode: 1, DurationMs: 1, Required: true}}
	in := g5InputOf(t, fx)
	before := jsonOf(in)
	var first string
	for i := 0; i < 20; i++ {
		got, err := ClassifyGreen(in)
		if err != nil {
			t.Fatalf("ClassifyGreen: %v", err)
		}
		if got.Outcome != "tamper_detected" {
			t.Fatalf("outcome = %q, want tamper_detected", got.Outcome)
		}
		if i == 0 {
			first = jsonOf(got)
		} else if jsonOf(got) != first {
			t.Fatalf("run %d: %s differs from %s", i, jsonOf(got), first)
		}
	}
	if jsonOf(in) != before {
		t.Error("ClassifyGreen modified its input")
	}
}
