package worker

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

// 27-AC-14: the schema copies embedded in sc-worker are byte-identical (sha256) to the repository contracts.
func TestEmbeddedSchemas_MatchContracts_27AC14(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"worker-input", "worker-result", "context", "handoff"} {
		t.Run(name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join(root, "contracts", name+".schema.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			got := EmbeddedSchema(name)
			if len(got) == 0 {
				t.Fatalf("EmbeddedSchema(%q) is empty", name)
			}
			if sha256.Sum256(got) != sha256.Sum256(want) {
				t.Errorf("embedded %s schema differs from contracts/%s.schema.yaml", name, name)
			}
		})
	}
}
