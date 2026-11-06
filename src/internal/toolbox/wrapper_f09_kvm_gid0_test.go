package toolbox_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// F09 S9b review note (05-AC-6; plan 02 section 7.1): `--kvm` with a device whose group is root (gid 0) is refused
// (WRAPPER_NO_KVM, exit 2), never `--group-add 0`. The POSIX-sh stub-engine script is the specification; it also runs in
// test/e2e/wrapper/run.sh.
func TestWrapperStubScript_F09KvmGid0(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh is not available: %v", err)
	}
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
	cmd := exec.Command(sh, filepath.Join(dir, "test", "e2e", "wrapper", "stub-f09-kvm-gid0.sh"))
	cmd.Env = append(os.Environ(), "SC_ALLOW_ROOT=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		var failed []string
		for _, l := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(l, "  FAIL ") {
				failed = append(failed, l)
			}
		}
		t.Fatalf("stub-f09-kvm-gid0.sh failed (%v): %d failing checks, first ones:\n%s", err, len(failed), strings.Join(firstN(failed, 12), "\n"))
	}
}
