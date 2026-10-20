package verify

import (
	"fmt"
	"strings"
	"testing"
)

// Decision D16 (S2 review 2): (B1) import specs are compared with their doc comments, and a cgo import decl (the one
// containing "C", preamble included) must stay byte-identical in R1b, R12 and R13; (B2) inconsistent Runner facts are
// verification_infra_failed; (N1) R14 enforces every per-item limit of test-manifest.schema.yaml, so ClassifyRedGen
// never returns red_valid for input BuildManifest rejects; (N2) the native limit checks agree with the real schema
// on a boundary set; (N3) BuildManifest's error text is deterministic.
//
// Reason ids: existing_test_weakened (R1b), manifest_not_superset (R12), unapproved_test_change (R13) and
// manifest_too_large (R14). Unusable facts are the *NotClassifiedError code verification_infra_failed. Every case
// has its twin that must stay valid.

// ---------------------------------------------------------------------------------------------------------------
// B1: import specs with their doc comments, cgo preambles.

const (
	d16Helper = "internal/testutil/eq.go"
	d16FnA    = "func TestA(t *testing.T) {\n\tif Do(1) != 2 {\n\t\tt.Errorf(\"a\")\n\t}\n}\n\n"
	d16FnB    = "func TestB(t *testing.T) {\n\tif Do(2) != 3 {\n\t\tt.Errorf(\"b\")\n\t}\n}\n"
	d16FnEq   = "func Eq(t *testing.T, a, b int) {\n\tif a != b {\n\t\tt.Errorf(\"x\")\n\t}\n}\n"
	d16FnEq2  = "\nfunc Eq2(t *testing.T, a, b int) {\n\tif a == b {\n\t\tt.Errorf(\"y\")\n\t}\n}\n"
)

// d16Form is one import section of a Go file and the head's edit of it. The twin of a form keeps the import decl
// byte-identical (the head is the base plus an addition elsewhere).
type d16Form struct {
	name string
	imp  string // base import section
	head string // head import section (the change under test)
}

func d16Forms() []d16Form {
	cgoU := "// #define STRICT 1\nimport \"C\"\n\nimport \"testing\"\n"
	cgoG := "import (\n\t// #define STRICT 1\n\t\"C\"\n\t\"testing\"\n)\n"
	return []d16Form{
		{"cgo_ungrouped_preamble_changed", cgoU, strings.Replace(cgoU, "STRICT 1", "STRICT 0", 1)},
		{"cgo_grouped_spec_doc_changed", cgoG, strings.Replace(cgoG, "STRICT 1", "STRICT 0", 1)},
		{"plain_import_spec_doc_changed", "import (\n\t// x\n\t\"testing\"\n)\n", "import (\n\t// y\n\t\"testing\"\n)\n"},
		{"plain_import_decl_doc_changed", "// x\nimport \"testing\"\n", "// y\nimport \"testing\"\n"},
		{"cgo_group_gains_plain_import", cgoG, strings.Replace(cgoG, "\t\"testing\"\n", "\t\"testing\"\n\t\"strings\"\n", 1)},
		{"cgo_import_new_in_head", "import \"testing\"\n", "// #define STRICT 1\nimport \"C\"\n\nimport \"testing\"\n"},
	}
}

func d16Src(pkg, imp, body string) string { return "package " + pkg + "\n\n" + imp + "\n" + body }

// d16R1bInput: a test_infra helper (internal/testutil/eq.go) is modified next to a new test file. R1b judges the
// helper like any pre-existing file: its body is not judged, its import decl and header are.
func d16R1bInput(t *testing.T, helperBase, helperHead string) GenerationInput {
	return d15Build(t, d15Spec{
		criteria: oneChange,
		declared: map[string][]string{"AC-1": {d15PkgH + ".TestNew"}},
		tests:    []d15Test{{d15ID(d15PkgH, "TestNew"), d15FileH, "absent", "failed_assertion"}},
		changed:  []ChangedFile{goChange(d15FileH, "added", "test"), goChange(d16Helper, "modified", "test_infra")},
		files: []TestFile{
			d15File(d15FileH, "", "package httpx\n\nimport \"testing\"\n\nfunc TestNew(t *testing.T) {\n\tif Do(9) != 10 {\n\t\tt.Errorf(\"new\")\n\t}\n}\n"),
			d15File(d16Helper, helperBase, helperHead),
		},
	})
}

// d16R12Input: a strengthened generation over a previous-manifest file; the head adds TestC.
func d16R12Input(t *testing.T, base, head string) GenerationInput {
	return d15Build(t, d15Spec{
		criteria: d15Two,
		declared: map[string][]string{"AC-1": {d15PkgH + ".TestA", d15PkgH + ".TestC"}, "AC-2": {d15PkgH + ".TestB"}},
		tests: []d15Test{
			{d15ID(d15PkgH, "TestA"), d15FileH, "absent", "failed_assertion"},
			{d15ID(d15PkgH, "TestB"), d15FileH, "absent", "failed_assertion"},
			{d15ID(d15PkgH, "TestC"), d15FileH, "absent", "failed_assertion"},
		},
		changed: []ChangedFile{goChange(d15FileH, "modified", "test")},
		files:   []TestFile{d15File(d15FileH, base, head)},
		prev:    d15Prev([]string{d15FileH}, d15MTest(d15PkgH, "TestA", "AC-1"), d15MTest(d15PkgH, "TestB", "AC-2")),
		opening: "strengthened",
	})
}

// d16R13Input: a test_change_approved generation (AC-2 approved) over a shared previous-manifest file.
func d16R13Input(t *testing.T, base, head string) GenerationInput {
	return d15Build(t, d15Spec{
		criteria: d15Two,
		declared: map[string][]string{"AC-1": {d15PkgH + ".TestA"}, "AC-2": {d15PkgH + ".TestB"}},
		tests: []d15Test{
			{d15ID(d15PkgH, "TestA"), d15FileH, "absent", "failed_assertion"},
			{d15ID(d15PkgH, "TestB"), d15FileH, "absent", "failed_assertion"},
		},
		changed: []ChangedFile{goChange(d15FileH, "modified", "test")},
		files:   []TestFile{d15File(d15FileH, base, head)},
		prev:    d15Prev([]string{d15FileH}, d15MTest(d15PkgH, "TestA", "AC-1"), d15MTest(d15PkgH, "TestB", "AC-2")),
		opening: "test_change_approved", approved: []string{"AC-2"},
	})
}

func d16EditB(t *testing.T, src string) string { return d15Replace(t, src, "Do(2) != 3", "Do(2) != 4") }

// 22-AC-30 (D16 B1, R1b): the doc comments of import specs, the cgo preamble and the whole `import "C"` decl of a
// pre-existing helper are part of what it does. Twin: the identical import decl and an added function.
func TestClassifyRedGen_R1bImportDocsAndCgo_D16_22AC30(t *testing.T) {
	for _, f := range d16Forms() {
		base := d16Src("testutil", f.imp, d16FnEq)
		t.Run(f.name, func(t *testing.T) {
			checkGen(t, d16R1bInput(t, base, d16Src("testutil", f.head, d16FnEq)), "red_invalid", []string{"existing_test_weakened"})
		})
		t.Run("twin_"+f.name, func(t *testing.T) {
			checkGen(t, d16R1bInput(t, base, base+d16FnEq2), "red_valid", []string{})
		})
	}
}

// 22-AC-30 (D16 B1, R12): a strengthened generation may not touch the import decl of a previous-manifest file. The
// head adds TestC next to the change; the twin adds TestC next to the identical decl.
func TestClassifyRedGen_R12ImportDocsAndCgo_D16_22AC30(t *testing.T) {
	for _, f := range d16Forms() {
		base := d16Src("httpx", f.imp, d16FnA+d16FnB)
		fnC := d15FnC
		t.Run(f.name, func(t *testing.T) {
			checkGen(t, d16R12Input(t, base, d16Src("httpx", f.head, d16FnA+d16FnB)+fnC), "red_invalid", []string{"manifest_not_superset"})
		})
		t.Run("twin_"+f.name, func(t *testing.T) {
			checkGen(t, d16R12Input(t, base, base+fnC), "red_valid", []string{})
		})
	}
}

// 22-AC-26 (D16 B1, R13): the same change in a test_change_approved generation. The head edits the approved
// TestB; the twin edits TestB alone.
func TestClassifyRedGen_R13ImportDocsAndCgo_D16_22AC26(t *testing.T) {
	for _, f := range d16Forms() {
		base := d16Src("httpx", f.imp, d16FnA+d16FnB)
		t.Run(f.name, func(t *testing.T) {
			checkGen(t, d16R13Input(t, base, d16EditB(t, d16Src("httpx", f.head, d16FnA+d16FnB))), "red_invalid", []string{"unapproved_test_change"})
		})
		t.Run("twin_"+f.name, func(t *testing.T) {
			checkGen(t, d16R13Input(t, base, d16EditB(t, base)), "red_valid", []string{})
		})
	}
}

// ---------------------------------------------------------------------------------------------------------------
// B2: inconsistent Runner facts are verification_infra_failed (22-R-3).

func d16GoldenDeleted(t *testing.T, head []byte) GenerationInput {
	const golden = "internal/httpx/testdata/golden.txt"
	return d15Build(t, d15Spec{
		criteria: d15Two,
		declared: map[string][]string{"AC-1": {d15PkgH + ".TestA"}, "AC-2": {d15PkgH + ".TestB"}},
		tests: []d15Test{
			{d15ID(d15PkgH, "TestA"), d15FileH, "absent", "failed_assertion"},
			{d15ID(d15PkgH, "TestB"), d15FileH, "absent", "failed_assertion"},
		},
		changed: []ChangedFile{goChange(golden, "deleted", "fixture")},
		files:   []TestFile{{Path: golden, Language: "text", Base: []byte("golden\n"), Head: head}},
		prev:    d15Prev([]string{d15FileH, golden}, d15MTest(d15PkgH, "TestA", "AC-1"), d15MTest(d15PkgH, "TestB", "AC-2")),
		opening: "strengthened",
	})
}

// 22-AC-30 (D16 B2): a previous manifest with no opening, a `.go` file whose Language is not exactly "go", and a
// ChangedFile whose Change contradicts its TestFile (modified without Base, added with Base, deleted with Head) are
// facts that contradict each other: verification_infra_failed, never a pass.
func TestClassifyRedGen_InconsistentFacts_D16_22AC30(t *testing.T) {
	base := d16Src("httpx", "import \"testing\"\n", d16FnA+d16FnB)
	r12 := func() GenerationInput { return d16R12Input(t, base, base+d15FnC) }
	r1b := func() GenerationInput {
		b := d16Src("testutil", "import \"testing\"\n", d16FnEq)
		return d16R1bInput(t, b, b+d16FnEq2)
	}
	t.Run("twin_r12_consistent", func(t *testing.T) { checkGen(t, r12(), "red_valid", []string{}) })
	t.Run("twin_r1b_consistent", func(t *testing.T) { checkGen(t, r1b(), "red_valid", []string{}) })
	t.Run("twin_deleted_golden_consistent", func(t *testing.T) {
		checkGen(t, d16GoldenDeleted(t, nil), "red_invalid", nil, "manifest_not_superset", "tester_deletion")
	})

	t.Run("a_previous_manifest_with_empty_opening", func(t *testing.T) {
		in := r12()
		in.Opening = ""
		d15Infra(t, in)
	})
	for _, lang := range []string{"golang", "Go", "GO"} {
		t.Run("b_go_file_language_"+lang, func(t *testing.T) {
			in := r1b()
			for i := range in.TestFiles {
				in.TestFiles[i].Language = lang
			}
			d15Infra(t, in)
		})
		t.Run("b_go_file_language_"+lang+"_r12", func(t *testing.T) {
			in := r12()
			in.TestFiles[0].Language = lang
			d15Infra(t, in)
		})
	}
	t.Run("c_modified_without_base_r12", func(t *testing.T) {
		in := r12()
		in.TestFiles[0].Base = nil
		d15Infra(t, in)
	})
	t.Run("c_modified_without_base_r1b", func(t *testing.T) {
		in := r1b()
		in.TestFiles[1].Base = nil
		d15Infra(t, in)
	})
	t.Run("c_added_with_base", func(t *testing.T) {
		in := r1b()
		in.TestFiles[0].Base = []byte("package httpx\n")
		d15Infra(t, in)
	})
	t.Run("c_deleted_with_head", func(t *testing.T) {
		d15Infra(t, d16GoldenDeleted(t, []byte("golden\n")))
	})
}

// ---------------------------------------------------------------------------------------------------------------
// N1 and N2: the schema's per-item limits and the agreement of the native checks with the real schema.

// d16Sc184 is the sc-184 manifest input with a mutation of its fixture, and the generation input of the same RED.
func d16Sc184(t *testing.T, mut ...func(*redFixture)) (ManifestInput, GenerationInput) {
	t.Helper()
	// sc-184 records one stub_unimplemented test, which go does not allow as RED evidence (panic stubs): make it an
	// assertion failure so that the unmutated input classifies red_valid.
	muts := append([]func(*redFixture){d16AssertionRed}, mut...)
	mi := sc184Input(t, muts...)
	return mi, GenerationInput{RedInput: mi.Red, Repeats: &Repeats{Runs: 3}}
}

func d16AssertionRed(fx *redFixture) {
	for i := range fx.Observed.Tests {
		if tt := &fx.Observed.Tests[i]; tt.StatusHead == "stub_unimplemented" {
			tt.StatusHead, tt.FailureType, tt.Message = "failed_assertion", "t.Errorf", "got want"
		}
	}
	for i := range fx.Observed.Runs {
		if c := &fx.Observed.Runs[i].Counts; c.StubUnimplemented > 0 {
			c.FailedAssertion, c.StubUnimplemented = c.FailedAssertion+c.StubUnimplemented, 0
		}
	}
}

// d16SetID replaces a test id everywhere the fixture holds it (observation, id sets, declared names).
func d16SetID(fx *redFixture, oldID, newID string) {
	for i := range fx.Observed.Tests {
		if fx.Observed.Tests[i].ID == oldID {
			fx.Observed.Tests[i].ID = newID
		}
	}
	swap := func(l []string) {
		for i := range l {
			if l[i] == oldID {
				l[i] = newID
			}
		}
	}
	swap(fx.IDSets.BasePassing)
	swap(fx.IDSets.BaseSkipped)
	swap(fx.IDSets.HeadPresent)
	for _, l := range fx.Declared {
		swap(l)
	}
}

// d16SetSuite renames the suite "unit" everywhere (runs, language map, observations, id prefixes).
func d16SetSuite(fx *redFixture, suite string) {
	for i := range fx.Observed.Tests {
		old := fx.Observed.Tests[i].ID
		d16SetID(fx, old, suite+strings.TrimPrefix(old, "unit"))
		fx.Observed.Tests[i].Suite = suite
	}
	for i := range fx.Observed.Runs {
		fx.Observed.Runs[i].Suite = suite
	}
	fx.Suites = map[string]string{suite: "go"}
}

const d16FirstID = "unit::example.com/x/internal/httpx.TestRedirect307PreservesMethod"

// d16Valid reports whether ClassifyRedGen accepts the generation as red_valid (an error is a rejection).
func d16Valid(in GenerationInput) (bool, string) {
	got, err := ClassifyRedGen(in)
	if err != nil {
		return false, "error: " + err.Error()
	}
	return got.Outcome == outcomeRedValid, fmt.Sprintf("%s %v", got.Outcome, got.Reasons)
}

// d16TooLarge asserts red_invalid / manifest_too_large, and that BuildManifest rejects the same input. The red-result
// schema is not checked: the limits make the input outside it by construction (as in the D15 cases).
func d16TooLarge(t *testing.T, mut func(*redFixture)) {
	t.Helper()
	mi, gi := d16Sc184(t, mut)
	got, err := ClassifyRedGen(gi)
	if err != nil {
		t.Fatalf("ClassifyRedGen: %v", err)
	}
	reasons := []string{"manifest_too_large"}
	checkRed(t, gi.RedInput, got, nil, redExpect{Outcome: "red_invalid", Reasons: &reasons})
	if m, berr := BuildManifest(mi); berr == nil {
		t.Errorf("BuildManifest accepted the input (digest %s)", m.Digest)
	}
}

// d16Accepted asserts red_valid and that BuildManifest builds a manifest that validates against the schema.
func d16Accepted(t *testing.T, mut func(*redFixture)) {
	t.Helper()
	mi, gi := d16Sc184(t, mut)
	checkGen(t, gi, "red_valid", []string{})
	m, err := BuildManifest(mi)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if verr := validateManifest(t, m); verr != nil {
		t.Errorf("manifest does not validate: %v", verr)
	}
}

func d16Stub(f func(*StubFact)) func(*redFixture) {
	return func(fx *redFixture) { f(&fx.Scope.Stubs[0]) }
}

// 22-AC-32 (D16 N1): the stub items' limits of the schema (symbol maxLength 200, counted in code points as the
// schema validator counts them, and the marker pattern) are R14 limits. Twins sit at the maxima.
//
// AUDIT: the schema has no language enum (stub `language` is maxLength 30, and the library derives it from the file
// extension from a fixed list), so there is no language case; the suite and id limits are in
// TestClassifyRedGen_R14SuiteAndIDLimits_D16_22AC32.
func TestClassifyRedGen_R14StubLimits_D16_22AC32(t *testing.T) {
	astral := "\U0001D7D8"
	tooLong := map[string]func(*StubFact){
		"symbol_201":            func(s *StubFact) { s.Symbol = strings.Repeat("a", 201) },
		"symbol_201_astral":     func(s *StubFact) { s.Symbol = strings.Repeat(astral, 201) },
		"marker_AC_1000":        func(s *StubFact) { s.Marker = "SC-STUB(sc-184 AC-1000)" },
		"marker_AC_0":           func(s *StubFact) { s.Marker = "SC-STUB(sc-184 AC-0)" },
		"marker_no_criterion":   func(s *StubFact) { s.Marker = "SC-STUB(sc-184)" },
		"marker_trailing_nl":    func(s *StubFact) { s.Marker = "SC-STUB(sc-184 AC-2)\n" },
		"marker_comment_prefix": func(s *StubFact) { s.Marker = "// SC-STUB(sc-184 AC-2)" },
		"marker_bad_task_id":    func(s *StubFact) { s.Marker = "SC-STUB(task-184 AC-2)" },
	}
	for name, f := range tooLong {
		t.Run(name, func(t *testing.T) { d16TooLarge(t, d16Stub(f)) })
	}
	twins := map[string]func(*StubFact){
		"twin_symbol_200":        func(s *StubFact) { s.Symbol = strings.Repeat("a", 200) },
		"twin_symbol_100_astral": func(s *StubFact) { s.Symbol = strings.Repeat(astral, 100) },
		"twin_symbol_200_astral": func(s *StubFact) { s.Symbol = strings.Repeat(astral, 200) },
		"twin_marker_AC_999":     func(s *StubFact) { s.Marker = "SC-STUB(sc-184 AC-2 AC-999)" },
	}
	for name, f := range twins {
		t.Run(name, func(t *testing.T) { d16Accepted(t, d16Stub(f)) })
	}
}

// 22-AC-32 (D16 N1): the suite pattern `^[a-z][a-z0-9-]{0,40}$` (an observation's suite field, which a manifest
// records) and the TestId pattern are R14 limits: a suite name of 42 characters, an upper-case one, and a test id
// with a line terminator or over the length are not red_valid, and BuildManifest rejects them as well. Twins sit at
// the maxima.
//
// AUDIT: if an earlier S1 rule rejects one of these with another reason, red_invalid is still required but the
// reason list is not pinned; the cases pin only "not red_valid" and "BuildManifest rejects".
func TestClassifyRedGen_R14SuiteAndIDLimits_D16_22AC32(t *testing.T) {
	longSuite := strings.Repeat("a", 42)
	bad := map[string]func(*redFixture){
		"suite_42":            func(fx *redFixture) { d16SetSuite(fx, longSuite) },
		"suite_uppercase":     func(fx *redFixture) { d16SetSuite(fx, "Unit") },
		"suite_field_only_42": func(fx *redFixture) { fx.Observed.Tests[0].Suite = longSuite },
		"suite_field_only_upper": func(fx *redFixture) {
			fx.Observed.Tests[0].Suite = "Unit"
		},
		"id_with_line_separator": func(fx *redFixture) { d16SetID(fx, d16FirstID, d16FirstID+"\u2028x") },
		"id_500_after_separator": func(fx *redFixture) { d16SetID(fx, d16FirstID, "unit::"+strings.Repeat("a", 500)) },
	}
	for name, mut := range bad {
		t.Run(name, func(t *testing.T) {
			mi, gi := d16Sc184(t, mut)
			if ok, what := d16Valid(gi); ok {
				t.Errorf("ClassifyRedGen returned red_valid (%s) for input outside the schema limits", what)
			}
			if _, err := BuildManifest(mi); err == nil {
				t.Errorf("BuildManifest accepted the input")
			}
		})
	}
	t.Run("twin_suite_41", func(t *testing.T) { d16Accepted(t, func(fx *redFixture) { d16SetSuite(fx, strings.Repeat("a", 41)) }) })
	t.Run("twin_id_499_after_separator", func(t *testing.T) {
		d16Accepted(t, func(fx *redFixture) { d16SetID(fx, d16FirstID, "unit::"+strings.Repeat("a", 499)) })
	})
}

type d16Case struct {
	name, kind, value string
}

// d16Boundary is the boundary set: the 33 cases of review 2 (18 ids, 10 paths, 5 created_at), plus U+2028 and
// U+2029 in an id, astral characters at the length limit, lower-case `t` / `z` and leap seconds in created_at, and
// stub symbols and markers at their limits.
func d16Boundary() []d16Case {
	astral := "\U0001D7D8"
	var cs []d16Case
	id := func(name, v string) { cs = append(cs, d16Case{name, "id", v}) }
	id("nbsp_first", "unit::\u00a0x")
	id("bom_first", "unit::\ufeffx")
	id("ideo_first", "unit::\u3000x")
	id("cr_inside", "unit::x\ry")
	id("ls_inside", "unit::x\u2028y")
	id("nel_inside", "unit::x\u0085y")
	id("tab_inside", "unit::x\ty")
	id("trailing_nl", "unit::x\n")
	id("astral_499_units_998", "unit::"+strings.Repeat(astral, 499))
	id("ascii_499", "unit::"+strings.Repeat("a", 499))
	id("ascii_500", "unit::"+strings.Repeat("a", 500))
	id("suite_41", strings.Repeat("a", 41)+"::x")
	id("suite_42", strings.Repeat("a", 42)+"::x")
	id("triple_colon", "unit:::x")
	id("empty_rest", "unit::")
	id("zwsp_first", "unit::\u200bx")
	id("mongolian_first", "unit::\u180ex")
	id("ls_first", "unit::\u2028x")
	id("ls_last", "unit::x\u2028")
	id("ps_inside", "unit::x\u2029y")
	id("astral_249", "unit::"+strings.Repeat(astral, 249))
	id("astral_250", "unit::"+strings.Repeat(astral, 250))
	id("astral_249_plus_ascii", "unit::"+strings.Repeat(astral, 249)+"a")
	id("astral_249_plus_two_ascii", "unit::"+strings.Repeat(astral, 249)+"ab")
	id("astral_first", "unit::"+astral+"x")
	id("bmp_499_multibyte", "unit::"+strings.Repeat("\u00e9", 499))
	id("bmp_500_multibyte", "unit::"+strings.Repeat("\u00e9", 500))
	path := func(name, v string) { cs = append(cs, d16Case{name, "path", v}) }
	path("dotdot_mid", "a/../b")
	path("dotdot_end", "a/..")
	path("dots3", "a/.../b")
	path("lead_slash", "/a")
	path("trailing_nl", "a\n")
	path("len400", strings.Repeat("a", 400))
	path("len401", strings.Repeat("a", 401))
	path("dot", ".")
	path("double_slash", "a//b")
	path("space", "a b")
	path("dotdot_first", "../a")
	path("dotdot_only", "..")
	path("unicode_letter", "a/\u00e9.txt")
	ts := func(name, v string) { cs = append(cs, d16Case{name, "created_at", v}) }
	ts("lower_t", "2026-10-13t12:20:01z")
	ts("lower_t_upper_z", "2026-10-13t12:20:01Z")
	ts("upper_t_lower_z", "2026-10-13T12:20:01z")
	ts("leap", "2026-12-31T23:59:60Z")
	ts("leap_real_date", "2016-12-31T23:59:60Z")
	ts("leap_offset", "2016-12-31T23:59:60+00:00")
	ts("offset", "2026-10-13T12:20:01+01:00")
	ts("frac", "2026-10-13T12:20:01.5Z")
	ts("no_tz", "2026-10-13T12:20:01")
	ts("hour_24", "2026-10-13T24:00:00Z")
	ts("feb_30", "2026-02-30T12:00:00Z")
	ts("space_separator", "2026-10-13 12:20:01Z")
	sym := func(name, v string) { cs = append(cs, d16Case{name, "symbol", v}) }
	sym("symbol_200", strings.Repeat("a", 200))
	sym("symbol_201", strings.Repeat("a", 201))
	sym("symbol_100_astral", strings.Repeat(astral, 100))
	sym("symbol_101_astral", strings.Repeat(astral, 101))
	sym("symbol_199_plus_astral", strings.Repeat("a", 199)+astral)
	sym("symbol_198_plus_astral", strings.Repeat("a", 198)+astral)
	mk := func(name, v string) { cs = append(cs, d16Case{name, "marker", v}) }
	mk("marker_ok", "SC-STUB(sc-184 AC-2)")
	mk("marker_AC_999", "SC-STUB(sc-184 AC-2 AC-999)")
	mk("marker_AC_1000", "SC-STUB(sc-184 AC-1000)")
	mk("marker_AC_0", "SC-STUB(sc-184 AC-0)")
	mk("marker_trailing_nl", "SC-STUB(sc-184 AC-2)\n")
	mk("marker_leading_space", " SC-STUB(sc-184 AC-2)")
	return cs
}

// d16Apply returns the fixture mutation of a case, and the mutation of a valid manifest document for the schema.
func d16Apply(c d16Case) (func(*redFixture), func(*Manifest)) {
	const fixturePath = "internal/httpx/testdata/body.txt"
	switch c.kind {
	case "id":
		suite := "unit"
		if i := strings.Index(c.value, "::"); i >= 0 && i != 4 {
			suite = c.value[:i]
		}
		fx := func(fx *redFixture) {
			if suite != "unit" {
				d16SetSuite(fx, suite)
				return
			}
			d16SetID(fx, d16FirstID, c.value)
		}
		doc := func(m *Manifest) {
			m.Tests = append([]ManifestTest(nil), m.Tests...)
			m.Tests[0].ID, m.Tests[0].Suite = c.value, suite
			m.ACMapping = append([]ManifestACMapping(nil), m.ACMapping...)
			m.ACMapping[0].Tests = []string{c.value}
		}
		return fx, doc
	case "path":
		return func(fx *redFixture) {
				for i := range fx.Scope.ChangedFiles {
					if fx.Scope.ChangedFiles[i].Path == fixturePath {
						fx.Scope.ChangedFiles[i].Path = c.value
					}
				}
			}, func(m *Manifest) {
				m.Files = append([]ManifestFile(nil), m.Files...)
				for i := range m.Files {
					if m.Files[i].Path == fixturePath {
						m.Files[i].Path = c.value
					}
				}
			}
	case "created_at":
		return func(*redFixture) {}, nil
	case "symbol":
		return d16Stub(func(s *StubFact) { s.Symbol = c.value }), func(m *Manifest) {
			m.Stubs = append([]ManifestStub(nil), m.Stubs...)
			m.Stubs[0].Symbol = c.value
		}
	default: // marker
		return d16Stub(func(s *StubFact) { s.Marker = c.value }), func(m *Manifest) {
			m.Stubs = append([]ManifestStub(nil), m.Stubs...)
			m.Stubs[0].Marker = c.value
		}
	}
}

// 22-AC-32 / 22-AC-9 (D16 N2): the native limit checks of R14 and BuildManifest agree with the real schema
// (contracts/test-manifest.schema.yaml, compiled by the helper the manifest tests use) on accept / reject for every
// boundary case. The schema verdict is taken on the sc-184 manifest with the one field changed; the native verdicts
// are BuildManifest's error and, for the fields ClassifyRedGen sees (ids, paths, stub symbols and markers),
// whether ClassifyRedGen returns red_valid.
func TestManifestLimits_MatchSchema_D16_22AC32(t *testing.T) {
	valid, err := BuildManifest(sc184Input(t))
	if err != nil {
		t.Fatal(err)
	}
	if verr := validateManifest(t, valid); verr != nil {
		t.Fatalf("the unmodified sc-184 manifest is invalid: %v", verr)
	}
	for _, c := range d16Boundary() {
		t.Run(c.kind+"_"+c.name, func(t *testing.T) {
			fxMut, docMut := d16Apply(c)
			mi, gi := d16Sc184(t, fxMut)
			doc := valid
			if docMut != nil {
				docMut(&doc)
			}
			if c.kind == "created_at" {
				doc.CreatedAt = c.value
				mi.Facts.CreatedAt = c.value
			}
			schemaOK := validateManifest(t, doc) == nil
			_, berr := BuildManifest(mi)
			if buildOK := berr == nil; buildOK != schemaOK {
				t.Errorf("BuildManifest accepts=%v, the schema accepts=%v (value %q; error %v)", buildOK, schemaOK, c.value, berr)
			}
			if c.kind == "created_at" {
				return
			}
			if classOK, what := d16Valid(gi); classOK != schemaOK {
				t.Errorf("ClassifyRedGen red_valid=%v (%s), the schema accepts=%v (value %q)", classOK, what, schemaOK, c.value)
			}
		})
	}
	// An id with invalid UTF-8 cannot be serialized faithfully (encoding/json would replace it, so the schema check
	// of the encoded document is no judge): the stricter reading is that both native checks reject it.
	t.Run("id_invalid_utf8_rejected", func(t *testing.T) {
		fxMut, _ := d16Apply(d16Case{"invalid_utf8", "id", "unit::\xffx"})
		mi, gi := d16Sc184(t, fxMut)
		if ok, what := d16Valid(gi); ok {
			t.Errorf("ClassifyRedGen returned red_valid (%s) for an id with invalid UTF-8", what)
		}
		if _, berr := BuildManifest(mi); berr == nil {
			t.Errorf("BuildManifest accepted an id with invalid UTF-8")
		}
	})
}

// ---------------------------------------------------------------------------------------------------------------
// N3: BuildManifest's error text is deterministic.

// 22-AC-9 (D16 N3): the same schema-invalid input with several violations returns a byte-identical error every
// time (no map iteration order in the message). The twin builds the same manifest every time.
func TestBuildManifest_ErrorIsDeterministic_D16_22AC9(t *testing.T) {
	long := strings.Repeat("g", 201)
	several := func(fx *redFixture) {
		fx.Effective["/spec/testPaths"] = []any{long}
		fx.Effective["/spec/fixturePaths"] = []any{long}
		fx.Effective["/spec/testInfraPaths"] = []any{long}
	}
	withStub := func(fx *redFixture) {
		several(fx)
		fx.Scope.Stubs[0].Symbol = strings.Repeat("a", 201)
		fx.Scope.ChangedFiles[2].Path = "internal/httpx/testdata/a b.txt"
	}
	for name, mut := range map[string]func(*redFixture){"test_paths_violations": several, "mixed_violations": withStub} {
		t.Run(name, func(t *testing.T) {
			in := sc184Input(t, mut)
			var first string
			for i := 0; i < 20; i++ {
				_, err := BuildManifest(in)
				if err == nil {
					t.Fatal("BuildManifest accepted a schema-invalid input")
				}
				if i == 0 {
					first = err.Error()
				} else if err.Error() != first {
					t.Fatalf("call %d returned %q, call 0 returned %q", i, err.Error(), first)
				}
			}
		})
	}
	t.Run("twin_valid_input_same_manifest", func(t *testing.T) {
		in := sc184Input(t)
		first, err := BuildManifest(in)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 20; i++ {
			m, err := BuildManifest(in)
			if err != nil || m.Digest != first.Digest {
				t.Fatalf("call %d: digest %s err %v, want %s", i, m.Digest, err, first.Digest)
			}
		}
	})
}
