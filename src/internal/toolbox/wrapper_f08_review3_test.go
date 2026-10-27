package toolbox_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// F08 S3 carried review item: `./bootstrap exec -- -x` runs -x in the container and is not dispatched to
// sc-bootstrap. The POSIX-sh stub-engine script is the specification; it also runs in test/e2e/wrapper/run.sh.
func TestWrapperStubScript_F08Review3(t *testing.T) {
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
	cmd := exec.Command("sh", filepath.Join(dir, "test", "e2e", "wrapper", "stub-f08-review3.sh"))
	cmd.Env = append(os.Environ(), "SC_ALLOW_ROOT=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		var failed []string
		for _, l := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(l, "  FAIL ") {
				failed = append(failed, l)
			}
		}
		t.Fatalf("stub-f08-review3.sh failed (%v): %d failing checks, first ones:\n%s", err, len(failed), strings.Join(firstN(failed, 12), "\n"))
	}
}
