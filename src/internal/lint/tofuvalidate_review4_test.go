package lint_test

import (
	"fmt"
	"strings"
	"testing"
)

// CD-73 review of S3 (04-AC-6, 04-AC-2).

// 04-AC-6 (CD-73): only public-key names are exempt from the secret-variable rule; credentials that merely contain
// "public" are not.
func TestTofuValidate_OVHStaticChecks_NarrowedPublicExemption_04AC6(t *testing.T) {
	res := validate(t, tofuFixture("ovh-secret-public-prefix"))
	fs := ruleFindings(res, ruleOVHSecretVars)
	text := findingsText(res)
	for _, name := range []string{"public_cloud_token", "ovh_public_cloud_consumer_key"} {
		found := false
		for _, f := range fs {
			if strings.Contains(f.Message, name) {
				found = true
			}
		}
		if !found {
			t.Errorf("want a %s finding naming %s, got:\n%s", ruleOVHSecretVars, name, text)
		}
	}
	for _, name := range []string{"ssh_public_key", "public_key", "pub_key"} {
		for _, f := range fs {
			if strings.Contains(f.Message, "variable "+name+" ") {
				t.Errorf("%s must stay exempt, got %s", name, f.String())
			}
		}
	}
}

// 04-AC-6: OpenTofu also loads .tofu, .tf.json and .tofu.json; a violation there must not go unseen (checked, or
// reported as unsupported, but never silently passed).
func TestTofuValidate_OVHStaticChecks_OtherConfigExtensions_04AC6(t *testing.T) {
	cases := []struct{ tree, rule string }{
		{"ovh-tofu-ext-no-dnsp", ruleOVHNoPassword},
		{"ovh-tf-json-secret-var", ruleOVHSecretVars},
		{"ovh-tofu-json-secret-var", ruleOVHSecretVars},
	}
	for _, c := range cases {
		t.Run(c.tree, func(t *testing.T) {
			res := validate(t, tofuFixture(c.tree))
			if len(ruleFindings(res, c.rule)) == 0 {
				t.Fatalf("want a %s finding, got:\n%s", c.rule, findingsText(res))
			}
		})
	}
}

// 04-AC-2: expect.yaml module and case file values must be local paths; others are an error finding and are not read.
func TestTofuValidate_GuardFixtures_RejectsNonLocalPaths_04AC2(t *testing.T) {
	for _, tree := range []string{"module-dotdot", "module-absolute", "case-file-dotdot"} {
		t.Run(tree, func(t *testing.T) {
			res := validate(t, guardFixtureRoot(tree))
			if len(ruleFindings(res, ruleGuardFixtures)) == 0 {
				t.Fatalf("want a %s error finding, got:\n%s", ruleGuardFixtures, findingsText(res))
			}
		})
	}
}

// 04-AC-2: findings come in a stable order when both guard files of a module fail (guard.yaml, then
// guard-destroy.yaml), not in map iteration order.
func TestTofuValidate_GuardFixtures_DeterministicFindingOrder_04AC2(t *testing.T) {
	var first string
	for i := 0; i < 20; i++ {
		res := validate(t, guardFixtureRoot("both-guards-invalid"))
		fs := ruleFindings(res, ruleGuardFixtures)
		if len(fs) != 2 {
			t.Fatalf("want 2 guard findings, got:\n%s", findingsText(res))
		}
		var parts []string
		for _, f := range fs {
			parts = append(parts, f.Path)
		}
		got := fmt.Sprint(parts)
		if i == 0 {
			first = got
			want := "[infra/tofu/compute/ovh-vps/guard-destroy.yaml infra/tofu/compute/ovh-vps/guard.yaml]"
			if got != want && got != "[infra/tofu/compute/ovh-vps/guard.yaml infra/tofu/compute/ovh-vps/guard-destroy.yaml]" {
				t.Fatalf("unexpected findings: %s", got)
			}
		}
		if got != first {
			t.Fatalf("run %d order %s differs from %s", i, got, first)
		}
	}
}
