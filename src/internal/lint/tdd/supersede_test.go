package tdd_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// CD-57(a) (plan 52 8.3 T7): a RED test that a later R of the same PR legitimately renamed or moved may be
// absent at HEAD if the PR head commit carries `TDD-Superseded: <test-id> by <commit>: <reason >= 20 chars>`.
// <test-id> is everything up to the last " by " that precedes the commit sha, so wrapper ids with spaces
// (and colons) can be listed. A malformed or unmatched line is TDD_SUPERSEDED_INVALID; listed ids appear in
// the JSON report under the key "superseded" (an array of the test ids).

const absRenamedTest = `package calc

import "testing"

func TestAbsNegativeRenamed(t *testing.T) {
	if got := Abs(-4); got != 4 {
		t.Fatalf("Abs(-4) = %d, want 4", got)
	}
}
`

const longReason = "the test was renamed in the later RED commit"

type supScen struct {
	s              *scen
	r1, r2, middle string // R1 adds A, R2 renames A to A2, a docs commit sits between R2 and the green commit
}

// newSupScen builds base, R1 (adds TestAbsNegative), R2 (replaces it by TestAbsNegativeRenamed) and a docs
// commit. The caller adds the green head commit.
func newSupScen(t *testing.T) *supScen {
	t.Helper()
	s := newScen(t, baseOpts{})
	r1 := absRed(s)
	r2 := s.commit(redMsg("test(F99): Abs test renamed", pkgCalc), map[string]string{
		"src/calc/abs_test.go":  deleteMark,
		"src/calc/abs2_test.go": absRenamedTest,
	})
	mid := s.commit("docs: note\n", map[string]string{"NOTES.md": "note\n"})
	return &supScen{s: s, r1: r1, r2: r2, middle: mid}
}

func supMsg(subject, line string) string {
	if line == "" {
		return subject + "\n"
	}
	return subject + "\n\nTDD-Superseded: " + line + "\n"
}

var idA = id("calc", "TestAbsNegative")

func TestDiscipline_Superseded(t *testing.T) {
	green := map[string]string{"src/calc/calc.go": calcAbsFixed}

	t.Run("renamed RED test without a TDD-Superseded line", func(t *testing.T) { // 52-AC-4 CD-57a (a)
		t.Parallel()
		x := newSupScen(t)
		x.s.commit(supMsg("fix: Abs", ""), green)
		wantFail(t, x.s.check(nil), "TDD_RED_TESTS_NOT_GREEN")
	})

	t.Run("valid line on the head commit passes and is reported", func(t *testing.T) { // 52-AC-4 CD-57a (b)
		t.Parallel()
		x := newSupScen(t)
		x.s.commit(supMsg("fix: Abs", idA+" by "+x.r2+": "+longReason), green)
		r := x.s.check(nil)
		wantPass(t, r)
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		list, _ := m["superseded"].([]any)
		found := false
		for _, v := range list {
			if v == idA {
				found = true
			}
		}
		if !found {
			t.Errorf("report JSON key \"superseded\" must list %q, got %v", idA, m["superseded"])
		}
	})

	for _, tc := range []struct {
		name string
		sha  func(x *supScen) string
	}{
		{"the R that added the test", func(x *supScen) string { return x.r1 }},
		{"a non-R commit", func(x *supScen) string { return x.middle }},
		{"the merge base", func(x *supScen) string { return x.s.git("merge-base", "main", "HEAD") }},
		{"an unknown sha", func(x *supScen) string { return strings.Repeat("ab", 20) }},
	} {
		tc := tc
		t.Run("by names "+tc.name, func(t *testing.T) { // 52-AC-4 CD-57a (c)
			t.Parallel()
			x := newSupScen(t)
			x.s.commit(supMsg("fix: Abs", idA+" by "+tc.sha(x)+": "+longReason), green)
			wantFailHas(t, x.s.check(nil), "TDD_SUPERSEDED_INVALID")
		})
	}

	t.Run("reason shorter than 20 characters", func(t *testing.T) { // 52-AC-4 CD-57a (d)
		t.Parallel()
		x := newSupScen(t)
		x.s.commit(supMsg("fix: Abs", idA+" by "+x.r2+": renamed"), green)
		wantFailHas(t, x.s.check(nil), "TDD_SUPERSEDED_INVALID")
	})

	t.Run("line on a non-head commit is ignored", func(t *testing.T) { // 52-AC-4 CD-57a (e)
		t.Parallel()
		x := newSupScen(t)
		x.s.commit(supMsg("fix: Abs", idA+" by "+x.r2+": "+longReason), green)
		x.s.commit("docs: later note\n", map[string]string{"NOTES.md": "later\n"})
		wantFailHas(t, x.s.check(nil), "TDD_RED_TESTS_NOT_GREEN", "TDD_SUPERSEDED_INVALID")
	})
}

// (f) wrapper ids contain spaces, colons and even " by "; the id is delimited by the last " by " before the sha.
func wrapperScript(check string) string {
	return "#!/bin/sh\nrc=0\n" +
		"if [ -f toolbox/tool.sh ]; then echo \"  ok   " + check + "\"; else echo \"  FAIL " + check + "\"; rc=1; fi\n" +
		"echo \"  ok   other\"\n" +
		"echo \"stub-x.sh: 2 checks, 1 failed\"\nexit $rc\n"
}

func TestDiscipline_SupersededWrapperIDs(t *testing.T) {
	for _, tc := range []struct{ name, check string }{
		{"spaces and colon", "02-AC-16 podman-rootless: --userns=keep-id"},
		{"contains by", "02-AC-17 runs by root: ok"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) { // 52-AC-4 CD-57a (f)
			t.Parallel()
			s := newScen(t, baseOpts{noMakefile: true})
			s.commit(redMsg("test(F99): toolbox wrapper", "toolbox-check"), map[string]string{"test/e2e/wrapper/run.sh": wrapperScript(tc.check)})
			r2 := s.commit(redMsg("test(F99): wrapper check renamed", "toolbox-check"), map[string]string{
				"test/e2e/wrapper/run.sh": wrapperScript(tc.check + " renamed"),
			})
			wid := "stub-x.sh." + tc.check
			s.commit(supMsg("feat: tool", wid+" by "+r2+": "+longReason), map[string]string{"toolbox/tool.sh": "#!/bin/sh\n:\n"})
			wantPass(t, s.check(nil))
		})
	}
}
