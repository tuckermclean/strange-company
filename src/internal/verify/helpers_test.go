package verify

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"strange.company/sc/internal/contracts"
	"strange.company/sc/internal/policy"
)

// repoRoot is the repository root (this package lives in src/internal/verify).
func repoRoot(t testing.TB) string {
	t.Helper()
	r, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// readYAMLMap parses a YAML file of the repository into JSON-compatible values.
func readYAMLMap(t testing.TB, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := contracts.ParseYAML(b)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s: not a mapping", path)
	}
	return m
}

// platformPolicy is the real platform verification policy (policy/verification.yaml).
func platformPolicy(t testing.TB) policy.Platform {
	t.Helper()
	return policy.Platform{Verification: readYAMLMap(t, filepath.Join(repoRoot(t), "policy", "verification.yaml"))}
}

func mustRead(t testing.TB, path ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(path...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readJSONFile(t testing.TB, v any, path ...string) {
	t.Helper()
	b := mustRead(t, path...)
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", filepath.Join(path...), err)
	}
}

// encodeIDSet builds a $defs/TestIdSet independently of the library: sorted ids, newline separated, gzip, base64.
func encodeIDSet(t testing.TB, ids []string) *TestIDSet {
	t.Helper()
	s := append([]string(nil), ids...)
	sort.Strings(s)
	text := strings.Join(s, "\n")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(text))
	return &TestIDSet{Count: len(s), Sha256: hex.EncodeToString(sum[:]), Data: base64.StdEncoding.EncodeToString(buf.Bytes())}
}

// validateRedResult validates a classified red-result document built from the inputs and the classification
// against contracts/red-result.schema.yaml.
func validateRedResult(t testing.TB, obs Observation, sc Scope, cl RedClassification) error {
	t.Helper()
	schemas, err := contracts.CompileSchemas(filepath.Join(repoRoot(t), "contracts"))
	if err != nil {
		t.Fatal(err)
	}
	s := schemas["red-result"]
	if s == nil {
		t.Fatal("no red-result schema")
	}
	doc := map[string]any{
		"schema_version": "v1alpha1", "document": "red-result", "stage": "classified",
		"observed": obs, "scope": sc, "classification": cl,
		"pipeline": map[string]any{"number": 1, "intent_id": obs.IntentID, "created_by": "sc-runner",
			"config_revision": obs.BaseSha, "config_sha256": strings.Repeat("7", 64), "evidence_sha256": strings.Repeat("8", 64)},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return s.Validate(v)
}

func readIfExists(path string) ([]byte, error) { return os.ReadFile(path) }

func trimNL(b []byte) []byte { return []byte(strings.TrimSpace(string(b))) }
