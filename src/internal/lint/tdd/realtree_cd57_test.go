package tdd_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"go.yaml.in/yaml/v3"
)

func yamlAt(m map[string]any, path ...string) any {
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func listHas(v any, s string) bool {
	l, _ := v.([]any)
	for _, e := range l {
		if e == s {
			return true
		}
	}
	return false
}

// CD-57(b): cloneImage and verify.installerImage are optional until the node that publishes those images.
func TestRealTree_CIPolicyImagesOptional_CD57b(t *testing.T) { // 52-AC-4
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "contracts", "ci-policy.schema.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := yaml.Unmarshal(b, &schema); err != nil {
		t.Fatal(err)
	}
	if listHas(yamlAt(schema, "properties", "spec", "required"), "cloneImage") {
		t.Error("contracts/ci-policy.schema.yaml: spec.required must not list cloneImage (CD-57b)")
	}
	if listHas(yamlAt(schema, "properties", "spec", "properties", "verify", "required"), "installerImage") {
		t.Error("contracts/ci-policy.schema.yaml: verify.required must not list installerImage (CD-57b)")
	}
	if yamlAt(schema, "properties", "spec", "properties", "cloneImage") == nil {
		t.Error("cloneImage must stay a declared (optional) property")
	}
}

func TestRealTree_CIPolicyHasNoPlaceholderImages_CD57b(t *testing.T) { // 52-AC-4
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "policy", "ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`sha256:(5{64}|6{64})`).Match(b) {
		t.Error("policy/ci.yaml holds a placeholder digest (sha256:5555... or sha256:6666...)")
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if yamlAt(doc, "spec", "cloneImage") != nil {
		t.Error("policy/ci.yaml must omit spec.cloneImage (CD-57b)")
	}
	if yamlAt(doc, "spec", "verify", "installerImage") != nil {
		t.Error("policy/ci.yaml must omit spec.verify.installerImage (CD-57b)")
	}
}
