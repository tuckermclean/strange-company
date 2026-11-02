package install_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"strange.company/sc/internal/contracts"
)

// F09 slice S7 (plan 05 sections 7 to 9 and 14.1, plan 03 sections 14.6 and 14.7): PROVIDER=existing through the install
// pipeline. The SSH transport is replaced under SC_TEST_MODE=1 by the test seam SC_EXISTING_DISCOVERY_DIR=<dir>, which
// holds <node>.facts.json (the output of the discovery script, test/fixtures/existing/discovery/README.md) and
// <node>.hostkeys (ssh-keyscan output) for every node of EXISTING_NODES; nothing is dialled.
//
// The effective profile is read from <repo>/infra/profiles/<profile>.yaml (the scratch repository has none, so the tests
// commit the real profile files into it).

const (
	exCase      = "debian13-baremetal-nvme" // the node of test/fixtures/existing/discovery that these tests install
	exNodeHost  = "203.0.113.10"
	exFootprint = "configure will take over these hosts (k3s, firewall, sysctls)"
)

var exPlanDigest = regexp.MustCompile(`Plan digest: (sha256:[0-9a-f]{64})`)

func exFixtureDir(t testing.TB) string {
	return filepath.Join(repoRoot(t), "test", "fixtures", "existing", "discovery", exCase)
}

// exFingerprints are the SHA256 host-key fingerprints the discovery fixture expects, sorted.
func exFingerprints(t testing.TB) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(exFixtureDir(t), "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var exp struct {
		Node struct {
			SSH struct {
				FPs []string `json:"host_key_fingerprints"`
			} `json:"ssh"`
		} `json:"node"`
	}
	if err := json.Unmarshal(b, &exp); err != nil || len(exp.Node.SSH.FPs) != 3 {
		t.Fatalf("expected.json fingerprints: %v (%v)", exp.Node.SSH.FPs, err)
	}
	sort.Strings(exp.Node.SSH.FPs)
	return exp.Node.SSH.FPs
}

// newExisting is a fake installation switched to PROVIDER=existing with one node whose discovery comes from the seam.
func newExisting(t testing.TB) (*inst, map[string]string) {
	t.Helper()
	i := newInst(t)
	for _, p := range []string{"self-hosting-minimum", "small-ha"} {
		b, err := os.ReadFile(filepath.Join(repoRoot(t), "infra", "profiles", p+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(i.repo, "infra", "profiles", p+".yaml"), string(b), 0o644)
	}
	i.git("add", "-A")
	i.git("-c", "user.name=fixture", "-c", "user.email=fixture@example.net", "commit", "-q", "-m", "profiles")

	disc := filepath.Join(i.dir, "discovery")
	facts, err := os.ReadFile(filepath.Join(exFixtureDir(t), "facts.json"))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := os.ReadFile(filepath.Join(exFixtureDir(t), "hostkeys.txt"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(disc, "node-01.facts.json"), string(facts), 0o644)
	write(t, filepath.Join(disc, "node-01.hostkeys"), string(keys), 0o644)

	i.setVars(map[string]string{"PROVIDER": "existing", "EXISTING_NODES": "node-01=debian@" + exNodeHost, "EXPECTED_NODE_COUNT": "1", "MAX_MONTHLY_COST": "0"})
	env := merge(i.passphraseFile(), map[string]string{
		"SC_CONFIRM_STORAGE": clusterName, "SC_CONFIRM_BUDGET": budgetMonthly, "SC_CONFIRM_BACKSTOPS": i.backstopFile(),
		"SC_EXISTING_DISCOVERY_DIR": disc, "SC_TEST_STOP_AFTER": "provision",
	})
	return i, env
}

// 05-AC-1: two EXISTING_NODES entries named node-01 fail at preflight with a configuration error (exit 2) that names
// the field and the node, before any node is contacted.
func TestExisting_PreflightRejectsDuplicateNodeName_05AC1(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	i.setVars(map[string]string{"PROVIDER": "existing", "EXPECTED_NODE_COUNT": "2",
		"EXISTING_NODES": "node-01=debian@192.0.2.10,node-01=debian@192.0.2.11"})
	code, out := i.run("", nil, i.args("preflight"))
	requireExit(t, code, 2, out)
	for _, w := range []string{"EXISTING_NODES", "node-01", "duplicate"} {
		if !strings.Contains(out, w) {
			t.Errorf("the preflight output does not name %q:\n%s", w, out)
		}
	}
}

// 05-AC-4: the footprint of an existing-nodes plan shows `0 paid resources`, the node list with its SSH endpoint, each
// host-key fingerprint and the takeover warning; --non-interactive without a confirmation exits 5 before provision.
func TestExisting_FootprintAndNonInteractiveExit5_05AC4(t *testing.T) {
	t.Parallel()
	i, env := newExisting(t)
	code, out := i.run("", env, i.envOnlyArgs("--non-interactive"))
	requireExit(t, code, 5, out)

	for _, w := range append([]string{"0 paid resources", exNodeHost, "node-01", exFootprint}, exFingerprints(t)...) {
		if !strings.Contains(out, w) {
			t.Errorf("the footprint does not show %q:\n%s", w, out)
		}
	}
	if !exPlanDigest.MatchString(out) {
		t.Errorf("the footprint does not print 'Plan digest: sha256:<64 hex>':\n%s", out)
	}
	j := pvRead(t, i)
	if s, ok := j.step("plan.compute-plan"); !ok || s.Status != "succeeded" {
		t.Errorf("step plan.compute-plan = %q (found %v), want succeeded", s.Status, ok)
	}
	if s, ok := j.step("plan.confirm"); !ok || s.Status != "failed" {
		t.Errorf("step plan.confirm = %q (found %v), want failed", s.Status, ok)
	}
	if p, ok := j.phase("provision"); !ok || p.Status != "pending" {
		t.Errorf("phase provision = %q (found %v), want pending: provision must not start unconfirmed", p.Status, ok)
	}
	if _, ok := j.confirmation("existing-nodes", "paid-footprint"); ok {
		t.Errorf("the journal records a plan confirmation that was never given")
	}
}

// 05-AC-4: the typed <cluster> token confirms the plan; the existing-nodes confirmation records the printed plan digest.
func TestExisting_TypedClusterTokenRecordsPlanDigest_05AC4(t *testing.T) {
	t.Parallel()
	i, env := newExisting(t)
	code, out := i.run(clusterName+"\n", env, i.envOnlyArgs())
	requireExit(t, code, 0, out)

	m := exPlanDigest.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no 'Plan digest: sha256:<64 hex>' in the output:\n%s", out)
	}
	if !strings.Contains(out, "0 paid resources") {
		t.Errorf("the footprint does not say '0 paid resources':\n%s", out)
	}
	j := pvRead(t, i)
	cf, ok := j.confirmation("existing-nodes")
	if !ok {
		t.Fatalf("the journal has no existing-nodes confirmation: %+v", j.Confirmations)
	}
	if cf.SubjectDigest != m[1] {
		t.Errorf("existing-nodes subject_digest = %q, want the printed plan digest %q", cf.SubjectDigest, m[1])
	}
	if cf.Method != "interactive" {
		t.Errorf("existing-nodes confirmation method = %q, want interactive", cf.Method)
	}
	if _, has := j.confirmation("paid-footprint"); has {
		t.Errorf("the journal records a paid-footprint confirmation for a provider that spends nothing")
	}
	if p, ok := j.phase("provision"); !ok || p.Status != "succeeded" {
		t.Errorf("phase provision = %q (found %v), want succeeded", p.Status, ok)
	}
}

// 05-AC-4: a wrong typed token three times exits 5 and records nothing.
func TestExisting_WrongTypedTokenExits5_05AC4(t *testing.T) {
	t.Parallel()
	i, env := newExisting(t)
	code, out := i.run("wrong\nwrong\nwrong\n", env, i.envOnlyArgs())
	requireExit(t, code, 5, out)
	if !strings.Contains(out, "0 paid resources") {
		t.Errorf("the footprint was not printed before the refusal:\n%s", out)
	}
	if j, ok := i.journalIfAny(); ok {
		if _, has := j.confirmation("existing-nodes"); has {
			t.Errorf("the journal records an existing-nodes confirmation that was never given")
		}
	}
}

// 05-AC-5: MAX_MONTHLY_COST=0 passes the cost check, and provider-output.json is a valid ProviderOutput with
// cost_estimate.monthly_total "0" and complete true.
func TestExisting_ProviderOutputCostsNothing_05AC5(t *testing.T) {
	t.Parallel()
	i, env := newExisting(t)
	if got := i.vars["MAX_MONTHLY_COST"]; got != "0" {
		t.Fatalf("MAX_MONTHLY_COST = %q, the test needs 0", got)
	}
	code, out := i.run(clusterName+"\n", env, i.envOnlyArgs())
	requireExit(t, code, 0, out)

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

	var po struct {
		Provider struct {
			Name         string `json:"name"`
			Capabilities struct {
				Paid          bool   `json:"paid"`
				Provisions    bool   `json:"provisions_compute"`
				HostKeySource string `json:"host_key_source"`
			} `json:"capabilities"`
		} `json:"provider"`
		Cost struct {
			MonthlyTotal string `json:"monthly_total"`
			MaxMonthly   string `json:"max_monthly_cost"`
			WithinLimit  bool   `json:"within_limit"`
			Complete     bool   `json:"complete"`
		} `json:"cost_estimate"`
		Nodes []struct {
			Name      string `json:"name"`
			Addresses struct {
				Internal string `json:"internal_ip"`
			} `json:"addresses"`
			SSH struct {
				Host string   `json:"host"`
				FPs  []string `json:"host_key_fingerprints"`
			} `json:"ssh"`
			Hardware struct {
				Disks []struct {
					Class string `json:"class"`
				} `json:"disks"`
			} `json:"hardware"`
			Validation struct {
				MeetsMinimum bool `json:"meets_minimum"`
			} `json:"substrate_validation"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &po); err != nil {
		t.Fatal(err)
	}
	if po.Cost.MonthlyTotal != "0" || !po.Cost.Complete || !po.Cost.WithinLimit || po.Cost.MaxMonthly != "0" {
		t.Errorf("cost_estimate = %+v, want monthly_total \"0\", max_monthly_cost \"0\", within_limit true, complete true", po.Cost)
	}
	if po.Provider.Name != "existing" || po.Provider.Capabilities.Paid || po.Provider.Capabilities.Provisions || po.Provider.Capabilities.HostKeySource != "tofu" {
		t.Errorf("provider = %+v, want existing, paid false, provisions_compute false, host_key_source tofu", po.Provider)
	}
	if len(po.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(po.Nodes))
	}
	n := po.Nodes[0]
	sort.Strings(n.SSH.FPs)
	if n.Name != "node-01" || n.SSH.Host != exNodeHost || n.Addresses.Internal != exNodeHost {
		t.Errorf("node = %+v, want node-01 at %s", n, exNodeHost)
	}
	if strings.Join(n.SSH.FPs, ",") != strings.Join(exFingerprints(t), ",") {
		t.Errorf("host_key_fingerprints = %v, want %v", n.SSH.FPs, exFingerprints(t))
	}
	if len(n.Hardware.Disks) != 1 || n.Hardware.Disks[0].Class != "nvme" || !n.Validation.MeetsMinimum {
		t.Errorf("hardware/validation = %+v / %+v, want an nvme root disk that meets the minimum", n.Hardware, n.Validation)
	}
}
