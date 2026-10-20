package verify

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/policy"
)

// ClassifyRed fixtures (plan 22 section 14.3). Every case directory testdata/red/<group>/<case> holds
// input.json (the observed document, the Runner's Git facts, the declared mapping, the frozen criteria and the
// policy knobs) and expect.json (the golden outcome and reasons). The id sets are plain lists in input.json; the
// test encodes them as $defs/TestIdSet (gzip, base64), so the library has to decode them. Stub facts that the
// library can derive come from real sources: `stubs_from` names a testdata/stubs case that AnalyzeStubs reads.
// Each test names the directories it iterates; no test walks testdata/red as a whole.

type redFixture struct {
	Identity  Identity            `json:"identity"`
	Suites    map[string]string   `json:"suites"`
	HeadScope map[string][]string `json:"head_scope"`
	Criteria  []Criterion         `json:"criteria"`
	Declared  map[string][]string `json:"declared"`
	IDSets    struct {
		BasePassing []string `json:"base_passing"`
		BaseSkipped []string `json:"base_skipped"`
		HeadPresent []string `json:"head_present"`
	} `json:"id_sets"`
	Observed  Observation `json:"observed"`
	Scope     Scope       `json:"scope"`
	StubsFrom *struct {
		Case     string `json:"case"`
		Language string `json:"language"`
	} `json:"stubs_from"`
	Effective map[string]any `json:"effective"`
}

type redExpect struct {
	Error       string          `json:"error"`
	Outcome     string          `json:"outcome"`
	Decisive    string          `json:"decisive"`
	Reasons     *[]string       `json:"reasons"`
	Contains    []string        `json:"contains"`
	NewTests    *[]string       `json:"new_tests"`
	ACResults   *[]ACResult     `json:"ac_results"`
	SchemaValid bool            `json:"schema_valid"`
}

func loadRedFixture(t testing.TB, caseDir string) (redFixture, redExpect) {
	t.Helper()
	var fx redFixture
	var exp redExpect
	dir := filepath.Join("testdata", "red", filepath.FromSlash(caseDir))
	readJSONFile(t, &fx, dir, "input.json")
	readJSONFile(t, &exp, dir, "expect.json")
	return fx, exp
}

// redInputOf builds the library input of a fixture: id sets encoded, stub facts derived from source.
func redInputOf(t testing.TB, fx redFixture) RedInput {
	t.Helper()
	obs := fx.Observed
	obs.TestIDSets = &TestIDSets{
		BasePassing: encodeIDSet(t, fx.IDSets.BasePassing),
		BaseSkipped: encodeIDSet(t, fx.IDSets.BaseSkipped),
		HeadPresent: encodeIDSet(t, fx.IDSets.HeadPresent),
	}
	sc := fx.Scope
	if fx.StubsFrom != nil {
		sp, err := LoadStubPolicy(platformPolicy(t).Verification)
		if err != nil {
			t.Fatalf("LoadStubPolicy: %v", err)
		}
		facts, err := AnalyzeStubs(sp, stubFileOf(t, filepath.Join("testdata", "stubs", fx.StubsFrom.Case)))
		if err != nil {
			t.Fatalf("AnalyzeStubs(%s): %v", fx.StubsFrom.Case, err)
		}
		if len(facts) == 0 {
			t.Fatalf("AnalyzeStubs(%s) derived no stub fact from a stub fixture", fx.StubsFrom.Case)
		}
		sc.Stubs = facts
	}
	if sc.Stubs == nil {
		sc.Stubs = []StubFact{}
	}
	return RedInput{
		Observed: obs, Scope: sc, Identity: fx.Identity, Declared: fx.Declared, Criteria: fx.Criteria,
		SuiteLanguages: fx.Suites, HeadScope: fx.HeadScope, Platform: platformPolicy(t),
		Effective:  policy.Effective{Values: fx.Effective},
		ComputedAt: "2026-10-13T12:20:00Z", PolicyRevision: "sha256:" + strings.Repeat("a", 64),
	}
}

// checkRed compares a classification with the golden expectation.
func checkRed(t *testing.T, in RedInput, got RedClassification, err error, exp redExpect) {
	t.Helper()
	if exp.Error != "" {
		var nc *NotClassifiedError
		if !errors.As(err, &nc) {
			t.Fatalf("err = %v (classification %s), want a *NotClassifiedError", err, jsonOf(got))
		}
		if nc.Code != exp.Error {
			t.Errorf("error code = %q, want %q", nc.Code, exp.Error)
		}
		return
	}
	if err != nil {
		t.Fatalf("ClassifyRed: %v", err)
	}
	if got.Outcome != exp.Outcome {
		t.Errorf("outcome = %q, want %q (reasons %v)", got.Outcome, exp.Outcome, got.Reasons)
	}
	if exp.Reasons != nil && jsonOf(got.Reasons) != jsonOf(*exp.Reasons) {
		t.Errorf("reasons = %s, want %s", jsonOf(got.Reasons), jsonOf(*exp.Reasons))
	}
	if exp.Decisive != "" && (len(got.Reasons) == 0 || got.Reasons[0] != exp.Decisive) {
		t.Errorf("first reason = %v, want %q first", got.Reasons, exp.Decisive)
	}
	for _, c := range exp.Contains {
		found := false
		for _, r := range got.Reasons {
			found = found || r == c
		}
		if !found {
			t.Errorf("reasons %v lack %q", got.Reasons, c)
		}
	}
	if exp.NewTests != nil && jsonOf(got.NewTests) != jsonOf(*exp.NewTests) {
		t.Errorf("new_tests = %s, want %s", jsonOf(got.NewTests), jsonOf(*exp.NewTests))
	}
	if exp.ACResults != nil && jsonOf(got.ACResults) != jsonOf(*exp.ACResults) {
		t.Errorf("ac_results = %s, want %s", jsonOf(got.ACResults), jsonOf(*exp.ACResults))
	}
	if got.ComputedBy != "runner" || got.ComputedAt != in.ComputedAt || got.PolicyRevision != in.PolicyRevision || got.VerifierLibVersion == "" {
		t.Errorf("provenance fields = %+v", got)
	}
	if exp.SchemaValid {
		if verr := validateRedResult(t, in.Observed, in.Scope, got); verr != nil {
			t.Errorf("classified red-result does not validate against red-result.schema.yaml: %v", verr)
		}
	}
}

func runRedCases(t *testing.T, cases []string) {
	t.Helper()
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c, "/", "_"), func(t *testing.T) {
			fx, exp := loadRedFixture(t, c)
			in := redInputOf(t, fx)
			got, err := ClassifyRed(in)
			checkRed(t, in, got, err, exp)
		})
	}
}

// 22-AC-4: a Go test that only fails to compile (no stub) is red_invalid with build_failed; the same test with a
// zero-value SC-STUB declaration (derived from the Go source by the AST) is red_valid; a panic stub in Go is
// red_invalid (panic_stub_not_allowed); in Rust it is red_valid; a repository that removes rust from
// stubPanicLanguages makes the Rust panic stub invalid again.
func TestClassifyRed_StubFixtures_22AC4(t *testing.T) {
	runRedCases(t, []string{
		"R3/head_build_failed", "valid/go_zero_value_stub", "R1/go_panic_stub", "valid/rust_panic_stub",
		"R1/rust_panic_repo_forbids",
	})
}

// 22-AC-4: the compile-only RED derived end to end from a recorded `go test -json` report: the adapter reports
// a build failure, and ClassifyRed turns it into red_invalid/build_failed. The same report shape with a
// zero-value stub in place (the test then compiles and fails by assertion) is red_valid.
func TestClassifyRed_CompileOnlyFromReport_22AC4(t *testing.T) {
	fx, _ := loadRedFixture(t, "valid/go_zero_value_stub")
	dir := filepath.Join("testdata", "adapter")

	// Without a stub: the report is the recorded build failure.
	noStub := fx
	noStub.StubsFrom = nil
	noStub.Scope.ChangedFiles = noStub.Scope.ChangedFiles[:1]
	rep, err := NormalizeReport(loadAdapter(t, "go-test-json"), "unit", mustRead(t, dir, "go_build_failed", "report.json"))
	if err != nil {
		t.Fatalf("NormalizeReport: %v", err)
	}
	if !rep.BuildFailed {
		t.Fatal("the recorded compile failure is not reported as BuildFailed")
	}
	applyReport(&noStub, rep)
	in := redInputOf(t, noStub)
	got, err := ClassifyRed(in)
	if err != nil {
		t.Fatalf("ClassifyRed: %v", err)
	}
	if got.Outcome != "red_invalid" || len(got.Reasons) == 0 || got.Reasons[0] != "build_failed" {
		t.Errorf("compile-only RED = %s %v, want red_invalid with build_failed first", got.Outcome, got.Reasons)
	}

	// With the zero-value stub from source: the report is the recorded assertion failure.
	rep, err = NormalizeReport(loadAdapter(t, "go-test-json"), "unit", mustRead(t, dir, "go_mixed", "report.json"))
	if err != nil {
		t.Fatalf("NormalizeReport: %v", err)
	}
	withStub := fx
	withStub.Declared = map[string][]string{"AC-1": {"unit::example.com/x/p.TestA"}}
	withStub.IDSets.BasePassing = []string{}
	applyReport(&withStub, rep)
	in = redInputOf(t, withStub)
	got, err = ClassifyRed(in)
	checkRed(t, in, got, err, redExpect{Outcome: "red_valid", Reasons: &[]string{}, SchemaValid: true})
}

// applyReport replaces the head observations of a fixture by a normalized report.
func applyReport(fx *redFixture, rep Report) {
	for i := range fx.Observed.Runs {
		if fx.Observed.Runs[i].Tree == "head" {
			fx.Observed.Runs[i].BuildFailed = rep.BuildFailed
			fx.Observed.Runs[i].TimedOut = rep.TimedOut
			fx.Observed.Runs[i].ReportFound = true
		}
	}
	fx.Observed.Tests = nil
	fx.IDSets.HeadPresent = nil
	for _, r := range rep.Tests {
		fx.Observed.Tests = append(fx.Observed.Tests, TestObservation{ID: r.ID, Suite: "unit", File: r.File, StatusBase: "absent", StatusHead: r.Status, FailureType: r.FailureType, Message: r.Message})
		fx.IDSets.HeadPresent = append(fx.IDSets.HeadPresent, r.ID)
	}
	if rep.BuildFailed {
		fx.Observed.Tests = []TestObservation{}
	}
}

// 22-AC-5: a new test raising ImportError (pytest) or ReferenceError (jest) is red_invalid/error_class_failure,
// and a failure type matching neither adapter list is failed_error and therefore error-class too (fail closed).
// The statuses come from the recorded adapter reports, not from the fixture.
func TestClassifyRed_ErrorClassFromReports_22AC5(t *testing.T) {
	cases := []struct{ name, adapter, caseDir, wantID string }{
		{"pytest ImportError", "pytest-junit", "pytest_importerror", "unit::tests/test_x.py::test_imp"},
		{"pytest NameError (message only)", "pytest-junit", "pytest_nameerror_message_only", "unit::tests/test_x.py::test_name"},
		{"jest ReferenceError", "jest-junit", "jest_referenceerror", "unit::src/redirect.test.js::redirect uses helper"},
		{"pytest neither list", "pytest-junit", "pytest_neither", "unit::tests/test_x.py::test_val"},
		{"jest neither list", "jest-junit", "jest_neither", "unit::src/redirect.test.js::redirect boom"},
	}
	for _, c := range cases {
		t.Run(strings.ReplaceAll(c.name, " ", "_"), func(t *testing.T) {
			rep, err := NormalizeReport(loadAdapter(t, c.adapter), "unit", reportFile(t, filepath.Join("testdata", "adapter", c.caseDir)))
			if err != nil {
				t.Fatalf("NormalizeReport: %v", err)
			}
			if len(rep.Tests) != 1 || rep.Tests[0].ID != c.wantID || rep.Tests[0].Status != "failed_error" {
				t.Fatalf("report = %+v, want %s failed_error", rep.Tests, c.wantID)
			}
			fx, _ := loadRedFixture(t, "R7/only_error")
			fx.Suites = map[string]string{"unit": "python"}
			fx.Declared = map[string][]string{"AC-1": {c.wantID}}
			fx.IDSets.BasePassing = []string{}
			applyReport(&fx, rep)
			in := redInputOf(t, fx)
			got, err := ClassifyRed(in)
			if err != nil {
				t.Fatalf("ClassifyRed: %v", err)
			}
			if got.Outcome != "red_invalid" || len(got.Reasons) == 0 || got.Reasons[0] != "error_class_failure" {
				t.Errorf("classification = %s %v, want red_invalid / error_class_failure", got.Outcome, got.Reasons)
			}
		})
	}
	// The assertion-class twin of the pytest case is not error-class: a library that always answers
	// error_class_failure cannot pass.
	rep, err := NormalizeReport(loadAdapter(t, "pytest-junit"), "unit", reportFile(t, filepath.Join("testdata", "adapter", "pytest_assertion")))
	if err != nil {
		t.Fatalf("NormalizeReport: %v", err)
	}
	fx, _ := loadRedFixture(t, "valid/suffix_declared")
	fx.Suites = map[string]string{"unit": "python"}
	fx.Declared = map[string][]string{"AC-1": {"unit::tests/test_x.py::test_a"}}
	fx.IDSets.BasePassing = []string{}
	applyReport(&fx, rep)
	in := redInputOf(t, fx)
	got, err := ClassifyRed(in)
	if err != nil {
		t.Fatalf("ClassifyRed: %v", err)
	}
	for _, r := range got.Reasons {
		if r == "error_class_failure" {
			t.Errorf("assertion-class failure classified error-class: %s %v", got.Outcome, got.Reasons)
		}
	}
}

// 22-AC-5: the error-class rule on observed documents (a failed_error among several new tests).
func TestClassifyRed_ErrorClass_22AC5(t *testing.T) {
	runRedCases(t, []string{"R7/one_of_two_errors", "R7/only_error"})
}

// 22-AC-6: all change-criterion tests pass at head -> already_satisfied; one of two passes -> partially_satisfied;
// a preservation criterion whose test fails at head -> red_invalid (preservation_ac_not_passing).
func TestClassifyRed_AlreadySatisfied_22AC6(t *testing.T) {
	runRedCases(t, []string{"R10/already_satisfied", "R10/partially_satisfied", "R9/preservation_failing", "valid/two_acs"})
}

// 22-AC-7: a failing pre-existing test (or a failing build) at base -> baseline_failing; a test failing at base
// and passing at head with an unchanged file is flaky_baseline -> inconclusive.
func TestClassifyRed_Baseline_22AC7(t *testing.T) {
	runRedCases(t, []string{"R2/base_test_failing", "R2/base_build_failed", "R2/flaky_baseline", "valid/two_acs"})
}

// Plan 22 section 14.3: one or more fixtures per rule R0-R11 and the red_valid paths.
func TestClassifyRed_RuleFixtures_R0toR11(t *testing.T) {
	runRedCases(t, []string{
		"R0/identity_failed", "R0/validation_blob_mismatch",
		"R1/scope_other_file", "R1/deleted_file", "R1/renamed_file", "R1/stub_malformed", "R1/go_panic_stub", "R1/rust_panic_repo_forbids",
		"R2/base_test_failing", "R2/base_build_failed", "R2/flaky_baseline",
		"R3/head_build_failed", "R3/head_report_missing", "R3/head_timed_out",
		"R4/no_new_tests",
		"R5/declared_not_discovered", "R5/declared_not_new", "R5/new_test_skipped",
		"R6/ac_not_covered",
		"R7/one_of_two_errors", "R7/only_error",
		"R8/preexisting_broken", "R8/preexisting_removed", "R8/removed_inside_scope", "R8/removed_outside_scope", "R8/removed_other_pkg_unscoped",
		"R9/preservation_failing",
		"R10/already_satisfied", "R10/partially_satisfied",
		"R11/truncated_declared_missing", "R11/truncated_all_reported",
		"valid/two_acs", "valid/suffix_declared", "valid/modified_existing_test", "valid/go_zero_value_stub", "valid/rust_panic_stub",
	})
}

// 22-R-3: facts the classification depends on that are unusable make ClassifyRed fail, never default to a pass.
func TestClassifyRed_UnusableFacts_FailClosed_22AC7(t *testing.T) {
	fx, _ := loadRedFixture(t, "valid/two_acs")
	t.Run("id set count mismatch", func(t *testing.T) {
		in := redInputOf(t, fx)
		in.Observed.TestIDSets.BasePassing.Count++
		got, err := ClassifyRed(in)
		if err == nil && got.Outcome == "red_valid" {
			t.Errorf("a corrupt base id set classified red_valid")
		}
		if err == nil && got.Outcome == "" {
			t.Errorf("empty classification without an error")
		}
	})
	t.Run("missing id sets", func(t *testing.T) {
		in := redInputOf(t, fx)
		in.Observed.TestIDSets = nil
		got, err := ClassifyRed(in)
		if err == nil && got.Outcome == "red_valid" {
			t.Errorf("a document without id sets classified red_valid")
		}
		if err == nil && got.Outcome == "" {
			t.Errorf("empty classification without an error")
		}
	})
	t.Run("unknown status", func(t *testing.T) {
		in := redInputOf(t, fx)
		in.Observed.Tests = append([]TestObservation(nil), in.Observed.Tests...)
		in.Observed.Tests[0].StatusHead = "mostly_passed"
		got, err := ClassifyRed(in)
		if err == nil && got.Outcome == "red_valid" {
			t.Errorf("an unknown test status classified red_valid")
		}
		if err == nil && got.Outcome == "" {
			t.Errorf("empty classification without an error")
		}
	})
	t.Run("unknown criterion kind", func(t *testing.T) {
		in := redInputOf(t, fx)
		in.Criteria = []Criterion{{ID: "AC-1", Kind: "wish"}, in.Criteria[1]}
		got, err := ClassifyRed(in)
		if err == nil && got.Outcome == "red_valid" {
			t.Errorf("an unknown criterion kind classified red_valid")
		}
		if err == nil && got.Outcome == "" {
			t.Errorf("empty classification without an error")
		}
	})
}
