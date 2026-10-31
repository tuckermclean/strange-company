package lint_test

import (
	"strings"
	"testing"

	"strange.company/sc/internal/lint"
)

// 06-AC-9 (plan 06 section 14.2): the check covers every file OpenTofu 1.13.1 loads: *.tf, *.tofu (which shadows a
// *.tf of the same name), *.tf.json and *.tofu.json, in any directory except .terraform. An `out` directory is an
// ordinary module directory under infra/tofu.
func TestTofuValidate_CoversEveryFileTofuLoads_06AC9(t *testing.T) {
	cases := []struct{ fixture, path string }{
		{"unprotected-tofu", "infra/tofu/storage/buckets.tofu"},
		{"unprotected-tf-json", "infra/tofu/storage/buckets.tf.json"},
		{"unprotected-tofu-json", "infra/tofu/storage/buckets.tofu.json"},
		{"shadowed-tofu", "infra/tofu/storage/main.tofu"},
		{"unprotected-out-dir", "infra/tofu/storage/out/extra.tf"},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			res, err := lint.TofuValidate(tofuFixture(c.fixture))
			if err != nil {
				t.Fatalf("TofuValidate: %v", err)
			}
			found := false
			for _, f := range errorFindings(res) {
				if f.Rule == "tofu-prevent-destroy" && f.Path == c.path && strings.Contains(f.Message, "aws_s3_bucket.leak") {
					found = true
				}
			}
			if !found {
				t.Errorf("no tofu-prevent-destroy finding for aws_s3_bucket.leak in %s; findings:\n%s", c.path, findingsText(res))
			}
		})
	}
}

// 06-AC-9: the CLI exits 1 for an unprotected bucket in a JSON-form file.
func TestTofuValidate_CLI_JSONFormFails_06AC9(t *testing.T) {
	for _, fx := range []string{"unprotected-tf-json", "unprotected-tofu-json", "unprotected-tofu"} {
		var out, errb strings.Builder
		if code := lint.Main([]string{"tofu-validate", "--root", tofuFixture(fx)}, &out, &errb); code != 1 {
			t.Errorf("%s: exit %d, want 1\nstdout: %s\nstderr: %s", fx, code, out.String(), errb.String())
		}
	}
}

// 06-AC-9: protected buckets in .tofu and .tf.json form pass; a .terraform directory (downloaded modules) stays
// skipped.
func TestTofuValidate_AcceptsProtectedTofuForms_06AC9(t *testing.T) {
	for _, fx := range []string{"ok-tofu-forms", "terraform-dir-skipped"} {
		t.Run(fx, func(t *testing.T) {
			res, err := lint.TofuValidate(tofuFixture(fx))
			if err != nil {
				t.Fatalf("TofuValidate: %v", err)
			}
			if fs := errorFindings(res); len(fs) != 0 {
				t.Errorf("findings:\n%s", findingsText(res))
			}
		})
	}
}

// 06-AC-9: a guard.yaml in JSON flow form (YAML is a superset) with a delete action (any case) still fails.
func TestTofuValidate_GuardDeleteInJSONForm_06AC9(t *testing.T) {
	res, err := lint.TofuValidate(tofuFixture("guard-delete-json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range errorFindings(res) {
		if f.Rule == "tofu-guard-no-delete" && f.Path == "infra/tofu/storage/guard.yaml" {
			return
		}
	}
	t.Errorf("no tofu-guard-no-delete finding; findings:\n%s", findingsText(res))
}
