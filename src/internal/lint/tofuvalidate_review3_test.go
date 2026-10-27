package lint_test

import (
	"testing"

	"strange.company/sc/internal/lint"
)

// 06-AC-9: OpenTofu 1.13.1 loads every repeated block key in a .tf.json file while encoding/json keeps only the
// last value, so a repeated key can hide an unprotected aws_s3_bucket behind a protected one. Decision: tofu-validate
// fails closed on ANY repeated object key in a JSON-form file, reported under rule tofu-prevent-destroy at the
// file's path (the message is free, it should name the key).
func TestTofuValidate_RepeatedJSONKeyFailsClosed_06AC9(t *testing.T) {
	cases := []string{"dupkey-resource", "dupkey-type", "dupkey-lifecycle", "dupkey-attribute"}
	for _, fx := range cases {
		t.Run(fx, func(t *testing.T) {
			res, err := lint.TofuValidate(tofuFixture(fx))
			if err != nil {
				t.Fatalf("TofuValidate: %v", err)
			}
			found := false
			for _, f := range errorFindings(res) {
				if f.Rule == "tofu-prevent-destroy" && f.Path == "infra/tofu/storage/buckets.tf.json" {
					found = true
				}
			}
			if !found {
				t.Errorf("no tofu-prevent-destroy finding for the repeated key; findings:\n%s", findingsText(res))
			}
		})
	}
}
