package lint_test

import (
	"bytes"
	"context"
	"encoding/json"
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
// `make verify` runs `make test`, which runs these tests. The long tests set SC_NESTED_VERIFY=1 for the
// make they start and skip themselves when it is set, so there is no recursion.

// verifyTargetOrder is the tier-V order of plan 01 section 7.1 (catalogue row order; `gitops-cache` is
// the first step), restricted to the targets that exist at F01.
var verifyTargetOrder = []string{
	"gitops-cache", "fmt-check", "lint", "layout-check", "locks-check", "secrets-scan", "contracts",
	"test", "build", "toolbox-check", "dag-check", "quarantine-check",
}

func skipIfNested(t *testing.T) {
	t.Helper()
	if os.Getenv("SC_NESTED_VERIFY") == "1" {
		t.Skip("nested run started by a long verify test")
	}
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

func linkCache(t *testing.T, root, dst string) {
	t.Helper()
	cache := filepath.Join(root, ".cache")
	if _, err := os.Stat(filepath.Join(cache, "go", "mod")); err != nil {
		t.Fatalf("warm module cache %s/go/mod missing (run `go mod download` first, 01-AC-6): %v", cache, err)
	}
	if err := os.Symlink(cache, filepath.Join(dst, ".cache")); err != nil {
		t.Fatal(err)
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
	skipIfNested(t)
	base := workCopy(t)
	sums := make([]map[string]string, 2)
	for i := range sums {
		clone := filepath.Join(t.TempDir(), fmt.Sprintf("clone%d", i))
		git(t, base, "clone", "-q", "--no-hardlinks", base, clone)
		linkCache(t, repoRoot(t), clone)
		if out, err := runCmd(t, clone, 8*time.Minute, makeEnv("SC_NESTED_VERIFY=1"), "make", "build"); err != nil {
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

type summaryRow struct{ target, result string }

// summaryRows walks summary.json for objects that carry a "result" and a "target" (or "name") string.
func summaryRows(v any, rows *[]summaryRow) {
	switch x := v.(type) {
	case map[string]any:
		res, _ := x["result"].(string)
		name, _ := x["target"].(string)
		if name == "" {
			name, _ = x["name"].(string)
		}
		if res != "" && name != "" {
			*rows = append(*rows, summaryRow{name, res})
		}
		for _, c := range x {
			summaryRows(c, rows)
		}
	case []any:
		for _, c := range x {
			summaryRows(c, rows)
		}
	}
}

// stripKeys is the Go form of jq 'del(..|.duration_ms?, .started_at?)'.
func stripKeys(v any) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "duration_ms")
		delete(x, "started_at")
		for k, c := range x {
			x[k] = stripKeys(c)
		}
	case []any:
		for i, c := range x {
			x[i] = stripKeys(c)
		}
	}
	return v
}

func hasKey(v any, key string) bool {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x[key]; ok {
			return true
		}
		for _, c := range x {
			if hasKey(c, key) {
				return true
			}
		}
	case []any:
		for _, c := range x {
			if hasKey(c, key) {
				return true
			}
		}
	}
	return false
}

// 01-AC-3 (in-harness part), 01-AC-6, 52-AC-5, 52-AC-8: two consecutive `make verify` runs on a clean
// checkout, offline (this process has no network), with SC_FORBID_PAID=1, exit 0, write
// out/verify/summary.json that lists every tier-V target in order with result pass, and are identical
// after deleting duration_ms and started_at.
func TestVerify_OfflineRepeatableNoPaid_01AC3_01AC6_52AC5_52AC8(t *testing.T) {
	skipIfNested(t)
	dir := workCopy(t)
	var docs [2]any
	var raw [2][]byte
	for i := range docs {
		out, err := runCmd(t, dir, 20*time.Minute, makeEnv("SC_NESTED_VERIFY=1", "SC_FORBID_PAID=1", "GOFLAGS=-mod=readonly"), "make", "verify")
		if err != nil {
			t.Fatalf("run %d: make verify: %v\n%s", i, err, tail(out, 60))
		}
		b, err := os.ReadFile(filepath.Join(dir, "out", "verify", "summary.json"))
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		raw[i] = b
		if err := json.Unmarshal(b, &docs[i]); err != nil {
			t.Fatalf("run %d: summary.json is not JSON: %v\n%s", i, err, b)
		}
		var rows []summaryRow
		summaryRows(docs[i], &rows)
		var names []string
		for _, r := range rows {
			names = append(names, r.target)
			if r.result != "pass" {
				t.Errorf("run %d: target %s has result %q, want pass", i, r.target, r.result)
			}
		}
		// every tier-V target of F01 is listed, in the verify order (other rows may sit between).
		pos := 0
		for _, n := range names {
			if pos < len(verifyTargetOrder) && n == verifyTargetOrder[pos] {
				pos++
			}
		}
		if pos != len(verifyTargetOrder) {
			t.Errorf("run %d: summary.json rows %v do not contain %v in order", i, names, verifyTargetOrder)
		}
		for _, k := range []string{"duration_ms", "started_at"} {
			if !hasKey(docs[i], k) {
				t.Errorf("run %d: summary.json has no %q field", i, k)
			}
		}
	}
	a, _ := json.Marshal(stripKeys(docs[0]))
	b, _ := json.Marshal(stripKeys(docs[1]))
	if !bytes.Equal(a, b) {
		t.Errorf("52-AC-5: summary.json differs between runs after deleting duration_ms/started_at:\n%s\n%s", raw[0], raw[1])
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
	skipIfNested(t)
	root := repoRoot(t)
	out, err := runCmd(t, root, 10*time.Minute, makeEnv("SC_NESTED_VERIFY=1"), "make", "lint")
	if err != nil {
		t.Fatalf("make lint on the tree: %v\n%s", err, tail(out, 60))
	}
}
