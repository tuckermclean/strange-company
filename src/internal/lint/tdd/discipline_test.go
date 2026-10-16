package tdd_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/lint"
	"strange.company/sc/internal/lint/tdd"
)

// 52-AC-4: one synthetic git repository per outcome of the bootstrap discipline check (plan 52 8.3).
// Every repository holds a tiny stdlib-only Go module under src/ and a Makefile whose `test` target
// has the contract of mk/test.mk (GO_TEST_PKGS, GO_TEST_RUN, JUNIT, exit status of go test).
//
// API under test (declared in tdd.go): tdd.Check(Options) (Report, error); failures are reported in
// Report.Failures (never as an error); refused exclusions and TDD_RED_TEST_MODIFIED in Warnings.

const policyYAML = `apiVersion: strange.company/v1alpha1
kind: CIPolicy
metadata: {name: synthetic}
spec:
  tddDiscipline:
    excludedBranches: ["agent/*", "renovate/*"]
    behaviorPaths: ["src/**", "policy/**", "toolbox/**", "Makefile", "mk/**"]
    testPaths: ["**/*_test.go", "**/testdata/**", "test/**"]
    redTargets: [test, toolbox-check]
    redTargetFallbacks:
      toolbox-check: "sh test/e2e/wrapper/run.sh"
    exemptionClasses:
      docs-only: {paths: ["**/*.md"]}
      dependency-update: {paths: ["src/go.mod", "src/go.sum"]}
      revert: {subjectRegex: '^Revert "'}
`

const makefile = "SC_LINT_BIN ?= sc-lint\n" +
	"GO_TEST_PKGS ?= ./...\n" +
	"GO_TEST_RUN ?=\n" +
	"JUNIT ?= out/test/junit.xml\n" +
	"GO_TEST_PKGS_REL := $(patsubst ./src/%,./%,$(GO_TEST_PKGS))\n" +
	".PHONY: test\n" +
	"test:\n" +
	"\t@mkdir -p out/test \"$(dir $(JUNIT))\"\n" +
	"\t@rc=0; (cd src && go test -json -count=1 $(if $(GO_TEST_RUN),-run \"$(GO_TEST_RUN)\",) $(GO_TEST_PKGS_REL)) > out/test/test.json || rc=$$?; \\\n" +
	"\t\"$(SC_LINT_BIN)\" junit < out/test/test.json > \"$(JUNIT)\" || exit 1; exit $$rc\n"

const calcBase = `package calc

// Add returns a+b.
func Add(a, b int) int { return a + b }

// Double returns twice a.
func Double(a int) int { return a * 2 }

// Abs is incomplete: it does not handle negatives yet.
func Abs(a int) int { return a }

// Version names the data format.
func Version() string { return "v1" }
`

const calcAbsFixed = `package calc

// Add returns a+b.
func Add(a, b int) int { return a + b }

// Double returns twice a.
func Double(a int) int { return a * 2 }

// Abs returns the absolute value of a.
func Abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// Version names the data format.
func Version() string { return "v1" }
`

const calcTestBase = `package calc

import (
	"os"
	"strings"
	"testing"
)

func TestAddBase(t *testing.T) {
	if got := Add(1, 2); got != 3 {
		t.Fatalf("Add(1,2) = %d, want 3", got)
	}
}

func TestDoubleBase(t *testing.T) {
	if got := Double(2); got != 4 {
		t.Fatalf("Double(2) = %d, want 4", got)
	}
}

func TestAbsPositive(t *testing.T) {
	if got := Abs(3); got != 3 {
		t.Fatalf("Abs(3) = %d, want 3", got)
	}
}

func TestVersionMatchesTestdata(t *testing.T) {
	b, err := os.ReadFile("testdata/want.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != Version() {
		t.Fatalf("testdata says %q, Version() = %q", got, Version())
	}
}
`

const geomBase = `package geom

// Scale is incomplete: it ignores the factor.
func Scale(a, k int) int { return a }
`

const geomTestBase = `package geom

import "testing"

func TestScaleIdentity(t *testing.T) {
	if got := Scale(2, 1); got != 2 {
		t.Fatalf("Scale(2,1) = %d, want 2", got)
	}
}
`

const geomFixed = `package geom

// Scale multiplies a by k.
func Scale(a, k int) int { return a * k }
`

const absNegTest = `package calc

import "testing"

func TestAbsNegative(t *testing.T) {
	if got := Abs(-4); got != 4 {
		t.Fatalf("Abs(-4) = %d, want 4", got)
	}
}
`

const absZeroTest = `package calc

import "testing"

func TestAbsZero(t *testing.T) {
	if got := Abs(0); got != 0 {
		t.Fatalf("Abs(0) = %d, want 0", got)
	}
}
`

const subTest = `package calc

import "testing"

func TestSub(t *testing.T) {
	if got := Sub(5, 3); got != 2 {
		t.Fatalf("Sub(5,3) = %d, want 2", got)
	}
}
`

const subTestSkipped = `package calc

import "testing"

func TestSub(t *testing.T) {
	t.Skip("later")
	if got := Sub(5, 3); got != 2 {
		t.Fatalf("Sub(5,3) = %d, want 2", got)
	}
}
`

const scaleTest = `package geom

import "testing"

func TestScaleFactor(t *testing.T) {
	if got := Scale(2, 3); got != 6 {
		t.Fatalf("Scale(2,3) = %d, want 6", got)
	}
}
`

const stubMarker = "\n// SC-STUB(F99 99-AC-1)\n"

func calcWithStub(body string) string {
	return calcBase + stubMarker + "func Sub(a, b int) int { " + body + " }\n"
}

const deleteMark = "\x00delete"

// ---------------------------------------------------------------------------------------------

var (
	binOnce sync.Once
	binPath string
	binDir  string
	binErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if binDir != "" {
		os.RemoveAll(binDir)
	}
	os.Exit(code)
}

// lintBin builds cmd/sc-lint once; the synthetic Makefiles use its `junit` subcommand.
func lintBin(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		binDir, binErr = os.MkdirTemp("", "tdd-sclint-")
		if binErr != nil {
			return
		}
		binPath = filepath.Join(binDir, "sc-lint")
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/sc-lint")
		cmd.Dir = filepath.Join(repoRoot(t), "src")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = &buildError{err: err, out: string(out)}
		}
	})
	if binErr != nil {
		t.Fatalf("building sc-lint: %v", binErr)
	}
	return binPath
}

type buildError struct {
	err error
	out string
}

func (e *buildError) Error() string { return e.err.Error() + "\n" + e.out }

func repoRoot(t *testing.T) string {
	t.Helper()
	d, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(d, "SPEC.md")); err == nil {
			if _, err := os.Stat(filepath.Join(d, "plans", "dag.yaml")); err == nil {
				return d
			}
		}
		p := filepath.Dir(d)
		if p == d {
			t.Fatal("repository root not found")
		}
		d = p
	}
}

func syntheticPolicy(t *testing.T) tdd.Policy {
	t.Helper()
	var doc struct {
		Spec struct {
			TDD tdd.Policy `yaml:"tddDiscipline"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(policyYAML), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Spec.TDD
}

type baseOpts struct {
	noMakefile bool // the base commit has no Makefile (CD-56)
	brokenBase bool // a test fails at the base commit
}

type scen struct {
	t   *testing.T
	dir string
}

func (s *scen) git(args ...string) string {
	s.t.Helper()
	return gitIn(s.t, s.dir, "", args...)
}

func gitIn(t *testing.T, dir, author string, args ...string) string {
	t.Helper()
	name, email := "Dev", "dev@example.invalid"
	if author != "" {
		name, email = author, author+"@example.invalid"
	}
	full := append([]string{"-c", "user.name=" + name, "-c", "user.email=" + email, "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email, "GIT_COMMITTER_NAME="+name, "GIT_COMMITTER_EMAIL="+email)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (s *scen) write(files map[string]string) {
	s.t.Helper()
	for rel, content := range files {
		p := filepath.Join(s.dir, rel)
		if content == deleteMark {
			if err := os.Remove(p); err != nil {
				s.t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			s.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			s.t.Fatal(err)
		}
	}
}

// commit writes files, stages everything and commits with msg as the whole message; returns the SHA.
func (s *scen) commit(msg string, files map[string]string) string {
	return s.commitAs("", msg, files)
}

func (s *scen) commitAs(author, msg string, files map[string]string) string {
	s.t.Helper()
	s.write(files)
	gitIn(s.t, s.dir, author, "add", "-A")
	f := filepath.Join(s.t.TempDir(), "msg")
	if err := os.WriteFile(f, []byte(msg), 0o644); err != nil {
		s.t.Fatal(err)
	}
	gitIn(s.t, s.dir, author, "commit", "-q", "--allow-empty", "-F", f)
	return s.git("rev-parse", "HEAD")
}

// newScen builds the base commit on main and checks out a branch "feature/x".
func newScen(t *testing.T, o baseOpts) *scen {
	t.Helper()
	s := &scen{t: t, dir: t.TempDir()}
	s.git("init", "-q")
	s.git("checkout", "-q", "-b", "main")
	calcTest := calcTestBase
	if o.brokenBase {
		calcTest += "\nfunc TestBroken(t *testing.T) { t.Fatal(\"main is broken\") }\n"
	}
	files := map[string]string{
		".gitignore":                 "out/\n",
		"README.md":                  "synthetic\n",
		"policy/ci.yaml":             policyYAML,
		"src/go.mod":                 "module synth\n\ngo 1.24\n",
		"src/calc/calc.go":           calcBase,
		"src/calc/calc_test.go":      calcTest,
		"src/calc/testdata/want.txt": "v1\n",
		"src/geom/geom.go":           geomBase,
		"src/geom/geom_test.go":      geomTestBase,
		"src/calc/doc.go":            "// Package calc is a synthetic module.\npackage calc\n",
	}
	if !o.noMakefile {
		files["Makefile"] = makefile
	}
	s.commit("chore: base\n", files)
	s.git("checkout", "-q", "-b", "feature/x")
	return s
}

func redMsg(subject, target string) string {
	return subject + "\n\nTDD-Red-Target: " + target + "\nCo-Authored-By: Tester <tester@example.invalid>\n"
}

const pkgCalc = "test GO_TEST_PKGS=./src/calc"

// check runs the checker on the scenario; mod may adjust the options.
func (s *scen) check(mod func(*tdd.Options)) tdd.Report {
	s.t.Helper()
	pol := syntheticPolicy(s.t)
	o := tdd.Options{
		Root:   s.dir,
		Base:   "main",
		Branch: "feature/x",
		Author: "dev",
		Policy: &pol,
		Env:    []string{"SC_LINT_BIN=" + lintBin(s.t), "SC_IN_CONTAINER=1"},
	}
	if mod != nil {
		mod(&o)
	}
	rep, err := tdd.Check(o)
	if err != nil {
		s.t.Fatalf("Check returned an error (outcomes belong in the report): %v", err)
	}
	return rep
}

func failCodes(r tdd.Report) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range r.Failures {
		if !seen[f.Code] {
			seen[f.Code] = true
			out = append(out, f.Code)
		}
	}
	sort.Strings(out)
	return out
}

func hasCode(fs []tdd.Finding, code string) bool {
	for _, f := range fs {
		if f.Code == code {
			return true
		}
	}
	return false
}

func dump(r tdd.Report) string {
	var b strings.Builder
	b.WriteString("result=" + r.Result + "\n")
	for _, f := range r.Failures {
		b.WriteString("  failure " + f.Code + " " + f.Message + "\n")
	}
	for _, f := range r.Warnings {
		b.WriteString("  warning " + f.Code + " " + f.Message + "\n")
	}
	return b.String()
}

// wantFail asserts Result "fail" with exactly these failure codes.
func wantFail(t *testing.T, r tdd.Report, codes ...string) {
	t.Helper()
	sort.Strings(codes)
	if r.Result != "fail" || !reflect.DeepEqual(failCodes(r), codes) {
		t.Errorf("want result=fail with failure codes %v, got:\n%s", codes, dump(r))
	}
}

// wantFailHas asserts Result "fail", the code present and the other codes absent.
func wantFailHas(t *testing.T, r tdd.Report, code string, absent ...string) {
	t.Helper()
	if r.Result != "fail" || !hasCode(r.Failures, code) {
		t.Errorf("want result=fail including %s, got:\n%s", code, dump(r))
	}
	for _, a := range absent {
		if hasCode(r.Failures, a) {
			t.Errorf("unexpected %s in:\n%s", a, dump(r))
		}
	}
}

func wantPass(t *testing.T, r tdd.Report) {
	t.Helper()
	if r.Result != "pass" || len(r.Failures) != 0 {
		t.Errorf("want result=pass with no failures, got:\n%s", dump(r))
	}
}

func id(pkg, test string) string { return "synth/" + pkg + "." + test }

// stubRed builds the common RED+GREEN for the missing-symbol case: R adds TestSub and a stub of Sub.
func stubRed(s *scen, stubBody string) {
	s.commit(redMsg("test(F99): Sub is specified", pkgCalc), map[string]string{
		"src/calc/sub_test.go": subTest,
		"src/calc/calc.go":     calcWithStub(stubBody),
	})
}

// absRed adds R for the incomplete Abs (no stub needed: the symbol exists).
func absRed(s *scen) string {
	return s.commit(redMsg("test(F99): Abs handles negatives", pkgCalc), map[string]string{"src/calc/abs_test.go": absNegTest})
}

func absGreen(s *scen) string {
	return s.commit("fix: Abs handles negatives\n", map[string]string{"src/calc/calc.go": calcAbsFixed})
}

// ---------------------------------------------------------------------------------------------

func TestDiscipline(t *testing.T) {
	t.Run("not-applicable docs only", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit("docs: explain\n", map[string]string{"README.md": "synthetic, explained\n"})
		r := s.check(nil)
		if r.Result != "not-applicable" || len(r.Failures) != 0 {
			t.Errorf("want not-applicable, got:\n%s", dump(r))
		}
	})

	t.Run("not-applicable tests only (T1)", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit("test: more\n", map[string]string{"src/calc/abs_test.go": absZeroTest})
		r := s.check(nil)
		if r.Result != "not-applicable" || len(r.Failures) != 0 {
			t.Errorf("want not-applicable, got:\n%s", dump(r))
		}
	})

	t.Run("not-applicable agent branch (T0)", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit("feat: Abs\n", map[string]string{"src/calc/calc.go": calcAbsFixed})
		r := s.check(func(o *tdd.Options) { o.Branch = "agent/task-1"; o.Author = "sc-runner"; o.ProvenanceBound = true }) // CD-58b
		if r.Result != "not-applicable" || len(r.Failures) != 0 {
			t.Errorf("want not-applicable, got:\n%s", dump(r))
		}
	})

	t.Run("valid exemption", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit("chore: bump dependency\n\nTDD-Exempt: dependency-update: bump golang.org/x/text to v0.20.0 for the advisory\nCo-Authored-By: Dev <dev@example.invalid>\n",
			map[string]string{"src/go.mod": "module synth\n\ngo 1.24\n\n// bumped\n"})
		r := s.check(nil)
		if r.Result != "exempt" || r.Exemption != "dependency-update" || len(r.Failures) != 0 {
			t.Errorf("want exempt (dependency-update), got exemption=%q:\n%s", r.Exemption, dump(r))
		}
	})

	t.Run("invalid exemption class", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit("chore: tweak\n\nTDD-Exempt: made-up-class: this reason is long enough to be valid\nCo-Authored-By: Dev <dev@example.invalid>\n",
			map[string]string{"src/calc/calc.go": calcAbsFixed})
		wantFailHas(t, s.check(nil), "TDD_EXEMPTION_INVALID")
	})

	t.Run("invalid exemption path constraint", func(t *testing.T) { // 52-AC-4 (T2)
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit("docs: notes\n\nTDD-Exempt: docs-only: documentation only, honest reason here\nCo-Authored-By: Dev <dev@example.invalid>\n",
			map[string]string{"src/calc/calc.go": calcAbsFixed})
		wantFailHas(t, s.check(nil), "TDD_EXEMPTION_INVALID")
	})

	t.Run("invalid exemption reason too short", func(t *testing.T) { // 52-AC-4 (T2)
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit("chore: bump\n\nTDD-Exempt: dependency-update: bump\nCo-Authored-By: Dev <dev@example.invalid>\n",
			map[string]string{"src/go.mod": "module synth\n\ngo 1.24\n\n// bumped\n"})
		wantFailHas(t, s.check(nil), "TDD_EXEMPTION_INVALID")
	})

	t.Run("no RED commit", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit("feat: Abs handles negatives\n", map[string]string{"src/calc/calc.go": calcAbsFixed, "src/calc/abs_test.go": absNegTest})
		wantFail(t, s.check(nil), "TDD_NO_RED_COMMIT")
	})

	t.Run("implementation before RED", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		absGreen(s)
		absRed(s)
		wantFailHas(t, s.check(nil), "TDD_IMPLEMENTATION_BEFORE_RED", "TDD_NO_RED_COMMIT")
	})

	t.Run("RED that fails to compile", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit(redMsg("test(F99): Sub is specified", pkgCalc), map[string]string{"src/calc/sub_test.go": subTest})
		s.commit("feat: Sub\n", map[string]string{"src/calc/calc.go": calcBase + "\nfunc Sub(a, b int) int { return a - b }\n"})
		r := s.check(nil)
		wantFail(t, r, "TDD_RED_BUILD_FAILURE")
		if r.Red.BuildOK {
			t.Errorf("red.build_ok must be false for a RED that does not compile")
		}
	})

	t.Run("RED failing only on a pre-existing test", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit(redMsg("test(F99): data format moves to v2", pkgCalc), map[string]string{"src/calc/testdata/want.txt": "v2\n"})
		s.commit("feat: Version is v2\n", map[string]string{"src/calc/calc.go": strings.Replace(calcBase, `"v1"`, `"v2"`, 1)})
		wantFail(t, s.check(nil), "TDD_RED_PREEXISTING_FAILURE")
	})

	t.Run("RED that passes", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit(redMsg("test(F99): Abs of zero", pkgCalc), map[string]string{"src/calc/abs_test.go": absZeroTest})
		absGreen(s)
		wantFail(t, s.check(nil), "TDD_RED_PASSED")
	})

	t.Run("RED tests deleted at HEAD", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		stubRed(s, "return 0")
		s.commit("feat: Sub\n", map[string]string{
			"src/calc/calc.go":     calcBase + "\nfunc Sub(a, b int) int { return a - b }\n",
			"src/calc/sub_test.go": deleteMark,
		})
		wantFail(t, s.check(nil), "TDD_RED_TESTS_NOT_GREEN")
	})

	t.Run("RED tests skipped at HEAD", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		stubRed(s, "return 0")
		s.commit("feat: Sub\n", map[string]string{
			"src/calc/calc.go":     calcBase + "\nfunc Sub(a, b int) int { return a - b }\n",
			"src/calc/sub_test.go": subTestSkipped,
		})
		wantFail(t, s.check(nil), "TDD_RED_TESTS_NOT_GREEN")
	})

	t.Run("merge commit", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		absRed(s)
		absGreen(s)
		s.git("checkout", "-q", "-b", "side", "main")
		s.commit("docs: side note\n", map[string]string{"NOTES.md": "side\n"})
		s.git("checkout", "-q", "feature/x")
		s.git("merge", "--no-ff", "-q", "-m", "Merge side", "side")
		wantFailHas(t, s.check(nil), "TDD_MERGE_COMMIT")
	})

	t.Run("renovate/feature by a human without RED", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commitAs("alice", "feat: Abs\n", map[string]string{"src/calc/calc.go": calcAbsFixed})
		r := s.check(func(o *tdd.Options) { o.Branch = "renovate/feature"; o.Author = "alice" })
		wantFail(t, r, "TDD_NO_RED_COMMIT")
		if !hasCode(r.Warnings, "TDD_EXCLUSION_INVALID") && !hasCode(r.Failures, "TDD_EXCLUSION_INVALID") {
			t.Errorf("refused exclusion not recorded:\n%s", dump(r))
		}
	})

	t.Run("renovate/go-x by sc-renovate outside dependency-update paths", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commitAs("sc-renovate", "chore(deps): bump and patch\n", map[string]string{
			"src/go.mod":       "module synth\n\ngo 1.24\n\n// bumped\n",
			"src/calc/calc.go": calcAbsFixed,
		})
		r := s.check(func(o *tdd.Options) { o.Branch = "renovate/go-x"; o.Author = "sc-renovate" })
		wantFail(t, r, "TDD_NO_RED_COMMIT") // evaluated normally
		if !hasCode(r.Warnings, "TDD_EXCLUSION_INVALID") && !hasCode(r.Failures, "TDD_EXCLUSION_INVALID") {
			t.Errorf("refused exclusion not recorded:\n%s", dump(r))
		}
	})

	t.Run("renovate/go-x by sc-renovate inside dependency-update paths", func(t *testing.T) { // 52-AC-4 (T0)
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commitAs("sc-renovate", "chore(deps): bump\n", map[string]string{"src/go.mod": "module synth\n\ngo 1.24\n\n// bumped\n"})
		r := s.check(func(o *tdd.Options) { o.Branch = "renovate/go-x"; o.Author = "sc-renovate" })
		if r.Result != "not-applicable" || len(r.Failures) != 0 {
			t.Errorf("want not-applicable, got:\n%s", dump(r))
		}
	})

	t.Run("RED with a valid declared stub that HEAD replaces", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		stubRed(s, "return 0")
		s.commit("feat: Sub\n", map[string]string{"src/calc/calc.go": calcBase + "\nfunc Sub(a, b int) int { return a - b }\n"})
		r := s.check(nil)
		wantPass(t, r)
		if !reflect.DeepEqual(r.Red.Failed, []string{id("calc", "TestSub")}) || !r.Red.BuildOK {
			t.Errorf("red.failed=%v build_ok=%v, want [synth/calc.TestSub] true", r.Red.Failed, r.Red.BuildOK)
		}
	})

	t.Run("stub in a new file with a type declaration (CD-50)", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit(redMsg("test(F99): Compute", pkgCalc), map[string]string{
			"src/calc/compute_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestCompute(t *testing.T) {\n\tif got := Compute().N; got != 5 {\n\t\tt.Fatalf(\"N = %d, want 5\", got)\n\t}\n}\n",
			"src/calc/compute.go":      "package calc\n\n// Result holds a computation.\ntype Result struct{ N int }\n\n// SC-STUB(F99 99-AC-2)\nfunc Compute() Result { return Result{} }\n",
		})
		s.commit("feat: Compute\n", map[string]string{
			"src/calc/compute.go": "package calc\n\n// Result holds a computation.\ntype Result struct{ N int }\n\n// Compute returns the answer.\nfunc Compute() Result { return Result{N: 5} }\n",
		})
		wantPass(t, s.check(nil))
	})

	t.Run("stub returning a non-zero value", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		stubRed(s, "return 1")
		s.commit("feat: Sub\n", map[string]string{"src/calc/calc.go": calcBase + "\nfunc Sub(a, b int) int { return a - b }\n"})
		wantFailHas(t, s.check(nil), "TDD_STUB_INVALID")
	})

	t.Run("stub change modifying an existing line", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		src := strings.Replace(calcWithStub("return 0"), "return a * 2", "return a + a", 1)
		s.commit(redMsg("test(F99): Sub is specified", pkgCalc), map[string]string{"src/calc/sub_test.go": subTest, "src/calc/calc.go": src})
		s.commit("feat: Sub\n", map[string]string{"src/calc/calc.go": calcBase + "\nfunc Sub(a, b int) int { return a - b }\n"})
		wantFailHas(t, s.check(nil), "TDD_STUB_INVALID")
	})

	t.Run("new file with a var next to a stub", func(t *testing.T) { // 52-AC-4 (CD-50)
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit(redMsg("test(F99): Sub is specified", pkgCalc), map[string]string{
			"src/calc/sub_test.go": subTest,
			"src/calc/sub.go":      "package calc\n\nvar limit = 3\n\n// SC-STUB(F99 99-AC-1)\nfunc Sub(a, b int) int { return 0 }\n",
		})
		s.commit("feat: Sub\n", map[string]string{"src/calc/sub.go": "package calc\n\nfunc Sub(a, b int) int { return a - b }\n"})
		wantFailHas(t, s.check(nil), "TDD_STUB_INVALID")
	})

	t.Run("stub marker still present at HEAD", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		stubRed(s, "return 0")
		s.commit("feat: Sub\n", map[string]string{"src/calc/calc.go": calcBase + stubMarker + "func Sub(a, b int) int { return a - b }\n"})
		wantFail(t, s.check(nil), "TDD_STUB_NOT_REPLACED")
	})

	t.Run("valid case", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		absRed(s)
		absGreen(s)
		r := s.check(nil)
		wantPass(t, r)
		if !reflect.DeepEqual(r.Red.Failed, []string{id("calc", "TestAbsNegative")}) || !r.Red.BuildOK {
			t.Errorf("red.failed=%v build_ok=%v, want [synth/calc.TestAbsNegative] true", r.Red.Failed, r.Red.BuildOK)
		}
	})
}

// T4 (CD-54, CD-56): the red-target line.
func TestDiscipline_RedTargetLine(t *testing.T) {
	green := map[string]string{"src/calc/calc.go": calcAbsFixed}
	t.Run("line in the body, not a trailer, is accepted", func(t *testing.T) { // 52-AC-4 CD-54
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit("test(F99): Abs handles negatives\n\nTDD-Red-Target: test GO_TEST_PKGS=./src/calc\n\nA note between.\n\nCo-Authored-By: Tester <tester@example.invalid>\n",
			map[string]string{"src/calc/abs_test.go": absNegTest})
		s.commit("fix: Abs\n", green)
		wantPass(t, s.check(nil))
	})
	t.Run("two lines", func(t *testing.T) { // 52-AC-4 CD-56
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit("test(F99): Abs handles negatives\n\nTDD-Red-Target: test GO_TEST_PKGS=./src/calc\nTDD-Red-Target: test GO_TEST_PKGS=./src/geom\n",
			map[string]string{"src/calc/abs_test.go": absNegTest})
		s.commit("fix: Abs\n", green)
		wantFail(t, s.check(nil), "TDD_RED_TARGET_INVALID")
	})
	t.Run("no line", func(t *testing.T) { // 52-AC-4 CD-56
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit("test(F99): Abs handles negatives\n", map[string]string{"src/calc/abs_test.go": absNegTest})
		s.commit("fix: Abs\n", green)
		wantFail(t, s.check(nil), "TDD_RED_TARGET_INVALID")
	})
	t.Run("target not in redTargets", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit(redMsg("test(F99): Abs handles negatives", "nosuch-target"), map[string]string{"src/calc/abs_test.go": absNegTest})
		s.commit("fix: Abs\n", green)
		wantFail(t, s.check(nil), "TDD_RED_TARGET_INVALID")
	})
	t.Run("indented line is not a target line", func(t *testing.T) { // 52-AC-4 CD-56 (must start a line)
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit("test(F99): Abs handles negatives\n\n  TDD-Red-Target: test GO_TEST_PKGS=./src/calc\n", map[string]string{"src/calc/abs_test.go": absNegTest})
		s.commit("fix: Abs\n", green)
		wantFail(t, s.check(nil), "TDD_RED_TARGET_INVALID")
	})
	t.Run("multi-package GO_TEST_PKGS", func(t *testing.T) { // 52-AC-4 CD-54
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit(redMsg("test(F99): Abs and Scale", "test GO_TEST_PKGS=./src/calc ./src/geom"),
			map[string]string{"src/calc/abs_test.go": absNegTest, "src/geom/scale_test.go": scaleTest})
		s.commit("fix: Abs and Scale\n", map[string]string{"src/calc/calc.go": calcAbsFixed, "src/geom/geom.go": geomFixed})
		r := s.check(nil)
		wantPass(t, r)
		want := []string{id("calc", "TestAbsNegative"), id("geom", "TestScaleFactor")}
		got := append([]string{}, r.Red.Failed...)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("red.failed = %v, want %v", got, want)
		}
		if len(r.Red.Commits) != 1 || !reflect.DeepEqual(r.Red.Commits[0].Packages, []string{"./src/calc", "./src/geom"}) {
			t.Errorf("red.commits = %+v, want one run over ./src/calc ./src/geom", r.Red.Commits)
		}
	})
	t.Run("default packages are those with changed test files", func(t *testing.T) { // 52-AC-4 T4
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit(redMsg("test(F99): Abs handles negatives", "test"), map[string]string{"src/calc/abs_test.go": absNegTest})
		s.commit("fix: Abs\n", green)
		r := s.check(nil)
		wantPass(t, r)
		if len(r.Red.Commits) != 1 || !reflect.DeepEqual(r.Red.Commits[0].Packages, []string{"./src/calc"}) {
			t.Errorf("red.commits = %+v, want one run over ./src/calc", r.Red.Commits)
		}
	})
}

// T5: baseline.
func TestDiscipline_Baseline(t *testing.T) {
	t.Run("target fails at B", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{brokenBase: true})
		absRed(s)
		absGreen(s)
		wantFail(t, s.check(nil), "TDD_BASELINE_FAILED")
	})
}

// Non-Go red targets, fallbacks and the wrapper-output adapter (CD-54, CD-56). The base has no Makefile
// and no wrapper script, like F01's first RED.
const wrapperRun = `#!/bin/sh
rc=0
chk() { if [ -f toolbox/tool.sh ]; then echo "  ok   $1"; else echo "  FAIL $1"; echo "       detail line"; rc=1; fi; }
echo "  ok   always passes"
chk "tool present"
chk "tool present"
echo "alpha.sh: 3 checks, 2 failed"
echo "  ok   beta check"
echo "beta.sh: 1 checks, 0 failed"
exit $rc
`

func TestDiscipline_WrapperAdapterAndFallback(t *testing.T) {
	tc := "toolbox-check"
	t.Run("fallback used, empty baseline, adapter ids and duplicate suffix", func(t *testing.T) { // 52-AC-4 CD-56
		t.Parallel()
		s := newScen(t, baseOpts{noMakefile: true})
		s.commit(redMsg("test(F99): toolbox wrapper", tc), map[string]string{"test/e2e/wrapper/run.sh": wrapperRun})
		s.commit("feat: tool\n", map[string]string{"toolbox/tool.sh": "#!/bin/sh\n:\n"})
		r := s.check(nil)
		wantPass(t, r)
		want := []string{"alpha.sh.tool present", "alpha.sh.tool present #2"}
		got := append([]string{}, r.Red.Failed...)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) || !r.Red.BuildOK {
			t.Errorf("red.failed = %v build_ok=%v, want %v true", r.Red.Failed, r.Red.BuildOK, want)
		}
		if len(r.Red.Commits) != 1 || !r.Red.Commits[0].FallbackUsed {
			t.Errorf("the report must record that the redTargetFallbacks command was used: %+v", r.Red.Commits)
		}
	})
	t.Run("harness crash without a FAIL line is a build failure", func(t *testing.T) { // 52-AC-4 CD-56
		t.Parallel()
		s := newScen(t, baseOpts{noMakefile: true})
		s.commit(redMsg("test(F99): toolbox wrapper", tc), map[string]string{
			"test/e2e/wrapper/run.sh": "#!/bin/sh\necho \"  ok   a\"\necho \"x.sh: 1 checks, 0 failed\"\nexit 3\n"})
		s.commit("feat: tool\n", map[string]string{"toolbox/tool.sh": "#!/bin/sh\n:\n"})
		wantFail(t, s.check(nil), "TDD_RED_BUILD_FAILURE")
	})
	t.Run("all checks ok and exit 0 is a passing RED", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{noMakefile: true})
		s.commit(redMsg("test(F99): toolbox wrapper", tc), map[string]string{
			"test/e2e/wrapper/run.sh": "#!/bin/sh\necho \"  ok   a\"\necho \"x.sh: 1 checks, 0 failed\"\n"})
		s.commit("feat: tool\n", map[string]string{"toolbox/tool.sh": "#!/bin/sh\n:\n"})
		wantFail(t, s.check(nil), "TDD_RED_PASSED")
	})
	t.Run("HEAD must pass every failed id", func(t *testing.T) { // 52-AC-4 T7 with the adapter
		t.Parallel()
		s := newScen(t, baseOpts{noMakefile: true})
		s.commit(redMsg("test(F99): toolbox wrapper", tc), map[string]string{"test/e2e/wrapper/run.sh": wrapperRun})
		// The tool is added, but the wrapper no longer reports the RED checks (deleted check lines).
		s.commit("feat: tool\n", map[string]string{
			"toolbox/tool.sh":         "#!/bin/sh\n:\n",
			"test/e2e/wrapper/run.sh": "#!/bin/sh\necho \"  ok   other\"\necho \"alpha.sh: 1 checks, 0 failed\"\n",
		})
		wantFail(t, s.check(nil), "TDD_RED_TESTS_NOT_GREEN")
	})
}

// Several RED commits in one PR; chore commits; T8.
func TestDiscipline_MultipleRedCommits(t *testing.T) {
	t.Run("both evaluated and valid", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		r1 := absRed(s)
		absGreen(s)
		r2 := s.commit(redMsg("test(F99): Scale factor", "test GO_TEST_PKGS=./src/geom"), map[string]string{"src/geom/scale_test.go": scaleTest})
		s.commit("fix: Scale\n", map[string]string{"src/geom/geom.go": geomFixed})
		r := s.check(nil)
		wantPass(t, r)
		if len(r.Red.Commits) != 2 || !sameCommit(r.Red.Commits[0].Commit, r1) || !sameCommit(r.Red.Commits[1].Commit, r2) {
			t.Fatalf("want both RED commits evaluated in order (%s, %s), got %+v", r1, r2, r.Red.Commits)
		}
		for i, c := range r.Red.Commits {
			if len(c.Failed) == 0 || !c.BuildOK {
				t.Errorf("RED run %d: failed=%v build_ok=%v", i, c.Failed, c.BuildOK)
			}
		}
		if len(r.Red.Failed) != 2 {
			t.Errorf("red.failed = %v, want the union of both runs", r.Red.Failed)
		}
	})
	t.Run("the second RED is judged too", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		absRed(s)
		absGreen(s)
		s.commit(redMsg("test(F99): Abs of zero", pkgCalc), map[string]string{"src/calc/abs_zero_test.go": absZeroTest})
		s.commit("fix: Scale\n", map[string]string{"src/geom/geom.go": geomFixed})
		wantFail(t, s.check(nil), "TDD_RED_PASSED")
	})
	t.Run("implementation between two REDs is allowed", func(t *testing.T) { // 52-AC-4 T3 (first R governs)
		t.Parallel()
		s := newScen(t, baseOpts{})
		absRed(s)
		absGreen(s)
		s.commit(redMsg("test(F99): Scale factor", "test GO_TEST_PKGS=./src/geom"), map[string]string{"src/geom/scale_test.go": scaleTest})
		s.commit("fix: Scale\n", map[string]string{"src/geom/geom.go": geomFixed})
		if r := s.check(nil); hasCode(r.Failures, "TDD_IMPLEMENTATION_BEFORE_RED") {
			t.Errorf("only commits before the first R count:\n%s", dump(r))
		}
	})
	t.Run("chore test-only commit is not an R", func(t *testing.T) { // 52-AC-4 CD-54
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit("chore(F99): cover Abs of zero\n", map[string]string{"src/calc/abs_zero_test.go": absZeroTest})
		s.commit("fix: Abs handles negatives\n", map[string]string{"src/calc/calc.go": calcAbsFixed})
		wantFail(t, s.check(nil), "TDD_NO_RED_COMMIT")
	})
	t.Run("chore test-only commit before a real R is neither R nor implementation", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit("chore(F99): cover Abs of zero\n", map[string]string{"src/calc/abs_zero_test.go": absZeroTest})
		absRed(s)
		absGreen(s)
		r := s.check(nil)
		wantPass(t, r)
		if len(r.Red.Commits) != 1 {
			t.Errorf("want exactly one RED run, got %+v", r.Red.Commits)
		}
	})
	t.Run("modified existing test body counts as new RED", func(t *testing.T) { // 52-AC-4 T6
		t.Parallel()
		s := newScen(t, baseOpts{})
		s.commit(redMsg("test(F99): Double triples", pkgCalc), map[string]string{
			"src/calc/calc_test.go": strings.Replace(calcTestBase, "got != 4", "got != 6", 1)})
		s.commit("feat: Double triples\n", map[string]string{"src/calc/calc.go": strings.Replace(calcBase, "a * 2", "a * 3", 1)})
		r := s.check(nil)
		wantPass(t, r)
		if !reflect.DeepEqual(r.Red.Failed, []string{id("calc", "TestDoubleBase")}) {
			t.Errorf("red.failed = %v", r.Red.Failed)
		}
	})
	t.Run("T8 warning when a RED test file is modified later", func(t *testing.T) { // 52-AC-4
		t.Parallel()
		s := newScen(t, baseOpts{})
		absRed(s)
		s.commit("fix: Abs handles negatives\n", map[string]string{
			"src/calc/calc.go":     calcAbsFixed,
			"src/calc/abs_test.go": absNegTest + "\n// reformatted\n",
		})
		r := s.check(nil)
		wantPass(t, r)
		if !hasCode(r.Warnings, "TDD_RED_TEST_MODIFIED") {
			t.Errorf("want warning TDD_RED_TEST_MODIFIED:\n%s", dump(r))
		}
	})
}

// 52-AC-3 / 52-AC-4: `sc-lint tdd-discipline` prints the code and writes out/tdd-discipline/report.json.
// Flags: --root DIR --base REF --branch NAME --author LOGIN.
func TestDiscipline_CLI(t *testing.T) {
	t.Setenv("SC_LINT_BIN", lintBin(t))
	t.Setenv("SC_IN_CONTAINER", "1")
	run := func(s *scen) (int, string) {
		t.Helper()
		return runLintMain(t, "tdd-discipline", "--root", s.dir, "--base", "main", "--branch", "feature/x", "--author", "dev")
	}
	t.Run("pass writes report.json with red.failed and red.build_ok", func(t *testing.T) { // 52-AC-3 52-AC-4
		s := newScen(t, baseOpts{})
		absRed(s)
		absGreen(s)
		code, out := run(s)
		if code != 0 {
			t.Fatalf("exit %d, want 0:\n%s", code, out)
		}
		b, err := os.ReadFile(filepath.Join(s.dir, "out", "tdd-discipline", "report.json"))
		if err != nil {
			t.Fatal(err)
		}
		var rep struct {
			Result string `json:"result"`
			Red    struct {
				Failed  []string `json:"failed"`
				BuildOK bool     `json:"build_ok"`
			} `json:"red"`
		}
		if err := jsonUnmarshal(b, &rep); err != nil {
			t.Fatal(err)
		}
		if rep.Result != "pass" || len(rep.Red.Failed) == 0 || !rep.Red.BuildOK {
			t.Errorf("report.json: %s", b)
		}
	})
	t.Run("failure prints the exact code and exits non-zero", func(t *testing.T) { // 52-AC-4
		s := newScen(t, baseOpts{})
		s.commit("feat: Abs handles negatives\n", map[string]string{"src/calc/calc.go": calcAbsFixed})
		code, out := run(s)
		if code == 0 || !strings.Contains(out, "TDD_NO_RED_COMMIT") {
			t.Errorf("exit %d, output lacks TDD_NO_RED_COMMIT:\n%s", code, out)
		}
		if _, err := os.Stat(filepath.Join(s.dir, "out", "tdd-discipline", "report.json")); err != nil {
			t.Errorf("report.json not written on failure: %v", err)
		}
	})
}

func sameCommit(a, b string) bool {
	return a != "" && (strings.HasPrefix(a, b) || strings.HasPrefix(b, a))
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func runLintMain(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := lint.Main(args, &out, &errb)
	return code, out.String() + errb.String()
}
