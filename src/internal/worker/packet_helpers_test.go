package worker_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"strange.company/sc/internal/contracts"
	"strange.company/sc/internal/worker"
)

func repoRoot(t testing.TB) string {
	t.Helper()
	r, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// validate checks v (JSON-compatible) against contracts/<name>.schema.yaml with the repository's schema compiler.
func validate(t testing.TB, name string, v any) {
	t.Helper()
	set, err := contracts.CompileSchemas(filepath.Join(repoRoot(t), "contracts"))
	if err != nil {
		t.Fatal(err)
	}
	s, ok := set[name]
	if !ok {
		t.Fatalf("no compiled schema %q", name)
	}
	if err := s.Validate(v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// jsonRound normalizes a YAML-decoded value to JSON-decoded form.
func jsonRound(t testing.TB, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// examplePacket is the schema-valid tester packet of worker-input.schema.yaml examples[0] (execution sc-184-x1).
func examplePacket(t testing.TB) map[string]any {
	t.Helper()
	doc, err := contracts.ParseYAML(worker.EmbeddedSchema("worker-input"))
	if err != nil {
		t.Fatal(err)
	}
	ex := doc.(map[string]any)["examples"].([]any)[0]
	p := jsonRound(t, ex).(map[string]any)
	validate(t, "worker-input", p)
	return p
}
