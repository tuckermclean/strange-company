package guard_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"strange.company/sc/internal/provider/guard"
)

const fixtureDir = "../../../../test/fixtures/tofu-plans/generic-paid"

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

func load(t *testing.T, guardFile string) guard.Guard {
	t.Helper()
	g, err := guard.ParseGuard(read(t, guardFile))
	if err != nil {
		t.Fatalf("parse %s: %v", guardFile, err)
	}
	return g
}

func expect(destroy bool) guard.Expect {
	return guard.Expect{
		ExpectedNodeCount: 2,
		ProductCode:       "plan-small",
		Region:            guard.Attr{Type: "test_server", Path: "region", Want: "REG-1"},
		SSHKey:            guard.Attr{Type: "test_server", Path: "ssh_key_name", Want: "sc-prod-key"},
		Destroy:           destroy,
	}
}

// 03-AC-3: every fixture class of plan 03 section 22 yields its specific code; valid create passes.
func TestGuardEvaluate_FixtureClassesYieldTheirCode_03AC3(t *testing.T) {
	cases := []struct {
		fixture string
		guard   string
		destroy bool
		code    string // empty means pass
	}{
		{"valid-create.json", "guard.yaml", false, ""},
		{"destroy-valid.json", "guard-destroy.yaml", true, ""},
		{"format-unsupported.json", "guard.yaml", false, "GUARD_PLAN_FORMAT_UNSUPPORTED"},
		// Plan 03 names no code for a foreign provider (rule 2); GUARD_PROVIDER_FORBIDDEN is the chosen one.
		{"foreign-provider-config.json", "guard.yaml", false, "GUARD_PROVIDER_FORBIDDEN"},
		{"foreign-provider-resource.json", "guard.yaml", false, "GUARD_PROVIDER_FORBIDDEN"},
		{"extra-resource-type.json", "guard.yaml", false, "GUARD_UNEXPECTED_RESOURCE"},
		{"delete-normal-mode.json", "guard.yaml", false, "GUARD_ACTION_FORBIDDEN"},
		{"replace.json", "guard.yaml", false, "GUARD_REPLACE_FORBIDDEN"},
		{"replace-create-delete.json", "guard.yaml", false, "GUARD_REPLACE_FORBIDDEN"},
		{"extra-count.json", "guard.yaml", false, "GUARD_COUNT_EXCEEDED"},
		{"product-mismatch.json", "guard.yaml", false, "GUARD_PRODUCT_MISMATCH"},
		{"product-unknown.json", "guard.yaml", false, "GUARD_PRODUCT_MISMATCH"},
		{"region-mismatch.json", "guard.yaml", false, "GUARD_REGION_MISMATCH"},
		{"ssh-key-mismatch.json", "guard.yaml", false, "GUARD_SSH_KEY_MISMATCH"},
		{"destroy-stray-create.json", "guard-destroy.yaml", true, "GUARD_ACTION_FORBIDDEN"},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			v := load(t, c.guard).Evaluate(read(t, c.fixture), expect(c.destroy))
			if c.code == "" {
				if !v.Pass || v.Code != "" {
					t.Fatalf("want pass, got %+v", v)
				}
				return
			}
			if v.Pass || v.Code != c.code {
				t.Fatalf("want %s, got %+v", c.code, v)
			}
			if v.Address == "" {
				t.Fatalf("the verdict must name the offending change: %+v", v)
			}
		})
	}
}

// 03-AC-3: a normal-mode guard refuses the destroy plan, and the destroy guard refuses a normal create plan.
func TestGuardEvaluate_ModesAreNotInterchangeable_03AC3(t *testing.T) {
	if v := load(t, "guard.yaml").Evaluate(read(t, "destroy-valid.json"), expect(false)); v.Pass || v.Code != "GUARD_ACTION_FORBIDDEN" {
		t.Fatalf("normal guard on destroy plan: %+v", v)
	}
	if v := load(t, "guard-destroy.yaml").Evaluate(read(t, "valid-create.json"), expect(true)); v.Pass {
		t.Fatalf("destroy guard accepted a create plan: %+v", v)
	}
}

func digest(t *testing.T, g guard.Guard, plan []byte) string {
	t.Helper()
	d, err := g.Digest(plan)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	return d
}

// 03-AC-4: identical across 100 runs; a lowercase hex sha256.
func TestGuardDigest_StableAcross100Runs_03AC4(t *testing.T) {
	g := load(t, "guard.yaml")
	plan := read(t, "valid-create.json")
	first := digest(t, g, plan)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(first) {
		t.Fatalf("digest %q is not a sha256 hex string", first)
	}
	for i := 0; i < 100; i++ {
		if got := digest(t, g, plan); got != first {
			t.Fatalf("run %d: %s != %s", i, got, first)
		}
	}
}

// 03-AC-4: mutating plan_code changes the digest.
func TestGuardDigest_PlanCodeChangesIt_03AC4(t *testing.T) {
	g := load(t, "guard.yaml")
	plan := read(t, "valid-create.json")
	mutated := bytes.Replace(plan, []byte("plan-small"), []byte("plan-large"), 1)
	if bytes.Equal(plan, mutated) {
		t.Fatal("fixture has no plan_code to mutate")
	}
	if digest(t, g, plan) == digest(t, g, mutated) {
		t.Fatal("changing plan_code left the digest unchanged")
	}
	if digest(t, g, plan) == digest(t, g, read(t, "extra-count.json")) {
		t.Fatal("different plans share a digest")
	}
}

// 03-AC-4: re-rendering the same plan (key order, whitespace, volatile top-level fields) keeps the digest.
func TestGuardDigest_StableAcrossReRender_03AC4(t *testing.T) {
	g := load(t, "guard.yaml")
	plan := read(t, "valid-create.json")
	want := digest(t, g, plan)
	if want == "" {
		t.Fatal("empty digest")
	}

	var doc map[string]any
	if err := json.Unmarshal(plan, &doc); err != nil {
		t.Fatal(err)
	}
	compact, _ := json.Marshal(doc) // Go sorts map keys: a different key order from the file
	if got := digest(t, g, compact); got != want {
		t.Fatalf("compact re-render: %s != %s", got, want)
	}
	doc["terraform_version"] = "1.13.2"
	doc["timestamp"] = "2026-10-31T00:00:00Z"
	indented, _ := json.MarshalIndent(doc, "", "\t")
	if got := digest(t, g, indented); got != want {
		t.Fatalf("re-render with other version and timestamp: %s != %s", got, want)
	}
}

// 03-AC-4: sensitive values are masked, non-sensitive paid attributes count, unpaid attributes do not.
func TestGuardDigest_MasksSensitiveAndCoversPaidAttributes_03AC4(t *testing.T) {
	g := load(t, "guard.yaml")
	plan := read(t, "valid-create.json")
	want := digest(t, g, plan)
	if want == "" {
		t.Fatal("empty digest")
	}

	sensitive := bytes.ReplaceAll(plan, []byte("example-not-a-secret"), []byte("another-value"))
	if bytes.Equal(plan, sensitive) {
		t.Fatal("fixture has no sensitive value")
	}
	if got := digest(t, g, sensitive); got != want {
		t.Fatal("a sensitive value reached the digest")
	}
	paid := bytes.Replace(plan, []byte("sc-prod-node-01"), []byte("sc-prod-node-07"), 1)
	if digest(t, g, paid) == want {
		t.Fatal("a paid resource attribute change left the digest unchanged")
	}
	unpaid := bytes.Replace(plan, []byte("ExampleOnlyNotARealKey"), []byte("OtherOnlyNotARealKeyx"), 1)
	if bytes.Equal(plan, unpaid) {
		t.Fatal("fixture has no unpaid attribute to change")
	}
	if got := digest(t, g, unpaid); got != want {
		t.Fatal("an unpaid resource attribute reached the digest")
	}
}

// 03-AC-4: D8 is the first 8 hex characters.
func TestGuardD8_FirstEightHex_03AC4(t *testing.T) {
	d := digest(t, load(t, "guard.yaml"), read(t, "valid-create.json"))
	if len(d) < 8 {
		t.Fatalf("digest %q is too short", d)
	}
	if got := guard.D8(d); len(got) != 8 || got != d[:8] {
		t.Fatalf("D8(%q) = %q", d, got)
	}
	if got := guard.D8("0123456789abcdef"); got != "01234567" {
		t.Fatalf("D8 = %q", got)
	}
}
