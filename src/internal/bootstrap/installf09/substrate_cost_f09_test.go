package installf09_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/contracts"
)

// F09 slice S5 (plan 03 sections 9.1, 9.2, 9.4, 14.3, 14.5, 14.6 and 15, plan 50 section 7.2): substrate validation of
// the fake provider's nodes, the profile kind rules of PF-03, the stale price catalogue, the lab refusal and the cost
// check, through the install pipeline.
//
// Seams the tests fix (the implementer follows them):
//   - profiles are read from <repo>/infra/profiles/<profile>.yaml (the scratch repository has none; s5Profiles copies the
//     real profiles and the lab fixture test/fixtures/fake-provider/profiles/lab-small.yaml into it);
//   - the fake provider's scenario (test/fixtures/fake-provider/*.yaml) gains the knobs `capacity` (cpu, memoryBytes,
//     diskBytes, diskClass: the node hardware the fake reports), `paid` (Capabilities().Paid) and `machineClass` (the
//     catalogue productId a paid fake orders);
//   - the price catalogue of a paid provider is <repo>/infra/catalog/prices/<provider name>.yaml, here fake.yaml;
//   - the age of the catalogue is measured against the injected clock (Deps.Clock), never the wall clock;
//   - the fake provider writes provider-output.json (ProviderOutput with substrate_validation and cost_estimate) like the
//     existing provider does.

var s5Digest = regexp.MustCompile(`Plan digest: sha256:([0-9a-f]{64})`)

func s5Commit(i *inst, msg string) {
	i.git("add", "-A")
	i.git("-c", "user.name=fixture", "-c", "user.email=fixture@example.net", "commit", "-q", "-m", msg)
}

func s5Fixture(t testing.TB, rel ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{repoRoot(t), "test", "fixtures", "fake-provider"}, rel...)...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// s5Profiles puts the real substrate and cluster profile and the lab substrate fixture into the scratch repository.
func s5Profiles(t testing.TB, i *inst) {
	t.Helper()
	for _, p := range []string{"self-hosting-minimum", "small-ha"} {
		b, err := os.ReadFile(filepath.Join(repoRoot(t), "infra", "profiles", p+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(i.repo, "infra", "profiles", p+".yaml"), string(b), 0o644)
	}
	write(t, filepath.Join(i.repo, "infra", "profiles", "lab-small.yaml"), string(s5Fixture(t, "profiles", "lab-small.yaml")), 0o644)
	s5Commit(i, "profiles")
}

// s5Catalogue writes the fake price catalogue with every entry verified ageDays before the injected clock's now.
func s5Catalogue(t testing.TB, i *inst, ageDays int) {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(s5Fixture(t, "prices", "fake.yaml"), &doc); err != nil {
		t.Fatal(err)
	}
	day := i.clk.Now().AddDate(0, 0, -ageDays).Format("2006-01-02")
	for _, e := range doc["spec"].(map[string]any)["entries"].([]any) {
		m := e.(map[string]any)
		m["verifiedAt"], m["effectiveFrom"] = day, day
	}
	b, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(i.repo, "infra", "catalog", "prices", "fake.yaml"), string(b), 0o644)
	s5Commit(i, "price catalogue")
}

// s5Provision runs the install pipeline unattended up to provision. Without a plan confirmation the run stops at
// plan.confirm with exit 5 and prints the plan digest; the helper then confirms that digest by flag and runs again. Any
// other exit of the first run (a refusal before the confirmation) is the result.
func s5Provision(t *testing.T, i *inst, extraEnv map[string]string, args ...string) (int, string) {
	t.Helper()
	env := i.unattendedEnv()
	delete(env, "SC_CONFIRM_PLAN")
	env = merge(env, stopAfter("provision"), extraEnv)
	base := append([]string{"--non-interactive"}, args...)
	code, out := i.run("", env, i.envOnlyArgs(base...))
	if code != 5 {
		return code, out
	}
	m := s5Digest.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("the run stopped at the confirmation without printing 'Plan digest: sha256:<64 hex>':\n%s", out)
	}
	return i.run("", env, i.envOnlyArgs(append(base, "--confirm-plan", m[1][:8])...))
}

// s5Output reads the one provider-output.json of the run and validates it against provider.schema.yaml.
func s5Output(t testing.TB, i *inst) map[string]any {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(i.repo, ".bootstrap", "runs", "*", "provider-output.json"))
	if len(files) != 1 {
		t.Fatalf("provider-output.json files under .bootstrap/runs = %v, want exactly one", files)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := contracts.CompileSchemas(filepath.Join(repoRoot(t), "contracts"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("provider-output.json is not JSON: %v", err)
	}
	if err := schemas["provider"].Validate(doc); err != nil {
		t.Errorf("provider-output.json does not validate against provider.schema.yaml: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func s5Str(m map[string]any, path ...string) string {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[p]
	}
	s, _ := cur.(string)
	return s
}

func s5Cost(t testing.TB, po map[string]any) (monthly, currency string, complete, within bool) {
	t.Helper()
	ce, ok := po["cost_estimate"].(map[string]any)
	if !ok {
		t.Fatalf("provider-output.json has no cost_estimate: %v", po)
	}
	complete, _ = ce["complete"].(bool)
	within, _ = ce["within_limit"].(bool)
	return s5Str(ce, "monthly_total"), s5Str(ce, "currency"), complete, within
}

// --- 03-AC-8 substrate tolerance

// 03-AC-8 (plan 03 section 14.3): a fake node whose memory is 0.919 x 8 Gi, or whose root disk is 0.919 x 100 Gi, is below
// the 0.92 tolerance of the constrained lower bound: substrate validation fails the provision phase with
// NODE_BELOW_MINIMUM, exit 3 (plan 03 section 15, plan 05 section 15).
func TestSubstrateTolerance_NodeBelowToleranceFailsProvision_03AC8(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"capacity-below-minimum", "capacity-small-disk"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			i := newInst(t)
			s5Profiles(t, i)
			i.useScenario(scenario)
			code, out := s5Provision(t, i, nil)
			requireExit(t, code, 3, out)
			if !strings.Contains(out, "NODE_BELOW_MINIMUM") {
				t.Errorf("03-AC-8: output lacks NODE_BELOW_MINIMUM:\n%s", out)
			}
			if p, ok := pvRead(t, i).phase("provision"); !ok || p.Status == "succeeded" {
				t.Errorf("03-AC-8: phase provision = %q (found %v), want it not to succeed with a node below the minimum", p.Status, ok)
			}
		})
	}
}

// 03-AC-8: a fake node with 0.921 x 8 Gi memory passes substrate validation: the run succeeds and provider-output.json
// reports meets_preferred false with a warn finding and no fail finding.
func TestSubstrateTolerance_NodeAboveToleranceWarns_03AC8(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	s5Profiles(t, i)
	i.useScenario("capacity-tolerance")
	code, out := s5Provision(t, i, nil)
	requireExit(t, code, 0, out)
	if strings.Contains(out, "NODE_BELOW_MINIMUM") {
		t.Errorf("03-AC-8: output names NODE_BELOW_MINIMUM for a node within the tolerance:\n%s", out)
	}
	po := s5Output(t, i)
	nodes, _ := po["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("03-AC-8: provider-output.json nodes = %d, want 1", len(nodes))
	}
	sv, _ := nodes[0].(map[string]any)["substrate_validation"].(map[string]any)
	if sv == nil {
		t.Fatalf("03-AC-8: node-01 has no substrate_validation: %v", nodes[0])
	}
	if mp, ok := sv["meets_preferred"].(bool); !ok || mp {
		t.Errorf("03-AC-8: meets_preferred = %v, want false", sv["meets_preferred"])
	}
	warn := false
	findings, _ := sv["findings"].([]any)
	for _, f := range findings {
		switch f.(map[string]any)["severity"] {
		case "warn":
			warn = true
		case "fail":
			t.Errorf("03-AC-8: a node within the tolerance has a fail finding: %v", f)
		}
	}
	if !warn {
		t.Errorf("03-AC-8: substrate_validation.findings has no warn finding: %v", findings)
	}
}

// --- 03-AC-9 profile kinds

// 03-AC-9 (plan 03 section 9.1, PF-03): a fresh install requires kind BootstrapSubstrate. `--profile small-ha` is a
// ClusterProfile: preflight and the install pipeline exit 3 PROFILE_KIND_MISMATCH before anything is created.
func TestProfileKind_FreshInstallRejectsClusterProfile_03AC9(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	s5Profiles(t, i)
	for _, args := range [][]string{{"preflight", "--profile", "small-ha"}, {"--profile", "small-ha"}} {
		env := merge(i.unattendedEnv(), stopAfter("provision"))
		code, out := i.run("", env, i.envOnlyArgs(append([]string{"--non-interactive"}, args...)...))
		requireExit(t, code, 3, out)
		for _, w := range []string{"PF-03", "PROFILE_KIND_MISMATCH"} {
			if !strings.Contains(out, w) {
				t.Errorf("03-AC-9: `%s` output lacks %s:\n%s", strings.Join(args, " "), w, out)
			}
		}
		if got := i.createCount(); got != 0 {
			t.Errorf("03-AC-9: create counter = %d after the refusal, want 0", got)
		}
	}
}

// 03-AC-9: `expand --profile self-hosting-minimum` requires kind ClusterProfile; the substrate is refused with exit 3
// PROFILE_KIND_MISMATCH, which PF-03 reports before the not-yet-built expand reports COMMAND_NOT_IMPLEMENTED (CD-70e).
func TestProfileKind_ExpandRejectsSubstrate_03AC9(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	s5Profiles(t, i)
	code, out := i.run("", i.unattendedEnv(), i.envOnlyArgs("expand", "--profile", "self-hosting-minimum"))
	requireExit(t, code, 3, out)
	for _, w := range []string{"PF-03", "PROFILE_KIND_MISMATCH"} {
		if !strings.Contains(out, w) {
			t.Errorf("03-AC-9: expand output lacks %s:\n%s", w, out)
		}
	}
	if strings.Contains(out, "COMMAND_NOT_IMPLEMENTED") {
		t.Errorf("03-AC-9: the profile kind was not checked before the command was refused as not implemented:\n%s", out)
	}
	if got := i.createCount(); got != 0 {
		t.Errorf("03-AC-9: create counter = %d after the refusal, want 0", got)
	}
}

// --- 03-AC-10 stale price catalogue

// 03-AC-10 (plan 03 section 14.5): a catalogue whose verifiedAt is 181 days before the injected now is older than 180
// days: exit 4 PRICE_CATALOG_STALE, nothing created, no acceptance recorded.
func TestPriceCatalog_StaleExits4_03AC10(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	s5Profiles(t, i)
	i.setVars(map[string]string{"MAX_MONTHLY_COST": "25"})
	i.useScenario("paid")
	s5Catalogue(t, i, 181)
	code, out := s5Provision(t, i, nil)
	requireExit(t, code, 4, out)
	if !strings.Contains(out, "PRICE_CATALOG_STALE") {
		t.Errorf("03-AC-10: output lacks PRICE_CATALOG_STALE:\n%s", out)
	}
	if got := i.createCount(); got != 0 {
		t.Errorf("03-AC-10: create counter = %d after the refusal, want 0", got)
	}
	if j, ok := i.journalIfAny(); ok {
		if _, has := j.confirmation("accept-stale-prices"); has {
			t.Errorf("03-AC-10: the journal records accept-stale-prices that was never given")
		}
	}
}

// 03-AC-10: with SC_ACCEPT_STALE_PRICES=1 the run proceeds, creates the node once, and the journal records the
// confirmation accept-stale-prices.
func TestPriceCatalog_StaleAcceptedByEnvironmentProceeds_03AC10(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	s5Profiles(t, i)
	i.setVars(map[string]string{"MAX_MONTHLY_COST": "25"})
	i.useScenario("paid")
	s5Catalogue(t, i, 181)
	code, out := s5Provision(t, i, map[string]string{"SC_ACCEPT_STALE_PRICES": "1"})
	requireExit(t, code, 0, out)
	if got := i.createCount(); got != 1 {
		t.Errorf("03-AC-10: create counter = %d after the accepted stale run, want 1", got)
	}
	cf, ok := pvRead(t, i).confirmation("accept-stale-prices")
	if !ok {
		t.Fatalf("03-AC-10: the journal has no accept-stale-prices confirmation: %+v", pvRead(t, i).Confirmations)
	}
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(cf.SubjectDigest) {
		t.Errorf("03-AC-10: accept-stale-prices subject_digest = %q, want sha256:<64 hex>", cf.SubjectDigest)
	}
}

// 03-AC-10: a fresh catalogue (10 days) needs no acceptance; the paid fake proceeds and provider-output.json carries the
// catalogue price (plan 03 section 14.6: 1 node x 9.99 EUR = 9.99 EUR, complete).
func TestPriceCatalog_FreshProceedsAndPricesTheNode_03AC10(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	s5Profiles(t, i)
	i.setVars(map[string]string{"MAX_MONTHLY_COST": "25"})
	i.useScenario("paid")
	s5Catalogue(t, i, 10)
	code, out := s5Provision(t, i, nil)
	requireExit(t, code, 0, out)
	if strings.Contains(out, "PRICE_CATALOG_STALE") {
		t.Errorf("03-AC-10: output names PRICE_CATALOG_STALE for a fresh catalogue:\n%s", out)
	}
	if _, has := pvRead(t, i).confirmation("accept-stale-prices"); has {
		t.Errorf("03-AC-10: the journal records accept-stale-prices for a fresh catalogue")
	}
	monthly, cur, complete, within := s5Cost(t, s5Output(t, i))
	if monthly != "9.99" || cur != "EUR" || !complete || !within {
		t.Errorf("03-AC-10: cost_estimate = %s %s complete=%v within_limit=%v, want 9.99 EUR complete within the limit", monthly, cur, complete, within)
	}
}

// --- 03-AC-11 lab substrate

// 03-AC-11 (plan 03 section 9.2): a lab-labeled substrate is refused when the provider is paid: exit 3
// LAB_SUBSTRATE_PAID_REFUSED, before anything is created.
func TestLabSubstrate_PaidProviderIsRefused_03AC11(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	s5Profiles(t, i)
	i.setVars(map[string]string{"MAX_MONTHLY_COST": "25"})
	i.useScenario("paid")
	s5Catalogue(t, i, 10)
	code, out := s5Provision(t, i, nil, "--profile", "lab-small")
	requireExit(t, code, 3, out)
	if !strings.Contains(out, "LAB_SUBSTRATE_PAID_REFUSED") {
		t.Errorf("03-AC-11: output lacks LAB_SUBSTRATE_PAID_REFUSED:\n%s", out)
	}
	if got := i.createCount(); got != 0 {
		t.Errorf("03-AC-11: create counter = %d after the refusal, want 0", got)
	}
}

// 03-AC-11: the refusal is about the paid provider, not about the lab substrate: the unpaid fake runs on lab-small.
func TestLabSubstrate_UnpaidFakeProceeds_03AC11(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	s5Profiles(t, i)
	code, out := s5Provision(t, i, nil, "--profile", "lab-small")
	requireExit(t, code, 0, out)
	if strings.Contains(out, "LAB_SUBSTRATE_PAID_REFUSED") {
		t.Errorf("03-AC-11: output refuses an unpaid provider on a lab substrate:\n%s", out)
	}
	if got := i.createCount(); got != 1 {
		t.Errorf("03-AC-11: create counter = %d, want 1", got)
	}
}

// --- cost check (plan 03 section 14.6; the cost half of 05-AC-5)

// Plan 03 section 14.6: a paid fake whose estimate (1 x 9.99 EUR) exceeds MAX_MONTHLY_COST exits 4 COST_OVER_LIMIT before
// anything is created.
func TestCost_OverLimitExits4_05AC5(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	s5Profiles(t, i)
	i.setVars(map[string]string{"MAX_MONTHLY_COST": "5"})
	i.useScenario("paid")
	s5Catalogue(t, i, 10)
	code, out := s5Provision(t, i, nil)
	requireExit(t, code, 4, out)
	if !strings.Contains(out, "COST_OVER_LIMIT") {
		t.Errorf("14.6: output lacks COST_OVER_LIMIT:\n%s", out)
	}
	if got := i.createCount(); got != 0 {
		t.Errorf("14.6: create counter = %d after the refusal, want 0", got)
	}
}

// 05-AC-5 (cost half), plan 03 section 14.6: for an unpaid provider the estimate is 0, so MAX_MONTHLY_COST=0 passes the
// cost check; provider-output.json has cost_estimate.monthly_total "0" and complete true.
func TestCost_UnpaidFakeWithZeroLimitPasses_05AC5(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	s5Profiles(t, i)
	if got := i.vars["MAX_MONTHLY_COST"]; got != "0" {
		t.Fatalf("MAX_MONTHLY_COST = %q, the test needs 0", got)
	}
	code, out := s5Provision(t, i, nil)
	requireExit(t, code, 0, out)
	monthly, _, complete, within := s5Cost(t, s5Output(t, i))
	if monthly != "0" || !complete || !within {
		t.Errorf("cost_estimate = monthly_total %q complete=%v within_limit=%v, want \"0\", complete, within the limit", monthly, complete, within)
	}
}
