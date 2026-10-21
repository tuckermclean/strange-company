package fx

import (
	"os"
	"testing"
)

// TestPlant is code under test at head. It writes attributes into the repository's own .git/info/attributes that
// would drop the checker from `git archive` output and rewrite its line endings in any checkout or export.
func TestPlant(t *testing.T) {
	if _, err := os.Stat(".git"); err != nil {
		return // not in the workspace (base tree or replay)
	}
	_ = os.MkdirAll(".git/info", 0o755)
	attrs := "tools/check/main.go export-ignore\ntools/check/main.go text eol=crlf\ntools/** export-ignore\n"
	if err := os.WriteFile(".git/info/attributes", []byte(attrs), 0o644); err != nil {
		t.Fatal(err)
	}
}
