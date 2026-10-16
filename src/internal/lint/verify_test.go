package lint_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/lint"
)

// Slice S4 of F01: `make verify` (summary.json), lint and determinism rules, `make build`, offline and
// no-paid runs. These tests run inside the ci image (SC_IN_CONTAINER=1) and offline (`--network none`).
//
// Host-level parts that cannot be evidenced here (the orchestrator records them from host and CI runs):
//   - 01-AC-3: "a host with Docker (and, separately, rootless Podman) but no Go, OpenTofu or kubectl";
//   - 01-AC-4: GitHub Actions summary.json parity.
//
// The nested `make verify` evidence (two offline, no-paid runs) is test/e2e/verify/run, not a Go test: a
// verify started from inside `make test` would run every RED test again.

// verifyTargetOrder is the tier-V order of plan 01 section 7.1 (catalogue row order; `gitops-cache` is
// the first step), restricted to the targets that exist at F01.
var verifyTargetOrder = []string{
	"gitops-cache", "fmt-check", "lint", "layout-check", "locks-check", "secrets-scan", "contracts",
	"test", "build", "toolbox-check", "dag-check", "quarantine-check",
}

// makeEnv is the in-container environment of plan 52 section 9.2 plus extras.
func makeEnv(extra ...string) []string {
	env := os.Environ()
	filtered := env[:0:0]
	for _, e := range env {
		if strings.HasPrefix(e, "SC_IN_CONTAINER=") {
			continue
		}
		filtered = append(filtered, e)
	}
	return append(filtered, append([]string{"SC_IN_CONTAINER=1", "SC_TEST_SEED=20261013"}, extra...)...)
}

func runCmd(t *testing.T, dir string, timeout time.Duration, env []string, name string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return buf.String(), err
}

// workCopy snapshots the working tree (tracked and untracked, not ignored) into a fresh one-commit
// repository, so uncommitted work is judged too. .cache is shared with the real tree (symlink, ignored).
func workCopy(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		src := filepath.Join(root, rel)
		fi, err := os.Lstat(src)
		if err != nil {
			continue // deleted in the working tree
		}
		to := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			l, _ := os.Readlink(src)
			if err := os.Symlink(l, to); err != nil {
				t.Fatal(err)
			}
			continue
		}
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, b, fi.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dst, "init", "-q")
	git(t, dst, "add", "-A")
	git(t, dst, "commit", "-q", "-m", "snapshot")
	linkCache(t, root, dst)
	return dst
}

// linkCache makes .cache a real directory in the clone and symlinks only its children (go/, golangci-lint/,
// ...) from the real tree, so no implementation-side knob is needed for a symlinked .cache.
func linkCache(t *testing.T, root, dst string) {
	t.Helper()
	cache := filepath.Join(root, ".cache")
	if _, err := os.Stat(filepath.Join(cache, "go", "mod")); err != nil {
		t.Fatalf("warm module cache %s/go/mod missing (run `go mod download` first, 01-AC-6): %v", cache, err)
	}
	ents, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dst, ".cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		link := filepath.Join(dst, ".cache", e.Name())
		if _, err := os.Lstat(link); err == nil {
			continue
		}
		if err := os.Symlink(filepath.Join(cache, e.Name()), link); err != nil {
			t.Fatal(err)
		}
	}
}

// 01-AC-6 / plan 01 section 7.1 `test` row: the interface is GO_TEST_PKGS, GO_TEST_RUN and JUNIT, nothing
// else. No -skip flag, no GO_TEST_SKIP variable, no `/.cache` line besides `/.cache/` in .gitignore (6.6).
func TestTestTarget_NoSelectionKnob_01AC6(t *testing.T) {
	root := repoRoot(t)
	out, err := runCmd(t, root, 2*time.Minute, makeEnv(), "make", "-n", "test")
	if err != nil {
		t.Fatalf("make -n test: %v\n%s", err, out)
	}
	if strings.Contains(out, "-skip") {
		t.Errorf("`make -n test` passes a -skip flag:\n%s", out)
	}
	mk := readFile(t, root, "mk/test.mk")
	for _, bad := range []string{"-skip", "GO_TEST_SKIP", "SC_NESTED_VERIFY"} {
		if strings.Contains(mk, bad) {
			t.Errorf("mk/test.mk mentions %q (not in the plan 01 section 7.1 interface)", bad)
		}
	}
	for _, l := range strings.Split(readFile(t, root, ".gitignore"), "\n") {
		if strings.TrimSpace(l) == "/.cache" {
			t.Errorf(".gitignore has a bare /.cache line; plan 01 section 6.6 lists only /.cache/")
		}
	}
}

// 01-AC-6: tests that shell out to make must not be satisfied by cached go test results.
func TestTestTarget_NoTestCache_01AC6(t *testing.T) {
	root := repoRoot(t)
	out, err := runCmd(t, root, 2*time.Minute, makeEnv(), "make", "-n", "test")
	if err != nil {
		t.Fatalf("make -n test: %v\n%s", err, out)
	}
	if !strings.Contains(out, "-count=1") {
		t.Errorf("`make -n test` does not pass -count=1 to go test:\n%s", out)
	}
}

// 52-AC-8 (plan 52 section 9.2): the ci image environment defaults.
func TestContainerfile_CIEnvDefaults_52AC8(t *testing.T) {
	root := repoRoot(t)
	var env []string
	for _, l := range strings.Split(strings.ReplaceAll(readFile(t, root, "toolbox/Containerfile"), "\\\n", " "), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "ENV ") {
			env = append(env, l)
		}
	}
	all := strings.Join(env, " ")
	for _, want := range []string{"SC_IN_CONTAINER=1", "TZ=UTC", "LC_ALL=C.UTF-8", "SC_FORBID_PAID=1", "SC_TEST_SEED=20261013", "GOFLAGS=-mod=readonly"} {
		if !regexp.MustCompile(`(^|\s)` + regexp.QuoteMeta(want) + `(\s|$)`).MatchString(all) {
			t.Errorf("toolbox/Containerfile ENV lacks %s:\n%s", want, all)
		}
	}
}

// 01-AC-9: the version comes from `git describe --abbrev=12` so shallow and full clones agree.
func TestBuild_DescribeAbbrev_01AC9(t *testing.T) {
	root := repoRoot(t)
	out, err := runCmd(t, root, 2*time.Minute, makeEnv(), "make", "-n", "build")
	if err != nil {
		t.Fatalf("make -n build: %v\n%s", err, out)
	}
	if !strings.Contains(readFile(t, root, "mk/build.mk"), "git describe") || !strings.Contains(readFile(t, root, "mk/build.mk"), "--abbrev=12") {
		t.Errorf("mk/build.mk: version must use `git describe ... --abbrev=12`")
	}
}

// 01-AC-5 (S1/S2 review follow-up): with SC_IN_CONTAINER=1 `make -n verify` runs the tier-V targets in the
// plan 01 section 7.1 order.
func TestVerify_DryRunSequenceOrder_01AC5(t *testing.T) {
	root := repoRoot(t)
	out, err := runCmd(t, root, 2*time.Minute, makeEnv(), "make", "-n", "--trace", "verify")
	if err != nil {
		t.Fatalf("SC_IN_CONTAINER=1 make -n verify: %v\n%s", err, out)
	}
	if strings.Contains(out, "./bootstrap exec") {
		t.Errorf("no re-exec expected inside the container:\n%s", out)
	}
	re := regexp.MustCompile(`update target '([^']+)'`)
	known := map[string]bool{}
	for _, v := range verifyTargetOrder {
		known[v] = true
	}
	var got []string
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(out, -1) {
		if known[m[1]] && !seen[m[1]] {
			seen[m[1]] = true
			got = append(got, m[1])
		}
	}
	if !reflect.DeepEqual(got, verifyTargetOrder) {
		t.Errorf("tier-V sequence of `make -n verify`:\n got  %v\n want %v\n%s", got, verifyTargetOrder, out)
	}
}

// 01-AC-6: `make gitops-cache` exists and succeeds offline (no pinned charts at F01).
func TestGitopsCache_OfflineNoop_01AC6(t *testing.T) {
	root := repoRoot(t)
	out, err := runCmd(t, root, 2*time.Minute, makeEnv(), "make", "gitops-cache")
	if err != nil {
		t.Fatalf("make gitops-cache: %v\n%s", err, out)
	}
}

// 01-AC-9 / plan 01 section 6.2: build flags of `make build`.
func TestBuild_RecipeFlags_01AC9(t *testing.T) {
	root := repoRoot(t)
	out, err := runCmd(t, root, 2*time.Minute, makeEnv(), "make", "-n", "build")
	if err != nil {
		t.Fatalf("make -n build: %v\n%s", err, out)
	}
	for _, want := range []string{
		"CGO_ENABLED=0", "go build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w",
		"-X strange.company/sc/internal/version.Version=", "-X strange.company/sc/internal/version.Commit=",
		"out/bin/linux_",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("`make -n build` lacks %q:\n%s", want, out)
		}
	}
}

// 01-AC-9: make build twice in clean clones of the same commit gives identical sha256 of out/bin/linux_amd64/*.
func TestBuild_Reproducible_01AC9(t *testing.T) {
	base := workCopy(t)
	sums := make([]map[string]string, 2)
	for i := range sums {
		clone := filepath.Join(t.TempDir(), fmt.Sprintf("clone%d", i))
		git(t, base, "clone", "-q", "--no-hardlinks", base, clone)
		linkCache(t, repoRoot(t), clone)
		if out, err := runCmd(t, clone, 8*time.Minute, makeEnv(), "make", "build"); err != nil {
			t.Fatalf("clone %d: make build: %v\n%s", i, err, out)
		}
		dir := filepath.Join(clone, "out", "bin", "linux_"+runtime.GOARCH)
		ents, err := os.ReadDir(dir)
		if err != nil || len(ents) == 0 {
			t.Fatalf("clone %d: no binaries in %s: %v", i, dir, err)
		}
		sums[i] = map[string]string{}
		for _, e := range ents {
			out, err := exec.Command("sha256sum", filepath.Join(dir, e.Name())).Output()
			if err != nil {
				t.Fatal(err)
			}
			sums[i][e.Name()] = strings.Fields(string(out))[0]
		}
	}
	if !reflect.DeepEqual(sums[0], sums[1]) {
		t.Errorf("build is not reproducible:\n clone0 %v\n clone1 %v", sums[0], sums[1])
	}
	if _, ok := sums[0]["sc-lint"]; !ok {
		t.Errorf("out/bin/linux_%s lacks sc-lint: %v", runtime.GOARCH, sums[0])
	}
}

func tail(s string, n int) string {
	l := strings.Split(s, "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

// 01-AC-3 (fmt-check is a tier-V target): passes on the tree, fails on a Go file with a gofmt diff.
func TestFmtCheck_FailsOnDiff_01AC3(t *testing.T) {
	dir := workCopy(t)
	if out, err := runCmd(t, dir, 3*time.Minute, makeEnv(), "make", "fmt-check"); err != nil {
		t.Fatalf("make fmt-check on the tree: %v\n%s", err, out)
	}
	write(t, dir, "src/internal/lint/zz_unformatted.go", "package lint\n\nfunc   zzUnformatted( ) {  }\n")
	out, err := runCmd(t, dir, 3*time.Minute, makeEnv(), "make", "fmt-check")
	if err == nil {
		t.Fatalf("make fmt-check accepted an unformatted file:\n%s", out)
	}
	if !strings.Contains(out, "zz_unformatted.go") {
		t.Errorf("fmt-check output must name the file:\n%s", out)
	}
}

// 52-AC-6 / plan 01 section 7.1: the lint recipe covers vet, golangci-lint (pinned config), the netcheck
// analyzer, module hygiene and the shell/workflow linters.
func TestLint_RecipeCoverage_52AC6(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "src", ".golangci.yaml")); err != nil {
		t.Errorf("src/.golangci.yaml (pinned golangci-lint config) missing: %v", err)
	}
	out, err := runCmd(t, root, 2*time.Minute, makeEnv(), "make", "-n", "lint")
	if err != nil {
		t.Fatalf("make -n lint: %v\n%s", err, out)
	}
	for _, want := range []string{"go vet", "golangci-lint", "sc-lint netcheck", "go mod verify", "go mod tidy -diff", "shellcheck", "actionlint"} {
		if !strings.Contains(out, want) {
			t.Errorf("`make -n lint` lacks %q:\n%s", want, out)
		}
	}
}

// 52-AC-6: golangci-lint with the repository config (src/.golangci.yaml) fails on the D7/D8 fixtures with
// the analyzer name forbidigo in the output, and does not flag the allowed places (internal/clock, main
// packages, internal/testutil).
func TestGolangciLint_ForbidigoFixtures_52AC6(t *testing.T) {
	root := repoRoot(t)
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		t.Fatalf("golangci-lint is not in the ci image (toolbox/tools.lock.yaml, baseline v2.14.0): %v", err)
	}
	cfg, err := os.ReadFile(filepath.Join(root, "src", ".golangci.yaml"))
	if err != nil {
		t.Fatalf("src/.golangci.yaml: %v", err)
	}
	mod := t.TempDir()
	write(t, mod, "go.mod", "module fixture\n\ngo 1.27\n")
	write(t, mod, ".golangci.yaml", string(cfg))
	for _, f := range []string{"time_now.go", "rand_global.go"} {
		write(t, mod, "internal/fixture/"+f, readFile(t, ".", "testdata/forbidigo/"+f))
	}
	write(t, mod, "cmd/x/main.go", "package main\n\nimport \"time\"\n\nfunc main() { _ = time.Now() }\n")
	write(t, mod, "internal/clock/clock.go", "package clock\n\nimport \"time\"\n\n// Now is the real clock.\nfunc Now() time.Time { return time.Now() }\n")
	write(t, mod, "internal/testutil/rand.go", "package testutil\n\nimport \"math/rand\"\n\n// Seed seeds the global source.\nfunc Seed(n int64) { rand.Seed(n) }\n")
	env := append(makeEnv(), "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local", "GOCACHE="+t.TempDir(), "GOMODCACHE="+t.TempDir())
	out, err := runCmd(t, mod, 5*time.Minute, env, "golangci-lint", "run", "--config", ".golangci.yaml", "./...")
	if err == nil {
		t.Fatalf("golangci-lint passed on the D7/D8 fixtures:\n%s", out)
	}
	for _, f := range []string{"time_now.go", "rand_global.go"} {
		found := false
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, f) && strings.Contains(l, "forbidigo") {
				found = true
			}
		}
		if !found {
			t.Errorf("no forbidigo finding for %s:\n%s", f, out)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "forbidigo") && (strings.Contains(l, "cmd/x/main.go") || strings.Contains(l, "internal/clock/") || strings.Contains(l, "internal/testutil/")) {
			t.Errorf("forbidigo flagged an allowed place: %s", l)
		}
	}
}

// 52-AC-6 (D5): the netcheck analyzer of sc-lint flags a non-loopback dial in a _test.go file and
// http.DefaultClient, names itself in the output, and leaves loopback dials and test/e2e/** alone.
func TestNetcheck_DialFixture_52AC6(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "src/internal/x/dial_external_test.go", readFile(t, ".", "testdata/forbidigo/dial_external_test.go"))
	write(t, dir, "src/internal/x/default_client_test.go", "package x\n\nimport (\n\t\"net/http\"\n\t\"testing\"\n)\n\nfunc TestDefault(t *testing.T) { _, _ = http.DefaultClient.Get(\"http://127.0.0.1:1\") }\n")
	var out, errb bytes.Buffer
	if code := lint.Main([]string{"netcheck", "--root", dir}, &out, &errb); code != 1 {
		t.Fatalf("sc-lint netcheck on the fixtures: exit %d (want 1)\n%s%s", code, out.String(), errb.String())
	}
	for _, want := range []string{"netcheck", "dial_external_test.go", "default_client_test.go"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("netcheck output lacks %q:\n%s", want, out.String())
		}
	}

	ok := t.TempDir()
	write(t, ok, "src/internal/x/loopback_test.go", "package x\n\nimport (\n\t\"net\"\n\t\"testing\"\n)\n\nfunc TestLoop(t *testing.T) { _, _ = net.Dial(\"tcp\", \"127.0.0.1:1\") }\n")
	write(t, ok, "test/e2e/x/dial_test.go", "package x\n\nimport (\n\t\"net\"\n\t\"testing\"\n)\n\nfunc TestE2E(t *testing.T) { _, _ = net.Dial(\"tcp\", \"example.com:80\") }\n")
	out.Reset()
	errb.Reset()
	if code := lint.Main([]string{"netcheck", "--root", ok}, &out, &errb); code != 0 {
		t.Errorf("sc-lint netcheck on loopback and test/e2e dials: exit %d (want 0)\n%s%s", code, out.String(), errb.String())
	}
}

// 52-AC-6: the real tree passes netcheck; testdata fixtures are excluded from the real-tree run.
func TestNetcheck_RealTreePasses_52AC6(t *testing.T) {
	var out, errb bytes.Buffer
	root := repoRoot(t)
	if code := lint.Main([]string{"netcheck", "--root", root}, &out, &errb); code != 0 {
		t.Fatalf("sc-lint netcheck on the tree: exit %d\n%s%s", code, out.String(), errb.String())
	}
}

// 52-AC-6: `make lint` passes on the tree (fixtures under testdata are not linted).
func TestLint_RealTreePasses_52AC6(t *testing.T) {
	root := repoRoot(t)
	out, err := runCmd(t, root, 10*time.Minute, makeEnv(), "make", "lint")
	if err != nil {
		t.Fatalf("make lint on the tree: %v\n%s", err, tail(out, 60))
	}
}

// 52-AC-6 (D7/D8 scoping): D8 (time) applies in internal/testutil, D7 (global rand) applies in cmd main
// packages and internal/clock; time in main/clock and rand in testutil stay unflagged.
func TestGolangciLint_ForbidigoScoping_52AC6(t *testing.T) {
	root := repoRoot(t)
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		t.Fatalf("golangci-lint is not in the ci image: %v", err)
	}
	cfg := readFile(t, root, "src/.golangci.yaml")
	mod := t.TempDir()
	write(t, mod, "go.mod", "module fixture\n\ngo 1.27\n")
	write(t, mod, ".golangci.yaml", cfg)
	write(t, mod, "internal/testutil/clock.go", "package testutil\n\nimport \"time\"\n\n// Stamp reads the wall clock.\nfunc Stamp() time.Time { return time.Now() }\n")
	write(t, mod, "cmd/x/main.go", "package main\n\nimport (\n\t\"math/rand\"\n\t\"time\"\n)\n\nfunc main() { _ = rand.Intn(3); _ = time.Now() }\n")
	write(t, mod, "internal/clock/clock.go", "package clock\n\nimport (\n\t\"math/rand\"\n\t\"time\"\n)\n\n// Now is the real clock.\nfunc Now() time.Time { _ = rand.Intn(3); return time.Now() }\n")
	write(t, mod, "internal/testutil/rand.go", "package testutil\n\nimport \"math/rand\"\n\n// Pick uses the global source.\nfunc Pick() int { return rand.Intn(3) }\n")
	env := append(makeEnv(), "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local", "GOCACHE="+t.TempDir(), "GOMODCACHE="+t.TempDir())
	out, _ := runCmd(t, mod, 5*time.Minute, env, "golangci-lint", "run", "--config", ".golangci.yaml", "./...")
	flagged := func(file, msg string) bool {
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, file) && strings.Contains(l, "forbidigo") && strings.Contains(l, msg) {
				return true
			}
		}
		return false
	}
	for _, c := range []struct {
		file, msg string
		want      bool
	}{
		{"internal/testutil/clock.go", "time.Now", true},
		{"cmd/x/main.go", "rand.Intn", true},
		{"internal/clock/clock.go", "rand.Intn", true},
		{"cmd/x/main.go", "time.Now", false},
		{"internal/clock/clock.go", "time.Now", false},
		{"internal/testutil/rand.go", "rand.Intn", false},
	} {
		if got := flagged(c.file, c.msg); got != c.want {
			t.Errorf("forbidigo flag for %s %s = %v, want %v\n%s", c.file, c.msg, got, c.want, out)
		}
	}
}

// 52-AC-6 (D5): an aliased net/http import is still http.DefaultClient; a ":port" dial is loopback.
func TestNetcheck_AliasAndBarePort_52AC6(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "src/internal/x/alias_test.go", "package x\n\nimport (\n\th \"net/http\"\n\t\"testing\"\n)\n\nfunc TestAlias(t *testing.T) { _, _ = h.DefaultClient.Get(\"http://127.0.0.1:1\") }\n")
	var out, errb bytes.Buffer
	if code := lint.Main([]string{"netcheck", "--root", dir}, &out, &errb); code != 1 || !strings.Contains(out.String(), "alias_test.go") {
		t.Errorf("netcheck on an aliased net/http import: exit %d (want 1, naming alias_test.go)\n%s%s", code, out.String(), errb.String())
	}
	ok := t.TempDir()
	write(t, ok, "src/internal/x/port_test.go", "package x\n\nimport (\n\t\"net\"\n\t\"testing\"\n)\n\nfunc TestPort(t *testing.T) { _, _ = net.Dial(\"tcp\", \":8080\") }\n")
	out.Reset()
	errb.Reset()
	if code := lint.Main([]string{"netcheck", "--root", ok}, &out, &errb); code != 0 {
		t.Errorf("netcheck on a loopback \":8080\" dial: exit %d (want 0)\n%s%s", code, out.String(), errb.String())
	}
}

// 01-AC-3, 01-AC-6, 52-AC-5, 52-AC-8: the offline, no-paid, twice-repeatable `make verify` evidence lives in
// test/e2e/verify/run (plan 01 section 6.1 allows it to be executable). This guards that it stays there.
func TestVerifyE2EScript_Present_01AC3_01AC6_52AC5_52AC8(t *testing.T) {
	root := repoRoot(t)
	out, err := runCmd(t, root, time.Minute, makeEnv(), "git", "ls-files", "-s", "test/e2e/verify/run")
	if err != nil || !strings.HasPrefix(out, "100755 ") {
		t.Fatalf("test/e2e/verify/run must be tracked with mode 100755: %q %v", out, err)
	}
	body := readFile(t, root, "test/e2e/verify/run")
	if !strings.HasPrefix(body, "#!/bin/sh\n") {
		t.Errorf("test/e2e/verify/run is not POSIX sh")
	}
	for _, want := range []string{
		"./bootstrap exec --image ci -- make gitops-cache",
		"go mod download",
		"./bootstrap exec --image ci --network none -- env SC_FORBID_PAID=1 make verify",
		"out/verify/summary.json", "duration_ms", "started_at", "python3",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("test/e2e/verify/run lacks %q", want)
		}
	}
	for _, target := range verifyTargetOrder {
		if !strings.Contains(body, target) {
			t.Errorf("test/e2e/verify/run does not list tier-V target %s", target)
		}
	}
}
