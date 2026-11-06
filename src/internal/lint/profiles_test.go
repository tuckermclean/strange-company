package lint_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/lint"
)

// Rule IDs of `sc-lint profiles` and the catalogue checks of `sc-lint locks`. The plans name no IDs; chosen here.
const (
	ruleProfilesMatch = "profiles-match"
	rulePriceFresh    = "price-catalog-fresh"
	rulePriceSchema   = "price-catalog-schema"
)

const specTmpl = "# Title\n\n# 11. Self-hosting minimum substrate\n\nCanonical logical manifest:\n\n```yaml\n%s```\n\n---\n\n# 12. Small-HA profile\n\nCanonical resilient target:\n\n```yaml\n%s```\n"

const (
	minProfile = "apiVersion: strange.company/v1alpha1\nkind: BootstrapSubstrate\nmetadata:\n  name: self-hosting-minimum\nspec:\n  nodes:\n    count: 1\n"
	haProfile  = "apiVersion: strange.company/v1alpha1\nkind: ClusterProfile\nmetadata:\n  name: small-ha\nspec:\n  nodes:\n    count: 3\n"
)

func profilesTree(t *testing.T, minFile, haFile string) string {
	t.Helper()
	d := t.TempDir()
	write(t, d, "SPEC.md", strings.Replace(strings.Replace(specTmpl, "%s", minProfile, 1), "%s", haProfile, 1))
	write(t, d, "infra/profiles/self-hosting-minimum.yaml", minFile)
	write(t, d, "infra/profiles/small-ha.yaml", haFile)
	return d
}

func mustProfiles(t *testing.T, dir string) lint.Result {
	t.Helper()
	r, err := lint.ProfilesCheck(dir)
	if err != nil {
		t.Fatalf("ProfilesCheck: %v", err)
	}
	return r
}

// 03-AC-2: profile files equal to the SPEC blocks pass; the comparison is on parsed YAML (comments and key order are free).
func TestProfilesCheck_EqualToSpecPasses_03AC2(t *testing.T) {
	reordered := "# a comment\nspec:\n  nodes: {count: 1}\nmetadata: {name: self-hosting-minimum}\nkind: BootstrapSubstrate\napiVersion: strange.company/v1alpha1\n"
	for name, minFile := range map[string]string{"verbatim": minProfile, "reordered": reordered} {
		t.Run(name, func(t *testing.T) {
			r := mustProfiles(t, profilesTree(t, minFile, haProfile))
			ranRules(t, r, ruleProfilesMatch)
			if f := errorsOf(r); len(f) != 0 {
				t.Errorf("equal profiles produced findings: %v", f)
			}
		})
	}
}

// 03-AC-2 negative: a profile that differs from the SPEC block, or is missing, fails with a finding naming the file.
func TestProfilesCheck_DiffersFromSpecFails_03AC2(t *testing.T) {
	cases := map[string]struct{ min, ha, path string }{
		"min-count-differs": {strings.Replace(minProfile, "count: 1", "count: 2", 1), haProfile, "infra/profiles/self-hosting-minimum.yaml"},
		"ha-count-differs":  {minProfile, strings.Replace(haProfile, "count: 3", "count: 5", 1), "infra/profiles/small-ha.yaml"},
		"ha-extra-key":      {minProfile, haProfile + "  extra: true\n", "infra/profiles/small-ha.yaml"},
		"min-empty":         {"", haProfile, "infra/profiles/self-hosting-minimum.yaml"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := mustProfiles(t, profilesTree(t, c.min, c.ha))
			ranRules(t, r, ruleProfilesMatch)
			found := false
			for _, f := range errorsOf(r) {
				found = found || (f.Rule == ruleProfilesMatch && f.Path == c.path)
			}
			if !found {
				t.Errorf("want a %s error for %s, got %v", ruleProfilesMatch, c.path, r.Findings)
			}
		})
	}
	t.Run("missing-file", func(t *testing.T) {
		d := profilesTree(t, minProfile, haProfile)
		if err := os.Remove(filepath.Join(d, "infra/profiles/small-ha.yaml")); err != nil {
			t.Fatal(err)
		}
		r := mustProfiles(t, d)
		if !hasRule(r, ruleProfilesMatch) {
			t.Errorf("missing small-ha.yaml not reported: %v", r.Findings)
		}
	})
}

// 03-AC-2: the make target `profiles-check` runs `sc-lint profiles`; exit 0 on equal, 1 on a difference.
func TestProfilesMain_ExitCodes_03AC2(t *testing.T) {
	var out, errb bytes.Buffer
	if code := lint.Main([]string{"profiles", "--root", profilesTree(t, minProfile, haProfile)}, &out, &errb); code != 0 {
		t.Errorf("equal profiles: exit %d, want 0\n%s%s", code, out.String(), errb.String())
	}
	out.Reset()
	errb.Reset()
	bad := strings.Replace(minProfile, "count: 1", "count: 2", 1)
	if code := lint.Main([]string{"profiles", "--root", profilesTree(t, bad, haProfile)}, &out, &errb); code != 1 {
		t.Errorf("differing profile: exit %d, want 1\n%s%s", code, out.String(), errb.String())
	}
}

// 03-AC-2: the committed profiles equal the committed SPEC.md blocks.
func TestRealTree_ProfilesMatchSpec_03AC2(t *testing.T) {
	r := mustProfiles(t, repoRoot(t))
	ranRules(t, r, ruleProfilesMatch)
	if f := errorsOf(r); len(f) != 0 {
		t.Errorf("real tree: %v", f)
	}
}

func catalogFixture(verifiedAt, monthlyPrice string) string {
	return `apiVersion: strange.company/v1alpha1
kind: InfraPriceCatalog
metadata:
  name: ovh
  version: 2026.10.31
spec:
  provider: ovh
  currency: EUR
  entries:
    - productId: ovh-vps-6c-12g-100nvme
      productLine: vps
      resourceType: ovh_vps
      planCode: vps-2025-model2
      displayName: VPS-2
      specs: {cpu: 6, memory: 12Gi, storage: {size: 100GB, class: nvme}}
      regions: [GRA]
      monthlyPrice: ` + monthlyPrice + `
      setupFee: '0'
      commitment: P1M
      taxIncluded: false
      effectiveFrom: '2026-10-01'
      verifiedAt: '` + verifiedAt + `'
      sourceUrl: https://eu.api.ovh.com/v1/order/catalog/public/vps?ovhSubsidiary=FR
`
}

func priceFinding(r lint.Result, rule string) bool {
	for _, f := range errorsOf(r) {
		if f.Rule == rule && f.Path == "infra/catalog/prices/ovh.yaml" {
			return true
		}
	}
	return false
}

func locksAt(t *testing.T, catalog string, now time.Time) lint.Result {
	t.Helper()
	d := locksTree(t)
	write(t, d, "infra/catalog/prices/ovh.yaml", catalog)
	r, err := lint.LocksCheckAt(d, now)
	if err != nil {
		t.Fatalf("LocksCheckAt: %v", err)
	}
	return r
}

// 04-AC-1: verifiedAt 90 days old passes, 91 days old fails (injected clock, never the wall clock).
func TestLocksCheck_PriceCatalogFreshness_04AC1(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name, verifiedAt string
		fail             bool
	}{
		{"today", "2026-10-31", false},
		{"90-days", "2026-08-02", false},
		{"91-days", "2026-08-01", true},
		{"181-days", "2026-05-03", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := locksAt(t, catalogFixture(c.verifiedAt, "'6.49'"), now)
			ranRules(t, r, rulePriceFresh, rulePriceSchema)
			if got := priceFinding(r, rulePriceFresh); got != c.fail {
				t.Errorf("verifiedAt %s: freshness error=%v, want %v (%v)", c.verifiedAt, got, c.fail, r.Findings)
			}
			if priceFinding(r, rulePriceSchema) {
				t.Errorf("valid catalogue reported schema errors: %v", r.Findings)
			}
		})
	}
}

// 04-AC-1: a catalogue that violates InfraPriceCatalog (money as a float) fails the schema rule.
func TestLocksCheck_PriceCatalogSchema_04AC1(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	r := locksAt(t, catalogFixture("2026-10-31", "6.49"), now)
	ranRules(t, r, rulePriceSchema)
	if !priceFinding(r, rulePriceSchema) {
		t.Errorf("float monthlyPrice not rejected: %v", r.Findings)
	}
}

// 04-AC-1: the committed catalogue holds the four plan 04 section 9.2 entries and is fresh at the injected date.
func TestRealTree_OVHCatalogue_04AC1(t *testing.T) {
	root := repoRoot(t)
	body := readFile(t, root, "infra/catalog/prices/ovh.yaml")
	for _, id := range []string{"ovh-vps-6c-12g-100nvme", "ovh-vps-4c-8g-75ssd", "ovh-vps-8c-24g-200nvme", "ovh-pci-4c-16g-100nvme"} {
		if n := strings.Count(body, "productId: "+id); n != 1 {
			t.Errorf("productId %s appears %d times, want 1", id, n)
		}
	}
	r, err := lint.LocksCheckAt(root, time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	ranRules(t, r, rulePriceFresh, rulePriceSchema)
	if priceFinding(r, rulePriceFresh) || priceFinding(r, rulePriceSchema) {
		t.Errorf("committed catalogue: %v", r.Findings)
	}
}
