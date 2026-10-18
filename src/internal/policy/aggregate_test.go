package policy

import (
	"os"
	"path/filepath"
	"testing"
)

// 26-AC-1 (plan 26 section 9.2 step 2): schema failures aggregate across files; one bad file does not hide
// another.
func TestCheck_AggregatesIssuesAcrossFiles_26AC1(t *testing.T) {
	dir := copyTree(t, td("valid"), false)
	bad := []string{"ci.yaml", "budgets.yaml", "harnesses.yaml", filepath.Join("roles", "tester.yaml")}
	for _, f := range bad {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("kind: Mystery\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, issues := Check(dir, opts(t, false))
	files := map[string]bool{}
	for _, i := range issues {
		if i.Code == "POLICY_UNKNOWN_KIND" {
			files[i.File] = true
		}
	}
	if len(files) != len(bad) {
		t.Errorf("want one POLICY_UNKNOWN_KIND per bad file (%d), got files %v", len(bad), files)
	}
}

// 26-AC-1: a flat ConfigMap-style tree still maps `roles__tester.yaml` to `roles/tester.yaml`.
func TestCheck_FlatConfigMapTreeStillMaps_26AC1(t *testing.T) {
	dir := copyTree(t, td("valid"), true)
	rev, issues := Check(dir, opts(t, true))
	if len(issues) != 0 {
		t.Fatalf("flat tree rejected: %v", issues)
	}
	if rev != recordedDigest(t) {
		t.Errorf("revision %q, want %q", rev, recordedDigest(t))
	}
}
