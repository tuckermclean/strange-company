package verify

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// S1 review round 1 (decision D9): blockers B1 (non-function declarations in stub files), B2 (R1 protected
// paths) and the folded non-blockers N1-N6. Fixtures live in new directories (testdata/stubs_d9,
// testdata/adapter_d9); no earlier test iterates them.

// ---- B1: stub check ----

type d9StubExpect struct {
	Facts     int `json:"facts"`
	Malformed int `json:"malformed"`
}

func d9StubFile(t testing.TB, name string) (StubFile, d9StubExpect) {
	t.Helper()
	dir := filepath.Join("testdata", "stubs_d9", name)
	lang := string(trimNL(mustRead(t, dir, "lang")))
	ext := map[string]string{"go": "go", "rust": "rs", "typescript": "ts", "python": "py"}[lang]
	f := StubFile{
		Path: string(trimNL(mustRead(t, dir, "path"))), Language: lang,
		Base: mustRead(t, dir, "base."+ext+".txt"), Head: mustRead(t, dir, "head."+ext+".txt"),
	}
	var exp d9StubExpect
	readJSONFile(t, &exp, dir, "expect.json")
	return f, exp
}

func d9Facts(t testing.TB, name string) ([]StubFact, StubFile) {
	t.Helper()
	sp, err := LoadStubPolicy(platformPolicy(t).Verification)
	if err != nil {
		t.Fatalf("LoadStubPolicy: %v", err)
	}
	f, _ := d9StubFile(t, name)
	facts, err := AnalyzeStubs(sp, f)
	if err != nil {
		t.Fatalf("AnalyzeStubs(%s): %v", name, err)
	}
	return facts, f
}

var d9MalformedStubCases = []string{
	"go_added_var", "go_added_const", "go_var_func_literal", "go_stub_plus_var",
	"ts_arrow_const", "py_module_assign", "rust_const",
}

var d9CleanStubCases = []string{
	"go_import_added", "go_comment_added", "go_marked_func",
	"ts_import_added", "ts_marked_func", "py_comment_added", "py_marked_func",
	"rust_use_added", "rust_marked_fn",
}

// 22-AC-4 (B1): an added non-function declaration in a stub-class file (Go var/const/func-literal var, TS arrow
// const, Python module assignment, Rust const) is a malformed stub fact. Twins: an added import or comment is
// no declaration, and a marked zero-value function stays a well-formed stub.
func TestAnalyzeStubs_NonFunctionDeclarations_D9B1_22AC4(t *testing.T) {
	for _, name := range append(append([]string{}, d9MalformedStubCases...), d9CleanStubCases...) {
		t.Run(name, func(t *testing.T) {
			_, exp := d9StubFile(t, name)
			facts, f := d9Facts(t, name)
			malformed := 0
			for _, fact := range facts {
				if fact.Path != f.Path {
					t.Errorf("fact path = %q, want %q", fact.Path, f.Path)
				}
				if !fact.WellFormed {
					malformed++
				}
			}
			if len(facts) != exp.Facts || malformed != exp.Malformed {
				t.Errorf("facts = %d (malformed %d), want %d (malformed %d): %s", len(facts), malformed, exp.Facts, exp.Malformed, jsonOf(facts))
			}
		})
	}
}

// d9BaseFixture is the valid RED (zero-value Go stub) with stub facts left to the caller.
func d9BaseFixture(t testing.TB) redFixture {
	t.Helper()
	fx, _ := loadRedFixture(t, "valid/go_zero_value_stub")
	fx.StubsFrom = nil
	return fx
}

const d9TestFile = "internal/httpx/redirect_test.go"

// d9StubInput builds a RED input whose stub-class changed file and facts come from a testdata/stubs_d9 case.
func d9StubInput(t testing.TB, name string) RedInput {
	t.Helper()
	facts, f := d9Facts(t, name)
	in := redInputOf(t, d9BaseFixture(t))
	in.Scope.Stubs = facts
	in.Scope.ChangedFiles = []ChangedFile{
		{Path: d9TestFile, Change: "added", Class: "test"},
		{Path: f.Path, Change: "modified", Class: "stub"},
	}
	return in
}

func d9HasReason(cl RedClassification, r string) bool {
	for _, x := range cl.Reasons {
		if x == r {
			return true
		}
	}
	return false
}

// 22-AC-4 (B1): ClassifyRed turns every such malformed fact into red_invalid / stub_malformed; the well-formed
// marked stubs of the other languages stay red_valid.
func TestClassifyRed_NonFunctionStubs_D9B1_22AC4(t *testing.T) {
	for _, name := range d9MalformedStubCases {
		t.Run("malformed_"+name, func(t *testing.T) {
			got, err := ClassifyRed(d9StubInput(t, name))
			if err != nil {
				t.Fatalf("ClassifyRed: %v", err)
			}
			if got.Outcome != "red_invalid" || len(got.Reasons) == 0 || got.Reasons[0] != "stub_malformed" {
				t.Errorf("classification = %s %v, want red_invalid with stub_malformed first", got.Outcome, got.Reasons)
			}
		})
	}
	for _, name := range []string{"go_marked_func", "ts_marked_func", "py_marked_func", "rust_marked_fn"} {
		t.Run("twin_"+name, func(t *testing.T) {
			got, err := ClassifyRed(d9StubInput(t, name))
			if err != nil {
				t.Fatalf("ClassifyRed: %v", err)
			}
			if got.Outcome != "red_valid" || len(got.Reasons) != 0 {
				t.Errorf("classification = %s %v, want red_valid", got.Outcome, got.Reasons)
			}
		})
	}
}

// 22-AC-4 (B1): every changed file of class stub has at least one stub fact, and every fact's path is a
// stub-class changed file.
func TestClassifyRed_StubFactsMatchStubFiles_D9B1_22AC4(t *testing.T) {
	t.Run("stub_file_without_fact", func(t *testing.T) {
		in := d9StubInput(t, "go_marked_func")
		in.Scope.Stubs = []StubFact{}
		got, err := ClassifyRed(in)
		if err != nil {
			t.Fatalf("ClassifyRed: %v", err)
		}
		if got.Outcome != "red_invalid" {
			t.Errorf("a stub-class file without a stub fact classified %s %v, want red_invalid", got.Outcome, got.Reasons)
		}
	})
	t.Run("one_of_two_stub_files_without_fact", func(t *testing.T) {
		in := d9StubInput(t, "go_marked_func")
		in.Scope.ChangedFiles = append(in.Scope.ChangedFiles, ChangedFile{Path: "internal/httpx/other.go", Change: "modified", Class: "stub"})
		got, err := ClassifyRed(in)
		if err != nil {
			t.Fatalf("ClassifyRed: %v", err)
		}
		if got.Outcome != "red_invalid" {
			t.Errorf("a second stub-class file without a fact classified %s %v, want red_invalid", got.Outcome, got.Reasons)
		}
	})
	t.Run("fact_for_test_class_file", func(t *testing.T) {
		in := d9StubInput(t, "go_marked_func")
		extra := in.Scope.Stubs[0]
		extra.Path, extra.Symbol = d9TestFile, "Smuggled"
		in.Scope.Stubs = append(append([]StubFact{}, in.Scope.Stubs...), extra)
		got, err := ClassifyRed(in)
		if err != nil {
			t.Fatalf("ClassifyRed: %v", err)
		}
		if got.Outcome != "red_invalid" {
			t.Errorf("a stub fact on a test-class file classified %s %v, want red_invalid", got.Outcome, got.Reasons)
		}
	})
	t.Run("fact_for_unchanged_file", func(t *testing.T) {
		in := d9StubInput(t, "go_marked_func")
		extra := in.Scope.Stubs[0]
		extra.Path, extra.Symbol = "internal/httpx/not_in_the_range.go", "Ghost"
		in.Scope.Stubs = append(append([]StubFact{}, in.Scope.Stubs...), extra)
		got, err := ClassifyRed(in)
		if err != nil {
			t.Fatalf("ClassifyRed: %v", err)
		}
		if got.Outcome != "red_invalid" {
			t.Errorf("a stub fact on a file outside the changed set classified %s %v, want red_invalid", got.Outcome, got.Reasons)
		}
	})
}

// ---- B2: R1 protected paths ----

func d9ProtectedSets(t testing.TB) (platform, repo []string) {
	t.Helper()
	spec, ok := pathMap(platformPolicy(t).Verification, "spec")
	if !ok {
		t.Fatal("platform verification policy has no spec")
	}
	platform, err := stringList(spec["protectedPaths"])
	if err != nil {
		t.Fatalf("platform spec.protectedPaths: %v", err)
	}
	rspec, ok := pathMap(readYAMLMap(t, filepath.Join(repoRoot(t), ".strange-company", "policy.yaml")), "spec")
	if !ok {
		t.Fatal("repository policy has no spec")
	}
	repo, err = stringList(rspec["protectedPaths"])
	if err != nil {
		t.Fatalf("repository spec.protectedPaths: %v", err)
	}
	return platform, repo
}

func d9Has(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// d9ProtectedInput is the valid RED plus one more changed file labelled class test at path p. The effective
// policy carries the repository's protectedPaths when withRepo is set.
func d9ProtectedInput(t testing.TB, p string, withRepo bool) RedInput {
	t.Helper()
	fx, _ := loadRedFixture(t, "valid/go_zero_value_stub")
	fx.Scope.ChangedFiles = append(append([]ChangedFile{}, fx.Scope.ChangedFiles...), ChangedFile{Path: p, Change: "added", Class: "test"})
	if withRepo {
		_, repo := d9ProtectedSets(t)
		l := make([]any, len(repo))
		for i, s := range repo {
			l[i] = s
		}
		fx.Effective = map[string]any{"/spec/protectedPaths": l}
	}
	return redInputOf(t, fx)
}

// 22-AC-33 / plan 22 section 14.3 R1 (B2): a changed path matching the platform spec.protectedPaths or the
// effective /spec/protectedPaths is a scope_violation, whatever class the Runner labelled it. The sets come
// from the real platform policy and the real repository policy.
func TestClassifyRed_R1ProtectedPath_D9B2(t *testing.T) {
	platform, repo := d9ProtectedSets(t)
	if !d9Has(platform, ".woodpecker/**") || !d9Has(platform, "**/AGENTS.md") {
		t.Fatalf("platform protectedPaths = %v, want .woodpecker/** and **/AGENTS.md", platform)
	}
	if !d9Has(repo, "SPEC.md") || d9Has(platform, "SPEC.md") {
		t.Fatalf("SPEC.md must be protected by the repository policy only (platform %v)", platform)
	}
	cases := []struct {
		name, path string
		withRepo   bool
		want       string
	}{
		{"platform_woodpecker", ".woodpecker/ci.yaml", false, "red_invalid"},
		{"platform_woodpecker_with_repo_set", ".woodpecker/ci.yaml", true, "red_invalid"},
		{"platform_nested_glob", "docs/sub/AGENTS.md", false, "red_invalid"},
		{"repo_effective_only", "SPEC.md", true, "red_invalid"},
		{"twin_repo_path_without_effective", "SPEC.md", false, "red_valid"},
		{"twin_unprotected_test_file", "internal/httpx/extra_test.go", true, "red_valid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ClassifyRed(d9ProtectedInput(t, c.path, c.withRepo))
			if err != nil {
				t.Fatalf("ClassifyRed: %v", err)
			}
			if got.Outcome != c.want {
				t.Fatalf("outcome = %s %v, want %s", got.Outcome, got.Reasons, c.want)
			}
			if c.want == "red_invalid" && (len(got.Reasons) == 0 || got.Reasons[0] != "scope_violation") {
				t.Errorf("reasons = %v, want scope_violation first", got.Reasons)
			}
		})
	}
}

// ---- N1, N2, N5 ----

// 22-R-3 (N1): a test present at head, new, and missing from `tests` while tests_truncated is false makes the
// document inconsistent: an infra error. With tests_truncated the same data is only inconclusive.
func TestClassifyRed_HeadPresentMissingFromTests_D9N1(t *testing.T) {
	fx, _ := loadRedFixture(t, "valid/two_acs")
	fx.IDSets.HeadPresent = append(append([]string{}, fx.IDSets.HeadPresent...), "unit::example.com/x/internal/httpx.TestGhost")
	t.Run("not_truncated", func(t *testing.T) {
		got, err := ClassifyRed(redInputOf(t, fx))
		var nc *NotClassifiedError
		if !errors.As(err, &nc) || nc.Code != "verification_infra_failed" {
			t.Fatalf("err = %v (classification %s %v), want a *NotClassifiedError verification_infra_failed", err, got.Outcome, got.Reasons)
		}
	})
	t.Run("twin_truncated", func(t *testing.T) {
		fx := fx
		fx.Observed.TestsTruncated = true
		got, err := ClassifyRed(redInputOf(t, fx))
		if err != nil {
			t.Fatalf("ClassifyRed: %v", err)
		}
		if got.Outcome != "inconclusive" {
			t.Errorf("outcome = %s %v, want inconclusive", got.Outcome, got.Reasons)
		}
	})
}

// 22-AC-6 / plan 22 section 14.3 (N2): every failing rule's reasons are listed, including R10's, while the
// outcome comes from the first failing blocking rule.
func TestClassifyRed_ReasonsIncludeR10_D9N2_22AC6(t *testing.T) {
	for _, c := range []string{"R10/already_satisfied", "R10/partially_satisfied"} {
		t.Run(strings.ReplaceAll(c, "/", "_"), func(t *testing.T) {
			fx, _ := loadRedFixture(t, c)
			fx.Scope.ChangedFiles = append(append([]ChangedFile{}, fx.Scope.ChangedFiles...),
				ChangedFile{Path: "internal/httpx/redirect.go", Change: "modified", Class: "other"})
			got, err := ClassifyRed(redInputOf(t, fx))
			if err != nil {
				t.Fatalf("ClassifyRed: %v", err)
			}
			if got.Outcome != "red_invalid" || jsonOf(got.Reasons) != jsonOf([]string{"scope_violation", "change_ac_not_failing"}) {
				t.Errorf("classification = %s %v, want red_invalid [scope_violation change_ac_not_failing]", got.Outcome, got.Reasons)
			}
		})
	}
	t.Run("twin_all_change_criteria_failing", func(t *testing.T) {
		fx, _ := loadRedFixture(t, "valid/two_acs")
		fx.Scope.ChangedFiles = append(append([]ChangedFile{}, fx.Scope.ChangedFiles...),
			ChangedFile{Path: "internal/httpx/redirect.go", Change: "modified", Class: "other"})
		got, err := ClassifyRed(redInputOf(t, fx))
		if err != nil {
			t.Fatalf("ClassifyRed: %v", err)
		}
		if got.Outcome != "red_invalid" || jsonOf(got.Reasons) != jsonOf([]string{"scope_violation"}) {
			t.Errorf("classification = %s %v, want red_invalid [scope_violation]", got.Outcome, got.Reasons)
		}
	})
}

// rawIDSet encodes ids exactly as given (no sorting, no de-duplication), with a consistent count and digest.
func rawIDSet(t testing.TB, ids []string) *TestIDSet {
	t.Helper()
	text := strings.Join(ids, "\n")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(text))
	return &TestIDSet{Count: len(ids), Sha256: hex.EncodeToString(sum[:]), Data: base64.StdEncoding.EncodeToString(buf.Bytes())}
}

// 22-R-3 (N5): a $defs/TestIdSet with duplicate or unsorted ids is not what the Runner writes: an infra error.
// The sorted, unique twin classifies.
func TestClassifyRed_TestIDSetDuplicateOrUnsorted_D9N5(t *testing.T) {
	fx, _ := loadRedFixture(t, "valid/two_acs")
	good := redInputOf(t, fx)
	if _, err := ClassifyRed(good); err != nil {
		t.Fatalf("twin: ClassifyRed: %v", err)
	}
	head := append([]string{}, fx.IDSets.HeadPresent...)
	base := append([]string{}, fx.IDSets.BasePassing...)
	sorted := func(ids []string) []string { return encodeIDSetIDs(t, ids) }
	cases := []struct {
		name string
		set  func(in *RedInput)
	}{
		{"head_present_duplicate", func(in *RedInput) {
			s := sorted(head)
			in.Observed.TestIDSets.HeadPresent = rawIDSet(t, append(append([]string{}, s[:1]...), s...))
		}},
		{"base_passing_duplicate", func(in *RedInput) {
			s := sorted(append(append([]string{}, base...), "unit::example.com/x/internal/httpx.TestZ"))
			in.Observed.TestIDSets.BasePassing = rawIDSet(t, append(append([]string{}, s...), s[len(s)-1]))
		}},
		{"head_present_unsorted", func(in *RedInput) {
			s := sorted(head)
			s[0], s[len(s)-1] = s[len(s)-1], s[0]
			in.Observed.TestIDSets.HeadPresent = rawIDSet(t, s)
		}},
		{"base_passing_unsorted", func(in *RedInput) {
			s := sorted(append(append([]string{}, base...), "unit::example.com/x/internal/httpx.TestZ"))
			s[0], s[len(s)-1] = s[len(s)-1], s[0]
			in.Observed.TestIDSets.BasePassing = rawIDSet(t, s)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := redInputOf(t, fx)
			c.set(&in)
			got, err := ClassifyRed(in)
			var nc *NotClassifiedError
			if !errors.As(err, &nc) || nc.Code != "verification_infra_failed" {
				t.Fatalf("err = %v (classification %s %v), want a *NotClassifiedError verification_infra_failed", err, got.Outcome, got.Reasons)
			}
		})
	}
}

// encodeIDSetIDs returns the sorted ids of a list.
func encodeIDSetIDs(t testing.TB, ids []string) []string {
	t.Helper()
	var out []string
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	// insertion sort keeps the helper independent of the library
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ---- N3, N4, N6: adapters ----

// 22-AC-5 (N3): the class comes from the failure type and the first message line only. A later line that looks
// like the other class does not change it.
func TestNormalizeReport_FirstMessageLineOnly_D9N3_22AC5(t *testing.T) {
	for _, name := range []string{"jest_typeerror_first", "jest_assertion_first", "pytest_assertion_first"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join("testdata", "adapter_d9", name)
			var exp struct {
				Status string `json:"status"`
			}
			readJSONFile(t, &exp, dir, "expect.json")
			rep, err := NormalizeReport(loadAdapter(t, string(trimNL(mustRead(t, dir, "adapter")))), "unit", reportFile(t, dir))
			if err != nil {
				t.Fatalf("NormalizeReport: %v", err)
			}
			if len(rep.Tests) != 1 || rep.Tests[0].Status != exp.Status {
				t.Errorf("tests = %+v, want one test %s", rep.Tests, exp.Status)
			}
		})
	}
}

// d9AdapterWithPanicMessage loads the adapters from the platform policy with stubPolicy.panicMessage replaced.
func d9AdapterWithPanicMessage(t testing.TB, name, msg string) Adapter {
	t.Helper()
	b, err := json.Marshal(platformPolicy(t).Verification)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	sp, ok := pathMap(doc, "spec", "stubPolicy")
	if !ok {
		t.Fatal("no spec.stubPolicy")
	}
	sp["panicMessage"] = msg
	ads, err := LoadAdapters(doc)
	if err != nil {
		t.Fatalf("LoadAdapters: %v", err)
	}
	return ads[name]
}

func pytestFailure(attrs, body string) []byte {
	return []byte(`<?xml version="1.0" encoding="utf-8"?>
<testsuites><testsuite name="s" tests="1">
<testcase classname="tests.test_x" name="test_stub" file="tests/test_x.py" time="0.001"><failure ` + attrs + `>` + body + `</failure></testcase>
</testsuite></testsuites>
`)
}

// 22-AC-4 (N4): the stub panic message is stubPolicy.panicMessage of the policy document, searched in the failure
// message only; the same text in the body of the test case (a source excerpt, a stack) is not a stub.
func TestNormalizeReport_StubPanicMessageFromPolicy_D9N4_22AC4(t *testing.T) {
	status := func(a Adapter, rep []byte) string {
		t.Helper()
		r, err := NormalizeReport(a, "unit", rep)
		if err != nil || len(r.Tests) != 1 {
			t.Fatalf("NormalizeReport: %v %+v", err, r)
		}
		return r.Tests[0].Status
	}
	def := loadAdapter(t, "pytest-junit")
	custom := d9AdapterWithPanicMessage(t, "pytest-junit", "XYZ-NOT-DONE")
	custom0 := pytestFailure(`message="NotImplementedError: XYZ-NOT-DONE" type="NotImplementedError"`, "")
	default0 := pytestFailure(`message="NotImplementedError: SC-STUB-NOT-IMPLEMENTED" type="NotImplementedError"`, "")
	bodyOnly := pytestFailure(`message="AssertionError: assert 1 == 2" type="AssertionError"`,
		"def f():\n    raise NotImplementedError('SC-STUB-NOT-IMPLEMENTED')\nAssertionError")
	if got := status(custom, custom0); got != "stub_unimplemented" {
		t.Errorf("custom policy message: status = %s, want stub_unimplemented", got)
	}
	if got := status(custom, default0); got == "stub_unimplemented" {
		t.Errorf("the default message under a custom panicMessage must not be a stub")
	}
	if got := status(def, default0); got != "stub_unimplemented" {
		t.Errorf("twin, default policy and message: status = %s, want stub_unimplemented", got)
	}
	if got := status(def, bodyOnly); got == "stub_unimplemented" {
		t.Errorf("the marker text only in the test case body is not stub_unimplemented")
	}
	if got := status(def, bodyOnly); got != "failed_assertion" {
		t.Errorf("body-only marker: status = %s, want failed_assertion", got)
	}
}

// 22-R-3 (N6): a Go package with a `start` event and no final package pass/fail event (the go command died, a
// build failure lost its package result) is BuildFailed, not clean.
func TestNormalizeReport_GoPackageWithoutResult_D9N6(t *testing.T) {
	ad := loadAdapter(t, "go-test-json")
	report := mustRead(t, "testdata", "adapter_d9", "go_package_no_result.json")
	rep, err := NormalizeReport(ad, "unit", report)
	if err != nil {
		t.Fatalf("NormalizeReport: %v", err)
	}
	if !rep.BuildFailed {
		t.Errorf("a package without a final result was not reported as BuildFailed: %+v", rep)
	}
	twin := append(append([]byte{}, report...),
		[]byte(`{"Time": "2026-10-13T12:00:00Z", "Action": "pass", "Package": "example.com/x/b"}`+"\n")...)
	rep, err = NormalizeReport(ad, "unit", twin)
	if err != nil {
		t.Fatalf("twin: NormalizeReport: %v", err)
	}
	if rep.BuildFailed {
		t.Errorf("twin with every package finished reported BuildFailed")
	}
}
