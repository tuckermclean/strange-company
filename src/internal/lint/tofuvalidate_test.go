package lint_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/lint"
)

func tofuFixture(name string) string { return filepath.Join("testdata", "tofu", name) }

func findingsText(r lint.Result) string {
	var sb strings.Builder
	for _, f := range r.Findings {
		sb.WriteString(f.String() + "\n")
	}
	return sb.String()
}

func errorFindings(r lint.Result) []lint.Finding {
	var out []lint.Finding
	for _, f := range r.Findings {
		if f.Severity != "warning" {
			out = append(out, f)
		}
	}
	return out
}

// 06-AC-9: a tree where every aws_s3_bucket (for_each, module and root alike) has prevent_destroy = true and the
// storage guard has no delete passes; a compute guard may allow delete.
func TestTofuValidate_AcceptsCompliantTree_06AC9(t *testing.T) {
	res, err := lint.TofuValidate(tofuFixture("ok"))
	if err != nil {
		t.Fatalf("TofuValidate: %v", err)
	}
	if fs := errorFindings(res); len(fs) != 0 {
		t.Errorf("findings on a compliant tree:\n%s", findingsText(res))
	}
	ran := strings.Join(res.Rules, ",")
	for _, rule := range []string{"tofu-prevent-destroy", "tofu-guard-no-delete"} {
		if !strings.Contains(ran, rule) {
			t.Errorf("rule %s was not evaluated (Rules = %v)", rule, res.Rules)
		}
	}
}

// 06-AC-9: the static checks fail on a fixture that violates either rule, naming the file and the resource.
func TestTofuValidate_FailsOnViolatingFixtures_06AC9(t *testing.T) {
	cases := []struct {
		fixture, rule, path, mention string
	}{
		{"missing-prevent-destroy", "tofu-prevent-destroy", "infra/tofu/storage/main.tf", "aws_s3_bucket.state"},
		{"prevent-destroy-false", "tofu-prevent-destroy", "infra/tofu/storage/main.tf", "aws_s3_bucket.state"},
		{"prevent-destroy-variable", "tofu-prevent-destroy", "infra/tofu/storage/main.tf", "aws_s3_bucket.state"},
		{"prevent-destroy-in-comment", "tofu-prevent-destroy", "infra/tofu/storage/main.tf", "aws_s3_bucket.state"},
		{"one-of-two", "tofu-prevent-destroy", "infra/tofu/modules/buckets/main.tf", "aws_s3_bucket.forgotten"},
		{"guard-delete-bucket", "tofu-guard-no-delete", "infra/tofu/storage/guard.yaml", "aws_s3_bucket"},
		{"guard-delete-other", "tofu-guard-no-delete", "infra/tofu/storage/guard.yaml", "aws_s3_bucket_policy"},
		{"guard-missing", "tofu-guard-no-delete", "infra/tofu/storage/guard.yaml", "guard.yaml"},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			res, err := lint.TofuValidate(tofuFixture(c.fixture))
			if err != nil {
				t.Fatalf("TofuValidate: %v", err)
			}
			var hit *lint.Finding
			for i, f := range errorFindings(res) {
				if f.Rule == c.rule && f.Path == c.path {
					hit = &errorFindings(res)[i]
				}
			}
			if hit == nil {
				t.Fatalf("no %s finding for %s; findings:\n%s", c.rule, c.path, findingsText(res))
			}
			if !strings.Contains(hit.Message, c.mention) {
				t.Errorf("message %q does not name %q", hit.Message, c.mention)
			}
		})
	}
}

// 06-AC-9: only the unprotected resource is reported, not its protected sibling.
func TestTofuValidate_ReportsOnlyTheViolator_06AC9(t *testing.T) {
	res, err := lint.TofuValidate(tofuFixture("one-of-two"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range errorFindings(res) {
		if strings.Contains(f.Message, "aws_s3_bucket.protected") {
			t.Errorf("the protected bucket is reported: %s", f)
		}
		if f.Rule == "tofu-prevent-destroy" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d prevent-destroy findings, want exactly 1:\n%s", n, findingsText(res))
	}
}

// 06-AC-9: `sc-lint tofu-validate --root DIR` exits 0 on a compliant tree and 1 with findings on a violating one.
func TestTofuValidate_CLI_06AC9(t *testing.T) {
	var out, errb bytes.Buffer
	if code := lint.Main([]string{"tofu-validate", "--root", tofuFixture("ok")}, &out, &errb); code != 0 {
		t.Errorf("exit %d on a compliant tree\nstdout: %s\nstderr: %s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "tofu-validate: pass") {
		t.Errorf("stdout %q lacks \"tofu-validate: pass\"", out.String())
	}
	out.Reset()
	errb.Reset()
	if code := lint.Main([]string{"tofu-validate", "--root", tofuFixture("missing-prevent-destroy")}, &out, &errb); code != 1 {
		t.Errorf("exit %d on a violating tree, want 1\nstdout: %s\nstderr: %s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "aws_s3_bucket.state") || !strings.Contains(out.String(), "tofu-validate: FAIL") {
		t.Errorf("stdout %q lacks the finding or \"tofu-validate: FAIL\"", out.String())
	}
}

// 06-AC-9 on the real repository: infra/tofu/storage exists and passes both checks.
func TestTofuValidate_Repository_06AC9(t *testing.T) {
	root := repoRoot(t)
	for _, f := range []string{"main.tf", "guard.yaml"} {
		if _, err := os.Stat(filepath.Join(root, "infra", "tofu", "storage", f)); err != nil {
			t.Fatalf("infra/tofu/storage/%s: %v", f, err)
		}
	}
	res, err := lint.TofuValidate(root)
	if err != nil {
		t.Fatalf("TofuValidate(repo): %v", err)
	}
	if fs := errorFindings(res); len(fs) != 0 {
		t.Errorf("findings on the repository:\n%s", findingsText(res))
	}
}

func readStorage(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "infra", "tofu", "storage", name))
	if err != nil {
		t.Fatalf("infra/tofu/storage/%s: %v", name, err)
	}
	return string(b)
}

// CD-70f / plan 14 section 9.1: the storage root module declares the whole bucket set as one for_each list.
func TestStorageModule_DeclaresTheBucketSet_06AC9(t *testing.T) {
	var all strings.Builder
	files, _ := filepath.Glob(filepath.Join(repoRoot(t), "infra", "tofu", "storage", "*.tf"))
	if len(files) == 0 {
		t.Fatal("infra/tofu/storage has no .tf files")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		all.Write(b)
	}
	src := all.String()
	for _, b := range []string{"tofu-state", "recovery", "bootstrap-seed", "pg-backups", "longhorn-backups", "etcd-snapshots", "forgejo-objects", "repo-backups", "evidence", "telemetry"} {
		if !regexp.MustCompile(`"` + regexp.QuoteMeta(b) + `"`).MatchString(src) {
			t.Errorf("the storage module does not name the bucket purpose %q", b)
		}
	}
	if n := len(regexp.MustCompile(`(?m)^resource\s+"aws_s3_bucket"\s`).FindAllString(src, -1)); n != 1 {
		t.Errorf("%d aws_s3_bucket resources, want exactly one (a for_each over the bucket list)", n)
	}
	if i := regexp.MustCompile(`(?m)^resource\s+"aws_s3_bucket"\s+"[^"]+"\s*\{`).FindStringIndex(src); i != nil {
		block := src[i[0]:]
		if j := strings.Index(block[1:], "\nresource "); j >= 0 {
			block = block[:j+1]
		}
		if !strings.Contains(block, "for_each") {
			t.Error("the aws_s3_bucket resource does not use for_each")
		}
	}
	if !regexp.MustCompile(`resource\s+"aws_s3_bucket_versioning"`).MatchString(src) || !strings.Contains(src, `"Enabled"`) {
		t.Error("versioning is not enabled by the storage module (14-R-1)")
	}
}

// 06-AC-9 / plan 03 section 14.4: the storage guard is a TofuPlanGuard for the storage module with no delete.
func TestStorageGuard_Content_06AC9(t *testing.T) {
	var g struct {
		Kind string `yaml:"kind"`
		Spec struct {
			RootModule       string   `yaml:"rootModule"`
			AllowedProviders []string `yaml:"allowedProviders"`
			Resources        []struct {
				Type           string   `yaml:"type"`
				Paid           bool     `yaml:"paid"`
				AllowedActions []string `yaml:"allowedActions"`
				MaxCount       any      `yaml:"maxCount"`
			} `yaml:"resources"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(readStorage(t, "guard.yaml")), &g); err != nil {
		t.Fatal(err)
	}
	if g.Kind != "TofuPlanGuard" || g.Spec.RootModule != "infra/tofu/storage" {
		t.Errorf("kind %q rootModule %q", g.Kind, g.Spec.RootModule)
	}
	found := map[string]bool{}
	for _, r := range g.Spec.Resources {
		found[r.Type] = true
		if r.Paid {
			t.Errorf("%s is marked paid", r.Type)
		}
		for _, a := range r.AllowedActions {
			if a == "delete" {
				t.Errorf("%s allows delete", r.Type)
			}
		}
	}
	for _, rt := range []string{"aws_s3_bucket", "aws_s3_bucket_versioning", "aws_s3_bucket_lifecycle_configuration", "aws_s3_bucket_public_access_block"} {
		if !found[rt] {
			t.Errorf("the storage guard lacks %s", rt)
		}
	}
	hasAWS := false
	for _, p := range g.Spec.AllowedProviders {
		hasAWS = hasAWS || p == "registry.opentofu.org/hashicorp/aws"
	}
	if !hasAWS {
		t.Errorf("allowedProviders = %v, want registry.opentofu.org/hashicorp/aws", g.Spec.AllowedProviders)
	}
}

func mkTargets(t *testing.T) map[string]string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(repoRoot(t), "mk", "*.mk"))
	out := map[string]string{}
	re := regexp.MustCompile(`(?m)^([a-z][a-z0-9-]*):`)
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			out[m[1]] = filepath.Base(f)
		}
	}
	return out
}

// 06-AC-9 and 06-AC-1: both make targets exist; tofu-validate is a tier-V check (offline), test-integration is not.
func TestMakeTargets_TofuValidateAndIntegration_06AC9(t *testing.T) {
	targets := mkTargets(t)
	for _, name := range []string{"tofu-validate", "test-integration"} {
		if _, ok := targets[name]; !ok {
			t.Errorf("no mk/*.mk defines the %s target", name)
		}
	}
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "mk", "verify.mk"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^VERIFY_TARGETS\s*:=\s*(.*)$`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("VERIFY_TARGETS not found in mk/verify.mk")
	}
	list := " " + m[1] + " "
	if !strings.Contains(list, " tofu-validate ") {
		t.Error("tofu-validate is not in VERIFY_TARGETS: make verify would not run the static checks")
	}
	if strings.Contains(list, " test-integration ") {
		t.Error("test-integration is in VERIFY_TARGETS: it starts a subprocess emulator and the real tofu, which is tier E (plan 52 D2)")
	}
}

// 06-AC-1: test-integration runs the build-tagged tier-E tests and nothing else.
func TestMakeTargets_IntegrationRunsTaggedTests_06AC1(t *testing.T) {
	file := mkTargets(t)["test-integration"]
	if file == "" {
		t.Fatal("no mk/*.mk defines test-integration")
	}
	b, _ := os.ReadFile(filepath.Join(repoRoot(t), "mk", file))
	recipe := regexp.MustCompile(`(?ms)^test-integration:[^\n]*\n((?:\t[^\n]*\n|\n)*)`).FindStringSubmatch(string(b))
	if recipe == nil || !strings.Contains(recipe[1], "-tags integration") && !strings.Contains(recipe[1], "-tags=integration") {
		t.Errorf("the test-integration recipe does not run go test with -tags integration:\n%v", recipe)
	}
}
