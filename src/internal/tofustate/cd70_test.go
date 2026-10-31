package tofustate_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"strange.company/sc/internal/contracts"
)

// CD-70 (b) and (d), plans/reviews/consolidation-decisions.md.

func repoDir(t testing.TB) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "plans", "06-opentofu-state.md")); err == nil {
			return d
		}
		p := filepath.Dir(d)
		if p == d {
			t.Fatal("repository root not found")
		}
		d = p
	}
}

func restoreResult(modules any, withModules bool, outcome string) map[string]any {
	doc := map[string]any{
		"schema_version": "v1alpha1",
		"result_id":      "9b8c7d6e-5f4a-4b3c-8d2e-1f0a9b8c7d6e",
		"test_id":        "rt-tofu-state",
		"test_kind":      "opentofu-state",
		"subject":        "opentofu-state",
		"installation":   map[string]any{"cluster": "lab-one"},
		"runner":         map[string]any{"kind": "bootstrap", "tool_version": "0.1.0"},
		"started_at":     "2026-10-27T09:12:00Z",
		"finished_at":    "2026-10-27T09:13:30Z",
		"outcome":        outcome,
		"checks":         []any{map[string]any{"id": "state-not-applicable", "outcome": "passed"}},
	}
	if withModules {
		doc["modules"] = modules
	}
	return doc
}

func validateRestoreResult(t testing.TB, doc map[string]any) error {
	t.Helper()
	schemas, err := contracts.CompileSchemas(filepath.Join(repoDir(t), "contracts"))
	if err != nil {
		t.Fatal(err)
	}
	sc := schemas["restore-test-result"]
	if sc == nil {
		t.Fatal("no restore-test-result schema")
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var inst any
	if err := dec.Decode(&inst); err != nil {
		t.Fatal(err)
	}
	return sc.Validate(inst)
}

// CD-70 (b), 06-AC-14: restore-test-result.schema.yaml gains an optional `modules` array; an empty one (no-module
// install) with outcome passed validates, and so does a list of module names.
func TestRestoreTestResultSchema_ModulesArray_CD70b_06AC14(t *testing.T) {
	if err := validateRestoreResult(t, restoreResult([]any{}, true, "passed")); err != nil {
		t.Errorf("a result with modules: [] and outcome passed does not validate: %v", err)
	}
	if err := validateRestoreResult(t, restoreResult([]any{"compute", "storage"}, true, "passed")); err != nil {
		t.Errorf("a result with modules [compute storage] does not validate: %v", err)
	}
	// optional: the documents of every other writer carry no modules field
	if err := validateRestoreResult(t, restoreResult(nil, false, "passed")); err != nil {
		t.Errorf("a result without modules no longer validates: %v", err)
	}
}

// CD-70 (b): `modules` is an array of module names (strings), nothing else.
func TestRestoreTestResultSchema_ModulesMustBeNames_CD70b(t *testing.T) {
	// The array form must be accepted before the wrong forms can be told apart from a closed schema.
	if err := validateRestoreResult(t, restoreResult([]any{"compute"}, true, "passed")); err != nil {
		t.Fatalf("a result with modules [compute] does not validate: %v", err)
	}
	for name, bad := range map[string]any{
		"a string":      "compute",
		"an object":     map[string]any{"compute": true},
		"a number item": []any{1},
	} {
		if err := validateRestoreResult(t, restoreResult(bad, true, "passed")); err == nil {
			t.Errorf("modules as %s validates", name)
		}
	}
}

// CD-70 (d): the fake compute module is built only from the built-in terraform_data resource: no provider block, no
// required_providers, no module call, no data source, and a validation that refuses it outside SC_TEST_MODE=1.
func TestFakeComputeModule_OnlyBuiltinResources_CD70d(t *testing.T) {
	dir := filepath.Join(repoDir(t), "infra", "tofu", "compute", "fake")
	files, _ := filepath.Glob(filepath.Join(dir, "*.tf"))
	if len(files) == 0 {
		t.Fatalf("%s has no .tf files: the fake compute module (CD-70(d)) does not exist", dir)
	}
	var resources []string
	vars := map[string]*hclsyntax.Block{}
	var srcs = map[string][]byte{}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		srcs[f] = src
		file, diags := hclsyntax.ParseConfig(src, f, hcl.InitialPos)
		if diags.HasErrors() {
			t.Fatalf("%s does not parse: %v", f, diags)
		}
		for _, bl := range file.Body.(*hclsyntax.Body).Blocks {
			switch bl.Type {
			case "resource":
				resources = append(resources, bl.Labels[0])
			case "variable":
				vars[bl.Labels[0]] = bl
			case "output", "locals":
			case "terraform":
				for _, in := range bl.Body.Blocks {
					if in.Type == "required_providers" || in.Type == "backend" {
						t.Errorf("%s: terraform { %s } in the module: providers need a plugin and the backend is rendered by sc-bootstrap", filepath.Base(f), in.Type)
					}
				}
			default:
				t.Errorf("%s: %s block %v is not allowed in the plugin-free fake module", filepath.Base(f), bl.Type, bl.Labels)
			}
		}
	}
	sort.Strings(resources)
	if len(resources) == 0 {
		t.Error("the module declares no resource: it must record the fake provider's nodes in state")
	}
	for _, r := range resources {
		if r != "terraform_data" {
			t.Errorf("resource type %q: only the built-in terraform_data is allowed", r)
		}
	}
	bl := vars["sc_test_mode"]
	if bl == nil {
		t.Fatalf("the module has no variable sc_test_mode (fed from SC_TEST_MODE as TF_VAR_sc_test_mode): it cannot refuse a run outside test mode")
	}
	var validation *hclsyntax.Block
	for _, in := range bl.Body.Blocks {
		if in.Type == "validation" {
			validation = in
		}
	}
	if validation == nil {
		t.Fatal("variable sc_test_mode has no validation block")
	}
	for f, src := range srcs {
		if strings.Contains(string(src), "sc_test_mode") && !strings.Contains(string(src), "SC_TEST_MODE") {
			t.Errorf("%s: the refusal message does not name SC_TEST_MODE", filepath.Base(f))
		}
	}
}
