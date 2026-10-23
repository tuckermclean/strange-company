package verify

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"testing"
)

// Slice S2 (red-generations): ClassifyRedGen applies rules R1b, R11b and R12-R14 of plan 22 section 14.3 on top of
// R0-R11. Interface choices (orchestrator-visible, documented in generations.go):
//
//   - the rules that need the content of files get it as TestFile{Path, Language, Base, Head}: Base is the file at
//     the generation start (nil for a new file), Head at the red commit; only changed test-, fixture- and
//     test-infra-classified files are passed;
//   - the repeat facts of section 14.1 step 4 come as Repeats{Runs, Inconsistent, MarkerSensitive}. A missing
//     Repeats, or Runs below the platform's red.repeatNewTests, is an unusable fact (verification_infra_failed);
//   - Opening "strengthened" applies R12, "test_change_approved" applies R13 (with ApprovedACIDs), "" neither;
//   - a test function is "mapped to a change criterion" when the function name (the part of a declared Go test name
//     after the last dot, subtests dropped) is declared for a change criterion.
//
// Each test names its own directories under testdata/gen; nothing walks testdata/gen as a whole.

const genPkg = "example.com/x/internal/httpx"

func gid(name string) string { return "unit::" + genPkg + "." + name }

type genTest struct {
	name, file, base, head string
}

type genSpec struct {
	criteria []Criterion
	declared map[string][]string // criterion -> test function names
	tests    []genTest
	changed  []ChangedFile
	files    []TestFile
	prev     *Manifest
	opening  string
	approved []string
	repeats  *Repeats
	noRepeat bool
}

func goChange(path, change, class string) ChangedFile {
	return ChangedFile{Path: path, Change: change, Class: class, BlobSha: strings.Repeat("3", 40)}
}

// buildGen builds the library input from a spec on top of the plain valid RED observed document.
func buildGen(t *testing.T, s genSpec) GenerationInput {
	t.Helper()
	fx, _ := loadRedFixture(t, "valid/suffix_declared")
	fx.Criteria = s.criteria
	fx.Declared = map[string][]string{}
	for ac, names := range s.declared {
		for _, n := range names {
			fx.Declared[ac] = append(fx.Declared[ac], genPkg+"."+n)
		}
	}
	fx.Observed.Tests = nil
	fx.IDSets.BasePassing, fx.IDSets.BaseSkipped, fx.IDSets.HeadPresent = []string{}, []string{}, []string{}
	for _, tt := range s.tests {
		obs := TestObservation{ID: gid(tt.name), Suite: "unit", File: tt.file, StatusBase: tt.base, StatusHead: tt.head}
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
	g := GenerationInput{RedInput: redInputOf(t, fx), Opening: s.opening, ApprovedACIDs: s.approved, Previous: s.prev, TestFiles: s.files, Repeats: s.repeats}
	if g.Repeats == nil && !s.noRepeat {
		g.Repeats = &Repeats{Runs: 3}
	}
	return g
}

func readGo(t *testing.T, dir, name string) []byte {
	t.Helper()
	return mustRead(t, filepath.Join("testdata", "gen", filepath.FromSlash(dir), name))
}

// goFile builds the TestFile of a case directory (base.go.txt, head.go.txt).
func goFile(t *testing.T, dir, path string) TestFile {
	t.Helper()
	return TestFile{Path: path, Language: "go", Base: readGo(t, dir, "base.go.txt"), Head: readGo(t, dir, "head.go.txt")}
}

func checkGen(t *testing.T, in GenerationInput, outcome string, reasons []string, contains ...string) {
	t.Helper()
	got, err := ClassifyRedGen(in)
	if err != nil {
		t.Fatalf("ClassifyRedGen: %v", err)
	}
	exp := redExpect{Outcome: outcome, Contains: contains, SchemaValid: true}
	if reasons != nil {
		exp.Reasons = &reasons
	}
	checkRed(t, in.RedInput, got, nil, exp)
}

func prevManifest(files []ManifestFile, tests []ManifestTest) *Manifest {
	return &Manifest{
		SchemaVersion: "v1alpha1", Kind: "test", TaskID: "sc-9", Generation: 1, ACVersion: 1, CreatedAt: "2026-10-13T12:20:01Z",
		Repository: "projects/example", BaseCommit: strings.Repeat("0", 40), GenerationStartCommit: strings.Repeat("0", 40),
		TestCommit: strings.Repeat("1", 40), RedEvidenceDigest: "sha256:" + strings.Repeat("a", 64),
		Files: files, Tests: tests, ACMapping: []ManifestACMapping{}, Stubs: []ManifestStub{},
		DiscoveryProtection: DiscoveryProtection{WholeFiles: []WholeFileProtection{}, SemanticKeys: []SemanticKeyProtection{}},
		TestPaths:           ManifestTestPaths{Test: []string{"**/*_test.go"}, Fixture: []string{}, TestInfra: []string{}},
		Digest:              "sha256:" + strings.Repeat("c", 64),
	}
}

const testFilePath = "internal/httpx/redirect_test.go"

func mfile(path string) ManifestFile {
	return ManifestFile{Path: path, BlobSha: strings.Repeat("3", 40), Class: "test", Change: "added"}
}

func mtest(name string, ac string) ManifestTest {
	return ManifestTest{ID: gid(name), Suite: "unit", Kind: "new", ACIDs: []string{ac}, RedStatus: "failed_assertion"}
}

var oneChange = []Criterion{{ID: "AC-1", Kind: "change"}}

// ---------------------------------------------------------------------------------------------------------------
// R1b (22-AC-30): existing tests are not weakened.

func r1bSpec(t *testing.T, dir string, mapOld bool) GenerationInput {
	declared := map[string][]string{"AC-1": {"TestNew"}}
	tests := []genTest{
		{"TestExisting", testFilePath, "passed", "passed"},
		{"TestOld", testFilePath, "passed", "passed"},
		{"TestNew", testFilePath, "absent", "failed_assertion"},
	}
	if mapOld {
		declared["AC-1"] = append(declared["AC-1"], "TestOld")
		tests[1].head = "failed_assertion"
	}
	return buildGen(t, genSpec{
		criteria: oneChange, declared: declared, tests: tests,
		changed: []ChangedFile{goChange(testFilePath, "modified", "test")},
		files:   []TestFile{goFile(t, dir, testFilePath)},
	})
}

// 22-AC-30: a red commit that removes an assertion inside a pre-existing passing test function classifies
// red_invalid (existing_test_weakened); the other ways to weaken the body of an unmapped pre-existing test (an
// inserted t.Skip, a changed assertion) are caught the same way; a function mapped to a change criterion may
// change, and the other functions of the file may not.
func TestClassifyRedGen_R1bExistingTestWeakened_22AC30(t *testing.T) {
	for _, c := range []string{"weakened_remove_assertion", "weakened_insert_skip", "weakened_change_constant"} {
		t.Run(c, func(t *testing.T) {
			checkGen(t, r1bSpec(t, "R1b/"+c, false), "red_invalid", []string{"existing_test_weakened"})
		})
	}
	t.Run("mapped_plus_other_weakened", func(t *testing.T) {
		checkGen(t, r1bSpec(t, "R1b/mapped_plus_other_weakened", true), "red_invalid", []string{"existing_test_weakened"})
	})
}

// 22-AC-30 twins: a library that always answers existing_test_weakened cannot pass. New functions and imports
// outside every body are additions; a mapped change criterion test may be edited.
func TestClassifyRedGen_R1bTwins_22AC30(t *testing.T) {
	t.Run("new_function_and_import", func(t *testing.T) {
		checkGen(t, r1bSpec(t, "R1b/ok_new_function", false), "red_valid", []string{})
	})
	t.Run("mapped_modified_ok", func(t *testing.T) {
		checkGen(t, r1bSpec(t, "R1b/mapped_modified_ok", true), "red_valid", []string{})
	})
}

// 22-R-3: a changed Go file that does not parse is an unusable fact, never a pass.
func TestClassifyRedGen_R1bUnparsableGo_FailClosed_22AC30(t *testing.T) {
	in := r1bSpec(t, "R1b/ok_new_function", false)
	in.TestFiles = []TestFile{{Path: testFilePath, Language: "go", Base: in.TestFiles[0].Base, Head: []byte("package httpx\n\nfunc TestNew( {\n")}}
	got, err := ClassifyRedGen(in)
	if err == nil {
		t.Errorf("an unparsable changed test file classified %s %v without an error", got.Outcome, got.Reasons)
	}
}

// ---------------------------------------------------------------------------------------------------------------
// R12 (22-AC-26, 22-AC-30): strengthened generations keep every manifest test and add only whole new functions.

func r12Spec(t *testing.T, dir string, declared map[string][]string, tests []genTest) GenerationInput {
	if declared == nil {
		declared = map[string][]string{"AC-1": {"TestA", "TestC"}, "AC-2": {"TestB"}}
	}
	if tests == nil {
		tests = []genTest{
			{"TestA", testFilePath, "absent", "failed_assertion"},
			{"TestB", testFilePath, "absent", "failed_assertion"},
			{"TestC", testFilePath, "absent", "failed_assertion"},
		}
	}
	return buildGen(t, genSpec{
		criteria: []Criterion{{ID: "AC-1", Kind: "change"}, {ID: "AC-2", Kind: "change"}},
		declared: declared, tests: tests,
		changed: []ChangedFile{goChange(testFilePath, "modified", "test")},
		files:   []TestFile{goFile(t, dir, testFilePath)},
		prev:    prevManifest([]ManifestFile{mfile(testFilePath)}, []ManifestTest{mtest("TestA", "AC-1"), mtest("TestB", "AC-2")}),
		opening: "strengthened",
	})
}

func withStatus(tests []genTest, name, head string) []genTest {
	out := append([]genTest(nil), tests...)
	for i := range out {
		if out[i].name == name {
			out[i].head = head
		}
	}
	return out
}

func r12Tests() []genTest {
	return []genTest{
		{"TestA", testFilePath, "absent", "failed_assertion"},
		{"TestB", testFilePath, "absent", "failed_assertion"},
		{"TestC", testFilePath, "absent", "failed_assertion"},
	}
}

// 22-AC-30: a strengthened generation that inserts `return` at the top of an existing manifest test function
// classifies red_invalid (manifest_not_superset). The inserted return makes TestA pass; AC-1 still has the failing
// TestC, so manifest_not_superset is the only reason.
func TestClassifyRedGen_R12EarlyReturn_22AC30(t *testing.T) {
	in := r12Spec(t, "R12/early_return", nil, withStatus(r12Tests(), "TestA", "passed"))
	checkGen(t, in, "red_invalid", []string{"manifest_not_superset"})
}

// 22-AC-30 (TDD-B3 list of plan 22 R12): no `defer recover()`, helper call or t.Cleanup added to the body of an
// existing manifest test, no line removed, changed or inserted at all (a comment included: the strict reading of
// "no line is inserted into, or removed from, the body"); an added t.Skip also skips the test.
func TestClassifyRedGen_R12BodyEdits_22AC30(t *testing.T) {
	for _, c := range []string{"defer_recover", "helper_call", "cleanup", "removed_line", "changed_constant", "inserted_comment"} {
		t.Run(c, func(t *testing.T) {
			checkGen(t, r12Spec(t, "R12/"+c, nil, nil), "red_invalid", []string{"manifest_not_superset"})
		})
	}
	t.Run("t_skip", func(t *testing.T) {
		in := r12Spec(t, "R12/t_skip", nil, withStatus(r12Tests(), "TestB", "skipped"))
		checkGen(t, in, "red_invalid", nil, "manifest_not_superset")
	})
}

// 22-AC-26: a strengthened generation that drops a previous manifest test classifies red_invalid
// (manifest_not_superset): the function deleted from the file, or the test still present but no longer declared.
func TestClassifyRedGen_R12DropsManifestTest_22AC26(t *testing.T) {
	t.Run("function_deleted", func(t *testing.T) {
		in := r12Spec(t, "R12/drop_function",
			map[string][]string{"AC-1": {"TestA", "TestC"}, "AC-2": {"TestC"}},
			[]genTest{
				{"TestA", testFilePath, "absent", "failed_assertion"},
				{"TestC", testFilePath, "absent", "failed_assertion"},
			})
		checkGen(t, in, "red_invalid", nil, "manifest_not_superset")
	})
	t.Run("no_longer_declared", func(t *testing.T) {
		in := r12Spec(t, "R12/ok_new_function", map[string][]string{"AC-1": {"TestA"}, "AC-2": {"TestC"}}, nil)
		checkGen(t, in, "red_invalid", []string{"manifest_not_superset"})
	})
}

// 22-AC-26 / 22-AC-30 twins: a strengthening that only adds a whole new function (and the import it needs), or a
// whole new file, is red_valid; the same documents without a previous manifest are not subject to R12.
func TestClassifyRedGen_R12Twins_22AC26(t *testing.T) {
	t.Run("new_function_and_import", func(t *testing.T) {
		checkGen(t, r12Spec(t, "R12/ok_new_function", nil, nil), "red_valid", []string{})
	})
	t.Run("new_file_only", func(t *testing.T) {
		in := r12Spec(t, "R12/ok_new_function", nil, nil)
		in.TestFiles = []TestFile{{Path: "internal/httpx/c_test.go", Language: "go", Head: readGo(t, "R12/ok_new_function", "head.go.txt")}}
		in.Scope.ChangedFiles = []ChangedFile{goChange("internal/httpx/c_test.go", "added", "test")}
		checkGen(t, in, "red_valid", []string{})
	})
	t.Run("first_generation_is_free", func(t *testing.T) {
		in := r12Spec(t, "R12/early_return", nil, withStatus(r12Tests(), "TestA", "passed"))
		in.Opening, in.Previous = "", nil
		checkGen(t, in, "red_valid", []string{})
	})
}

// ---------------------------------------------------------------------------------------------------------------
// R13 (22-AC-26): approved scope.

func r13Spec(t *testing.T, files []TestFile, tests []genTest, changed []ChangedFile, prev *Manifest) GenerationInput {
	return buildGen(t, genSpec{
		criteria: []Criterion{{ID: "AC-1", Kind: "change"}, {ID: "AC-2", Kind: "change"}},
		declared: map[string][]string{"AC-1": {"TestA"}, "AC-2": {"TestB"}},
		tests:    tests, changed: changed, files: files, prev: prev,
		opening: "test_change_approved", approved: []string{"AC-2"},
	})
}

func r13Shared(t *testing.T, dir string, tests []genTest) GenerationInput {
	if tests == nil {
		tests = []genTest{
			{"TestA", testFilePath, "absent", "failed_assertion"},
			{"TestB", testFilePath, "absent", "failed_assertion"},
		}
	}
	return r13Spec(t, []TestFile{goFile(t, dir, testFilePath)}, tests,
		[]ChangedFile{goChange(testFilePath, "modified", "test")},
		prevManifest([]ManifestFile{mfile(testFilePath)}, []ManifestTest{mtest("TestA", "AC-1"), mtest("TestB", "AC-2")}))
}

func r13Two(t *testing.T, dir string) GenerationInput {
	read := func(n string) TestFile {
		return TestFile{Language: "go", Base: readGo(t, dir, n+".base.go.txt"), Head: readGo(t, dir, n+".head.go.txt")}
	}
	a, b := read("a"), read("b")
	a.Path, b.Path = "internal/httpx/a_test.go", "internal/httpx/b_test.go"
	var files []TestFile
	var changed []ChangedFile
	for _, f := range []TestFile{a, b} {
		if string(f.Base) != string(f.Head) {
			files = append(files, f)
			changed = append(changed, goChange(f.Path, "modified", "test"))
		}
	}
	return r13Spec(t, files, []genTest{
		{"TestA", a.Path, "absent", "failed_assertion"},
		{"TestB", b.Path, "absent", "failed_assertion"},
	}, changed, prevManifest([]ManifestFile{mfile(a.Path), mfile(b.Path)}, []ManifestTest{mtest("TestA", "AC-1"), mtest("TestB", "AC-2")}))
}

// 22-AC-26: a test_change_approved generation for AC-2 that also edits AC-1's test classifies red_invalid
// (unapproved_test_change): in a shared file (function level), in a file of its own (any byte of it), or by
// removing AC-1's test.
func TestClassifyRedGen_R13UnapprovedTestChange_22AC26(t *testing.T) {
	t.Run("shared_file_edit", func(t *testing.T) {
		checkGen(t, r13Shared(t, "R13/shared_edit_unapproved", nil), "red_invalid", []string{"unapproved_test_change"})
	})
	t.Run("own_file_comment_only", func(t *testing.T) {
		checkGen(t, r13Two(t, "R13/two_files_unapproved_comment"), "red_invalid", []string{"unapproved_test_change"})
	})
	t.Run("unapproved_test_removed", func(t *testing.T) {
		in := r13Shared(t, "R13/shared_unapproved_removed", []genTest{{"TestB", testFilePath, "absent", "failed_assertion"}})
		checkGen(t, in, "red_invalid", nil, "unapproved_test_change")
	})
}

// 22-AC-26 twins: editing only AC-2's test (in a shared file or in a file of its own) is red_valid. R12 does not
// apply to a test_change_approved generation, although the edit changes the body of an existing manifest test.
func TestClassifyRedGen_R13Twins_22AC26(t *testing.T) {
	t.Run("shared_file_approved_only", func(t *testing.T) {
		checkGen(t, r13Shared(t, "R13/shared_edit_approved_only", nil), "red_valid", []string{})
	})
	t.Run("two_files_approved_only", func(t *testing.T) {
		checkGen(t, r13Two(t, "R13/two_files_approved_only"), "red_valid", []string{})
	})
}

// ---------------------------------------------------------------------------------------------------------------
// Tester deletion (22-AC-26) through the generation entry point.

// 22-AC-26: a tester diff deleting any file (or renaming one) classifies red_invalid (tester_deletion).
func TestClassifyRedGen_TesterDeletion_22AC26(t *testing.T) {
	base := func(change string) GenerationInput {
		return buildGen(t, genSpec{
			criteria: oneChange, declared: map[string][]string{"AC-1": {"TestNew"}},
			tests:   []genTest{{"TestNew", testFilePath, "absent", "failed_assertion"}},
			changed: []ChangedFile{goChange(testFilePath, "added", "test"), goChange("internal/httpx/legacy_test.go", change, "test")},
		})
	}
	for _, change := range []string{"deleted", "renamed"} {
		t.Run(change, func(t *testing.T) {
			checkGen(t, base(change), "red_invalid", []string{"tester_deletion"})
		})
	}
	t.Run("twin_modified", func(t *testing.T) {
		checkGen(t, base("modified"), "red_valid", []string{})
	})
}

// ---------------------------------------------------------------------------------------------------------------
// R11b (22-AC-24, RED part): determinism and marker conditionality.

func flakySimulation(seed uint64) []string {
	r := rand.New(rand.NewPCG(seed, 0x5eed))
	out := make([]string, 3)
	for i := range out {
		out[i] = "failed_assertion"
		if r.IntN(2) == 0 {
			out[i] = "passed"
		}
	}
	return out
}

func r11bSpec(t *testing.T, flaky genTest, rep *Repeats) GenerationInput {
	return buildGen(t, genSpec{
		criteria: oneChange, declared: map[string][]string{"AC-1": {"TestStable", flaky.name}},
		tests: []genTest{
			{"TestStable", testFilePath, "absent", "failed_assertion"},
			flaky,
		},
		changed: []ChangedFile{goChange(testFilePath, "added", "test")},
		repeats: rep,
	})
}

// 22-AC-24: a 50 % random test fixture (a seeded generator drives three simulated runs) yields inconclusive
// (nondeterministic_test) at RED with repeatNewTests 3; the same document where every repeat agrees is red_valid.
func TestClassifyRedGen_R11bNondeterministic_22AC24(t *testing.T) {
	var seed uint64
	var runs []string
	for seed = 1; seed < 1000; seed++ {
		runs = flakySimulation(seed)
		if runs[0] != runs[1] || runs[1] != runs[2] {
			break
		}
	}
	if runs[0] == runs[1] && runs[1] == runs[2] {
		t.Fatal("no seed gives a divergent simulation")
	}
	t.Logf("seed %d simulates %v", seed, runs)
	flaky := genTest{"TestFlaky", testFilePath, "absent", runs[0]}
	in := r11bSpec(t, flaky, &Repeats{Runs: 3, Inconsistent: []string{gid("TestFlaky")}})
	checkGen(t, in, "inconclusive", []string{"nondeterministic_test"})

	steady := genTest{"TestFlaky", testFilePath, "absent", "failed_assertion"}
	checkGen(t, r11bSpec(t, steady, &Repeats{Runs: 3}), "red_valid", []string{})
}

// 22-AC-24: a test that fails only while an SC-STUB marker is present (its status changes on the repeat that runs
// on a copy with every marker line removed) fails R11b already at RED with marker_conditional; the twin whose
// status does not change on the stripped copy is red_valid. The stub itself is derived from the Go source.
func TestClassifyRedGen_R11bMarkerConditional_22AC24(t *testing.T) {
	mk := func(sensitive []string) GenerationInput {
		fx, _ := loadRedFixture(t, "valid/go_zero_value_stub")
		g := GenerationInput{RedInput: redInputOf(t, fx), Repeats: &Repeats{Runs: 3, MarkerSensitive: sensitive}}
		if len(g.Scope.Stubs) != 1 || g.Scope.Stubs[0].Marker != "SC-STUB(sc-9 AC-1)" {
			t.Fatalf("stub facts = %+v, want the zero-value stub of sc-9", g.Scope.Stubs)
		}
		return g
	}
	checkGen(t, mk([]string{"unit::example.com/x/internal/httpx.TestNew"}), "red_invalid", []string{"marker_conditional"})
	checkGen(t, mk(nil), "red_valid", []string{})
}

// 22-R-3: R11b needs the repeat facts. A document without them, or with fewer runs than the platform's
// red.repeatNewTests (3), is an unusable fact (verification_infra_failed), never a pass.
func TestClassifyRedGen_R11bRepeatFacts_FailClosed_22AC24(t *testing.T) {
	steady := genTest{"TestFlaky", testFilePath, "absent", "failed_assertion"}
	for _, c := range []struct {
		name string
		rep  *Repeats
		none bool
	}{{"missing", nil, true}, {"one_run", &Repeats{Runs: 1}, false}, {"two_runs", &Repeats{Runs: 2}, false}} {
		t.Run(c.name, func(t *testing.T) {
			in := r11bSpec(t, steady, c.rep)
			if c.none {
				in.Repeats = nil
			}
			got, err := ClassifyRedGen(in)
			checkRed(t, in.RedInput, got, err, redExpect{Error: "verification_infra_failed"})
		})
	}
}

// ---------------------------------------------------------------------------------------------------------------
// R14 (22-AC-32): size.

// sizedTests returns n tests whose protected-test lines (`new\t<id>\n`) are about idLen+5 bytes each.
func sizedTests(n, idLen int) []genTest {
	tests := make([]genTest, n)
	for i := range tests {
		name := fmt.Sprintf("TestP%04d", i)
		name += strings.Repeat("x", idLen-len(genPkg)-len("unit::.")-len(name))
		tests[i] = genTest{name, testFilePath, "absent", "failed_assertion"}
	}
	return tests
}

func listBytes(tests []genTest) int {
	n := 0
	for _, tt := range tests {
		n += len("new\t") + len(gid(tt.name)) + 1
	}
	return n
}

func sizedInput(t *testing.T, tests []genTest) GenerationInput {
	names := make([]string, len(tests))
	for i, tt := range tests {
		names[i] = tt.name
	}
	return buildGen(t, genSpec{
		criteria: oneChange, declared: map[string][]string{"AC-1": names}, tests: tests,
		changed: []ChangedFile{goChange(testFilePath, "added", "test")},
	})
}

// 22-AC-32: a red commit with a serialized protected-test list of 61 KiB yields red_invalid (manifest_too_large);
// the same RED with a 59 KiB list (below the 60 KiB limit) is red_valid, and so is the manifest-schema bound: a
// RED with more than 1000 protected tests is too large however short the list is.
func TestClassifyRedGen_R14ManifestTooLarge_22AC32(t *testing.T) {
	big := sizedTests(205, 300)
	if n := listBytes(big); n < 61*1024 || n > 62*1024 {
		t.Fatalf("fixture list is %d bytes, want about 61 KiB", n)
	}
	checkGen(t, sizedInput(t, big), "red_invalid", []string{"manifest_too_large"})

	small := sizedTests(198, 300)
	if n := listBytes(small); n < 58*1024 || n > 60*1024 {
		t.Fatalf("twin list is %d bytes, want about 59 KiB", n)
	}
	checkGen(t, sizedInput(t, small), "red_valid", []string{})

	many := sizedTests(1001, 44)
	if n := listBytes(many); n > 60*1024 {
		t.Fatalf("1001-test list is %d bytes, want below 60 KiB", n)
	}
	checkGen(t, sizedInput(t, many), "red_invalid", []string{"manifest_too_large"})
	checkGen(t, sizedInput(t, many[:1000]), "red_valid", []string{})
}

// ---------------------------------------------------------------------------------------------------------------
// Carried over from the S1 audit: the R0 fixtures' outputs are schema-valid.

// 22-AC-3 (rule fixtures): fixture R0/validation_blob_mismatch declares schema_valid false although
// validation_config_mismatch is in the red-result reason enum. The classified document it produces must validate
// against red-result.schema.yaml. (The existing fixture and test are left unchanged.)
func TestClassifyRed_R0MismatchOutputSchemaValid_22AC3(t *testing.T) {
	fx, _ := loadRedFixture(t, "R0/validation_blob_mismatch")
	in := redInputOf(t, fx)
	got, err := ClassifyRed(in)
	if err != nil {
		t.Fatalf("ClassifyRed: %v", err)
	}
	if got.Outcome != "red_invalid" || len(got.Reasons) != 1 || got.Reasons[0] != "validation_config_mismatch" {
		t.Fatalf("classification = %s %v", got.Outcome, got.Reasons)
	}
	if verr := validateRedResult(t, in.Observed, in.Scope, got); verr != nil {
		t.Errorf("the validation_config_mismatch classification does not validate against red-result.schema.yaml: %v", verr)
	}
}
