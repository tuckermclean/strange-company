package worker_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/worker"
)

// Slice S6a of F07, 28-AC-3 (Layer 1): `sc-worker selftest` is the image self-check of plan 28 section 13.1. Inside the
// harness-fake image, run as uid 65532 with a read-only root, it proves what the table of 13.1 assigns to it: the image
// runs as USER 65532:65532, files are writable only under /workspace, /home/worker and /tmp, and no package manager is
// usable. It is side-effect free (plan 28 section 17) and exits 0 (ok) or 1 (a check failed); every failed check is named
// on stderr. The checks run against an injected environment so that Layer 1 needs neither root nor a container; S6b runs
// the real thing in the image.

func healthySelfTestEnv(t *testing.T) (worker.SelfTestEnv, string) {
	t.Helper()
	dir := t.TempDir()
	return worker.SelfTestEnv{
		Uid: 65532, Gid: 65532,
		WritableDirs:    []string{dir},
		PackageManagers: []string{filepath.Join(dir, "no-such-dir", "apt-get")},
	}, dir
}

func runSelfTest(env worker.SelfTestEnv) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = worker.SelfTest(env, &out, &errb)
	return code, out.String(), errb.String()
}

func TestSelfTest_HealthyImageEnvironmentPasses_28AC3(t *testing.T) {
	env, dir := healthySelfTestEnv(t)
	code, stdout, stderr := runSelfTest(env)
	if code != 0 || !strings.Contains(stdout, "selftest ok") || stderr != "" {
		t.Errorf("exit %d, stdout %q, stderr %q, want exit 0 with %q on stdout and nothing on stderr", code, stdout, stderr, "selftest ok")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("selftest left %d entries in a writable directory, want none (side-effect free)", len(ents))
	}
}

func TestSelfTest_FailingChecksExit1AndAreNamed_28AC3(t *testing.T) {
	cases := map[string]struct {
		mutate func(env *worker.SelfTestEnv, dir string)
		want   string
	}{
		"root uid":       {func(e *worker.SelfTestEnv, _ string) { e.Uid = 0 }, "uid"},
		"other uid":      {func(e *worker.SelfTestEnv, _ string) { e.Uid = 1000 }, "uid"},
		"root gid":       {func(e *worker.SelfTestEnv, _ string) { e.Gid = 0 }, "gid"},
		"unwritable dir": {func(e *worker.SelfTestEnv, d string) { e.WritableDirs = []string{filepath.Join(d, "missing")} }, "missing"},
		"writable root": {func(e *worker.SelfTestEnv, d string) {
			e.ReadOnlyDirs = []string{d} // a directory that can be written is not read-only, whoever runs the test
		}, "ReadOnly"},
		"package manager present": {func(e *worker.SelfTestEnv, d string) {
			p := filepath.Join(d, "apt-get")
			if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			e.PackageManagers = []string{p}
		}, "apt-get"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			env, dir := healthySelfTestEnv(t)
			c.mutate(&env, dir)
			code, stdout, stderr := runSelfTest(env)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if strings.Contains(stdout, "selftest ok") {
				t.Errorf("stdout claims ok: %q", stdout)
			}
			want := c.want
			if want == "ReadOnly" {
				want = dir
			}
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr %q does not name %q", stderr, want)
			}
		})
	}
}

func TestSelfTest_NonExecutablePackageManagerIsUnusable_28AC3(t *testing.T) {
	env, dir := healthySelfTestEnv(t)
	p := filepath.Join(dir, "dpkg")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env.PackageManagers = []string{p}
	if code, stdout, stderr := runSelfTest(env); code != 0 {
		t.Errorf("exit %d (stdout %q, stderr %q), want 0: a package manager without the execute bit is unusable", code, stdout, stderr)
	}
}

func TestSelfTest_ReportsEveryFailedCheck_28AC3(t *testing.T) {
	env, dir := healthySelfTestEnv(t)
	env.Uid = 0
	env.WritableDirs = []string{filepath.Join(dir, "missing")}
	code, _, stderr := runSelfTest(env)
	if code != 1 || !strings.Contains(stderr, "uid") || !strings.Contains(stderr, filepath.Join(dir, "missing")) {
		t.Errorf("exit %d, stderr %q, want exit 1 naming both the uid and the unwritable directory", code, stderr)
	}
}

// The production environment: the identity of the process, the writable roots of plan 28 section 13.1 (/workspace,
// /home/worker, /tmp), /usr as a directory that must not be writable and the package managers of a Debian base.
func TestSelfTest_DefaultEnvironment_28AC3(t *testing.T) {
	env := worker.DefaultSelfTestEnv()
	if env.Uid != os.Getuid() || env.Gid != os.Getgid() {
		t.Errorf("uid/gid = %d/%d, want the process's %d/%d", env.Uid, env.Gid, os.Getuid(), os.Getgid())
	}
	contains := func(list []string, want string) bool {
		for _, v := range list {
			if v == want {
				return true
			}
		}
		return false
	}
	for _, d := range []string{"/workspace", "/home/worker", "/tmp"} {
		if !contains(env.WritableDirs, d) {
			t.Errorf("WritableDirs %q lacks %s", env.WritableDirs, d)
		}
	}
	if !contains(env.ReadOnlyDirs, "/usr") {
		t.Errorf("ReadOnlyDirs %q lacks /usr", env.ReadOnlyDirs)
	}
	for _, p := range []string{"/usr/bin/apt-get", "/usr/bin/dpkg"} {
		if !contains(env.PackageManagers, p) {
			t.Errorf("PackageManagers %q lacks %s", env.PackageManagers, p)
		}
	}
}
