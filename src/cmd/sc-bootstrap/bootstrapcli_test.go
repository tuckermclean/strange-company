package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "sc-bootstrap")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "plans", "50-bootstrap-orchestrator.md")); err == nil {
			return filepath.Join(dir, "test", "fixtures", "bootstrap")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root not found")
		}
		dir = parent
	}
}

func runBin(t *testing.T, bin string, env []string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &ee):
		return ee.ExitCode(), string(out)
	}
	t.Fatalf("running %s: %v", bin, err)
	return -1, ""
}

// 50-AC-2: the binary exits 2 on an unknown key and PF-02 names the key and a suggestion.
func TestBinary_UnknownKeyExits2_50AC2(t *testing.T) {
	bin := buildBin(t)
	code, out := runBin(t, bin, nil, "--env-file", filepath.Join(fixtureDir(t), "invalid-unknown-key.env"), "preflight")
	if code != 2 {
		t.Fatalf("exit %d, want 2:\n%s", code, out)
	}
	for _, want := range []string{"PF-02", "MAX_MONTLY_COST", "MAX_MONTHLY_COST"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// 50-AC-7 (CLI leg): SC_TEST_CRASH_AT in the process environment with PROVIDER=ovh exits 2 TEST_MODE_REFUSED_PAID.
func TestBinary_TestModeRefusedForPaidProvider_50AC7(t *testing.T) {
	bin := buildBin(t)
	ovh := filepath.Join(fixtureDir(t), "valid", "ovh-tunnel.env")
	code, out := runBin(t, bin, []string{"SC_TEST_MODE=1", "SC_TEST_CRASH_AT=plan.storage-apply:after"}, "--env-file", ovh, "preflight")
	if code != 2 || !strings.Contains(out, "TEST_MODE_REFUSED_PAID") {
		t.Fatalf("exit %d, want 2 with TEST_MODE_REFUSED_PAID:\n%s", code, out)
	}
}

// Plan 50 section 7.1 and CD-70(e): the binary knows the whole tree; a command that is not built exits 2 with
// COMMAND_NOT_IMPLEMENTED, an unknown one does not.
func TestBinary_NotImplementedVersusUnknown_CD70e(t *testing.T) {
	bin := buildBin(t)
	code, out := runBin(t, bin, nil, "edge-bypass")
	if code != 2 || !strings.Contains(out, "COMMAND_NOT_IMPLEMENTED") {
		t.Errorf("edge-bypass: exit %d, want 2 with COMMAND_NOT_IMPLEMENTED:\n%s", code, out)
	}
	code, out = runBin(t, bin, nil, "frobnicate")
	if code != 2 || strings.Contains(out, "COMMAND_NOT_IMPLEMENTED") {
		t.Errorf("frobnicate: exit %d, want 2 without COMMAND_NOT_IMPLEMENTED:\n%s", code, out)
	}
}
