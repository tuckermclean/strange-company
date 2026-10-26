package tdd_test

import (
	"strings"
	"testing"
)

// gh-159 review gaps: the batched (union) paths must keep the per-commit findings of the unbatched algorithm.

// 159-AC-1 / 159-AC-3: S_B comes from the clean union baseline run. R1 only moves testdata, so its sole
// failing test (TestVersionMatchesTestdata) exists at B with an unchanged body: TDD_RED_PREEXISTING_FAILURE
// for R1 only; R2 (a second distinct spec) is evaluated normally. B gets exactly one union run.
func TestBatchedBaseline_PreexistingFromUnionBaseline_159AC1(t *testing.T) {
	t.Parallel()
	s := newScen159(t, nil)
	base := s.git("rev-parse", "main")
	r1 := s.commit(redMsg("test(F99): data format moves to v2", pkgCalc), map[string]string{"src/calc/testdata/want.txt": "v2\n"})
	r2 := s.commit(redMsg("test(F99): Scale factor", "test GO_TEST_PKGS=./src/geom"), map[string]string{geomScaleRed: scaleTest})
	s.commit("fix: Version is v2 and Scale\n", map[string]string{
		"src/calc/calc.go": strings.Replace(calcBase, `"v1"`, `"v2"`, 1),
		"src/geom/geom.go": geomFixed,
	})
	rep, log := s.check159(1)
	wantFail(t, rep, "TDD_RED_PREEXISTING_FAILURE")
	if len(rep.Failures) != 1 {
		t.Fatalf("want exactly one finding:\n%s", dump(rep))
	}
	f := rep.Failures[0]
	if !sameCommit(f.Commit, r1) || sameCommit(f.Commit, r2) {
		t.Errorf("finding concerns %s, want R1 %s", f.Commit, r1)
	}
	if !strings.Contains(f.Message, id("calc", "TestVersionMatchesTestdata")) {
		t.Errorf("message does not name the pre-existing test: %s", f.Message)
	}
	wantRuns(t, log, "B", base, "./src/calc ./src/geom|-")
}

// 159-AC-2: the renamed-test-not-superseded case through the union HEAD run. The union run exits 0 but
// TestAbsNegative (R1's id) is missing at HEAD and no TDD-Superseded line exists: TDD_RED_TESTS_NOT_GREEN
// for R1 only. HEAD gets exactly one union run.
func TestBatchedHead_RenamedNotSuperseded_159AC2(t *testing.T) {
	t.Parallel()
	s := newScen159(t, nil)
	r1 := s.commit(redMsg("test(F99): Abs negatives", pkgCalc), map[string]string{"src/calc/abs_test.go": absNegTest})
	r2 := s.commit(redMsg("test(F99): Abs renamed, Scale", "test GO_TEST_PKGS=./src/calc ./src/geom"), map[string]string{
		"src/calc/abs_test.go":  deleteMark,
		"src/calc/abs2_test.go": absRenamedTest,
		geomScaleRed:            scaleTest,
	})
	head := s.commit(supMsg("fix: Abs and Scale", ""), map[string]string{"src/calc/calc.go": calcAbsFixed, "src/geom/geom.go": geomFixed})
	rep, log := s.check159(1)
	wantFail(t, rep, "TDD_RED_TESTS_NOT_GREEN")
	if len(rep.Failures) != 1 {
		t.Fatalf("want exactly one finding:\n%s", dump(rep))
	}
	f := rep.Failures[0]
	if !sameCommit(f.Commit, r1) || sameCommit(f.Commit, r2) {
		t.Errorf("finding concerns %s, want R1 %s", f.Commit, r1)
	}
	if !strings.Contains(f.Message, id("calc", "TestAbsNegative")) {
		t.Errorf("message does not name the missing test: %s", f.Message)
	}
	wantRuns(t, log, "HEAD", head, unionCG)
}
