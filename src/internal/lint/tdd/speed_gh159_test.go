package tdd_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"strange.company/sc/internal/lint/tdd"
)

// gh-159: batching of the T5 baseline and T7 HEAD runs, parallel runs and TDD_JOBS (plan 52 section 8.3).
//
// The fixture Makefile logs every `test` run to $RUNLOG as `<GO_TEST_PKGS>|<GO_TEST_RUN>|<commit>|<TDD_JOBS>`
// ("-" for a variable that was not passed), so the tests count the runs per commit. When $SYNC_DIR is set
// and test/sync/id exists in the checkout, the run creates its marker and waits (bounded, in sh) for the
// marker of the other id: it passes only if the two runs overlap. A run that timed out writes seq-<id>.

const absNeg2Test = `package calc

import "testing"

func TestAbsNegativeAgain(t *testing.T) {
	if got := Abs(-9); got != 9 {
		t.Fatalf("Abs(-9) = %d, want 9", got)
	}
}
`

const zzFailTest = `package calc

import "testing"

func TestZZAlwaysFails(t *testing.T) { t.Fatal("outside every filter") }
`

const newpkgRedTest = `package newpkg

import "testing"

func TestNew(t *testing.T) { t.Fatal("not yet") }
`

const newpkgGreenTest = `package newpkg

import "testing"

func TestNew(t *testing.T) {}
`

const geomScaleRed = "src/geom/scale_test.go"

const logRecipe = "\t@if [ -n \"$$RUNLOG\" ]; then echo \"$(if $(filter command line,$(origin GO_TEST_PKGS)),$(GO_TEST_PKGS),-)|$(if $(filter command line,$(origin GO_TEST_RUN)),$(GO_TEST_RUN),-)|$$(git rev-parse HEAD)|$$TDD_JOBS\" >> \"$$RUNLOG\"; fi\n" +
	"\t@if [ -n \"$$SYNC_DIR\" ] && [ -f test/sync/id ]; then \\\n" +
	"\t\tme=$$(cat test/sync/id); case $$me in a) o=b;; *) o=a;; esac; \\\n" +
	"\t\ttouch \"$$SYNC_DIR/$$me\"; i=0; \\\n" +
	"\t\twhile [ ! -f \"$$SYNC_DIR/$$o\" ] && [ $$i -lt 200 ]; do sleep 0.1; i=$$((i+1)); done; \\\n" +
	"\t\t[ -f \"$$SYNC_DIR/$$o\" ] || touch \"$$SYNC_DIR/seq-$$me\"; \\\n" +
	"\tfi\n"

var makefile159 = strings.Replace(makefile, "test:\n", "test:\n"+logRecipe, 1)

// newScen159 is newScen with the instrumented Makefile already at the merge base.
func newScen159(t *testing.T, extra map[string]string) *scen {
	t.Helper()
	s := newScen(t, baseOpts{})
	s.git("checkout", "-q", "main")
	files := map[string]string{"Makefile": makefile159}
	for k, v := range extra {
		files[k] = v
	}
	s.commit("chore: instrumented makefile\n", files)
	s.git("checkout", "-q", "-B", "feature/x")
	return s
}

type runLine struct{ pkgs, run, sha, jobs string }

func readRunLog(t *testing.T, path string) []runLine {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []runLine
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		f := strings.Split(l, "|")
		if len(f) != 4 {
			t.Fatalf("bad run log line %q", l)
		}
		out = append(out, runLine{f[0], f[1], f[2], f[3]})
	}
	return out
}

// runsAt returns the "pkgs|run" of every logged run at commit sha, sorted.
func runsAt(lines []runLine, sha string) []string {
	var out []string
	for _, l := range lines {
		if l.sha == sha {
			out = append(out, l.pkgs+"|"+l.run)
		}
	}
	sort.Strings(out)
	return out
}

func wantRuns(t *testing.T, lines []runLine, what, sha string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := runsAt(lines, sha); !reflect.DeepEqual(got, want) {
		t.Errorf("runs at %s (%s) = %q, want %q", what, short159(sha), got, want)
	}
}

func short159(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// check159 runs the checker with the run log (and optional extra env) and returns the report and the log.
func (s *scen) check159(jobs int, env ...string) (tdd.Report, []runLine) {
	s.t.Helper()
	log := filepath.Join(s.t.TempDir(), "runs.log")
	rep := s.check(func(o *tdd.Options) {
		o.Jobs = jobs
		o.Env = append(o.Env, "RUNLOG="+log)
		o.Env = append(o.Env, env...)
	})
	return rep, readRunLog(s.t, log)
}

// twoSpecScen: R1 (calc, Abs) and R2 (geom+calc, unsorted, Scale) are two distinct specs of target test.
func twoSpecScen(t *testing.T) (s *scen, base, r1, r2, head string) {
	t.Helper()
	s = newScen159(t, nil)
	base = s.git("rev-parse", "main")
	r1 = s.commit(redMsg("test(F99): Abs negatives", pkgCalc), map[string]string{"src/calc/abs_test.go": absNegTest})
	r2 = s.commit(redMsg("test(F99): Scale factor", "test GO_TEST_PKGS=./src/geom ./src/calc"), map[string]string{geomScaleRed: scaleTest})
	head = s.commit("fix: Abs and Scale\n", map[string]string{"src/calc/calc.go": calcAbsFixed, "src/geom/geom.go": geomFixed})
	return
}

// threeSpecScen: R1 and R2 name the same spec (R2 derives ./src/calc from its test file), R3 a second one.
func threeSpecScen(t *testing.T) (s *scen, base, r1, r2, r3, head string) {
	t.Helper()
	s = newScen159(t, nil)
	base = s.git("rev-parse", "main")
	r1 = s.commit(redMsg("test(F99): Abs negatives", pkgCalc), map[string]string{"src/calc/abs_test.go": absNegTest})
	r2 = s.commit(redMsg("test(F99): Abs negatives again", "test"), map[string]string{"src/calc/abs2_test.go": absNeg2Test})
	r3 = s.commit(redMsg("test(F99): Scale factor", "test GO_TEST_PKGS=./src/geom"), map[string]string{geomScaleRed: scaleTest})
	head = s.commit("fix: Abs and Scale\n", map[string]string{"src/calc/calc.go": calcAbsFixed, "src/geom/geom.go": geomFixed})
	return
}

const unionCG = "./src/calc ./src/geom|-"

// 159-AC-1: two distinct specs of one target share a single baseline run at B over the sorted union of
// packages and without GO_TEST_RUN; the RED commits still run once each with their own spec.
func TestBatchedBaseline_OneRunPerTarget_159AC1(t *testing.T) {
	t.Parallel()
	s, base, r1, r2, _ := twoSpecScen(t)
	rep, log := s.check159(1)
	wantPass(t, rep)
	wantRuns(t, log, "B", base, unionCG)
	wantRuns(t, log, "R1", r1, "./src/calc|-")
	wantRuns(t, log, "R2", r2, "./src/geom ./src/calc|-")
}

// 159-AC-1: a target with exactly one distinct spec runs that spec unchanged (also when two REDs share it);
// the filter is not dropped. Preservation of the unbatched behavior.
func TestBatchedBaseline_SingleSpecUnchanged_159AC1(t *testing.T) {
	t.Parallel()
	s := newScen159(t, nil)
	base := s.git("rev-parse", "main")
	s.commit(redMsg("test(F99): Abs negatives", "test GO_TEST_PKGS=./src/calc GO_TEST_RUN=TestAbsNegative"), map[string]string{"src/calc/abs_test.go": absNegTest})
	s.commit(redMsg("test(F99): Abs negatives again", "test GO_TEST_PKGS=./src/calc GO_TEST_RUN=TestAbsNegative"), map[string]string{"src/calc/abs2_test.go": strings.Replace(absNeg2Test, "TestAbsNegativeAgain", "TestAbsNegativeAgain2", 1)})
	head := s.commit("fix: Abs\n", map[string]string{"src/calc/calc.go": calcAbsFixed})
	rep, log := s.check159(1)
	wantPass(t, rep)
	wantRuns(t, log, "B", base, "./src/calc|TestAbsNegative")
	wantRuns(t, log, "HEAD", head, "./src/calc|TestAbsNegative")
}

// 159-AC-1: a pattern absent at B is dropped from the union exactly as it is dropped today.
func TestBatchedBaseline_AbsentPatternDropped_159AC1(t *testing.T) {
	t.Parallel()
	s := newScen159(t, nil)
	base := s.git("rev-parse", "main")
	s.commit(redMsg("test(F99): Abs negatives", pkgCalc), map[string]string{"src/calc/abs_test.go": absNegTest})
	s.commit(redMsg("test(F99): new package", "test GO_TEST_PKGS=./src/newpkg"), map[string]string{"src/newpkg/new_test.go": newpkgRedTest})
	head := s.commit("fix: Abs and newpkg\n", map[string]string{"src/calc/calc.go": calcAbsFixed, "src/newpkg/new_test.go": newpkgGreenTest})
	rep, log := s.check159(1)
	if rep.Result != "pass" || len(rep.Failures) != 0 {
		t.Errorf("want pass:\n%s", dump(rep))
	}
	wantRuns(t, log, "B", base, "./src/calc|-")
	wantRuns(t, log, "HEAD", head, "./src/calc ./src/newpkg|-")
}

// 159-AC-2: one HEAD run per target over the union; deduplicated specs count once; the baseline is batched too.
func TestBatchedHead_OneRunPerTarget_159AC2(t *testing.T) {
	t.Parallel()
	s, base, r1, r2, r3, head := threeSpecScen(t)
	rep, log := s.check159(1)
	wantPass(t, rep)
	wantRuns(t, log, "HEAD", head, unionCG)
	wantRuns(t, log, "B", base, unionCG)
	wantRuns(t, log, "R1", r1, "./src/calc|-")
	wantRuns(t, log, "R2", r2, "./src/calc|-")
	wantRuns(t, log, "R3", r3, "./src/geom|-")
	if len(rep.Red.Commits) != 3 {
		t.Errorf("want 3 RED runs in the report, got %d", len(rep.Red.Commits))
	}
}

// 159-AC-2: every pending RED is evaluated against the batched HEAD run (CD-57 supersession included).
func TestBatchedHead_SupersessionAgainstBatchedRun_159AC2(t *testing.T) {
	t.Parallel()
	s := newScen159(t, nil)
	r1 := s.commit(redMsg("test(F99): Abs negatives", pkgCalc), map[string]string{"src/calc/abs_test.go": absNegTest})
	r2 := s.commit(redMsg("test(F99): Abs renamed, Scale", "test GO_TEST_PKGS=./src/calc ./src/geom"), map[string]string{
		"src/calc/abs_test.go":  deleteMark,
		"src/calc/abs2_test.go": absRenamedTest,
		geomScaleRed:            scaleTest,
	})
	_ = r1
	head := s.commit(supMsg("fix: Abs and Scale", idA+" by "+r2+": "+longReason), map[string]string{"src/calc/calc.go": calcAbsFixed, "src/geom/geom.go": geomFixed})
	rep, log := s.check159(1)
	wantPass(t, rep)
	if !reflect.DeepEqual(rep.Superseded, []string{idA}) {
		t.Errorf("superseded = %v, want [%s]", rep.Superseded, idA)
	}
	wantRuns(t, log, "HEAD", head, unionCG)
}

// 159-AC-3: when the union run fails (a test outside every spec's filter fails) the checker discards it and
// runs each spec separately, at B and at HEAD; the verdict is the unbatched one (pass).
func TestBatchFallback_UnionFails_PerSpecRuns_159AC3(t *testing.T) {
	t.Parallel()
	s := newScen159(t, map[string]string{"src/calc/zz_test.go": zzFailTest})
	base := s.git("rev-parse", "main")
	s.commit(redMsg("test(F99): Abs negatives", "test GO_TEST_PKGS=./src/calc GO_TEST_RUN=TestAbsNegative"), map[string]string{"src/calc/abs_test.go": absNegTest})
	s.commit(redMsg("test(F99): Scale factor", "test GO_TEST_PKGS=./src/geom GO_TEST_RUN=TestScaleFactor"), map[string]string{geomScaleRed: scaleTest})
	head := s.commit("fix: Abs and Scale\n", map[string]string{"src/calc/calc.go": calcAbsFixed, "src/geom/geom.go": geomFixed})
	rep, log := s.check159(1)
	wantPass(t, rep)
	want := []string{unionCG, "./src/calc|TestAbsNegative", "./src/geom|TestScaleFactor"}
	wantRuns(t, log, "B", base, want...)
	wantRuns(t, log, "HEAD", head, want...)
}

// 159-AC-3: a per-spec run that genuinely fails at HEAD yields the finding the unbatched algorithm gives,
// for that commit only (the union run fails because of R2, R1's own run passes).
func TestBatchFallback_RealFailureKeepsFinding_159AC3(t *testing.T) {
	t.Parallel()
	s := newScen159(t, nil)
	r1 := s.commit(redMsg("test(F99): Abs negatives", pkgCalc), map[string]string{"src/calc/abs_test.go": absNegTest})
	r2 := s.commit(redMsg("test(F99): Scale factor", "test GO_TEST_PKGS=./src/geom"), map[string]string{geomScaleRed: scaleTest})
	s.commit("fix: Abs only\n", map[string]string{"src/calc/calc.go": calcAbsFixed})
	rep, _ := s.check159(1)
	wantFail(t, rep, "TDD_RED_TESTS_NOT_GREEN")
	if len(rep.Failures) != 1 {
		t.Fatalf("want exactly one finding:\n%s", dump(rep))
	}
	f := rep.Failures[0]
	if !sameCommit(f.Commit, r2) || sameCommit(f.Commit, r1) {
		t.Errorf("finding concerns %s, want R2 %s", f.Commit, r2)
	}
	if !strings.Contains(f.Message, fmt.Sprintf("RED commit %s: the red target test exits ", r2[:12])) ||
		!strings.Contains(f.Message, fmt.Sprintf(" at HEAD; failing: %s; output: ", id("geom", "TestScaleFactor"))) {
		t.Errorf("unexpected message: %s", f.Message)
	}
}

// 159-AC-3 / 159-AC-4: Report is identical for Jobs=1 and Jobs=4 and equals the expected unbatched verdict.
func TestReportEquivalence_Jobs1Jobs4_159AC3(t *testing.T) {
	type sc struct {
		name  string
		build func(t *testing.T) *scen
		codes []string
	}
	scs := []sc{
		{"three specs pass", func(t *testing.T) *scen { s, _, _, _, _, _ := threeSpecScen(t); return s }, nil},
		{"two specs pass", func(t *testing.T) *scen { s, _, _, _, _ := twoSpecScen(t); return s }, nil},
		{"second RED passes", func(t *testing.T) *scen {
			s := newScen159(t, nil)
			absRed(s)
			absGreen(s)
			s.commit(redMsg("test(F99): Abs zero", pkgCalc), map[string]string{"src/calc/abs_zero_test.go": absZeroTest})
			s.commit(redMsg("test(F99): Scale factor", "test GO_TEST_PKGS=./src/geom"), map[string]string{geomScaleRed: scaleTest})
			s.commit("fix: Scale\n", map[string]string{"src/geom/geom.go": geomFixed})
			return s
		}, []string{"TDD_RED_PASSED"}},
		{"renamed test not superseded", func(t *testing.T) *scen {
			x := newSupScen(t)
			x.s.commit(supMsg("fix: Abs", ""), map[string]string{"src/calc/calc.go": calcAbsFixed})
			return x.s
		}, []string{"TDD_RED_TESTS_NOT_GREEN"}},
		{"renamed test superseded", func(t *testing.T) *scen {
			x := newSupScen(t)
			x.s.commit(supMsg("fix: Abs", idA+" by "+x.r2+": "+longReason), map[string]string{"src/calc/calc.go": calcAbsFixed})
			return x.s
		}, nil},
	}
	for _, c := range scs {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := c.build(t)
			r1, _ := s.check159(1)
			r4, _ := s.check159(4)
			if !reflect.DeepEqual(r1, r4) {
				t.Errorf("Jobs=1 and Jobs=4 reports differ:\n--- 1\n%s--- 4\n%s", dump(r1), dump(r4))
			}
			if c.codes == nil {
				wantPass(t, r1)
			} else {
				wantFail(t, r1, c.codes...)
			}
		})
	}
}

// syncScen: R1 (id a) and R2 (id b) are two REDs whose runs wait for each other's marker.
func syncScen(t *testing.T) (*scen, string) {
	t.Helper()
	s := newScen159(t, nil)
	syncDir := t.TempDir()
	s.commit(redMsg("test(F99): Abs negatives", pkgCalc), map[string]string{"src/calc/abs_test.go": absNegTest, "test/sync/id": "a\n"})
	s.commit(redMsg("test(F99): Scale factor", "test GO_TEST_PKGS=./src/geom"), map[string]string{geomScaleRed: scaleTest, "test/sync/id": "b\n"})
	s.commit("fix: Abs and Scale\n", map[string]string{"src/calc/calc.go": calcAbsFixed, "src/geom/geom.go": geomFixed})
	return s, syncDir
}

func wantOverlap(t *testing.T, syncDir string) {
	t.Helper()
	for _, id := range []string{"a", "b"} {
		if _, err := os.Stat(filepath.Join(syncDir, "seq-"+id)); err == nil {
			t.Errorf("the run of RED %q waited in vain for the other RED run: the RED runs did not overlap", id)
		}
		if _, err := os.Stat(filepath.Join(syncDir, id)); err != nil {
			t.Errorf("the run of RED %q never happened: %v", id, err)
		}
	}
}

// 159-AC-4: with Jobs=2 the two RED runs overlap in time.
func TestParallelRuns_RedRunsOverlap_Jobs2_159AC4(t *testing.T) {
	t.Parallel()
	s, syncDir := syncScen(t)
	rep, _ := s.check159(2, "SYNC_DIR="+syncDir)
	wantPass(t, rep)
	wantOverlap(t, syncDir)
}

// 159-AC-4: Jobs <= 0 means min(NumCPU, 4); with at least two CPUs the RED runs overlap.
func TestParallelRuns_DefaultJobsOverlap_159AC4(t *testing.T) {
	if runtime.NumCPU() < 2 {
		t.Skip("the default is sequential on a single CPU")
	}
	t.Parallel()
	for _, jobs := range []int{0, -3} {
		s, syncDir := syncScen(t)
		rep, _ := s.check159(jobs, "SYNC_DIR="+syncDir)
		wantPass(t, rep)
		wantOverlap(t, syncDir)
	}
}

// 159-AC-4: many REDs at Jobs=4 give the same report, in commit order, as Jobs=1 (race-free worktrees).
func TestParallelRuns_ManyRedsOrderedAndRaceFree_159AC4(t *testing.T) {
	t.Parallel()
	s := newScen159(t, nil)
	var reds []string
	for i := 0; i < 6; i++ {
		src := strings.Replace(absNeg2Test, "TestAbsNegativeAgain", fmt.Sprintf("TestAbsNegativeN%d", i), 1)
		reds = append(reds, s.commit(redMsg(fmt.Sprintf("test(F99): Abs negatives %d", i), pkgCalc), map[string]string{fmt.Sprintf("src/calc/abs_n%d_test.go", i): src}))
	}
	s.commit("fix: Abs\n", map[string]string{"src/calc/calc.go": calcAbsFixed})
	r1, _ := s.check159(1)
	r4, _ := s.check159(4)
	wantPass(t, r4)
	if !reflect.DeepEqual(r1, r4) {
		t.Errorf("Jobs=1 and Jobs=4 reports differ:\n%s\n%s", dump(r1), dump(r4))
	}
	if len(r4.Red.Commits) != len(reds) {
		t.Fatalf("want %d RED runs, got %d", len(reds), len(r4.Red.Commits))
	}
	for i, c := range r4.Red.Commits {
		if !sameCommit(c.Commit, reds[i]) {
			t.Errorf("RED run %d is %s, want commit order (%s)", i, c.Commit, reds[i])
		}
	}
}

// 159-AC-4 / 159-AC-5: the CLI output and report.json are byte-identical for TDD_JOBS=1 and TDD_JOBS=4, and
// TDD_JOBS reaches the target runs like any other variable.
func TestCLI_TddJobs_ByteIdentical_159AC4(t *testing.T) {
	t.Setenv("SC_LINT_BIN", lintBin(t))
	t.Setenv("SC_IN_CONTAINER", "1")
	s := newScen159(t, nil)
	absRed(s)
	absGreen(s)
	s.commit(redMsg("test(F99): Abs zero", pkgCalc), map[string]string{"src/calc/abs_zero_test.go": absZeroTest})
	s.commit(redMsg("test(F99): Scale factor", "test GO_TEST_PKGS=./src/geom"), map[string]string{geomScaleRed: scaleTest})
	s.commit("fix: Scale\n", map[string]string{"src/geom/geom.go": geomFixed})
	logPath := filepath.Join(t.TempDir(), "runs.log")
	t.Setenv("RUNLOG", logPath)
	var outs, reports []string
	for _, j := range []string{"1", "4"} {
		t.Setenv("TDD_JOBS", j)
		code, out := runLintMain(t, "tdd-discipline", "--root", s.dir, "--base", "main", "--branch", "feature/x", "--author", "dev")
		if code != 1 || !strings.Contains(out, "TDD_RED_PASSED") {
			t.Fatalf("TDD_JOBS=%s: exit %d, want 1 with TDD_RED_PASSED:\n%s", j, code, out)
		}
		b, err := os.ReadFile(filepath.Join(s.dir, "out", "tdd-discipline", "report.json"))
		if err != nil {
			t.Fatal(err)
		}
		outs, reports = append(outs, out), append(reports, string(b))
	}
	if outs[0] != outs[1] {
		t.Errorf("CLI output differs between TDD_JOBS=1 and 4:\n%s\n---\n%s", outs[0], outs[1])
	}
	if reports[0] != reports[1] {
		t.Errorf("report.json differs between TDD_JOBS=1 and 4:\n%s\n---\n%s", reports[0], reports[1])
	}
	seen := map[string]bool{}
	for _, l := range readRunLog(t, logPath) {
		seen[l.jobs] = true
	}
	if !seen["1"] || !seen["4"] || len(seen) != 2 {
		t.Errorf("TDD_JOBS must be visible in the target runs unchanged; saw %v", seen)
	}
}

// 159-AC-5: a TDD_JOBS that is not a positive decimal integer exits 2 naming TDD_JOBS, before any git work
// (the root is not even a git repository and no report is written).
func TestCLI_TddJobs_Invalid_Exit2_159AC5(t *testing.T) {
	for _, v := range []string{"abc", "0", "-1", "2.5", "1e1", "two", "4x", "0x4", "-0"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("TDD_JOBS", v)
			dir := t.TempDir()
			code, out := runLintMain(t, "tdd-discipline", "--root", dir, "--base", "main", "--branch", "feature/x", "--author", "dev")
			if code != 2 {
				t.Errorf("TDD_JOBS=%q: exit %d, want 2:\n%s", v, code, out)
			}
			if !strings.Contains(out, "TDD_JOBS") {
				t.Errorf("TDD_JOBS=%q: message does not name TDD_JOBS:\n%s", v, out)
			}
			if _, err := os.Stat(filepath.Join(dir, "out")); err == nil {
				t.Errorf("TDD_JOBS=%q: work was done before validating the variable (out/ exists)", v)
			}
		})
	}
}

// 159-AC-5: unset, empty and positive values are accepted; no --jobs flag exists.
func TestCLI_TddJobs_ValidAndNoFlag_159AC5(t *testing.T) {
	t.Setenv("SC_LINT_BIN", lintBin(t))
	t.Setenv("SC_IN_CONTAINER", "1")
	s := newScen159(t, nil)
	s.commit(redMsg("test(F99): Abs negatives", pkgCalc), map[string]string{"src/calc/abs_test.go": absNegTest})
	s.commit("fix: Abs\n", map[string]string{"src/calc/calc.go": calcAbsFixed})
	args := []string{"tdd-discipline", "--root", s.dir, "--base", "main", "--branch", "feature/x", "--author", "dev"}
	for _, v := range []string{"", "1", "2", "16"} {
		t.Run("value "+v, func(t *testing.T) {
			t.Setenv("TDD_JOBS", v)
			if code, out := runLintMain(t, args...); code != 0 || !strings.Contains(out, "tdd-discipline: pass") {
				t.Errorf("TDD_JOBS=%q: exit %d, want 0 pass:\n%s", v, code, out)
			}
		})
	}
	t.Run("no --jobs flag", func(t *testing.T) {
		t.Setenv("TDD_JOBS", "")
		if code, out := runLintMain(t, append(args, "--jobs", "2")...); code != 2 {
			t.Errorf("--jobs must stay an unknown flag: exit %d:\n%s", code, out)
		}
	})
}
