package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 02-AC-12 (Go side): the built binary answers `version` with exit 0 and no SC_REPO_ROOT.
func TestBinary_VersionExitsZeroWithoutRepoRoot_02AC12(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "sc-bootstrap")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "version")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sc-bootstrap version: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "sc-bootstrap") {
		t.Fatalf("version output lacks the tool name: %q", out)
	}
}
