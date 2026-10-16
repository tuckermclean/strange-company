package tdd_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"strange.company/sc/internal/lint/tdd"
)

// Round 3 of slice S5: CD-57 (T7 supersession applies to absent ids only), plan 52 8.3 T3 (a RED commit
// changes only test files and declared stubs), CD-58 (trusted inputs, --provenance-bound, safe red-target
// values) and 8.3a (marker on the last line of a stub's comment group).
//
// API the implementation must provide:
//   - tdd.Options.ProvenanceBound bool; CLI flag `--provenance-bound` of `sc-lint tdd-discipline`.
//   - tdd.Report.PolicySource and tdd.Report.CheckerSource (JSON "policy_source", "checker_source"), each
//     "base" or "head". With Options.Policy == nil the policy is read from `git show <B>:policy/ci.yaml`
//     and only from the head checkout when B has no policy/ci.yaml. checker_source is "base" when B has a
//     file under src/cmd/sc-lint/ that mentions the tdd-discipline subcommand, else "head".

const absNegTestSkipped = `package calc

import "testing"

func TestAbsNegative(t *testing.T) {
	t.Skip("later")
	if got := Abs(-4); got != 4 {
		t.Fatalf("Abs(-4) = %d, want 4", got)
	}
}
`

// B1 (T7 + CD-57): a TDD-Superseded line applies to ABSENT ids only. A test that is still present but
// skipped at HEAD stays TDD_RED_TESTS_NOT_GREEN, and the line that matches no absent id is invalid.
func TestDiscipline_SupersededSkippedNotAbsent_B1(t *testing.T) { // 52-AC-4 CD-57
	s := newScen(t, baseOpts{})
	absRed(s) // R1 adds TestAbsNegative
	r2 := s.commit(redMsg("test(F99): Scale factor", "test GO_TEST_PKGS=./src/geom"), map[string]string{"src/geom/scale_test.go": scaleTest})
	s.commit(supMsg("fix: Abs and Scale", idA+" by "+r2+": "+longReason), map[string]string{
		"src/calc/calc.go":     calcAbsFixed,
		"src/geom/geom.go":     geomFixed,
		"src/calc/abs_test.go": absNegTestSkipped,
	})
	r := s.check(nil)
	wantFailHas(t, r, "TDD_RED_TESTS_NOT_GREEN")
	if !hasCode(r.Failures, "TDD_SUPERSEDED_INVALID") {
		t.Errorf("the line matches no absent id and must give TDD_SUPERSEDED_INVALID:\n%s", dump(r))
	}
	for _, v := range r.Superseded {
		if v == idA {
			t.Errorf("a skipped (present) test must not be reported as superseded: %v", r.Superseded)
		}
	}
}

// B2 (T3): a test(...) commit that also changes a file that is neither a test path nor a behavior path is
// TDD_STUB_INVALID: the RED commit holds test files and declared stubs only.
func TestDiscipline_RedWithOtherFiles_B2(t *testing.T) { // 52-AC-4 T3
	for _, f := range []string{".github/workflows/x.yaml", "NOTES.md", ".strange-company/repo.yaml"} {
		f := f
		t.Run(f, func(t *testing.T) {
			t.Parallel()
			s := newScen(t, baseOpts{})
			s.commit(redMsg("test(F99): Abs handles negatives", pkgCalc), map[string]string{"src/calc/abs_test.go": absNegTest, f: "extra\n"})
			absGreen(s)
			wantFailHas(t, s.check(nil), "TDD_STUB_INVALID")
		})
	}
	t.Run("only test files and a valid stub stay valid", func(t *testing.T) {
		t.Parallel()
		s := newScen(t, baseOpts{})
		stubRed(s, "return 0")
		s.commit("feat: Sub\n", map[string]string{"src/calc/calc.go": calcBase + "\nfunc Sub(a, b int) int { return a - b }\n"})
		wantPass(t, s.check(nil))
	})
}

// F7 (8.3a): the marker may be the LAST line of the stub's doc-comment group, directly above the func.
func TestDiscipline_StubMarkerLastLine_F7(t *testing.T) { // 52-AC-4 8.3a
	s := stubDocScen(t, calcWithDoc("// Sub does x.\n// SC-STUB(F01 01-AC-1)\n"))
	wantPass(t, s.check(nil))
}

// ---------------------------------------------------------------------------------------------
// CD-58(a): policy and checker come from the merge base.

var behaviorLineRe = regexp.MustCompile(`(?m)^    behaviorPaths: \[.*\]$`)

func weakenedPolicy(t *testing.T) string {
	t.Helper()
	w := behaviorLineRe.ReplaceAllString(policyYAML, "    behaviorPaths: []")
	if w == policyYAML {
		t.Fatal("test bug: behaviorPaths line not found")
	}
	return w
}

func TestTrusted_PolicyFromBase_CD58a(t *testing.T) { // 52-AC-4 CD-58a(i)
	weakened := weakenedPolicy(t)
	mk := func(t *testing.T) *scen {
		s := newScen(t, baseOpts{})
		s.commit("feat: Abs and relax the policy\n", map[string]string{"src/calc/calc.go": calcAbsFixed, "policy/ci.yaml": weakened})
		return s
	}
	t.Run("Check", func(t *testing.T) {
		t.Parallel()
		r := mk(t).check(func(o *tdd.Options) { o.Policy = nil })
		wantFailHas(t, r, "TDD_NO_RED_COMMIT")
		if r.PolicySource != "base" {
			t.Errorf("policy_source = %q, want base", r.PolicySource)
		}
	})
	t.Run("report.json", func(t *testing.T) {
		s := mk(t)
		t.Setenv("SC_LINT_BIN", lintBin(t))
		t.Setenv("SC_IN_CONTAINER", "1")
		code, out := runLintMain(t, "tdd-discipline", "--root", s.dir, "--base", "main", "--branch", "feature/x", "--author", "dev")
		if code != 1 || !strings.Contains(out, "TDD_NO_RED_COMMIT") {
			t.Errorf("exit %d, want 1 with TDD_NO_RED_COMMIT:\n%s", code, out)
		}
		b, err := os.ReadFile(filepath.Join(s.dir, "out", "tdd-discipline", "report.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		if m["policy_source"] != "base" {
			t.Errorf("report.json policy_source = %v, want base:\n%s", m["policy_source"], b)
		}
		if _, ok := m["checker_source"]; !ok {
			t.Errorf("report.json lacks checker_source:\n%s", b)
		}
	})
}

const checkerMain = "package main\n\n// main dispatches the tdd-discipline subcommand.\nfunc main() { _ = \"tdd-discipline\" }\n"

// mkTrustRepo builds a base on main without the policy and/or without the checker, then a feature branch.
func mkTrustRepo(t *testing.T, basePolicy, baseChecker bool) *scen {
	t.Helper()
	s := &scen{t: t, dir: t.TempDir()}
	s.git("init", "-q")
	s.git("checkout", "-q", "-b", "main")
	files := map[string]string{"README.md": "synthetic\n"}
	if basePolicy {
		files["policy/ci.yaml"] = policyYAML
	}
	if baseChecker {
		files["src/cmd/sc-lint/main.go"] = checkerMain
	}
	s.commit("chore: base\n", files)
	s.git("checkout", "-q", "-b", "feature/x")
	return s
}

func TestTrusted_PolicyFromHeadWhenBaseLacksIt_CD58a(t *testing.T) { // 52-AC-4 CD-58a(ii)
	s := mkTrustRepo(t, false, false)
	s.commit("chore: add the CI policy\n", map[string]string{"policy/ci.yaml": weakenedPolicy(t), "src/calc/calc.go": calcAbsFixed})
	r := s.check(func(o *tdd.Options) { o.Policy = nil })
	if r.Result != "not-applicable" {
		t.Errorf("the head's policy (no behavior paths) must apply when B has none, got:\n%s", dump(r))
	}
	if r.PolicySource != "head" || r.CheckerSource != "head" {
		t.Errorf("policy_source=%q checker_source=%q, want head head:\n%s", r.PolicySource, r.CheckerSource, dump(r))
	}
}

func TestTrusted_SourcesBaseWhenBaseHasBoth_CD58a(t *testing.T) { // 52-AC-4 CD-58a(iii)
	s := mkTrustRepo(t, true, true)
	s.commit("docs: note\n", map[string]string{"NOTES.md": "n\n"})
	r := s.check(func(o *tdd.Options) { o.Policy = nil })
	if r.PolicySource != "base" || r.CheckerSource != "base" {
		t.Errorf("policy_source=%q checker_source=%q, want base base:\n%s", r.PolicySource, r.CheckerSource, dump(r))
	}
}

func TestTrusted_CheckerFromHeadWhenBaseLacksSubcommand_CD58a(t *testing.T) { // 52-AC-4 CD-58a(iii)
	s := mkTrustRepo(t, true, false)
	s.commit("docs: note\n", map[string]string{"NOTES.md": "n\n"})
	r := s.check(func(o *tdd.Options) { o.Policy = nil })
	if r.PolicySource != "base" || r.CheckerSource != "head" {
		t.Errorf("policy_source=%q checker_source=%q, want base head:\n%s", r.PolicySource, r.CheckerSource, dump(r))
	}
}

// The checker binary itself cannot be swapped inside a Go test without building two checkers, so the
// make recipe is checked by dry run: with a TDD_BASE that has the tdd-discipline subcommand (HEAD of the
// real tree does) the recipe must build sc-lint from a git worktree of that base instead of running the
// head sources.
func TestTrusted_MakeRecipeUsesBaseWorktree_CD58a(t *testing.T) { // 52-AC-4 CD-58a(iii)
	root := repoRoot(t)
	cmd := exec.Command("make", "-n", "tdd-discipline-check", "TDD_BASE=HEAD", "TDD_BRANCH=feature/x")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "SC_IN_CONTAINER=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n tdd-discipline-check: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "git worktree add") {
		t.Errorf("the recipe must build sc-lint from a worktree of $(TDD_BASE) (git worktree add ...), got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------------------------
// CD-58(b): agent/* is excluded only with provenance binding.

func agentScen(t *testing.T) *scen {
	s := newScen(t, baseOpts{})
	s.commit("feat: Abs\n", map[string]string{"src/calc/calc.go": calcAbsFixed})
	return s
}

func TestTrusted_AgentBranchNeedsProvenance_CD58b(t *testing.T) { // 52-AC-4 CD-58b
	asAgent := func(bound bool) func(*tdd.Options) {
		return func(o *tdd.Options) { o.Branch = "agent/foo"; o.Author = "sc-runner"; o.ProvenanceBound = bound }
	}
	t.Run("without provenance it is evaluated normally", func(t *testing.T) {
		wantFail(t, agentScen(t).check(asAgent(false)), "TDD_NO_RED_COMMIT")
	})
	t.Run("with provenance it is not-applicable", func(t *testing.T) {
		r := agentScen(t).check(asAgent(true))
		if r.Result != "not-applicable" || len(r.Failures) != 0 {
			t.Errorf("want not-applicable, got:\n%s", dump(r))
		}
	})
	t.Run("CLI flag --provenance-bound", func(t *testing.T) {
		s := agentScen(t)
		args := []string{"tdd-discipline", "--root", s.dir, "--base", "main", "--branch", "agent/foo", "--author", "sc-runner"}
		code, out := runLintMain(t, args...)
		if code != 1 || !strings.Contains(out, "TDD_NO_RED_COMMIT") {
			t.Errorf("without the flag: exit %d, want 1 with TDD_NO_RED_COMMIT:\n%s", code, out)
		}
		code, out = runLintMain(t, append(args, "--provenance-bound")...)
		if code != 0 || !strings.Contains(out, "not-applicable") {
			t.Errorf("with --provenance-bound: exit %d, want 0 not-applicable:\n%s", code, out)
		}
	})
}

// ---------------------------------------------------------------------------------------------
// CD-58(c): red-target values reach a shell only after a character check.

func TestTrusted_RedTargetInjection_CD58c(t *testing.T) { // 52-AC-4 CD-58c
	for _, tc := range []struct{ name, line string }{
		{"double quote and semicolon", `test GO_TEST_PKGS=./src/calc GO_TEST_RUN=TestAbs";:>@P";"`},
		{"semicolon", `test GO_TEST_PKGS=./src/calc GO_TEST_RUN=TestAbs;:>@P`},
		{"command substitution", `test GO_TEST_PKGS=./src/calc GO_TEST_RUN=TestAbs$(:>@P)`},
		{"backquote", "test GO_TEST_PKGS=./src/calc GO_TEST_RUN=TestAbs`:>@P`"},
		{"single quote", `test GO_TEST_PKGS=./src/calc GO_TEST_RUN=TestAbs';:>@P;'`},
		{"packages with semicolon", `test GO_TEST_PKGS=./src/calc;:>@P`},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pwn := filepath.Join(t.TempDir(), "PWNED")
			s := newScen(t, baseOpts{})
			s.commit(redMsg("test(F99): Abs handles negatives", strings.ReplaceAll(tc.line, "@P", pwn)), map[string]string{"src/calc/abs_test.go": absNegTest})
			absGreen(s)
			r := s.check(nil)
			wantFailHas(t, r, "TDD_RED_TARGET_INVALID")
			if _, err := os.Stat(pwn); err == nil {
				t.Errorf("command injection: %s was created", pwn)
			}
			_ = filepath.WalkDir(s.dir, func(p string, d os.DirEntry, err error) error {
				if err == nil && d.Name() == "PWNED" {
					t.Errorf("command injection: %s was created", p)
				}
				return nil
			})
		})
	}
}
