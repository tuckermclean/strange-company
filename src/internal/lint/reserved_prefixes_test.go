package lint_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// CD-55 (plan 01 section 6.4): `.strange-company/policy.yaml` lists the reserved branch prefixes in
// spec.reservedBranchPrefixes and contracts/repo-policy.schema.yaml declares the field.

var wantReservedPrefixes = []string{"agent/", "bootstrap/", "renovate/", "sc-verify/", "release/"}

func policySpecField(t *testing.T, root string) (any, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, ".strange-company", "policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Spec map[string]any `yaml:"spec"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	v, ok := doc.Spec["reservedBranchPrefixes"]
	return v, ok
}

// CD-55: policy.yaml has reservedBranchPrefixes exactly [agent/, bootstrap/, renovate/, sc-verify/, release/].
func TestRealTree_PolicyReservedBranchPrefixes_CD55(t *testing.T) {
	v, ok := policySpecField(t, repoRoot(t))
	if !ok {
		t.Fatal("policy.yaml spec has no reservedBranchPrefixes")
	}
	list, _ := v.([]any)
	got := make([]string, 0, len(list))
	for _, e := range list {
		s, _ := e.(string)
		got = append(got, s)
	}
	if !reflect.DeepEqual(got, wantReservedPrefixes) {
		t.Errorf("reservedBranchPrefixes = %v, want %v", got, wantReservedPrefixes)
	}
}

// CD-55: the schema declares the property (so its absence fails the real tree, not only a copy).
func TestRealTree_SchemaDeclaresReservedBranchPrefixes_CD55(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "contracts", "repo-policy.schema.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Properties struct {
			Spec struct {
				Properties map[string]any `yaml:"properties"`
			} `yaml:"spec"`
		} `yaml:"properties"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Properties.Spec.Properties["reservedBranchPrefixes"]; !ok {
		t.Error("repo-policy.schema.yaml spec.properties has no reservedBranchPrefixes")
	}
}

// CD-55: `make contracts` passes on the real tree with the field present (and says it validated policy.yaml).
func TestRealTree_MakeContractsPassesWithReservedPrefixes_CD55(t *testing.T) {
	root := repoRoot(t)
	if _, ok := policySpecField(t, root); !ok {
		t.Fatal("policy.yaml spec has no reservedBranchPrefixes; nothing to validate")
	}
	code, out := runMakeContracts(t, root)
	if code != 0 {
		t.Errorf("`make contracts` exited %d, want 0:\n%s", code, out)
	}
}

// CD-55: a bad reservedBranchPrefixes value fails `make contracts` and names policy.yaml. The baseline
// (the unmodified copy, then the copy with a good list) must pass first.
func TestMakeContracts_RejectsBadReservedBranchPrefixes_CD55(t *testing.T) {
	cases := []struct{ name, block string }{
		{"no-trailing-slash", "  reservedBranchPrefixes:\n    - agent/\n    - bootstrap\n"},
		{"duplicate", "  reservedBranchPrefixes:\n    - agent/\n    - agent/\n"},
		{"non-string", "  reservedBranchPrefixes:\n    - agent/\n    - 42\n"},
	}
	good := "  reservedBranchPrefixes:\n    - agent/\n    - bootstrap/\n"
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			dir := tempTreeCopy(t)
			requireBaselinePasses(t, dir)
			p := filepath.Join(dir, ".strange-company", "policy.yaml")
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			s := string(b)
			if strings.Contains(s, "reservedBranchPrefixes") {
				t.Fatal("policy.yaml already holds reservedBranchPrefixes; the mutation below would double it")
			}
			// Insert the block as the first key of spec so it is valid YAML whatever follows.
			ins := strings.Replace(s, "spec:\n", "spec:\n"+good, 1)
			if err := os.WriteFile(p, []byte(ins), 0o644); err != nil {
				t.Fatal(err)
			}
			// The good list must be accepted (this fails while the schema lacks the property).
			if code, out := runMakeContracts(t, dir); code != 0 {
				t.Fatalf("`make contracts` rejected a valid reservedBranchPrefixes list (exit %d):\n%s", code, out)
			}
			mut := strings.Replace(s, "spec:\n", "spec:\n"+c.block, 1)
			if err := os.WriteFile(p, []byte(mut), 0o644); err != nil {
				t.Fatal(err)
			}
			code, out := runMakeContracts(t, dir)
			if code == 0 {
				t.Errorf("`make contracts` exited 0 with %s reservedBranchPrefixes:\n%s", c.name, out)
			}
			if !strings.Contains(out, "policy.yaml") {
				t.Errorf("output does not name policy.yaml:\n%s", out)
			}
		})
	}
}
