package verify

import (
	"testing"
)

// S1 review round 2 (decision D10): B1 fails closed. In a stub-class file every added head line that is not
// blank, a comment, a plain import, a pure type declaration, or part of a marked stub function is a malformed
// stub fact. Fixtures are inline (no earlier test iterates them).

type d10Lang struct {
	lang, ext, path, base, stub string
}

var d10Langs = map[string]d10Lang{
	"go": {"go", "go", "internal/httpx/redirect.go", "package httpx\n\nfunc Existing() int {\n\treturn 1\n}\n",
		"// SC-STUB(sc-9 AC-1)\nfunc ReplayBody(b []byte) ([]byte, error) {\n\treturn nil, nil\n}\n"},
	"python": {"python", "py", "pkg/limits.py", "def existing():\n    return 1\n",
		"# SC-STUB(sc-9 AC-1)\ndef replay():\n    return None\n"},
	"typescript": {"typescript", "ts", "src/handler.ts", "export function existing(): number {\n  return 1;\n}\n",
		"// SC-STUB(sc-9 AC-1)\nexport function replay(): number {\n  return 0;\n}\n"},
	"rust": {"rust", "rs", "src/limits.rs", "pub fn existing() -> u32 {\n    1\n}\n",
		"// SC-STUB(sc-9 AC-1)\npub fn replay() -> u32 {\n    0\n}\n"},
}

// d10File builds a stub-class file whose head is base, then pre (before the stub, after the base), the marked
// stub, then post. For Go, pre is placed right after the package clause so imports are legal.
func d10File(lang, pre, post string) StubFile {
	l := d10Langs[lang]
	base := l.base
	head := base
	if lang == "go" {
		head = "package httpx\n\n" + pre + "\nfunc Existing() int {\n\treturn 1\n}\n\n" + l.stub + "\n" + post
	} else {
		head = base + "\n" + pre + "\n" + l.stub + "\n" + post
	}
	return StubFile{Path: l.path, Language: l.lang, Base: []byte(base), Head: []byte(head)}
}

func d10Analyze(t testing.TB, f StubFile) (facts []StubFact, malformed int) {
	t.Helper()
	sp, err := LoadStubPolicy(platformPolicy(t).Verification)
	if err != nil {
		t.Fatalf("LoadStubPolicy: %v", err)
	}
	facts, err = AnalyzeStubs(sp, f)
	if err != nil {
		t.Fatalf("AnalyzeStubs: %v", err)
	}
	for _, x := range facts {
		if !x.WellFormed {
			malformed++
		}
	}
	return facts, malformed
}

type d10Case struct {
	name, lang, pre, post string
}

var d10Residuals = []d10Case{
	{"py_tuple_assign", "python", "", "a, b = 1, 2\n"},
	{"py_attr_lambda_assign", "python", "", "othermod.g = lambda *a: 42\n"},
	{"py_setattr_lambda", "python", "", "setattr(othermod, 'g', lambda *a: 42)\n"},
	{"py_class_attr", "python", "", "class Patch:\n    x = 42\n"},
	{"py_if_block_lambda", "python", "", "if True:\n    a = lambda: 42\n"},
	{"ts_module_exports_arrow", "typescript", "", "module.exports.g = () => 42;\n"},
	{"ts_globalthis_arrow", "typescript", "", "globalThis.fetch = () => 42;\n"},
	{"ts_export_class_method", "typescript", "", "export class C { m() { return 42; } }\n"},
	{"ts_export_default_function", "typescript", "", "export default function () { return 42; }\n"},
	{"rust_lazy_static", "rust", "", "lazy_static! { static ref X: u32 = 42; }\n"},
	{"rust_impl_const", "rust", "", "impl Foo { pub const X: u32 = 42; }\n"},
	{"go_blank_import", "go", "import _ \"net/http/pprof\"\n", ""},
	{"go_dot_import", "go", "import . \"strings\"\n", ""},
}

var d10Twins = []d10Case{
	{"go_plain_import", "go", "import \"strings\"\n", ""},
	{"go_import_block_comment_blank", "go", "import (\n\t\"fmt\"\n\n\t// used later\n\t\"strings\"\n)\n", "\n// trailing comment\n"},
	{"go_pure_struct", "go", "", "type T struct{ A int }\n"},
	{"py_from_import", "python", "from x import y\n", ""},
	{"py_import_comment_blank", "python", "import os\n\n# a comment\n", "\n"},
	{"ts_import", "typescript", "import { a } from './a';\n", ""},
	{"ts_interface", "typescript", "", "interface I { a: number }\n"},
	{"ts_type_alias", "typescript", "", "type X = number;\n"},
	{"ts_interface_multiline", "typescript", "// types\n", "export interface J {\n  a: number;\n  b: string;\n}\n"},
	{"rust_use", "rust", "use std::fmt;\n", ""},
	{"rust_pure_struct", "rust", "", "pub struct S { a: u32 }\n"},
	{"rust_comment_blank", "rust", "// note\n\n", "\n"},
}

// 22-AC-4 (D10 B1): residual code next to a well-formed marked stub makes the file malformed (>= 1 malformed
// fact); the marked stub alone stays one fact.
func TestAnalyzeStubs_ResidualCode_D10B1_22AC4(t *testing.T) {
	for _, c := range d10Residuals {
		t.Run(c.name, func(t *testing.T) {
			facts, malformed := d10Analyze(t, d10File(c.lang, c.pre, c.post))
			if malformed < 1 {
				t.Errorf("residual code was accepted: facts = %s, want >= 1 malformed fact", jsonOf(facts))
			}
		})
	}
}

// 22-AC-4 (D10 B1): imports, comments, blank lines and pure type declarations do not make a stub file
// malformed: exactly the one marked stub, well-formed.
func TestAnalyzeStubs_BenignLinesStayWellFormed_D10B1_22AC4(t *testing.T) {
	for _, c := range d10Twins {
		t.Run(c.name, func(t *testing.T) {
			facts, malformed := d10Analyze(t, d10File(c.lang, c.pre, c.post))
			if malformed != 0 || len(facts) != 1 {
				t.Errorf("facts = %s, want exactly one well-formed fact", jsonOf(facts))
			}
		})
	}
}

func d10StubInput(t testing.TB, f StubFile) RedInput {
	t.Helper()
	facts, _ := d10Analyze(t, f)
	in := redInputOf(t, d9BaseFixture(t))
	in.Scope.Stubs = facts
	in.Scope.ChangedFiles = []ChangedFile{
		{Path: d9TestFile, Change: "added", Class: "test"},
		{Path: f.Path, Change: "modified", Class: "stub"},
	}
	return in
}

// 22-AC-4 (D10 B1): ClassifyRed turns the residual's malformed fact into red_invalid / stub_malformed; the
// benign twins stay red_valid.
func TestClassifyRed_ResidualCode_D10B1_22AC4(t *testing.T) {
	for _, c := range d10Residuals {
		t.Run("malformed_"+c.name, func(t *testing.T) {
			got, err := ClassifyRed(d10StubInput(t, d10File(c.lang, c.pre, c.post)))
			if err != nil {
				t.Fatalf("ClassifyRed: %v", err)
			}
			if got.Outcome != "red_invalid" || !d9HasReason(got, "stub_malformed") {
				t.Errorf("classification = %s %v, want red_invalid with stub_malformed", got.Outcome, got.Reasons)
			}
		})
	}
	for _, c := range d10Twins {
		t.Run("twin_"+c.name, func(t *testing.T) {
			got, err := ClassifyRed(d10StubInput(t, d10File(c.lang, c.pre, c.post)))
			if err != nil {
				t.Fatalf("ClassifyRed: %v", err)
			}
			if got.Outcome != "red_valid" || len(got.Reasons) != 0 {
				t.Errorf("classification = %s %v, want red_valid", got.Outcome, got.Reasons)
			}
		})
	}
}

// ---- R10 / R11 reason order ----

func d10OtherFile(fx *redFixture) {
	fx.Scope.ChangedFiles = append(append([]ChangedFile{}, fx.Scope.ChangedFiles...),
		ChangedFile{Path: "internal/httpx/redirect.go", Change: "modified", Class: "other"})
}

// 22-AC-6 (D10 N2): R10's reason keeps its place after the blocking reasons when no truncation is involved.
func TestClassifyRed_R10ReasonOrder_D10_22AC6(t *testing.T) {
	fx, _ := loadRedFixture(t, "R10/already_satisfied")
	d10OtherFile(&fx)
	got, err := ClassifyRed(redInputOf(t, fx))
	if err != nil {
		t.Fatalf("ClassifyRed: %v", err)
	}
	if got.Outcome != "red_invalid" || jsonOf(got.Reasons) != jsonOf([]string{"scope_violation", "change_ac_not_failing"}) {
		t.Errorf("classification = %s %v, want red_invalid [scope_violation change_ac_not_failing]", got.Outcome, got.Reasons)
	}
}

// 22-AC-6 (D10 N2): when truncation hides a mapped test, R10's verdict rests on missing data, so its reason is
// omitted and tests_truncated is listed.
func TestClassifyRed_R10OmittedWhenTruncationHidesMappedTest_D10_22AC6(t *testing.T) {
	fx, _ := loadRedFixture(t, "R10/already_satisfied")
	d10OtherFile(&fx)
	fx.Observed.TestsTruncated = true
	fx.Observed.Tests = nil
	got, err := ClassifyRed(redInputOf(t, fx))
	if err != nil {
		t.Fatalf("ClassifyRed: %v", err)
	}
	if !d9HasReason(got, "tests_truncated") || d9HasReason(got, "change_ac_not_failing") {
		t.Errorf("classification = %s %v, want tests_truncated and no change_ac_not_failing", got.Outcome, got.Reasons)
	}
}

// 22-AC-6 (D10 N2): when both appear (truncation hides only an unmapped new test), change_ac_not_failing
// precedes tests_truncated.
func TestClassifyRed_R10BeforeR11_D10_22AC6(t *testing.T) {
	fx, _ := loadRedFixture(t, "R10/already_satisfied")
	fx.IDSets.HeadPresent = append(append([]string{}, fx.IDSets.HeadPresent...), "unit::example.com/x/internal/httpx.TestHiddenUnmapped")
	fx.Observed.TestsTruncated = true
	got, err := ClassifyRed(redInputOf(t, fx))
	if err != nil {
		t.Fatalf("ClassifyRed: %v", err)
	}
	if jsonOf(got.Reasons) != jsonOf([]string{"change_ac_not_failing", "tests_truncated"}) {
		t.Errorf("classification = %s %v, want reasons [change_ac_not_failing tests_truncated]", got.Outcome, got.Reasons)
	}
}

// ---- N3 twin: Rust panic location, assertion text on the third line ----

// 22-AC-5 (D10 N3): a Rust panic location on line 1 joins only the next non-blank line; assertion text on line
// 3 is never consulted, so the failure is failed_error. The twin with the assertion on line 2 is an assertion.
func TestNormalizeReport_RustPanicLocationJoinsOneLine_D10N3_22AC5(t *testing.T) {
	report := func(msg string) []byte {
		return []byte(`<?xml version="1.0" encoding="utf-8"?>
<testsuites><testsuite name="s" tests="1">
<testcase classname="my-crate::tests" name="test_x" time="0.001"><failure message="` + msg + `"></failure></testcase>
</testsuite></testsuites>
`)
	}
	ad := loadAdapter(t, "cargo-nextest-junit")
	status := func(msg string) string {
		t.Helper()
		r, err := NormalizeReport(ad, "unit", report(msg))
		if err != nil || len(r.Tests) != 1 {
			t.Fatalf("NormalizeReport: %v %+v", err, r)
		}
		return r.Tests[0].Status
	}
	if got := status("thread 'x' panicked at src/lib.rs:5:9:\nsomething else happened\nassertion `left == right` failed"); got != "failed_error" {
		t.Errorf("assertion text on line 3: status = %s, want failed_error", got)
	}
	if got := status("thread 'x' panicked at src/lib.rs:5:9:\nassertion `left == right` failed\n left: 1"); got != "failed_assertion" {
		t.Errorf("twin, assertion text on line 2: status = %s, want failed_assertion", got)
	}
}
