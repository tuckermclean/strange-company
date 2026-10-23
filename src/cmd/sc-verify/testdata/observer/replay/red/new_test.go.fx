package fx

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestStillRed fails against the stub and passes against the implementation.
func TestStillRed(t *testing.T) {
	if got := Impl(); got != 42 {
		t.Errorf("Impl() = %d, want 42", got)
	}
}

// TestMarker fails only while the stub marker is present in the source (blind TDD-B6). The literal is split so
// that this file does not contain the marker itself.
func TestMarker(t *testing.T) {
	src, err := os.ReadFile("stub.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "SC-STUB"+"(") {
		t.Fatalf("stub.go: the stub marker is present")
	}
}

// TestGit fails only while the stub is in place and a .git entry is visible (TDD-3).
func TestGit(t *testing.T) {
	if Impl() == 0 && gitAbove() {
		t.Fatalf("stub.go: a .git entry is visible and the implementation is absent")
	}
}

// TestSubject fails only while the stub is in place and the commit subject says test( (TDD-3).
func TestSubject(t *testing.T) {
	out, err := exec.Command("git", "log", "-1", "--format=%s").Output()
	if err == nil && Impl() == 0 && strings.HasPrefix(string(out), "test(") {
		t.Fatalf("stub.go: the commit subject is %q", strings.TrimSpace(string(out)))
	}
}

// TestReplayEnv passes everywhere; it records the environment it runs in, so that the replay can be inspected.
func TestReplayEnv(t *testing.T) { dumpEnv(t) }
