package installf09b_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// s5bCatalogue writes the fresh (10 days) fake price catalogue after mutate changed its spec and its first entry.
func s5bCatalogue(t testing.TB, i *inst, mutate func(spec, entry map[string]any)) {
	t.Helper()
	s5Catalogue(t, i, 10)
	path := filepath.Join(i.repo, "infra", "catalog", "prices", "fake.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	spec := doc["spec"].(map[string]any)
	mutate(spec, spec["entries"].([]any)[0].(map[string]any))
	b, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(b), 0o644)
	s5Commit(i, "price catalogue variant")
}

// 03-AC-10 (plan 03 14.5 and 14.6): the cost check refuses what it cannot price, before anything is created: a
// catalogue in another currency than COST_CURRENCY is PRICE_CURRENCY_MISMATCH, an unknown product or a malformed
// verifiedAt is COST_INCOMPLETE (all exit 4).
func TestCost_UnpriceableCataloguesExit4_03AC10(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		vars   map[string]string
		mutate func(spec, entry map[string]any)
		code   string
	}{
		{"currency-mismatch", map[string]string{"COST_CURRENCY": "USD"}, func(_, e map[string]any) { e["displayName"] = "variant" }, "PRICE_CURRENCY_MISMATCH"},
		{"product-missing", nil, func(_, e map[string]any) { e["productId"] = "fake-vps-other" }, "COST_INCOMPLETE"},
		{"verifiedAt-malformed", nil, func(_, e map[string]any) { e["verifiedAt"] = "last tuesday" }, "COST_INCOMPLETE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			i := newInst(t)
			s5Profiles(t, i)
			i.setVars(map[string]string{"MAX_MONTHLY_COST": "25"})
			i.setVars(c.vars)
			i.useScenario("paid")
			s5bCatalogue(t, i, c.mutate)
			code, out := s5Provision(t, i, nil)
			requireExit(t, code, 4, out)
			if !strings.Contains(out, c.code) {
				t.Errorf("output lacks %s:\n%s", c.code, out)
			}
			if got := i.createCount(); got != 0 {
				t.Errorf("create counter = %d after the refusal, want 0", got)
			}
		})
	}
}

// 03-AC-11 (control): the unpaid fake passes PF-03 on a lab substrate at preflight.
func TestPreflight_UnpaidProviderAcceptsLabSubstrate_03AC11(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	s5Profiles(t, i)
	env := merge(i.unattendedEnv(), stopAfter("provision"))
	code, out := i.run("", env, i.envOnlyArgs("--non-interactive", "preflight", "--profile", "lab-small"))
	requireExit(t, code, 0, out)
	if strings.Contains(out, "LAB_SUBSTRATE_PAID_REFUSED") {
		t.Errorf("preflight refuses an unpaid provider on a lab substrate:\n%s", out)
	}
}
