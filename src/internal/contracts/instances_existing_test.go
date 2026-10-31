package contracts_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Instance-validation helpers (plan 01 8.3, 01-AC-23) shared with instances_test.go.

// instRow is one governed glob member: the instance path placed in a tree copy, the schema file that must
// govern it, and the kind carried by the instance ("" for the register files, which are real files).
type instRow struct{ path, schema, kind string }

func retag(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && (n.ShortTag() == "!!timestamp" || n.ShortTag() == "!!binary") {
		n.Tag = "!!str"
	}
	for _, c := range n.Content {
		retag(c)
	}
}

func readYAMLAny(t *testing.T, path string) any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var n yaml.Node
	if err := yaml.Unmarshal(b, &n); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	retag(&n)
	var v any
	if err := n.Decode(&v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

func kindOf(s any) string {
	m, _ := s.(map[string]any)
	p, _ := m["properties"].(map[string]any)
	k, _ := p["kind"].(map[string]any)
	c, _ := k["const"].(string)
	return c
}

// schemaExample returns a copy-safe valid instance of kind taken from the schema's examples or
// x-defs-examples, and the schema node (top level or $defs entry) that declares the kind.
func schemaExample(t *testing.T, root, schemaFile, kind string) (map[string]any, map[string]any) {
	t.Helper()
	sc, _ := readYAMLAny(t, filepath.Join(root, "contracts", schemaFile)).(map[string]any)
	var node map[string]any
	if kindOf(sc) == kind {
		node = sc
	}
	defs, _ := sc["$defs"].(map[string]any)
	if node == nil {
		for _, d := range defs {
			if kindOf(d) == kind {
				node, _ = d.(map[string]any)
			}
		}
	}
	if node == nil {
		t.Fatalf("%s: no schema node with kind %s", schemaFile, kind)
	}
	var cands []any
	cands = append(cands, sc["examples"].([]any)...)
	if dx, ok := sc["x-defs-examples"].(map[string]any); ok {
		names := make([]string, 0, len(dx))
		for n := range dx {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			l, _ := dx[n].([]any)
			cands = append(cands, l...)
		}
	}
	for _, c := range cands {
		if m, ok := c.(map[string]any); ok && m["kind"] == kind {
			return m, node
		}
	}
	t.Fatalf("%s: no example of kind %s", schemaFile, kind)
	return nil, nil
}

// violate removes the first required top-level member (other than apiVersion and kind) the schema node
// names; failing that it corrupts apiVersion. Either way the instance no longer validates.
func violate(doc, node map[string]any) {
	req, _ := node["required"].([]any)
	for _, r := range req {
		k, _ := r.(string)
		if _, ok := doc[k]; ok && k != "apiVersion" && k != "kind" {
			delete(doc, k)
			return
		}
	}
	doc["apiVersion"] = "strange.company/v0bogus"
}

func putYAML(t *testing.T, dir, rel string, v any) {
	t.Helper()
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	writeIn(t, dir, rel, string(b))
}

func passRow(rows []row, file string) bool {
	for _, r := range rows {
		if r.File == file && r.Result == "pass" {
			return true
		}
	}
	return false
}

func failRow(rows []row, file string) bool {
	for _, r := range rows {
		if r.File == file && r.Result == "fail" {
			return true
		}
	}
	return false
}

// checkPlaced places a valid instance for every row in a tree copy, runs `make contracts`, and requires
// exit 0, a pass row per file and an output line naming the file and its schema file.
func checkPlaced(t *testing.T, rs []instRow) {
	t.Helper()
	dir := treeCopy(t)
	root := repoRoot(t)
	for _, r := range rs {
		doc, _ := schemaExample(t, root, r.schema, r.kind)
		putYAML(t, dir, r.path, doc)
	}
	code, out := makeContracts(t, dir)
	if code != 0 {
		t.Errorf("`make contracts` exited %d, want 0:\n%s", code, out)
	}
	rows := readRows(t, dir)
	for _, r := range rs {
		if !passRow(rows, r.path) {
			t.Errorf("%s: no pass row in report.json", r.path)
		}
		if failRow(rows, r.path) {
			t.Errorf("%s: fail row in report.json", r.path)
		}
		if !hasLine(out, r.path, r.schema) {
			t.Errorf("%s: no output line naming the file and %s", r.path, r.schema)
		}
	}
}

// checkViolating places a violating instance for every row; each must fail, naming the file.
func checkViolating(t *testing.T, rs []instRow) {
	t.Helper()
	dir := treeCopy(t)
	root := repoRoot(t)
	for _, r := range rs {
		doc, node := schemaExample(t, root, r.schema, r.kind)
		violate(doc, node)
		putYAML(t, dir, r.path, doc)
	}
	code, out := makeContracts(t, dir)
	if code == 0 {
		t.Errorf("`make contracts` exited 0 with violating instances:\n%s", out)
	}
	rows := readRows(t, dir)
	for _, r := range rs {
		if !failRow(rows, r.path) {
			t.Errorf("%s: no fail row in report.json", r.path)
		}
		if !hasLine(out, r.path, "FAIL") {
			t.Errorf("%s: no FAIL output line naming the file", r.path)
		}
	}
}

// the lock-file globs implemented by slice S1.
var lockRows = []instRow{
	{"versions/platform.yaml", "version-locks.schema.yaml", "PlatformVersionLock"},
	{"versions/k3s.yaml", "version-locks.schema.yaml", "K3sVersionLock"},
	{"toolbox/image.lock", "version-locks.schema.yaml", "ToolboxImageLock"},
	{"infra/lab/lab.yaml", "version-locks.schema.yaml", "LabSpec"},
}

// 01-AC-23: violating lock instances at the same globs fail, naming the file.
func TestInstances_LockGlobsViolationFails_01AC23(t *testing.T) {
	t.Parallel()
	checkViolating(t, lockRows)
}

// 01-AC-23: a versions/x.yaml with kind HelmLock fails CONTRACT_KIND_UNKNOWN.
func TestInstances_HelmLockKindUnknown_01AC23(t *testing.T) {
	t.Parallel()
	body := "apiVersion: strange.company/v1alpha1\nkind: HelmLock\nspec: {}\n"
	expectFail(t, map[string]string{"versions/x.yaml": body}, "CONTRACT_KIND_UNKNOWN", "versions/x.yaml")
}

// 01-AC-23 (decision: plan 01 8.3 is silent on the ID for a missing kind; it is a kind with no schema, so
// CONTRACT_KIND_UNKNOWN): a file under a kind-selected glob without kind fails.
func TestInstances_NoKindUnderVersionsGlob_01AC23(t *testing.T) {
	t.Parallel()
	body := "apiVersion: strange.company/v1alpha1\nspec: {}\n"
	expectFail(t, map[string]string{"versions/nokind.yaml": body}, "CONTRACT_KIND_UNKNOWN", "versions/nokind.yaml")
}
