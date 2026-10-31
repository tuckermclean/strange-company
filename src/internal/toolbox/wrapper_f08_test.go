package toolbox_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// F08 S2 wrapper behaviour (bare ./bootstrap is run, top-level global flags, runtime-only variable passthrough,
// passphrase file mount): the POSIX-sh stub-engine script is the specification; this test runs it under `make test`.
// It is also part of `make toolbox-check` through test/e2e/wrapper/run.sh.
func TestWrapperStubScript_F08(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "plans", "50-bootstrap-orchestrator.md")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root not found")
		}
		dir = parent
	}
	script := filepath.Join(dir, "test", "e2e", "wrapper", "stub-f08-wrapper.sh")
	cmd := exec.Command("sh", script)
	cmd.Env = append(os.Environ(), "SC_ALLOW_ROOT=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		var failed []string
		for _, l := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(l, "  FAIL ") {
				failed = append(failed, l)
			}
		}
		t.Fatalf("stub-f08-wrapper.sh failed (%v): %d failing checks, first ones:\n%s", err, len(failed), strings.Join(firstN(failed, 12), "\n"))
	}
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
