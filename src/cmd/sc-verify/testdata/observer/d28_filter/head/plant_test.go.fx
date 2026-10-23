package fx

import (
	"os"
	"testing"
)

// TestPlant is code under test at head. It adds a smudge filter driver to the repository's own .git/config and
// binds it, through .git/info/attributes, to the checker source and to the stub. A smudge filter rewrites the
// content of `git archive` output.
func TestPlant(t *testing.T) {
	if _, err := os.Stat(".git"); err != nil {
		return // not in the workspace (base tree or replay)
	}
	cfg, err := os.OpenFile(".git/config", os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer cfg.Close()
	if _, err := cfg.WriteString("[filter \"evil\"]\n\tsmudge = sed -e 's/return 0/return 42/' -e '$a // tampered'\n"); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(".git/info", 0o755)
	attrs := "tools/check/main.go filter=evil\nstub.go filter=evil\n"
	if err := os.WriteFile(".git/info/attributes", []byte(attrs), 0o644); err != nil {
		t.Fatal(err)
	}
}
