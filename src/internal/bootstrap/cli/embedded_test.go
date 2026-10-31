package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The schema copy embedded for PF-02 is byte-identical to the contract (plan 50 section 14.2).
func TestEmbeddedSchema_MatchesContract(t *testing.T) {
	want, err := os.ReadFile(filepath.Join(repoRoot(t), "contracts", "bootstrap-config.schema.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("schemas/bootstrap-config.schema.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("src/internal/bootstrap/cli/schemas/bootstrap-config.schema.yaml differs from contracts/bootstrap-config.schema.yaml; refresh it with cp")
	}
}
