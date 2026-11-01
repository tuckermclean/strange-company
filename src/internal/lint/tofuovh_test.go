package lint_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Static HCL checks for the OVH compute modules (04-AC-6, plan 04 section 10 and plan 03 l.143), run by
// lint.TofuValidate under `make tofu-validate` over infra/tofu/compute/ovh-*/ only (any other directory is not
// checked):
//   - tofu-ovh-secret-vars: each `variable` whose name matches (?i)(secret|password|token|key) has the literal
//     `sensitive = true` and the literal `ephemeral = true`. A module with no such variable is compliant
//     (credentials come only from the provider's environment variables).
//   - tofu-ovh-no-password: each `resource "ovh_vps"` has the literal `do_not_send_password = true` (a variable,
//     false, a comment or a missing attribute fails). Findings are on the .tf file and name the variable or resource.
const (
	ruleOVHSecretVars = "tofu-ovh-secret-vars"
	ruleOVHNoPassword = "tofu-ovh-no-password"
	vpsMain           = "infra/tofu/compute/ovh-vps/main.tf"
	vpsVars           = "infra/tofu/compute/ovh-vps/variables.tf"
)

// 04-AC-6: compliant modules pass; non-OVH compute modules and non-secret names are not checked.
func TestTofuValidate_OVHStaticChecks_AcceptCompliantModules_04AC6(t *testing.T) {
	res := validate(t, tofuFixture("ovh-ok"))
	for _, rule := range []string{ruleOVHSecretVars, ruleOVHNoPassword} {
		if !ranRule(res, rule) {
			t.Errorf("rule %s was not evaluated (Rules = %v)", rule, res.Rules)
		}
	}
	if fs := errorFindings(res); len(fs) != 0 {
		t.Errorf("findings on a compliant tree:\n%s", findingsText(res))
	}
}

// 04-AC-6 (amended): a variable whose name also matches (?i)public is not a secret (a public SSH key; an ephemeral
// variable cannot feed ovh_vps.public_ssh_key at OpenTofu 1.13.1), so a plain ssh_public_key is compliant.
func TestTofuValidate_OVHStaticChecks_PublicNameExempt_04AC6(t *testing.T) {
	res := validate(t, tofuFixture("ovh-var-public-exempt"))
	if !ranRule(res, ruleOVHSecretVars) {
		t.Errorf("rule %s was not evaluated (Rules = %v)", ruleOVHSecretVars, res.Rules)
	}
	if fs := ruleFindings(res, ruleOVHSecretVars); len(fs) != 0 {
		t.Errorf("ssh_public_key must be exempt:\n%s", findingsText(res))
	}
	if fs := errorFindings(res); len(fs) != 0 {
		t.Errorf("findings on a compliant tree:\n%s", findingsText(res))
	}
}

// 04-AC-6: each violation is reported by its rule on the file that holds it and names the offender.
func TestTofuValidate_OVHStaticChecks_FailOnViolations_04AC6(t *testing.T) {
	cases := []struct{ tree, rule, path, mention string }{
		{"ovh-var-not-sensitive", ruleOVHSecretVars, vpsVars, "api_token"},
		{"ovh-var-sensitive-false", ruleOVHSecretVars, vpsVars, "api_token"},
		{"ovh-var-not-ephemeral", ruleOVHSecretVars, vpsVars, "root_password"},
		{"ovh-var-case-insensitive", ruleOVHSecretVars, vpsVars, "App_SECRET_value"},
		{"ovh-var-key-name", ruleOVHSecretVars, vpsVars, "ssh_private_key"},
		{"ovh-var-in-cloud-module", ruleOVHSecretVars, "infra/tofu/compute/ovh-public-cloud/main.tf", "consumer_token"},
		{"ovh-no-dnsp", ruleOVHNoPassword, vpsMain, "ovh_vps.node"},
		{"ovh-dnsp-false", ruleOVHNoPassword, vpsMain, "ovh_vps.node"},
		{"ovh-dnsp-variable", ruleOVHNoPassword, vpsMain, "ovh_vps.node"},
		{"ovh-dnsp-in-comment", ruleOVHNoPassword, vpsMain, "ovh_vps.node"},
		{"ovh-dnsp-one-of-two", ruleOVHNoPassword, vpsMain, "ovh_vps.b"},
	}
	for _, c := range cases {
		t.Run(c.tree, func(t *testing.T) {
			res := validate(t, tofuFixture(c.tree))
			fs := ruleFindings(res, c.rule)
			if len(fs) != 1 {
				t.Fatalf("want exactly one %s finding, got:\n%s", c.rule, findingsText(res))
			}
			if fs[0].Path != c.path || !regexp.MustCompile(regexp.QuoteMeta(c.mention)+`\b`).MatchString(fs[0].Message) {
				t.Errorf("finding %q: want path %s mentioning %s", fs[0].String(), c.path, c.mention)
			}
			if all := errorFindings(res); len(all) != 1 {
				t.Errorf("unrelated findings:\n%s", findingsText(res))
			}
		})
	}
}

// 04-AC-6 on the real repository: both OVH modules exist, the ovh-vps module declares an ovh_vps resource (the
// check is not vacuous), and the static checks find nothing.
func TestTofuValidate_Repository_OVHStaticChecks_04AC6(t *testing.T) {
	root := repoRoot(t)
	for _, name := range []string{"ovh-vps", "ovh-public-cloud"} {
		files, _ := filepath.Glob(filepath.Join(root, "infra", "tofu", "compute", name, "*.tf"))
		if len(files) == 0 {
			t.Errorf("infra/tofu/compute/%s has no .tf files", name)
		}
	}
	var all []byte
	files, _ := filepath.Glob(filepath.Join(root, "infra", "tofu", "compute", "ovh-vps", "*.tf"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, b...)
	}
	if !regexp.MustCompile(`resource\s+"ovh_vps"\s+"`).Match(all) {
		t.Error("infra/tofu/compute/ovh-vps declares no ovh_vps resource")
	}
	res := validate(t, root)
	for _, rule := range []string{ruleOVHSecretVars, ruleOVHNoPassword} {
		if !ranRule(res, rule) {
			t.Errorf("rule %s was not evaluated on the repository (Rules = %v)", rule, res.Rules)
		}
	}
	if fs := append(ruleFindings(res, ruleOVHSecretVars), ruleFindings(res, ruleOVHNoPassword)...); len(fs) != 0 {
		t.Errorf("findings on the repository modules: %v", fs)
	}
}
