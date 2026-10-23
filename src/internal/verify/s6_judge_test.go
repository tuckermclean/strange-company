package verify

import (
	"reflect"
	"testing"
)

// Slice S6, 22-AC-3: one or more fixtures per judge rule (plan 22 section 14.8). Fixtures live under
// testdata/judge/<rule>/<case>/{overlay.json,expect.json[,judge.yaml.fx]} and are overlaid on the valid baseline of
// s6BaseFixture (s6_helpers_test.go); each test names its directories, none walks testdata/judge as a whole.
// Adversarial twins: a verdict document of the wrong phase or subject, an integrity fact of another judge commit, a
// characterization run on another commit, a check missing from the observation, a judge of another class, task or
// kind, an unparsable artifact, a producer outside its class paths, and the passing twin of each.

// Judge validation: the artifact is well-formed and belongs to the task, every frozen criterion has a check, every
// check ran, and the answer does not exist yet (judge_valid), else judge_invalid / already_satisfied /
// partially_satisfied.
func TestClassifyJudge_Validation_22AC3(t *testing.T) {
	s6RunRules(t, "validation", "schema")
}

// judge_check_error, ac_not_covered and judge_passes_on_base (already_satisfied, partially_satisfied).
func TestClassifyJudge_CheckErrorsCoverageAndSatisfied_22AC3(t *testing.T) {
	s6RunRules(t, "judge_check_error", "ac_not_covered", "judge_passes_on_base")
}

// Characterization judges (refactor class, TDD-B9): checks must pass at the judge commit and at base, there is no
// "must fail" rule, judge_passes_on_base is not an outcome, and the producer diff may not touch test-classified paths.
func TestClassifyJudge_Characterization_22AC3(t *testing.T) {
	s6RunRules(t, "characterization")
}

// Judging: the manifest is intact, the producer diff is within the class paths, every judge check passes and the
// required checks pass (judging_pass), else judging_fail; a changed judge artifact is tamper_detected.
func TestClassifyJudging_Rules_22AC3(t *testing.T) {
	s6RunRules(t, "judging", "judge_check_failed", "judge_artifact_changed")
}

// Rubric judges: the research class is judged on its report (non-empty, every AC id by heading).
func TestClassifyJudging_Rubric_22AC3(t *testing.T) {
	s6RunRules(t, "rubric")
}

// 22-R-3 / plan 22 section 16: the classifications are pure; the inputs are not modified.
func TestClassifyJudge_DoesNotModifyItsInput_22AC3(t *testing.T) {
	fx := s6BaseFixture(t)
	want, _ := s6ValidateInput(t, fx)
	in, _ := s6ValidateInput(t, fx)
	if _, err := ClassifyJudge(in); err != nil {
		t.Fatalf("ClassifyJudge: %v", err)
	}
	if !reflect.DeepEqual(in, want) {
		t.Errorf("ClassifyJudge modified its input")
	}
	cl, err := ClassifyJudge(in)
	if err != nil || cl.Outcome != "judge_valid" {
		t.Errorf("second classification = %s, %v, want judge_valid", jsonOf(cl), err)
	}
}

func TestClassifyJudging_DoesNotModifyItsInput_22AC3(t *testing.T) {
	fx := s6BaseFixture(t)
	want := s6JudgingInput(t, fx)
	in := s6JudgingInput(t, fx)
	if _, err := ClassifyJudging(in); err != nil {
		t.Fatalf("ClassifyJudging: %v", err)
	}
	if !reflect.DeepEqual(in, want) {
		t.Errorf("ClassifyJudging modified its input")
	}
	cl, err := ClassifyJudging(in)
	if err != nil || cl.Outcome != "judging_pass" {
		t.Errorf("second classification = %s, %v, want judging_pass", jsonOf(cl), err)
	}
}

// 22-R-3: missing exemption decision, a decision for a class the platform does not define, a decision that
// contradicts itself and an artifact class without manifest all fail closed (an error, never a pass).
func TestClassifyJudging_UnusableFacts_FailClosed_22AC3(t *testing.T) {
	fx := s6BaseFixture(t)
	bad := map[string]func(in *JudgingInput){
		"no_exemption":           func(in *JudgingInput) { in.Exemption = nil },
		"unknown_class":          func(in *JudgingInput) { in.Exemption = s4Producer("bogus", "auto") },
		"auto_for_a_human_class": func(in *JudgingInput) { in.Exemption = s4Producer("refactor", "auto") },
		"human_for_an_auto_class": func(in *JudgingInput) {
			in.Exemption = &ExemptionDecision{Class: "documentation-only", Status: "granted", Approval: "human", PolicyRevision: s4Revision}
		},
		"no_manifest":               func(in *JudgingInput) { in.Manifest = nil },
		"no_head_run":               func(in *JudgingInput) { in.Observed.Runs = nil },
		"no_policy_for_the_class":   func(in *JudgingInput) { in.Platform.Verification = map[string]any{} },
		"head_sha_is_not_a_commit":  func(in *JudgingInput) { in.Observed.HeadSha = "HEAD" },
		"manifest_for_another_repo": func(in *JudgingInput) { in.Manifest.Repository = "o/other" },
	}
	for name, mutate := range bad {
		t.Run(name, func(t *testing.T) {
			in := s6JudgingInput(t, fx)
			if in.Manifest != nil {
				m := *in.Manifest
				in.Manifest = &m
			}
			mutate(&in)
			got, err := ClassifyJudging(in)
			if err == nil {
				t.Errorf("classified %s without an error", jsonOf(got))
			}
		})
	}
}

// Plan 22 section 14.8 (TDD-B9) and section 9.2: "the producer diff must not touch any test-classified path" and
// "within class paths" are consumed from CheckScope (D21), so ClassifyJudging and CheckScope agree on every class
// and path: judging_pass exactly when CheckScope reports no violation for the producer diff. Real Git diffs.
func TestClassifyJudging_ProducerScopeAgreesWithCheckScope_22AC3(t *testing.T) {
	pathSets := map[string][]string{
		"docs":          {"docs/a.md"},
		"source":        {"src/a.go"},
		"test_file":     {"src/a_test.go"},
		"testdata":      {"src/testdata/f.txt"},
		"test_infra":    {"src/internal/testutil/zz.go"},
		"protected":     {".woodpecker/x.yaml"},
		"chart_values":  {"charts/x/values.yaml"},
		"plain_yaml":    {"config/a.yaml"},
		"mixed_with_ok": {"docs/a.md", "src/b_test.go"},
	}
	diffs := map[string][]DiffEntry{}
	for name, paths := range pathSets {
		diffs[name] = s4Diff(t, nil, s4Add(paths...))
	}
	classes := map[string]string{
		"documentation-only": "auto", "metadata": "auto", "infrastructure-config": "human", "migration": "human", "benchmark": "human", "refactor": "human",
	}
	flagged := 0
	for class, approval := range classes {
		for name, entries := range diffs {
			t.Run(class+"/"+name, func(t *testing.T) {
				in := s6JudgingInput(t, s6BaseFixture(t))
				in.Exemption, in.Diff = s4Producer(class, approval), entries
				want, err := CheckScope(ScopeInput{Role: "producer", Entries: entries, Exemption: in.Exemption, Platform: in.Platform, Effective: in.Effective})
				if err != nil {
					t.Fatalf("CheckScope: %v", err)
				}
				got, err := ClassifyJudging(in)
				if err != nil {
					t.Fatalf("ClassifyJudging: %v", err)
				}
				wantOutcome := "judging_pass"
				if len(want) > 0 {
					wantOutcome = "judging_fail"
					flagged++
				}
				if got.Outcome != wantOutcome {
					t.Errorf("outcome = %q, want %q (CheckScope: %d violations; reasons %v)", got.Outcome, wantOutcome, len(want), got.Reasons)
				}
			})
		}
	}
	if flagged < 20 {
		t.Fatalf("only %d of the combinations are flagged by CheckScope; the table does not exercise the scope rule", flagged)
	}
}
