package verify

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Decision D15 (S2 review 1): R1b, R12 and R13 judge every pre-existing Go test file structurally, with the
// identity of a test function being (file, function name), and the manifest checks of R14 / BuildManifest enforce the
// schema's per-item limits. Facts that cannot be used are verification_infra_failed (22-R-3).
//
// Reason ids: existing_test_weakened (R1b), manifest_not_superset (R12), unapproved_test_change (R13),
// manifest_too_large (R14, outcome red_invalid) and, for unusable facts, the *NotClassifiedError code
// verification_infra_failed. Inputs are hand-built (a test id is "unit::<package>.<Func>[/<subtest>]").

const (
	d15PkgH  = genPkg
	d15PkgO  = "example.com/x/internal/other"
	d15FileH = "internal/httpx/redirect_test.go"
	d15FileO = "internal/other/old_test.go"
)

type d15Test struct{ id, file, base, head string }

type d15Spec struct {
	criteria []Criterion
	declared map[string][]string // criterion -> full declared names (<package>.<Func>[/<subtest>])
	tests    []d15Test
	changed  []ChangedFile
	files    []TestFile
	prev     *Manifest
	opening  string
	approved []string
}

func d15ID(pkg, name string) string { return "unit::" + pkg + "." + name }

var d15Two = []Criterion{{ID: "AC-1", Kind: "change"}, {ID: "AC-2", Kind: "change"}}

func d15Build(t *testing.T, s d15Spec) GenerationInput {
	t.Helper()
	fx, _ := loadRedFixture(t, "valid/suffix_declared")
	fx.Criteria = s.criteria
	fx.Declared = s.declared
	fx.Observed.Tests = nil
	fx.IDSets.BasePassing, fx.IDSets.BaseSkipped, fx.IDSets.HeadPresent = []string{}, []string{}, []string{}
	for _, tt := range s.tests {
		obs := TestObservation{ID: tt.id, Suite: "unit", File: tt.file, StatusBase: tt.base, StatusHead: tt.head}
		if tt.head == "failed_assertion" {
			obs.FailureType, obs.Message = "t.Errorf", "got want"
		}
		fx.Observed.Tests = append(fx.Observed.Tests, obs)
		if tt.base == "passed" {
			fx.IDSets.BasePassing = append(fx.IDSets.BasePassing, obs.ID)
		}
		if tt.head != "absent" {
			fx.IDSets.HeadPresent = append(fx.IDSets.HeadPresent, obs.ID)
		}
	}
	fx.Scope.ChangedFiles = s.changed
	return GenerationInput{RedInput: redInputOf(t, fx), Opening: s.opening, ApprovedACIDs: s.approved, Previous: s.prev,
		TestFiles: s.files, Repeats: &Repeats{Runs: 3}}
}

func d15File(path, base, head string) TestFile {
	f := TestFile{Path: path, Language: "go", Head: []byte(head)}
	if base != "" {
		f.Base = []byte(base)
	}
	return f
}

func d15Prev(files []string, tests ...ManifestTest) *Manifest {
	var mf []ManifestFile
	for _, p := range files {
		mf = append(mf, mfile(p))
	}
	return prevManifest(mf, tests)
}

func d15MTest(pkg, name, ac string) ManifestTest {
	return ManifestTest{ID: d15ID(pkg, name), Suite: "unit", Kind: "new", ACIDs: []string{ac}, RedStatus: "failed_assertion"}
}

func d15Replace(t *testing.T, s, old, repl string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("fixture bug: %q not in the source", old)
	}
	return strings.Replace(s, old, repl, 1)
}

func d15Infra(t *testing.T, in GenerationInput) {
	t.Helper()
	got, err := ClassifyRedGen(in)
	checkRed(t, in.RedInput, got, err, redExpect{Error: "verification_infra_failed"})
}

// The pieces of the shared test file: header, imports, declarations.
const (
	d15Hdr     = "//go:build !race\n\npackage httpx\n\n"
	d15Imports = "import (\n\t\"testing\"\n\n\t\"example.com/x/internal/testutil\"\n)\n\n"
	d15Var     = "var wantA = 2\n\n"
	d15Const   = "const limit = 5\n\n"
	d15Type    = "type row struct{ in, out int }\n\n"
	d15FnA     = "func TestA(t *testing.T) {\n\ttestutil.Eq(t, Do(1), wantA)\n}\n\n"
	d15FnB     = "func TestB(t *testing.T) {\n\tif Do(2) != limit {\n\t\tt.Errorf(\"b\")\n\t}\n}\n"
	d15FnC     = "\nfunc TestC(t *testing.T) {\n\tif Do(9) != 10 {\n\t\tt.Errorf(\"c\")\n\t}\n}\n"
	d15Base    = d15Hdr + d15Imports + d15Var + d15Const + d15Type + d15FnA + d15FnB
)

// ---------------------------------------------------------------------------------------------------------------
// R12 (22-AC-26, 22-AC-30): a strengthened generation adds only whole new declarations, imports and functions to a
// previous manifest file; header, imports, var/const/type declarations and every function stay byte-identical.

func d15R12(t *testing.T, head string) GenerationInput {
	return d15Build(t, d15Spec{
		criteria: d15Two,
		declared: map[string][]string{"AC-1": {d15PkgH + ".TestA", d15PkgH + ".TestC"}, "AC-2": {d15PkgH + ".TestB"}},
		tests: []d15Test{
			{d15ID(d15PkgH, "TestA"), d15FileH, "absent", "failed_assertion"},
			{d15ID(d15PkgH, "TestB"), d15FileH, "absent", "failed_assertion"},
			{d15ID(d15PkgH, "TestC"), d15FileH, "absent", "failed_assertion"},
		},
		changed: []ChangedFile{goChange(d15FileH, "modified", "test")},
		files:   []TestFile{d15File(d15FileH, d15Base, head)},
		prev:    d15Prev([]string{d15FileH}, d15MTest(d15PkgH, "TestA", "AC-1"), d15MTest(d15PkgH, "TestB", "AC-2")),
		opening: "strengthened",
	})
}

// 22-AC-30 (D15 b, c): outside the bodies of functions, any changed line of a previous-manifest file breaks R12: a
// changed var, const or type declaration, a swapped, removed or `_`-added import, a changed build constraint.
func TestClassifyRedGen_R12NonFunctionEdits_D15_22AC30(t *testing.T) {
	cases := map[string]string{
		"var_changed":      d15Replace(t, d15Base, "var wantA = 2", "var wantA = 1"),
		"const_changed":    d15Replace(t, d15Base, "const limit = 5", "const limit = 0"),
		"type_changed":     d15Replace(t, d15Base, "type row struct{ in, out int }", "type row struct{ in, out, skip int }"),
		"import_swapped":   d15Replace(t, d15Base, "\"example.com/x/internal/testutil\"", "\"example.com/x/internal/testutil/noop\""),
		"import_removed":   d15Replace(t, d15Base, "\n\t\"example.com/x/internal/testutil\"\n", "\n"),
		"blank_import_new": d15Replace(t, d15Base, "\t\"testing\"\n", "\t\"testing\"\n\t_ \"example.com/x/internal/sidefx\"\n"),
		"dot_import_new":   d15Replace(t, d15Base, "\t\"testing\"\n", "\t\"testing\"\n\t. \"example.com/x/internal/sidefx\"\n"),
		"build_constraint": d15Replace(t, d15Base, "//go:build !race", "//go:build ignore"),
		"header_comment":   d15Replace(t, d15Base, "package httpx", "// Package httpx is tested.\npackage httpx"),
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			checkGen(t, d15R12(t, src+d15FnC), "red_invalid", []string{"manifest_not_superset"})
		})
	}
}

// 22-AC-26 / 22-AC-30 twins: whole new declarations (var, const, type, func), a new plain import and a moved
// declaration are additions, never changed lines.
func TestClassifyRedGen_R12StructuralTwins_D15_22AC26(t *testing.T) {
	moved := d15Hdr + d15Imports + d15FnB + "\n" + d15Type + d15Var + d15Const + strings.TrimSuffix(d15FnA, "\n\n") + "\n"
	cases := map[string]string{
		"new_whole_var":     d15Base + "\nvar extra = 3\n" + d15FnC,
		"new_whole_const":   d15Base + "\nconst more = 4\n" + d15FnC,
		"new_whole_type":    d15Base + "\ntype pair struct{ a, b int }\n" + d15FnC,
		"new_func":          d15Base + d15FnC,
		"new_plain_import":  d15Replace(t, d15Base, "\t\"testing\"\n", "\t\"strings\"\n\t\"testing\"\n") + "\nfunc TestC(t *testing.T) {\n\tif strings.ToUpper(\"a\") != \"B\" {\n\t\tt.Errorf(\"c\")\n\t}\n}\n",
		"moved_declaration": moved + d15FnC,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			checkGen(t, d15R12(t, src), "red_valid", []string{})
		})
	}
}

// 22-R-3 (D15 d): a previous-manifest file that the red range deletes breaks R12 (manifest_not_superset), with or
// without a content entry for it, and R13 (unapproved_test_change). tester_deletion is listed as well.
func TestClassifyRedGen_DeletedPreviousFile_D15_22AC26(t *testing.T) {
	const golden = "internal/httpx/testdata/golden.txt"
	mk := func(opening string, withEntry bool) GenerationInput {
		s := d15Spec{
			criteria: d15Two,
			declared: map[string][]string{"AC-1": {d15PkgH + ".TestA"}, "AC-2": {d15PkgH + ".TestB"}},
			tests: []d15Test{
				{d15ID(d15PkgH, "TestA"), d15FileH, "absent", "failed_assertion"},
				{d15ID(d15PkgH, "TestB"), d15FileH, "absent", "failed_assertion"},
			},
			changed:  []ChangedFile{goChange(golden, "deleted", "fixture")},
			prev:     d15Prev([]string{d15FileH, golden}, d15MTest(d15PkgH, "TestA", "AC-1"), d15MTest(d15PkgH, "TestB", "AC-2")),
			opening:  opening,
			approved: []string{"AC-2"},
		}
		if withEntry {
			s.files = []TestFile{{Path: golden, Language: "text", Base: []byte("golden\n")}}
		}
		return d15Build(t, s)
	}
	for _, entry := range []bool{false, true} {
		name := "no_entry"
		if entry {
			name = "with_entry"
		}
		t.Run("R12_"+name, func(t *testing.T) {
			checkGen(t, mk("strengthened", entry), "red_invalid", nil, "manifest_not_superset", "tester_deletion")
		})
		t.Run("R13_"+name, func(t *testing.T) {
			checkGen(t, mk("test_change_approved", entry), "red_invalid", nil, "unapproved_test_change", "tester_deletion")
		})
	}
}

// ---------------------------------------------------------------------------------------------------------------
// R1b (22-AC-30): existing tests are not weakened; identity is (file, function name).

const d15R1bBase = "package httpx\n\nimport (\n\t\"testing\"\n\n\t\"example.com/x/internal/testutil\"\n)\n\n" +
	"var cases = []int{1, 2, 3}\n\n" +
	"func TestExisting(t *testing.T) {\n\tfor _, c := range cases {\n\t\ttestutil.Eq(t, Do(c), c)\n\t}\n}\n\n" +
	"func TestOld(t *testing.T) {\n\tif Do(2) != 2 {\n\t\tt.Errorf(\"old\")\n\t}\n}\n"

const (
	d15R1bOldEdited = "func TestOld(t *testing.T) {\n\tif Do(2) != 3 {\n\t\tt.Errorf(\"old\")\n\t}\n}\n"
	d15R1bNew       = "\nfunc TestNew(t *testing.T) {\n\tif Do(9) != 10 {\n\t\tt.Errorf(\"new\")\n\t}\n}\n"
)

// d15R1bHead is the red commit's file: TestOld (mapped to AC-1) edited, TestNew added, plus a mutation.
func d15R1bHead(t *testing.T, mutate func(string) string) string {
	src := d15Replace(t, d15R1bBase, "func TestOld(t *testing.T) {\n\tif Do(2) != 2 {\n\t\tt.Errorf(\"old\")\n\t}\n}\n", d15R1bOldEdited)
	if mutate != nil {
		src = mutate(src)
	}
	return src + d15R1bNew
}

func d15R1b(t *testing.T, head string, mod func(*d15Spec)) GenerationInput {
	s := d15Spec{
		criteria: oneChange,
		declared: map[string][]string{"AC-1": {d15PkgH + ".TestNew", d15PkgH + ".TestOld"}},
		tests: []d15Test{
			{d15ID(d15PkgH, "TestExisting"), d15FileH, "passed", "passed"},
			{d15ID(d15PkgH, "TestOld"), d15FileH, "passed", "failed_assertion"},
			{d15ID(d15PkgH, "TestNew"), d15FileH, "absent", "failed_assertion"},
		},
		changed: []ChangedFile{goChange(d15FileH, "modified", "test")},
		files:   []TestFile{d15File(d15FileH, d15R1bBase, head)},
	}
	if mod != nil {
		mod(&s)
	}
	return d15Build(t, s)
}

// 22-AC-30 (D15 a, b): a function with the same name as a mapped test, in another package and file, is not exempt:
// emptying other.TestOld while httpx.TestOld is the mapped test is a weakened existing test.
func TestClassifyRedGen_R1bSameNameOtherPackage_D15_22AC30(t *testing.T) {
	const otherBase = "package other\n\nimport \"testing\"\n\nfunc TestOld(t *testing.T) {\n\tif Do(2) != 2 {\n\t\tt.Errorf(\"old\")\n\t}\n}\n"
	mk := func(otherHead string) GenerationInput {
		return d15R1b(t, d15R1bHead(t, nil), func(s *d15Spec) {
			s.tests = append(s.tests, d15Test{d15ID(d15PkgO, "TestOld"), d15FileO, "passed", "passed"})
			s.changed = append(s.changed, goChange(d15FileO, "modified", "test"))
			s.files = append(s.files, d15File(d15FileO, otherBase, otherHead))
		})
	}
	t.Run("other_package_emptied", func(t *testing.T) {
		checkGen(t, mk("package other\n\nimport \"testing\"\n\nfunc TestOld(t *testing.T) {\n}\n"), "red_invalid", []string{"existing_test_weakened"})
	})
	t.Run("other_package_assertion_changed", func(t *testing.T) {
		checkGen(t, mk(strings.Replace(otherBase, "!= 2", "!= 3", 1)), "red_invalid", []string{"existing_test_weakened"})
	})
	t.Run("twin_other_package_new_function_only", func(t *testing.T) {
		checkGen(t, mk(otherBase+"\nfunc TestMore(t *testing.T) {\n}\n"), "red_valid", []string{})
	})
}

// 22-AC-30 (D15 b): the package-level declarations and imports of an existing test file are part of its tests. A
// changed `var cases` table or a swapped assertion import weakens every test that uses them.
func TestClassifyRedGen_R1bNonFunctionEdits_D15_22AC30(t *testing.T) {
	cases := map[string]func(string) string{
		"var_changed": func(s string) string { return d15Replace(t, s, "{1, 2, 3}", "{}") },
		"import_swapped": func(s string) string {
			return d15Replace(t, s, "\"example.com/x/internal/testutil\"", "\"example.com/x/internal/testutil/noop\"")
		},
		"import_removed": func(s string) string { return d15Replace(t, s, "\n\t\"example.com/x/internal/testutil\"\n", "\n") },
		"blank_import_new": func(s string) string {
			return d15Replace(t, s, "\t\"testing\"\n", "\t\"testing\"\n\t_ \"example.com/x/internal/sidefx\"\n")
		},
		"build_constraint_new": func(s string) string { return "//go:build ignore\n\n" + s },
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			checkGen(t, d15R1b(t, d15R1bHead(t, m), nil), "red_invalid", []string{"existing_test_weakened"})
		})
	}
}

// 22-AC-30 twins: the mapped function itself may change, and whole new declarations, plain imports and moved
// declarations are additions.
func TestClassifyRedGen_R1bStructuralTwins_D15_22AC30(t *testing.T) {
	cases := map[string]func(string) string{
		"mapped_function_changed": nil,
		"new_var":                 func(s string) string { return s + "\nvar extra = 3\n" },
		"new_type":                func(s string) string { return s + "\ntype pair struct{ a, b int }\n" },
		"new_plain_import": func(s string) string {
			return d15Replace(t, s, "\t\"testing\"\n", "\t\"strings\"\n\t\"testing\"\n") + "\nvar _ = strings.ToUpper\n"
		},
		"moved_declaration": func(s string) string {
			i := strings.Index(s, "var cases")
			j := strings.Index(s, "func TestExisting")
			return s[:i] + s[j:] + "\n" + strings.TrimSuffix(s[i:j], "\n\n") + "\n"
		},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			checkGen(t, d15R1b(t, d15R1bHead(t, m), nil), "red_valid", []string{})
		})
	}
}

// ---------------------------------------------------------------------------------------------------------------
// R13 (22-AC-26): approved scope per (file, function); the function holding an unapproved test is not editable.

func d15R13Shared(t *testing.T, head string, approved ...string) GenerationInput {
	return d15Build(t, d15Spec{
		criteria: d15Two,
		declared: map[string][]string{"AC-1": {d15PkgH + ".TestA"}, "AC-2": {d15PkgH + ".TestB"}},
		tests: []d15Test{
			{d15ID(d15PkgH, "TestA"), d15FileH, "absent", "failed_assertion"},
			{d15ID(d15PkgH, "TestB"), d15FileH, "absent", "failed_assertion"},
		},
		changed: []ChangedFile{goChange(d15FileH, "modified", "test")},
		files:   []TestFile{d15File(d15FileH, d15Base, head)},
		prev:    d15Prev([]string{d15FileH}, d15MTest(d15PkgH, "TestA", "AC-1"), d15MTest(d15PkgH, "TestB", "AC-2")),
		opening: "test_change_approved", approved: approved,
	})
}

func d15EditB(t *testing.T, src string) string {
	return d15Replace(t, src, "Do(2) != limit", "Do(2) != limit+1")
}

// 22-AC-26 (D15 a): httpx.TestX (AC-2, approved) and other.TestX (AC-1, not approved) share a function name; the
// edit of other's TestX in its own file is unapproved. The twin edits only httpx's.
func TestClassifyRedGen_R13SameNameOtherFile_D15_22AC26(t *testing.T) {
	const (
		fh = "internal/httpx/x_test.go"
		fo = "internal/other/x_test.go"
	)
	baseH := "package httpx\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {\n\tif Do(2) != 3 {\n\t\tt.Errorf(\"b\")\n\t}\n}\n"
	headH := strings.Replace(baseH, "!= 3", "!= 4", 1)
	baseO := "package other\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {\n\tif Do(1) != 2 {\n\t\tt.Errorf(\"a\")\n\t}\n}\n"
	headO := "package other\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {\n}\n"
	mk := func(editOther bool) GenerationInput {
		s := d15Spec{
			criteria: d15Two,
			declared: map[string][]string{"AC-1": {d15PkgO + ".TestX"}, "AC-2": {d15PkgH + ".TestX"}},
			tests: []d15Test{
				{d15ID(d15PkgH, "TestX"), fh, "absent", "failed_assertion"},
				{d15ID(d15PkgO, "TestX"), fo, "absent", "failed_assertion"},
			},
			changed:  []ChangedFile{goChange(fh, "modified", "test")},
			files:    []TestFile{d15File(fh, baseH, headH)},
			prev:     d15Prev([]string{fh, fo}, d15MTest(d15PkgH, "TestX", "AC-2"), d15MTest(d15PkgO, "TestX", "AC-1")),
			opening:  "test_change_approved",
			approved: []string{"AC-2"},
		}
		if editOther {
			s.changed = append(s.changed, goChange(fo, "modified", "test"))
			s.files = append(s.files, d15File(fo, baseO, headO))
		}
		return d15Build(t, s)
	}
	checkGen(t, mk(true), "red_invalid", []string{"unapproved_test_change"})
	checkGen(t, mk(false), "red_valid", []string{})
}

// 22-AC-26 (D15 c): TestT holds subtests of AC-1 and AC-2. With only AC-2 approved the function is not editable
// (it holds an unapproved previous test); with both approved the same edit is red_valid.
func TestClassifyRedGen_R13FunctionHoldingUnapprovedSubtest_D15_22AC26(t *testing.T) {
	base := "package httpx\n\nimport \"testing\"\n\nfunc TestT(t *testing.T) {\n\tt.Run(\"ac1\", func(t *testing.T) {\n\t\tif Do(1) != 2 {\n\t\t\tt.Errorf(\"a\")\n\t\t}\n\t})\n\tt.Run(\"ac2\", func(t *testing.T) {\n\t\tif Do(2) != 3 {\n\t\t\tt.Errorf(\"b\")\n\t\t}\n\t})\n}\n"
	mk := func(head string, approved ...string) GenerationInput {
		return d15Build(t, d15Spec{
			criteria: d15Two,
			declared: map[string][]string{"AC-1": {d15PkgH + ".TestT/ac1"}, "AC-2": {d15PkgH + ".TestT/ac2"}},
			tests: []d15Test{
				{d15ID(d15PkgH, "TestT/ac1"), d15FileH, "absent", "failed_assertion"},
				{d15ID(d15PkgH, "TestT/ac2"), d15FileH, "absent", "failed_assertion"},
			},
			changed: []ChangedFile{goChange(d15FileH, "modified", "test")},
			files:   []TestFile{d15File(d15FileH, base, head)},
			prev:    d15Prev([]string{d15FileH}, d15MTest(d15PkgH, "TestT/ac1", "AC-1"), d15MTest(d15PkgH, "TestT/ac2", "AC-2")),
			opening: "test_change_approved", approved: approved,
		})
	}
	editAC1 := strings.Replace(base, "Do(1) != 2", "Do(1) != 3", 1)
	editAC2 := strings.Replace(base, "Do(2) != 3", "Do(2) != 4", 1)
	t.Run("edit_ac1_subtest", func(t *testing.T) {
		checkGen(t, mk(editAC1, "AC-2"), "red_invalid", []string{"unapproved_test_change"})
	})
	t.Run("edit_ac2_subtest_in_shared_function", func(t *testing.T) {
		checkGen(t, mk(editAC2, "AC-2"), "red_invalid", []string{"unapproved_test_change"})
	})
	t.Run("twin_both_approved", func(t *testing.T) {
		checkGen(t, mk(editAC1, "AC-1", "AC-2"), "red_valid", []string{})
	})
}

// 22-AC-26 (D15 b, c): outside function bodies a shared file stays identical in a test_change_approved generation
// (header, imports, var/const/type). The twin edits the AC-2 function alone and may add whole declarations.
func TestClassifyRedGen_R13NonFunctionEdits_D15_22AC26(t *testing.T) {
	cases := map[string]string{
		"var_changed":      d15Replace(t, d15Base, "var wantA = 2", "var wantA = 1"),
		"const_changed":    d15Replace(t, d15Base, "const limit = 5", "const limit = 0"),
		"type_changed":     d15Replace(t, d15Base, "type row struct{ in, out int }", "type row struct{ in, out, skip int }"),
		"import_swapped":   d15Replace(t, d15Base, "\"example.com/x/internal/testutil\"", "\"example.com/x/internal/testutil/noop\""),
		"blank_import_new": d15Replace(t, d15Base, "\t\"testing\"\n", "\t\"testing\"\n\t_ \"example.com/x/internal/sidefx\"\n"),
		"build_constraint": d15Replace(t, d15Base, "//go:build !race", "//go:build ignore"),
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			checkGen(t, d15R13Shared(t, d15EditB(t, src), "AC-2"), "red_invalid", []string{"unapproved_test_change"})
		})
	}
	t.Run("twin_only_approved_function_edited", func(t *testing.T) {
		checkGen(t, d15R13Shared(t, d15EditB(t, d15Base), "AC-2"), "red_valid", []string{})
	})
	t.Run("twin_approved_function_edited_and_new_declarations", func(t *testing.T) {
		src := d15EditB(t, d15Base) + "\nvar extra = 3\n" + d15FnC
		checkGen(t, d15R13Shared(t, src, "AC-2"), "red_valid", []string{})
	})
	t.Run("unapproved_function_edited", func(t *testing.T) {
		src := d15Replace(t, d15Base, "Do(1), wantA", "Do(1), wantA+1")
		checkGen(t, d15R13Shared(t, d15EditB(t, src), "AC-2"), "red_invalid", []string{"unapproved_test_change"})
	})
}

// ---------------------------------------------------------------------------------------------------------------
// D15 (a), (d): unusable facts are errors, never passes (22-R-3).

// 22-AC-30 / 22-AC-26 (D15 a): a declared (R1b) or previous-manifest (R12, R13) test id that cannot be resolved to a
// file is verification_infra_failed. The observation reports the test without a file.
func TestClassifyRedGen_UnresolvableTestFile_D15_22AC30(t *testing.T) {
	t.Run("R1b_mapped_test_without_file", func(t *testing.T) {
		in := d15R1b(t, d15R1bHead(t, nil), func(s *d15Spec) { s.tests[1].file = "" })
		d15Infra(t, in)
	})
	t.Run("R12_manifest_test_without_file", func(t *testing.T) {
		in := d15R12(t, d15Base+d15FnC)
		for i := range in.Observed.Tests {
			if in.Observed.Tests[i].ID == d15ID(d15PkgH, "TestA") {
				in.Observed.Tests[i].File = ""
			}
		}
		d15Infra(t, in)
	})
	t.Run("R13_manifest_test_without_file", func(t *testing.T) {
		in := d15R13Shared(t, d15EditB(t, d15Base), "AC-2")
		for i := range in.Observed.Tests {
			if in.Observed.Tests[i].ID == d15ID(d15PkgH, "TestB") {
				in.Observed.Tests[i].File = ""
			}
		}
		d15Infra(t, in)
	})
}

// 22-R-3 (D15 d): a modified test-class file that holds observed tests, or a modified previous-manifest file of a
// strengthened / approved generation, without a TestFiles entry cannot be judged: verification_infra_failed. A
// `.go` TestFile with an empty Language is no Go file to the library: verification_infra_failed.
func TestClassifyRedGen_MissingTestFile_D15_22AC30(t *testing.T) {
	t.Run("R1b_modified_file_with_tests_no_entry", func(t *testing.T) {
		d15Infra(t, d15R1b(t, d15R1bHead(t, nil), func(s *d15Spec) { s.files = nil }))
	})
	t.Run("R12_previous_file_no_entry", func(t *testing.T) {
		in := d15R12(t, d15Base+d15FnC)
		in.TestFiles = nil
		d15Infra(t, in)
	})
	t.Run("R13_previous_file_no_entry", func(t *testing.T) {
		in := d15R13Shared(t, d15EditB(t, d15Base), "AC-2")
		in.TestFiles = nil
		d15Infra(t, in)
	})
	t.Run("R1b_empty_language", func(t *testing.T) {
		in := d15R1b(t, d15R1bHead(t, nil), nil)
		in.TestFiles[0].Language = ""
		d15Infra(t, in)
	})
	t.Run("R12_empty_language", func(t *testing.T) {
		in := d15R12(t, d15Base+d15FnC)
		in.TestFiles[0].Language = ""
		d15Infra(t, in)
	})
	t.Run("R13_empty_language", func(t *testing.T) {
		in := d15R13Shared(t, d15EditB(t, d15Base), "AC-2")
		in.TestFiles[0].Language = ""
		d15Infra(t, in)
	})
	t.Run("twin_go_language_set", func(t *testing.T) {
		checkGen(t, d15R1b(t, d15R1bHead(t, nil), nil), "red_valid", []string{})
	})
}

// ---------------------------------------------------------------------------------------------------------------
// R14 and BuildManifest (D15 e): the schema's per-item limits.

// d15NameLen is the longest test function name whose id `unit::<genPkg>.<name>` still fits the TestId pattern
// `^[a-z][a-z0-9-]{0,40}::[^\s].{0,498}$` (at most 499 characters after `::`).
func d15NameLen() int { return 499 - len(genPkg) - 1 }

func d15LongInput(t *testing.T, nameLen int, changedExtra ...ChangedFile) GenerationInput {
	name := "TestL" + strings.Repeat("y", nameLen-len("TestL"))
	return buildGen(t, genSpec{
		criteria: oneChange, declared: map[string][]string{"AC-1": {name}},
		tests:   []genTest{{name, testFilePath, "absent", "failed_assertion"}},
		changed: append([]ChangedFile{goChange(testFilePath, "added", "test")}, changedExtra...),
	})
}

// 22-AC-32 (D15 e): a test id longer than the schema's per-item maximum, or a path that violates the RepoPath
// pattern (a space) or length (401 characters), makes the manifest too large for the schema.
func TestClassifyRedGen_R14PerItemLimits_D15_22AC32(t *testing.T) {
	t.Run("test_id_over_maximum", func(t *testing.T) {
		checkGen(t, d15LongInput(t, d15NameLen()+1), "red_invalid", []string{"manifest_too_large"})
	})
	t.Run("path_with_space", func(t *testing.T) {
		in := d15LongInput(t, 20, goChange("internal/httpx/testdata/a b.txt", "added", "fixture"))
		checkGen(t, in, "red_invalid", []string{"manifest_too_large"})
	})
	t.Run("path_over_400", func(t *testing.T) {
		long := "internal/httpx/testdata/" + strings.Repeat("d", 400) + ".txt"
		checkGen(t, d15LongInput(t, 20, goChange(long, "added", "fixture")), "red_invalid", []string{"manifest_too_large"})
	})
	t.Run("twin_test_id_at_maximum", func(t *testing.T) {
		checkGen(t, d15LongInput(t, d15NameLen()), "red_valid", []string{})
	})
	t.Run("twin_ordinary_fixture_path", func(t *testing.T) {
		in := d15LongInput(t, 20, goChange("internal/httpx/testdata/a_b-1.txt", "added", "fixture"))
		checkGen(t, in, "red_valid", []string{})
	})
}

// d15ManifestInput is the sc-184 BuildManifest input plus one extra declared test and, optionally, one extra
// fixture file, with a hand-built red_valid classification (the classification is not under test here).
func d15ManifestInput(t *testing.T, extraName string, extraPath string) ManifestInput {
	extra := d15ID(d15PkgH, extraName)
	in := sc184Input(t, func(fx *redFixture) {
		if extraName != "" {
			fx.IDSets.HeadPresent = append(fx.IDSets.HeadPresent, extra)
			fx.Observed.Tests = append(fx.Observed.Tests, TestObservation{ID: extra, Suite: "unit", File: "internal/httpx/redirect_test.go", StatusBase: "absent", StatusHead: "failed_assertion"})
			fx.Declared["AC-1"] = append(fx.Declared["AC-1"], extra)
		}
		if extraPath != "" {
			fx.Scope.ChangedFiles = append(fx.Scope.ChangedFiles, goChange(extraPath, "added", "fixture"))
		}
	})
	if extraName != "" {
		in.Classification.NewTests = append(in.Classification.NewTests, extra)
		in.Classification.ACResults[0].Tests = append(in.Classification.ACResults[0].Tests, extra)
	}
	return in
}

// 22-AC-9 (D15 e): BuildManifest validates its output against the schema and errors when it is invalid, even with a
// classification that says red_valid: an over-long test id, a path that breaks the RepoPath pattern.
func TestBuildManifest_RejectsSchemaInvalidOutput_D15_22AC9(t *testing.T) {
	long := "TestL" + strings.Repeat("y", d15NameLen()+1-len("TestL"))
	for name, in := range map[string]ManifestInput{
		"test_id_over_maximum": d15ManifestInput(t, long, ""),
		"path_with_space":      d15ManifestInput(t, "", "internal/httpx/testdata/a b.txt"),
		"path_over_400":        d15ManifestInput(t, "", "internal/httpx/testdata/"+strings.Repeat("d", 400)+".txt"),
	} {
		t.Run(name, func(t *testing.T) {
			if m, err := BuildManifest(in); err == nil {
				t.Errorf("BuildManifest returned a manifest (digest %s) that does not validate: %v", m.Digest, validateManifest(t, m))
			}
		})
	}
	t.Run("twin_valid_input_builds", func(t *testing.T) {
		m, err := BuildManifest(d15ManifestInput(t, "TestExtra", "internal/httpx/testdata/extra.txt"))
		if err != nil {
			t.Fatalf("BuildManifest: %v", err)
		}
		if verr := validateManifest(t, m); verr != nil {
			t.Errorf("manifest does not validate: %v", verr)
		}
	})
}

// canonicalNoEscape is the canonical JSON of the digest (sorted keys, compact) without HTML escaping.
func canonicalNoEscape(t *testing.T, v any) []byte {
	t.Helper()
	var generic map[string]any
	if err := json.Unmarshal(canonicalJSON(t, v), &generic); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// 22-AC-9 (D15 e): the digest is the sha256 of the canonical JSON without HTML escaping. The test-id pattern of the
// schema allows `<`, `>` and `&` (paths do not: RepoPath is [A-Za-z0-9._@+/-]), so a test id carries them.
func TestBuildManifest_DigestDoesNotEscapeHTML_D15_22AC9(t *testing.T) {
	m, err := BuildManifest(d15ManifestInput(t, "TestAmp&Less<More>", ""))
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	found := false
	for _, tt := range m.Tests {
		found = found || strings.Contains(tt.ID, "&") && strings.Contains(tt.ID, "<") && strings.Contains(tt.ID, ">")
	}
	if !found {
		t.Fatalf("the manifest lost the test id with `&<>`: %+v", m.Tests)
	}
	want := sha256Of(canonicalNoEscape(t, withoutDigest(asGeneric(t, m))))
	if m.Digest != want {
		t.Errorf("digest = %s, want %s (canonical JSON without HTML escaping)", m.Digest, want)
	}
	if verr := validateManifest(t, m); verr != nil {
		t.Errorf("the manifest does not validate: %v", verr)
	}
}

// 22-AC-32 / 22-R-3 (D15 e): red.repeatNewTests comes from the platform policy only (3). A repository value of 1 in
// the effective values (the repository policy has no spec.red) does not lower the requirement: one run is
// verification_infra_failed. The twin with three runs is red_valid whatever the effective map says.
func TestClassifyRedGen_RepeatNewTestsPlatformOnly_D15_22AC32(t *testing.T) {
	mk := func(runs int) GenerationInput {
		in := r11bSpec(t, genTest{"TestFlaky", testFilePath, "absent", "failed_assertion"}, &Repeats{Runs: runs})
		vals := map[string]any{}
		for k, v := range in.Effective.Values {
			vals[k] = v
		}
		vals["/spec/red/repeatNewTests"] = 1
		in.Effective.Values = vals
		return in
	}
	d15Infra(t, mk(1))
	d15Infra(t, mk(2))
	checkGen(t, mk(3), "red_valid", []string{})
}
