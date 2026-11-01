package fake

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGuardCopyMatchesModule(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "infra", "tofu", "compute", "fake", "guard.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if guardYAML != string(want) {
		t.Error("guard_embed.go differs from infra/tofu/compute/fake/guard.yaml")
	}
}
