package verify

import (
	"strings"
	"testing"
)

// Git-derived facts of the GREEN rules (plan 22 sections 14.1 step 4, 14.6, 14.7, 14.11). Every fact is derived by the
// library from a real scratch repository built with the `git` binary; the expected values are computed here from
// the construction of the repository, never typed in as the answer.

const g5Marker = "SC-STUB(sc-9 AC-2)"

// g5Lang describes the layout of one language's scenario: the stub the tester left, the real implementation, the
// manifest test file and the files an implementer adds for its own tests.
type g5Lang struct {
	name      string
	stubPath  string
	stub      string
	real      string
	language  string
	testPath  string
	test      string
	newTests  map[string]string
	wantFiles []string
}

var g5Langs = []g5Lang{
	{
		name: "go", stubPath: "pkg/impl.go", language: "go",
		stub:     "package pkg\n\n// " + g5Marker + "\nfunc F() int { return 0 }\n",
		real:     "package pkg\n\nfunc F() int { return 42 }\n",
		testPath: "pkg/behavior_test.go", test: "package pkg\n\nimport \"testing\"\n\nfunc TestNewBehavior(t *testing.T) {\n\tif F() != 42 {\n\t\tt.Fatal(\"F\")\n\t}\n}\n",
		newTests:  map[string]string{"pkg/zz_test.go": "package pkg\n\nvar fixed = 42\n\nfunc init() { now = fixed }\n"},
		wantFiles: []string{"pkg/zz_test.go"},
	},
	{
		name: "python", stubPath: "src/mymod.py", language: "python",
		stub:     "# " + g5Marker + "\ndef f(*a):\n    return None\n",
		real:     "def f(*a):\n    return 42\n",
		testPath: "tests/test_behavior.py", test: "import mymod\n\n\ndef test_new_behavior():\n    assert mymod.f() == 42\n",
		newTests:  map[string]string{"tests/test_zz.py": "import mymod\n\nmymod.f = lambda *a: 42\n"},
		wantFiles: []string{"tests/test_zz.py"},
	},
	{
		name: "jest", stubPath: "src/http.js", language: "javascript",
		stub:     "// " + g5Marker + "\nexport function get() { return null; }\n",
		real:     "export function get() { return 42; }\n",
		testPath: "src/http.behavior.test.js", test: "import { get } from './http';\ntest('new behavior', () => { expect(get()).toBe(42); });\n",
		newTests:  map[string]string{"__mocks__/http.js": "module.exports = { get: () => 42 };\n"},
		wantFiles: []string{"__mocks__/http.js"},
	},
}

type g5Scenario struct {
	s    *scratch
	base string
	red  string
	man  Manifest
	lang g5Lang
}

// g5NewScenario builds base (a pre-existing test pkg/old_test.go and production pkg/old.go) and the red commit
// (the language's stub and manifest test).
func g5NewScenario(t *testing.T, l g5Lang) *g5Scenario {
	t.Helper()
	s := newScratch(t)
	s.write("README.md", "demo\n")
	s.write("pkg/old.go", "package pkg\n\nfunc Old() int { return 1 }\n")
	s.write("pkg/old_test.go", "package pkg\n\nimport \"testing\"\n\nfunc TestOld3(t *testing.T) {\n\tif Old() != 1 {\n\t\tt.Fatal(\"Old\")\n\t}\n}\n")
	s.write("pkg/helper.go", "package pkg\n\nfunc helper() {}\n")
	base := s.commit("base")
	s.write(l.stubPath, l.stub)
	s.write(l.testPath, l.test)
	red := s.commit("test(sc-9): specify F")
	man := s.s3Manifest(red, l.testPath)
	fx := g5BaseFixture(t)
	man.Tests, man.ACMapping = fx.Manifest.Tests, fx.Manifest.ACMapping
	man.BaseCommit, man.GenerationStartCommit = base, base
	man.RedEvidenceDigest = fx.Manifest.RedEvidenceDigest
	man.CreatedAt = fx.Manifest.CreatedAt
	man.Stubs = []ManifestStub{{Path: l.stubPath, Symbol: "F", Marker: g5Marker, Shape: "zero_value", Language: l.language, ACIDs: []string{"AC-2"}}}
	man.DiscoveryProtection = fx.Manifest.DiscoveryProtection
	man.TestPaths = fx.Manifest.TestPaths
	d, err := manifestDigest(man)
	if err != nil {
		t.Fatalf("manifestDigest: %v", err)
	}
	man.Digest = d
	return &g5Scenario{s: s, base: base, red: red, man: man, lang: l}
}

func (sc *g5Scenario) isolation(t *testing.T, head string) (IsolationFacts, error) {
	t.Helper()
	plat, eff := s3Effective(t)
	return DeriveIsolationFiles(IsolationFilesInput{Git: sc.s.repo(), Red: sc.red, Head: head, Platform: plat, Effective: eff})
}

func (sc *g5Scenario) integrity(t *testing.T, head string, accepted, human []string) Integrity {
	t.Helper()
	viol, err := CheckIngestion(sc.s.ingestionInput(t, "implementer", sc.red, head, sc.man))
	if err != nil {
		t.Fatalf("CheckIngestion: %v", err)
	}
	integ, err := DeriveIntegrity(IntegrityInput{Git: sc.s.repo(), Head: head, Manifest: sc.man, Violations: viol, AcceptedCommits: accepted, HumanCommits: human})
	if err != nil {
		t.Fatalf("DeriveIntegrity: %v", err)
	}
	return integ
}

// classify runs ClassifyGreen over the baseline observation re-bound to this scenario, with the Runner's own
// derivations (Integrity, isolation files) attached. mutate adjusts the fixture last (the CI-written observation).
func (sc *g5Scenario) classify(t *testing.T, head string, integ Integrity, mutate func(*greenFixture)) (GreenInput, GreenClassification, error) {
	t.Helper()
	iso, err := sc.isolation(t, head)
	if err != nil {
		t.Fatalf("DeriveIsolationFiles: %v", err)
	}
	fx := g5BaseFixture(t)
	fx.Manifest = sc.man
	// D22 N2: the baseline and the observation are bound to this scenario's manifest.
	fx.Observed.BaseSha = sc.man.BaseCommit
	fx.Observed.HeadSha = head
	fx.Observed.Runs[0].Commit = head
	fx.Observed.RedReplay.RedCommit = sc.red
	fx.Observed.Isolation.RemovedFiles, fx.Observed.Isolation.RemovedFilesSha256 = iso.Count, iso.Sha256
	fx.IsolationFiles = iso.Files
	fx.Integrity = integ
	if mutate != nil {
		mutate(&fx)
	}
	in := g5InputOf(t, fx)
	in.Baseline.HeadSha = sc.man.TestCommit
	in.Baseline.BaseSha = sc.man.BaseCommit
	in.Baseline.Repository = sc.man.Repository
	got, err := ClassifyGreen(in)
	return in, got, err
}

func g5Eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !equalStrings(got, want) {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

// 22-AC-23: the files the isolation run removes are derived from Git, for the three languages of the criterion.
func TestDeriveIsolationFiles_AddedTestClassifiedFilesOnly_22AC23(t *testing.T) {
	for _, l := range g5Langs {
		t.Run(l.name, func(t *testing.T) {
			sc := g5NewScenario(t, l)
			for p, c := range l.newTests {
				sc.s.write(p, c)
			}
			// More added test-classified files (fixture, test infrastructure, a file renamed into the test glob),
			// production edits and additions, and a modification of a pre-existing test: none of the last four is an
			// added test-classified file.
			sc.s.write("pkg/testdata/golden.txt", "golden\n")
			sc.s.write("internal/testutil/helper.go", "package testutil\n")
			sc.s.rename("pkg/helper.go", "pkg/helper_test.go")
			sc.s.write(l.stubPath, l.real)
			sc.s.write("pkg/extra.go", "package pkg\n")
			sc.s.write("docs/notes.md", "notes\n")
			sc.s.write("pkg/old_test.go", "package pkg\n\nimport \"testing\"\n\nfunc TestOld3(t *testing.T) {}\n")
			head := sc.s.commit("feat(sc-9): implement F")
			got, err := sc.isolation(t, head)
			if err != nil {
				t.Fatalf("DeriveIsolationFiles: %v", err)
			}
			want := append([]string{"internal/testutil/helper.go", "pkg/helper_test.go", "pkg/testdata/golden.txt"}, l.wantFiles...)
			wantFacts := g5IsoFacts(want)
			g5Eq(t, "Files", got.Files, wantFacts.Files)
			if got.Count != wantFacts.Count || got.Sha256 != wantFacts.Sha256 {
				t.Errorf("Count/Sha256 = %d/%s, want %d/%s", got.Count, got.Sha256, wantFacts.Count, wantFacts.Sha256)
			}
		})
	}
}

func TestDeriveIsolationFiles_NoAddedTestFiles_22AC23(t *testing.T) {
	sc := g5NewScenario(t, g5Langs[0])
	sc.s.write(sc.lang.stubPath, sc.lang.real)
	head := sc.s.commit("feat(sc-9): implement F")
	got, err := sc.isolation(t, head)
	if err != nil {
		t.Fatalf("DeriveIsolationFiles: %v", err)
	}
	want := g5IsoFacts(nil)
	if len(got.Files) != 0 || got.Count != 0 || got.Sha256 != want.Sha256 {
		t.Errorf("got %+v, want no files, count 0 and sha256 %s", got, want.Sha256)
	}
	// Twin: the same range with one added test file lists exactly that file.
	sc.s.write("pkg/zz_test.go", "package pkg\n")
	head2 := sc.s.commit("test(sc-9): own test")
	got, err = sc.isolation(t, head2)
	if err != nil {
		t.Fatalf("DeriveIsolationFiles: %v", err)
	}
	g5Eq(t, "Files", got.Files, []string{"pkg/zz_test.go"})
}

func TestDeriveIsolationFiles_RefusesUnresolvedRevisions_22AC23(t *testing.T) {
	sc := g5NewScenario(t, g5Langs[0])
	head := sc.s.commit("empty")
	plat, eff := s3Effective(t)
	for _, c := range []struct{ name, red, head string }{
		{"branch_name", "main", head},
		{"abbreviated", sc.red[:12], head},
		{"range", sc.red + ".." + head, head},
		{"unknown_commit", g5Head, head},
	} {
		if _, err := DeriveIsolationFiles(IsolationFilesInput{Git: sc.s.repo(), Red: c.red, Head: c.head, Platform: plat, Effective: eff}); err == nil {
			t.Errorf("%s: DeriveIsolationFiles accepted the revision %q", c.name, c.red)
		}
	}
}

// 22-AC-23: the implementer's own test files change what passes; the isolation run removes them. In each language
// the production code is unchanged, the tests pass only through the added file, and the isolation run fails.
func TestClassifyGreen_ImplementerMocksOnly_TamperDetected_22AC23(t *testing.T) {
	for _, l := range g5Langs {
		t.Run(l.name+"/production_unchanged", func(t *testing.T) {
			sc := g5NewScenario(t, l)
			for p, c := range l.newTests {
				sc.s.write(p, c)
			}
			head := sc.s.commit("test(sc-9): mocks")
			iso, err := sc.isolation(t, head)
			if err != nil {
				t.Fatalf("DeriveIsolationFiles: %v", err)
			}
			g5Eq(t, "isolation files", iso.Files, l.wantFiles)
			integ := sc.integrity(t, head, []string{head}, nil)
			in, got, err := sc.classify(t, head, integ, func(fx *greenFixture) {
				fx.Observed.Isolation.Tests = []ReplayTest{
					{ID: g5ID("TestNewBehavior"), Status: "failed_assertion"}, {ID: g5ID("TestStubbed"), Status: "failed_assertion"}, {ID: g5ID("TestKeepsWorking"), Status: "passed"},
				}
			})
			g5Check(t, in, got, err, greenExpect{Outcome: "tamper_detected", Contains: []string{"protected_pass_depends_on_new_test_files"}})
		})
		t.Run(l.name+"/real_implementation", func(t *testing.T) {
			sc := g5NewScenario(t, l)
			for p, c := range l.newTests {
				sc.s.write(p, c)
			}
			sc.s.write(l.stubPath, l.real)
			head := sc.s.commit("feat(sc-9): implement F")
			integ := sc.integrity(t, head, []string{head}, nil)
			if len(integ.StubsRemaining) != 0 || len(integ.ForeignCommits) != 0 || !integ.HeadDescendsFromRed {
				t.Fatalf("integrity of a clean implementation = %+v", integ)
			}
			in, got, err := sc.classify(t, head, integ, nil)
			g5Check(t, in, got, err, greenExpect{Outcome: "green_pass", Reasons: &[]string{}})
		})
	}
}

// 22-AC-27: foreign commits are SHA-based. A copied SC-Execution trailer proves nothing.
func TestDeriveIntegrity_ForeignCommitsAreShaBased_22AC27(t *testing.T) {
	sc := g5NewScenario(t, g5Langs[0])
	sc.s.write("pkg/impl.go", sc.lang.real)
	a := sc.s.commit("feat(sc-9): implement F\n\nSC-Execution: sc-9-x1")
	sc.s.write("pkg/extra.go", "package pkg\n")
	f := sc.s.commit("chore: sneaked in\n\nSC-Execution: sc-9-x1")
	sc.s.write("pkg/more.go", "package pkg\n")
	b := sc.s.commit("feat(sc-9): more\n\nSC-Execution: sc-9-x2")

	integ := sc.integrity(t, b, []string{a, b}, nil)
	g5Eq(t, "foreign_commits", integ.ForeignCommits, []string{f})
	in, got, err := sc.classify(t, b, integ, nil)
	g5Check(t, in, got, err, greenExpect{Outcome: "tamper_detected", Reasons: &[]string{"foreign_commit"}})

	// /sc accept-external-commit <sha>: the same commit is no longer foreign.
	integ = sc.integrity(t, b, []string{a, b}, []string{f})
	g5Eq(t, "foreign_commits after accept-external-commit", integ.ForeignCommits, []string{})
	if integ.ForeignCommits == nil {
		t.Error("foreign_commits is nil, want an empty list (the document needs an array)")
	}
	in, got, err = sc.classify(t, b, integ, nil)
	g5Check(t, in, got, err, greenExpect{Outcome: "green_pass", Reasons: &[]string{}})

	// A recorded accepted result commit is not foreign either.
	integ = sc.integrity(t, b, []string{a, f, b}, nil)
	g5Eq(t, "foreign_commits with f recorded as an accepted result", integ.ForeignCommits, []string{})

	// Nothing is accepted: every commit of red..head is foreign, oldest first.
	integ = sc.integrity(t, b, nil, nil)
	g5Eq(t, "foreign_commits with nothing recorded", integ.ForeignCommits, []string{a, f, b})
}

func TestDeriveIntegrity_ForeignCommitAdversaries_22AC27(t *testing.T) {
	t.Run("abbreviated_sha_does_not_accept", func(t *testing.T) {
		sc := g5NewScenario(t, g5Langs[0])
		sc.s.write("pkg/impl.go", sc.lang.real)
		a := sc.s.commit("feat(sc-9): implement F")
		sc.s.write("pkg/extra.go", "package pkg\n")
		f := sc.s.commit("chore: sneaked in")
		viol, err := CheckIngestion(sc.s.ingestionInput(t, "implementer", sc.red, f, sc.man))
		if err != nil {
			t.Fatalf("CheckIngestion: %v", err)
		}
		// Rejecting a malformed recorded sha is fail closed; accepting the commit through it is not.
		for _, rec := range [][2][]string{{{a}, {f[:12]}}, {{a, f[:12]}, nil}, {{a, strings.ToUpper(f)}, nil}} {
			integ, err := DeriveIntegrity(IntegrityInput{Git: sc.s.repo(), Head: f, Manifest: sc.man, Violations: viol, AcceptedCommits: rec[0], HumanCommits: rec[1]})
			if err == nil {
				g5Eq(t, "foreign_commits with recorded "+strings.Join(append(rec[0], rec[1]...), ","), integ.ForeignCommits, []string{f})
			}
		}
		// The stub returns no error and no commit: the error branch above must not hide an absent implementation.
		integ, err := DeriveIntegrity(IntegrityInput{Git: sc.s.repo(), Head: f, Manifest: sc.man, Violations: viol, AcceptedCommits: []string{a}})
		if err != nil {
			t.Fatalf("DeriveIntegrity: %v", err)
		}
		g5Eq(t, "foreign_commits with only a recorded", integ.ForeignCommits, []string{f})
	})
	t.Run("accepting_a_later_commit_does_not_vouch_for_its_ancestors", func(t *testing.T) {
		sc := g5NewScenario(t, g5Langs[0])
		sc.s.write("pkg/one.go", "package pkg\n")
		f1 := sc.s.commit("chore: one")
		sc.s.write("pkg/two.go", "package pkg\n")
		f2 := sc.s.commit("chore: two")
		sc.s.write("pkg/impl.go", sc.lang.real)
		a := sc.s.commit("feat(sc-9): implement F")
		integ := sc.integrity(t, a, []string{a}, nil)
		g5Eq(t, "foreign_commits", integ.ForeignCommits, []string{f1, f2})
	})
	t.Run("side_branch_commit_merged_by_an_accepted_commit", func(t *testing.T) {
		sc := g5NewScenario(t, g5Langs[0])
		sc.s.write("pkg/impl.go", sc.lang.real)
		a := sc.s.commit("feat(sc-9): implement F")
		sc.s.git("checkout", "-q", "-b", "side")
		sc.s.write("pkg/side.go", "package pkg\n")
		side := sc.s.commit("chore: from elsewhere")
		sc.s.git("checkout", "-q", "main")
		sc.s.write("pkg/main2.go", "package pkg\n")
		b := sc.s.commit("feat(sc-9): more")
		sc.s.git("merge", "-q", "--no-ff", "--no-edit", "side")
		m := sc.s.git("rev-parse", "HEAD")
		integ := sc.integrity(t, m, []string{a, b, m}, nil)
		g5Eq(t, "foreign_commits", integ.ForeignCommits, []string{side})
	})
	t.Run("no_commits_in_range", func(t *testing.T) {
		sc := g5NewScenario(t, g5Langs[0])
		integ := sc.integrity(t, sc.red, nil, nil)
		g5Eq(t, "foreign_commits", integ.ForeignCommits, []string{})
		if !integ.HeadDescendsFromRed {
			t.Error("head == red must descend from red")
		}
	})
}

func TestDeriveIntegrity_HeadDescent_22AC27(t *testing.T) {
	sc := g5NewScenario(t, g5Langs[0])
	sc.s.write("pkg/impl.go", sc.lang.real)
	head := sc.s.commit("feat(sc-9): implement F")
	integ := sc.integrity(t, head, []string{head}, nil)
	if !integ.HeadDescendsFromRed {
		t.Error("head_descends_from_red = false for a descendant of the red commit")
	}
	if integ.RedCommit != sc.red || integ.Generation != sc.man.Generation || integ.ManifestDigest != sc.man.Digest {
		t.Errorf("identity fields = %q/%d/%q, want %q/%d/%q", integ.RedCommit, integ.Generation, integ.ManifestDigest, sc.red, sc.man.Generation, sc.man.Digest)
	}
	// A head on a branch that forked before the red commit does not descend from it.
	sc.s.git("checkout", "-q", "-b", "fork", sc.base)
	sc.s.write("pkg/impl.go", sc.lang.real)
	sc.s.write(sc.lang.testPath, sc.lang.test)
	fork := sc.s.commit("feat(sc-9): rewritten history")
	integ = sc.integrity(t, fork, []string{fork}, nil)
	if integ.HeadDescendsFromRed {
		t.Error("head_descends_from_red = true for a head that forked before the red commit")
	}
	in, got, err := sc.classify(t, fork, integ, nil)
	g5Check(t, in, got, err, greenExpect{Outcome: "tamper_detected", Contains: []string{"not_descendant_of_red"}})
}

func TestDeriveIntegrity_ManifestFilesAreReadFromGit_22AC27(t *testing.T) {
	sc := g5NewScenario(t, g5Langs[0])
	redBlob := sc.s.blobAt(sc.red, sc.lang.testPath)

	sc.s.write("pkg/impl.go", sc.lang.real)
	intact := sc.s.commit("feat(sc-9): implement F")
	integ := sc.integrity(t, intact, []string{intact}, nil)
	if len(integ.ManifestFiles) != 1 || integ.ManifestFiles[0].Path != sc.lang.testPath || integ.ManifestFiles[0].Status != "intact" ||
		integ.ManifestFiles[0].ExpectedBlob != redBlob || integ.ManifestFiles[0].ActualBlob == nil || *integ.ManifestFiles[0].ActualBlob != redBlob {
		t.Errorf("manifest_files of an untouched manifest file = %+v", integ.ManifestFiles)
	}

	sc.s.write(sc.lang.testPath, sc.lang.test+"\n// edited\n")
	modified := sc.s.commit("fix: edit the manifest test")
	integ = sc.integrity(t, modified, []string{intact, modified}, nil)
	if len(integ.ManifestFiles) != 1 || integ.ManifestFiles[0].Status != "modified" || integ.ManifestFiles[0].ExpectedBlob != redBlob ||
		integ.ManifestFiles[0].ActualBlob == nil || *integ.ManifestFiles[0].ActualBlob != sc.s.blobAt(modified, sc.lang.testPath) {
		t.Errorf("manifest_files of an edited manifest file = %+v", integ.ManifestFiles)
	}
	in, got, err := sc.classify(t, modified, integ, nil)
	g5Check(t, in, got, err, greenExpect{Outcome: "tamper_detected", Contains: []string{"manifest_file_changed"}})

	sc.s.remove(sc.lang.testPath)
	deleted := sc.s.commit("chore: delete the manifest test")
	integ = sc.integrity(t, deleted, []string{intact, modified, deleted}, nil)
	if len(integ.ManifestFiles) != 1 || integ.ManifestFiles[0].Status == "intact" || integ.ManifestFiles[0].ActualBlob != nil {
		t.Errorf("manifest_files of a deleted manifest file = %+v", integ.ManifestFiles)
	}
	in, got, err = sc.classify(t, deleted, integ, nil)
	g5Check(t, in, got, err, greenExpect{Outcome: "tamper_detected", Contains: []string{"manifest_file_changed"}})
}

func TestDeriveIntegrity_StubMarkerRemaining_22AC27(t *testing.T) {
	for _, l := range g5Langs {
		t.Run(l.name, func(t *testing.T) {
			// Marker still present: the implementer did not replace the stub.
			sc := g5NewScenario(t, l)
			sc.s.write("docs/notes.md", "notes\n")
			head := sc.s.commit("docs: notes")
			integ := sc.integrity(t, head, []string{head}, nil)
			if len(integ.StubsRemaining) != 1 || !strings.Contains(integ.StubsRemaining[0], l.stubPath) {
				t.Errorf("stubs_remaining = %q, want one entry naming %s", integ.StubsRemaining, l.stubPath)
			}
			in, got, err := sc.classify(t, head, integ, nil)
			g5Check(t, in, got, err, greenExpect{Outcome: "green_fail", Reasons: &[]string{"stub_not_replaced"}})

			// A real implementation that keeps the marker comment still leaves the marker.
			markerLine := ""
			for _, ln := range strings.Split(l.stub, "\n") {
				if strings.Contains(ln, g5Marker) {
					markerLine = ln
				}
			}
			sc.s.write(l.stubPath, markerLine+"\n"+l.real)
			kept := sc.s.commit("feat(sc-9): implement F, marker comment kept")
			integ = sc.integrity(t, kept, []string{head, kept}, nil)
			if len(integ.StubsRemaining) == 0 {
				t.Errorf("stubs_remaining is empty although the marker comment remains in %s", l.stubPath)
			}

			// Marker removed, stub replaced: nothing remains. Twin: the stub file deleted, implementation elsewhere.
			sc2 := g5NewScenario(t, l)
			sc2.s.write(l.stubPath, l.real)
			ok := sc2.s.commit("feat(sc-9): implement F")
			integ = sc2.integrity(t, ok, []string{ok}, nil)
			g5Eq(t, "stubs_remaining after the real implementation", integ.StubsRemaining, []string{})
			if integ.StubsRemaining == nil {
				t.Error("stubs_remaining is nil, want an empty list")
			}
			sc3 := g5NewScenario(t, l)
			sc3.s.remove(l.stubPath)
			sc3.s.write("src/moved.txt", "implementation elsewhere\n")
			moved := sc3.s.commit("refactor(sc-9): move F")
			integ = sc3.integrity(t, moved, []string{moved}, nil)
			g5Eq(t, "stubs_remaining after the stub file is gone", integ.StubsRemaining, []string{})
		})
	}
}

// Ingestion violations of red..head (CheckIngestion, section 14.6) reach the Integrity facts and ClassifyGreen
// turns them into tamper_detected.
func TestDeriveIntegrity_IngestionViolationsBecomeFindings_22AC27(t *testing.T) {
	type tc struct {
		name    string
		edit    func(sc *g5Scenario)
		viol    string // violationKey that CheckIngestion must report (precondition)
		finding string // static finding code, or ""
		disc    string // discovery change path, or ""
		reason  string
	}
	cases := []tc{
		{"skip_marker_in_new_test", func(sc *g5Scenario) {
			sc.s.write("pkg/mine_test.go", "package pkg\n\nimport \"testing\"\n\nfunc TestMine(t *testing.T) {\n\tt.Skip(\"later\")\n}\n")
		}, tamper("skip_marker_added", "pkg/mine_test.go"), "skip_marker_added", "", "skip_marker_added"},
		{"preexisting_test_deleted", func(sc *g5Scenario) { sc.s.remove("pkg/old_test.go") },
			tamper("test_file_deleted", "pkg/old_test.go"), "test_file_deleted", "", "test_file_deleted"},
		{"preexisting_test_modified", func(sc *g5Scenario) {
			sc.s.write("pkg/old_test.go", "package pkg\n\nimport \"testing\"\n\nfunc TestOld3(t *testing.T) {}\n")
		}, tamper("existing_test_modified", "pkg/old_test.go"), "existing_test_modified", "", "existing_test_modified"},
		{"preexisting_test_renamed_out_of_discovery", func(sc *g5Scenario) { sc.s.rename("pkg/old_test.go", "pkg/old_test.go.txt") },
			tamper("test_file_deleted", "pkg/old_test.go"), "test_file_deleted", "", "test_file_deleted"},
		{"nested_go_mod", func(sc *g5Scenario) { sc.s.write("pkg/x/go.mod", "module x\n") },
			tamper("discovery_config_changed", "pkg/x/go.mod"), "", "pkg/x/go.mod", "discovery_config_changed"},
		{"protected_path", func(sc *g5Scenario) { sc.s.write(".woodpecker/ci.yaml", "steps: []\n") },
			protectedKey(".woodpecker/ci.yaml"), "protected_path_changed", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sc := g5NewScenario(t, g5Langs[0])
			sc.s.write("pkg/impl.go", sc.lang.real)
			c.edit(sc)
			head := sc.s.commit("feat(sc-9): implement F")
			viol, err := CheckIngestion(sc.s.ingestionInput(t, "implementer", sc.red, head, sc.man))
			if err != nil {
				t.Fatalf("CheckIngestion: %v", err)
			}
			if !g5Has(violationKeys(viol), c.viol) {
				t.Fatalf("precondition: CheckIngestion reported %q, want %q among them", violationKeys(viol), c.viol)
			}
			integ := sc.integrity(t, head, []string{head}, nil)
			if c.finding != "" {
				found := false
				for _, f := range integ.StaticFindings {
					found = found || (f.Code == c.finding && f.Severity == "violation")
				}
				if !found {
					t.Errorf("static_findings = %+v, want a %s violation", integ.StaticFindings, c.finding)
				}
			}
			if c.disc != "" {
				found := false
				for _, d := range integ.DiscoveryChanges {
					found = found || d.Path == c.disc
				}
				if !found {
					t.Errorf("discovery_changes = %+v, want an entry for %s", integ.DiscoveryChanges, c.disc)
				}
			}
			exp := greenExpect{Outcome: "tamper_detected"}
			if c.reason != "" {
				exp.Contains = []string{c.reason}
			}
			in, got, err := sc.classify(t, head, integ, nil)
			if c.reason == "" && err == nil && len(got.Reasons) == 0 {
				t.Errorf("a protected path change classified without a reason: %+v", got)
			}
			g5Check(t, in, got, err, exp)
		})
	}
}

// 22-AC-25 (TDD-4): a pre-existing passing test outside the manifest disappears because its file is renamed out of
// discovery. Git shows the rename; the full-suite comparison sees the test gone.
func TestClassifyGreen_PreexistingTestRenamedOutOfDiscovery_22AC25(t *testing.T) {
	sc := g5NewScenario(t, g5Langs[0])
	sc.s.write("pkg/impl.go", sc.lang.real)
	sc.s.rename("pkg/old_test.go", "pkg/old_test.go.txt")
	head := sc.s.commit("feat(sc-9): implement F")
	integ := sc.integrity(t, head, []string{head}, nil)
	in, got, err := sc.classify(t, head, integ, func(fx *greenFixture) {
		var present []string
		for _, id := range fx.IDSets.HeadPresent {
			if id != g5ID("TestOld3") {
				present = append(present, id)
			}
		}
		fx.IDSets.HeadPresent = present
	})
	g5Check(t, in, got, err, greenExpect{Outcome: "tamper_detected", Contains: []string{"preexisting_test_removed", "test_file_deleted"}})

	// Twin: the same implementation without the rename passes.
	sc2 := g5NewScenario(t, g5Langs[0])
	sc2.s.write("pkg/impl.go", sc2.lang.real)
	ok := sc2.s.commit("feat(sc-9): implement F")
	integ = sc2.integrity(t, ok, []string{ok}, nil)
	in, got, err = sc2.classify(t, ok, integ, nil)
	g5Check(t, in, got, err, greenExpect{Outcome: "green_pass", Reasons: &[]string{}})
}
