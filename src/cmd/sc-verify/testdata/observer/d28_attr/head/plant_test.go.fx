package fx

import (
	"os"
	"testing"
)

// TestPlant is code under test at head. It writes one line into the repository's own .git/info/attributes (same
// uid; the workspace is the repository) and nothing else.
func TestPlant(t *testing.T) {
	if _, err := os.Stat(".git"); err != nil {
		return // not in the workspace (base tree or replay)
	}
	_ = os.MkdirAll(".git/info", 0o755)
	if err := os.WriteFile(".git/info/attributes", []byte("red_test.go export-ignore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
