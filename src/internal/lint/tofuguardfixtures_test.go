package lint_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/lint"
	"strange.company/sc/internal/provider/guard"
)

// Rule IDs the implementer adds to lint.TofuValidate (the `make tofu-validate` check, plan 04 section 22):
//   - tofu-guard-fixtures: for each test/fixtures/tofu-plans/<dir>/expect.yaml whose "module" directory exists,
//     parse <module>/guard.yaml and <module>/guard-destroy.yaml with guard.ParseGuard and evaluate every case
//     with Guard.Evaluate; a verdict other than the case's "want" (pass, or the exact GUARD_* code) is an error
//     finding on the plan file; a *.json in the directory that no case names, a case file that does not exist, or
//     a missing guard file of an existing module are error findings too. Directories without expect.yaml (the
//     generic S2 fixtures) and expectations whose module directory does not exist are skipped.
//   - tofu-ovh-secret-vars and tofu-ovh-no-password (tofuovh_test.go).
const ruleGuardFixtures = "tofu-guard-fixtures"

func guardFixtureRoot(name string) string { return filepath.Join("testdata", "tofu-guard", name) }

func ruleFindings(r lint.Result, rule string) []lint.Finding {
	var out []lint.Finding
	for _, f := range errorFindings(r) {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

func ranRule(r lint.Result, rule string) bool {
	for _, x := range r.Rules {
		if x == rule {
			return true
		}
	}
	return false
}

func validate(t *testing.T, root string) lint.Result {
	t.Helper()
	res, err := lint.TofuValidate(root)
	if err != nil {
		t.Fatalf("TofuValidate(%s): %v", root, err)
	}
	return res
}

// 04-AC-2: a tree whose expectations match the guard verdicts (pass, exact code, destroy mode) is clean.
func TestTofuValidate_GuardFixtures_AcceptsMatchingExpectations_04AC2(t *testing.T) {
	res := validate(t, guardFixtureRoot("ok"))
	if !ranRule(res, ruleGuardFixtures) {
		t.Errorf("rule %s was not evaluated (Rules = %v)", ruleGuardFixtures, res.Rules)
	}
	if fs := errorFindings(res); len(fs) != 0 {
		t.Errorf("findings on a matching tree:\n%s", findingsText(res))
	}
}

// 04-AC-2: every divergence between an expectation and the guard verdict is an error finding on the plan file
// that names the expected and the actual outcome.
func TestTofuValidate_GuardFixtures_ReportsDivergences_04AC2(t *testing.T) {
	const dir = "test/fixtures/tofu-plans/ovh-vps/"
	cases := []struct {
		tree, path string
		mention    []string
	}{
		{"wrong-code", dir + "product-mismatch.json", []string{"GUARD_REGION_MISMATCH", "GUARD_PRODUCT_MISMATCH"}},
		{"expect-pass-but-fails", dir + "product-mismatch.json", []string{"GUARD_PRODUCT_MISMATCH"}},
		{"expect-fail-but-passes", dir + "valid-create.json", []string{"GUARD_PRODUCT_MISMATCH", "pass"}},
		{"wrong-count", dir + "valid-create.json", []string{"GUARD_COUNT_EXCEEDED"}},
		{"uncovered-json", dir + "replace.json", []string{"expect.yaml"}},
		{"missing-case-file", dir + "destroy-valid.json", []string{"destroy-valid.json"}},
		{"missing-guard-destroy", "infra/tofu/compute/ovh-vps/guard-destroy.yaml", []string{"guard-destroy.yaml"}},
	}
	for _, c := range cases {
		t.Run(c.tree, func(t *testing.T) {
			res := validate(t, guardFixtureRoot(c.tree))
			fs := ruleFindings(res, ruleGuardFixtures)
			if len(fs) == 0 {
				t.Fatalf("no %s finding; findings:\n%s", ruleGuardFixtures, findingsText(res))
			}
			var hit bool
			for _, f := range fs {
				if f.Path != c.path {
					continue
				}
				hit = true
				for _, m := range c.mention {
					if !strings.Contains(f.Message, m) {
						t.Errorf("finding %q does not mention %q", f.String(), m)
					}
				}
			}
			if !hit {
				t.Errorf("no finding on path %s:\n%s", c.path, findingsText(res))
			}
		})
	}
}

// 04-AC-2: fixture directories without expect.yaml (the generic S2 fixtures) and expectations of a module that
// does not exist yet are skipped, so the rule is safe to run before a module lands.
func TestTofuValidate_GuardFixtures_SkipsWhatItCannotJudge_04AC2(t *testing.T) {
	for _, tree := range []string{"no-expect", "module-absent"} {
		res := validate(t, guardFixtureRoot(tree))
		if !ranRule(res, ruleGuardFixtures) {
			t.Errorf("%s: rule %s was not evaluated", tree, ruleGuardFixtures)
		}
		if fs := errorFindings(res); len(fs) != 0 {
			t.Errorf("%s: findings:\n%s", tree, findingsText(res))
		}
	}
}

// ---- real tree -------------------------------------------------------------------------------------------

type expectFile struct {
	Module      string `yaml:"module"`
	ProductCode string `yaml:"productCode"`
	Region      struct {
		Type string `yaml:"type"`
		Path string `yaml:"path"`
		Want string `yaml:"want"`
	} `yaml:"region"`
	SSHKey struct {
		Type string `yaml:"type"`
		Path string `yaml:"path"`
		Want string `yaml:"want"`
	} `yaml:"sshKey"`
	Cases []struct {
		File    string `yaml:"file"`
		Nodes   int    `yaml:"nodes"`
		Destroy bool   `yaml:"destroy"`
		Want    string `yaml:"want"`
	} `yaml:"cases"`
}

var ovhModules = map[string]struct {
	resource string
}{
	"ovh-vps":          {"ovh_vps"},
	"ovh-public-cloud": {"ovh_cloud_project_instance"},
}

// 04-AC-2: the table test. For both OVH modules, every fixture in test/fixtures/tofu-plans/<module>/ is judged by
// the module's own guard.yaml / guard-destroy.yaml and yields exactly the code in expect.yaml; the table covers
// the valid create, the 3-node expansion and the six negative classes of the criterion.
func TestOVHGuardFixtures_Table_04AC2(t *testing.T) {
	root := repoRoot(t)
	required := map[string]bool{
		"GUARD_PRODUCT_MISMATCH": false, "GUARD_REGION_MISMATCH": false, "GUARD_SSH_KEY_MISMATCH": false,
		"GUARD_REPLACE_FORBIDDEN": false, "GUARD_UNEXPECTED_RESOURCE": false, "GUARD_ACTION_FORBIDDEN": false,
	}
	for name, mod := range ovhModules {
		t.Run(name, func(t *testing.T) {
			fixDir := filepath.Join(root, "test", "fixtures", "tofu-plans", name)
			raw, err := os.ReadFile(filepath.Join(fixDir, "expect.yaml"))
			if err != nil {
				t.Fatalf("expect.yaml: %v", err)
			}
			var ex expectFile
			if err := yaml.Unmarshal(raw, &ex); err != nil {
				t.Fatalf("expect.yaml: %v", err)
			}
			if want := "infra/tofu/compute/" + name; ex.Module != want {
				t.Fatalf("module = %q, want %q", ex.Module, want)
			}
			if ex.Region.Type != mod.resource || ex.SSHKey.Type != mod.resource {
				t.Fatalf("region/sshKey type = %q/%q, want %s", ex.Region.Type, ex.SSHKey.Type, mod.resource)
			}
			guards := map[bool]guard.Guard{}
			for destroy, file := range map[bool]string{false: "guard.yaml", true: "guard-destroy.yaml"} {
				b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ex.Module), file))
				if err != nil {
					t.Fatalf("%s/%s: %v (the module owns its guard files, plan 04 section 8)", ex.Module, file, err)
				}
				g, err := guard.ParseGuard(b)
				if err != nil {
					t.Fatalf("%s: %v", file, err)
				}
				guards[destroy] = g
			}
			seenPass := map[string]bool{}
			for _, c := range ex.Cases {
				plan, err := os.ReadFile(filepath.Join(fixDir, c.File))
				if err != nil {
					t.Fatalf("fixture %s: %v", c.File, err)
				}
				v := guards[c.Destroy].Evaluate(plan, guard.Expect{
					ExpectedNodeCount: c.Nodes,
					ProductCode:       ex.ProductCode,
					Region:            guard.Attr{Type: ex.Region.Type, Path: ex.Region.Path, Want: ex.Region.Want},
					SSHKey:            guard.Attr{Type: ex.SSHKey.Type, Path: ex.SSHKey.Path, Want: ex.SSHKey.Want},
					Destroy:           c.Destroy,
				})
				got := v.Code
				if v.Pass {
					got = "pass"
				}
				if got != c.Want {
					t.Errorf("%s (destroy=%v): verdict %s (%s), want %s", c.File, c.Destroy, got, v.Message, c.Want)
				}
				if _, ok := required[c.Want]; ok {
					required[c.Want] = true
				}
				if c.Want == "pass" {
					seenPass[c.File] = true
				}
			}
			for _, f := range []string{"valid-create.json", "valid-expand.json", "destroy-valid.json"} {
				if !seenPass[f] {
					t.Errorf("expect.yaml has no passing case for %s", f)
				}
			}
			for code, ok := range required {
				if !ok {
					t.Errorf("expect.yaml has no case that yields %s", code)
				}
			}
			for k := range required {
				required[k] = false
			}
		})
	}
}

// 04-AC-2: the same table is part of `make tofu-validate`: on the real repository the rule runs and is clean,
// and both OVH modules exist with their guard files.
func TestTofuValidate_Repository_RunsOVHGuardFixtures_04AC2(t *testing.T) {
	root := repoRoot(t)
	for name := range ovhModules {
		for _, f := range []string{"main.tf", "guard.yaml", "guard-destroy.yaml"} {
			if _, err := os.Stat(filepath.Join(root, "infra", "tofu", "compute", name, f)); err != nil {
				t.Errorf("infra/tofu/compute/%s/%s: %v", name, f, err)
			}
		}
	}
	res := validate(t, root)
	if !ranRule(res, ruleGuardFixtures) {
		t.Errorf("rule %s was not evaluated on the repository (Rules = %v)", ruleGuardFixtures, res.Rules)
	}
	if fs := ruleFindings(res, ruleGuardFixtures); len(fs) != 0 {
		t.Errorf("guard fixture findings on the repository:\n%s", findingsText(res))
	}
}
