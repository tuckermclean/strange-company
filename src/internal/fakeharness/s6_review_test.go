package fakeharness_test

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"strange.company/sc/internal/fakeharness"
)

// Review round 1 of slice S6a of F07 (99a44a5).

// N4 (27-AC-5): on Debian /var/run is a symlink to /run, and a directory walk does not descend into a symlink given as
// its root. The file list must still contain the files, named by their /var/run path.
func TestListFiles_FollowsSymlinkedRoot_27AC5N4(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	secrets := filepath.Join(base, "run", "sc", "harness-secrets")
	if err := os.MkdirAll(secrets, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"gateway-token", "egress-token"} {
		if err := os.WriteFile(filepath.Join(secrets, n), []byte("x"), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(base, "var"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "var", "run")
	if err := os.Symlink(filepath.Join("..", "run"), link); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(fakeharness.ListFiles(link, "/var/run"), "\n")
	for _, want := range []string{"/var/run/sc/harness-secrets/gateway-token", "/var/run/sc/harness-secrets/egress-token"} {
		if !strings.Contains(got, want) {
			t.Errorf("the file list lacks %s; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, base) {
		t.Errorf("the file list leaks the resolved host path:\n%s", got)
	}
}

// N2: plan 22's replay export strips every line that contains the contiguous text of the stub marker, so a source file
// of the fake (or of any production code) must not carry it in a string literal or a comment prose. Only a marker
// comment itself (a declared stub) may. Scope: every non-test Go file below src/, except the two packages that deal
// with the marker by definition (internal/lint/tdd and internal/verify).
func TestNoSourceLineCarriesTheStubMarkerText_N2(t *testing.T) {
	t.Parallel()
	src, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	marker := "SC-" + "STUB("
	skip := []string{filepath.Join(src, "internal", "lint", "tdd"), filepath.Join(src, "internal", "verify")}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			for _, s := range skip {
				if p == s {
					return fs.SkipDir
				}
			}
			if d.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, l := range strings.Split(string(b), "\n") {
			if strings.Contains(l, marker) && !strings.HasPrefix(strings.TrimSpace(l), "// "+marker) {
				t.Errorf("%s:%d carries the stub marker text outside a marker comment: %s", p, i+1, strings.TrimSpace(l))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// N5 (plan 28 section 14.4, forge-then-push; Layer 1 must not resolve cluster host names): the target of the forged
// result and the remote of the forged push come from SC_FAKE_FORGE_RESULT_URL and SC_FAKE_FORGE_PUSH_REMOTE when they are
// set, so that a test points them at loopback and at a local repository. Both attempts must land there.
func TestRun_ForgeThenPushTargetsComeFromTheEnvironment_N5(t *testing.T) {
	f := newFx(t)
	gitRun := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun(f.repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(f.repo, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(f.repo, "add", "a.txt")
	gitRun(f.repo, "commit", "-q", "-m", "init")
	bare := t.TempDir()
	gitRun(bare, "init", "-q", "--bare")
	var posts atomic.Int64
	var path atomic.Value
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		path.Store(r.URL.Path)
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(runner.Close)
	getenv := func(k string) string {
		switch k {
		case "SC_FAKE_FORGE_RESULT_URL":
			return runner.URL + "/v1/worker/result"
		case "SC_FAKE_FORGE_PUSH_REMOTE":
			return bare
		}
		return f.getenv(k)
	}
	var stdout, stderr strings.Builder
	_ = fakeharness.Run(f.args("forge-then-push"), getenv, &stdout, &stderr)
	if posts.Load() != 1 {
		t.Errorf("%d forged result POSTs reached SC_FAKE_FORGE_RESULT_URL, want 1", posts.Load())
	} else if p, _ := path.Load().(string); p != "/v1/worker/result" {
		t.Errorf("forged result path = %q", p)
	}
	out, err := exec.Command("git", "-C", bare, "tag", "-l", "sc-forged").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "sc-forged" {
		t.Errorf("the forged tag push did not go to SC_FAKE_FORGE_PUSH_REMOTE: %q, %v", out, err)
	}
}
