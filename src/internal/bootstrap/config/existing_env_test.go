package config_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/config"
)

// 05-AC-1: the EXISTING_NODES / EXISTING_NODE_OVERRIDES parser against test/fixtures/existing (plan 05 section 9, plan 50
// section 9.2): golden cases produce the expected spec.provider.existing; malformed and duplicate entries fail with
// field-level errors (exit 2 at preflight).

func existingFixtures(t testing.TB, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "test", "fixtures", "existing", dir, "*.env"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures in test/fixtures/existing/%s (%v)", dir, err)
	}
	return files
}

// existingEnv merges a fixture over the minimal valid install; the fixture owns the three existing-node variables.
func existingEnv(t testing.TB, file string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	over, err := config.ParseEnv(b)
	if err != nil {
		t.Fatal(err)
	}
	return merge(baseExisting(), over, []string{"EXISTING_NODES", "EXISTING_NODE_OVERRIDES", "EXPECTED_NODE_COUNT"})
}

func wantSubstrings(t testing.TB, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if w, ok := strings.CutPrefix(l, "# want:"); ok {
			out = append(out, strings.TrimSpace(w))
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s has no '# want:' line", file)
	}
	return out
}

// 05-AC-1: every case of test/fixtures/existing/env produces the expected spec.provider.existing JSON.
func TestExistingEnv_GoldenCases_05AC1(t *testing.T) {
	for _, f := range existingFixtures(t, "env") {
		name := strings.TrimSuffix(filepath.Base(f), ".env")
		t.Run(name, func(t *testing.T) {
			res := mustNormalize(t, existingEnv(t, f), "")
			var want any
			b, err := os.ReadFile(strings.TrimSuffix(f, ".env") + ".json")
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, &want); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(get2(res.Doc, "spec", "provider", "existing"))
			if err != nil {
				t.Fatal(err)
			}
			var got any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("spec.provider.existing = %s\nwant %s", raw, b)
			}
		})
	}
}

func get2(doc map[string]any, path ...string) any {
	var cur any = doc
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func requireFieldErrors(t *testing.T, file string) {
	t.Helper()
	res, err := config.Normalize(existingEnv(t, file), opts(""))
	var ce *config.Error
	if err == nil || !errors.As(err, &ce) {
		t.Fatalf("want a *config.Error, got res=%v err=%v", res != nil, err)
	}
	if ce.ExitCode != 2 {
		t.Errorf("exit code %d, want 2 (plan 50 section 7.2)", ce.ExitCode)
	}
	for _, w := range wantSubstrings(t, file) {
		if !strings.Contains(ce.Error(), w) {
			t.Errorf("error %q does not name %q", ce.Error(), w)
		}
	}
}

// 05-AC-1: malformed entries (node-1, port=0, host_key=MD5, ...) fail with field-level errors.
func TestExistingEnv_MalformedEntries_05AC1(t *testing.T) {
	for _, f := range existingFixtures(t, "env-invalid") {
		t.Run(strings.TrimSuffix(filepath.Base(f), ".env"), func(t *testing.T) { requireFieldErrors(t, f) })
	}
}

// 05-AC-1: a duplicate node name (node-01 twice) fails with a field-level error naming the field and the node.
func TestExistingEnv_DuplicateNodeName_05AC1(t *testing.T) {
	for _, f := range existingFixtures(t, "env-duplicates") {
		t.Run(strings.TrimSuffix(filepath.Base(f), ".env"), func(t *testing.T) { requireFieldErrors(t, f) })
	}
}
