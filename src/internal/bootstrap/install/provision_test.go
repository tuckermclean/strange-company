package install_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/bootstrap/install"
	"strange.company/sc/internal/provider"
	"strange.company/sc/internal/provider/fake"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

// S6b of F08 (plan 50 sections 14.3 to 14.6, 03 section 14.7 and 22): plan.compute-plan, plan.confirm, provision
// through the provider interface, the node inventory commit, ownership of the provision keys, unattended
// confirmations and the stubbed post-provision phases.
//
// Decisions the plans leave open (recorded in the handoff):
//   - the fake compute module infra/tofu/compute/fake (CD-70(d)) is tier E (06-AC-3/-8, S7/S8); provision in this layer
//     goes through the OpenTofu-free provider interface, so no Tofu seam is added here;
//   - the fake node's discovery reads <.bootstrap/fake-provider>/scenario.yaml (discovery.resolvers), plan 03 section 22
//     "scenarios from fixtures";
//   - the run log carries the prompt counter as one record {"event":"prompts","fields":{"count":N}} (50-AC-31).

const (
	pvNodeInventorySubject = "bootstrap(" + clusterName + "): node inventory"
	pvInstallSubject       = "bootstrap(" + clusterName + "): installation config"
	pvClusterVarsPath      = "gitops/clusters/" + clusterName + "/vars/cluster-vars.yaml"
	pvInventoryPath        = "gitops/clusters/" + clusterName + "/inventory.yaml"
)

// pvStubbedPhases are the post-provision phases that are stubs in S6b, in the order of plan 50 sections 14.5 and 14.6.
var pvStubbedPhases = []string{"seed", "forge-init", "flux-bootstrap", "sso-init", "ci-init", "verify", "control-transfer"}

// --- journal view with phase outputs

type pvRef struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

type pvPhase struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Attempt    int     `json:"attempt"`
	OutputRefs []pvRef `json:"output_refs"`
	Steps      []jStep `json:"steps"`
}

type pvJournal struct {
	InstallUID   string `json:"install_uid"`
	Installation struct {
		State string `json:"state"`
	} `json:"installation"`
	Pipelines []struct {
		Pipeline string    `json:"pipeline"`
		Phases   []pvPhase `json:"phases"`
	} `json:"pipelines"`
	Confirmations []jConfirm `json:"confirmations"`
}

func pvRead(t testing.TB, i *inst) pvJournal {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(i.repo, ".bootstrap", "journal.json"))
	if err != nil {
		t.Fatalf("no local journal: %v", err)
	}
	var j pvJournal
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatalf("journal does not parse: %v", err)
	}
	return j
}

func (j pvJournal) phases() []pvPhase {
	for _, p := range j.Pipelines {
		if p.Pipeline == "install" {
			return p.Phases
		}
	}
	return nil
}

func (j pvJournal) phase(name string) (pvPhase, bool) {
	for _, p := range j.phases() {
		if p.Name == name {
			return p, true
		}
	}
	return pvPhase{}, false
}

func (j pvJournal) step(name string) (jStep, bool) {
	for _, p := range j.phases() {
		for _, s := range p.Steps {
			if s.Name == name {
				return s, true
			}
		}
	}
	return jStep{}, false
}

func (j pvJournal) confirmation(kinds ...string) (jConfirm, bool) {
	for _, c := range j.Confirmations {
		for _, k := range kinds {
			if c.Kind == k {
				return c, true
			}
		}
	}
	return jConfirm{}, false
}

// pvOrdered reports whether the names occur in that relative order in the list (others may lie between).
func pvOrdered(list []string, names ...string) bool {
	at := -1
	for _, n := range names {
		found := -1
		for k := at + 1; k < len(list); k++ {
			if list[k] == n {
				found = k
				break
			}
		}
		if found < 0 {
			return false
		}
		at = found
	}
	return true
}

// --- plan digest, inventory, commits, cluster-vars

// pvPlanDigest is the digest of the fake provider's plan for the fixture installation: the subject of the plan
// confirmation (plan 03 section 14.7: the full digest; D8 is its first 8 hex characters).
func pvPlanDigest(t testing.TB) (full, d8 string) {
	t.Helper()
	p, err := fake.New(map[string]string{"SC_TEST_MODE": "1"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := p.Plan(context.Background(), provider.Desired{Cluster: clusterName, NodeCount: 1})
	if err != nil || plan == nil || !strings.HasPrefix(plan.Digest, "sha256:") || len(plan.Digest) < len("sha256:")+8 {
		t.Fatalf("fake provider plan = %+v, %v", plan, err)
	}
	return plan.Digest, strings.TrimPrefix(plan.Digest, "sha256:")[:8]
}

// pvNodeIP is the internal IP of node-01 as the fake provider's inventory reports it after the run.
func pvNodeIP(t testing.TB, i *inst) string {
	t.Helper()
	p, err := fake.New(map[string]string{"SC_TEST_MODE": "1"}, i.fakeProviderDir())
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Inventory(context.Background())
	if err != nil || out == nil || len(out.Nodes) != 1 || out.Nodes[0].Name != "node-01" {
		t.Fatalf("fake provider inventory = %+v, %v; want node-01", out, err)
	}
	return out.Nodes[0].InternalIP
}

func pvCommits(i *inst, subject string) []string {
	var out []string
	for _, l := range strings.Split(i.git("log", "--format=%H%x1f%s"), "\n") {
		if f := strings.SplitN(l, "\x1f", 2); len(f) == 2 && f[1] == subject {
			out = append(out, f[0])
		}
	}
	return out
}

func pvClusterVars(t testing.TB, i *inst, rev string) map[string]string {
	t.Helper()
	var doc struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(i.git("show", rev+":"+pvClusterVarsPath)), &doc); err != nil || len(doc.Data) == 0 {
		t.Fatalf("%s at %s does not parse as a ConfigMap with data: %v", pvClusterVarsPath, rev, err)
	}
	return doc.Data
}

// pvScenario writes the fake provider's discovery scenario (the resolvers the fake node reports).
func pvScenario(t testing.TB, i *inst, resolvers ...string) {
	t.Helper()
	doc := map[string]any{
		"apiVersion": "strange.company/v1alpha1", "kind": "FakeProviderScenario",
		"discovery": map[string]any{"resolvers": resolvers},
	}
	b, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(i.fakeProviderDir(), "scenario.yaml"), string(b), 0o600)
}

// --- unattended runs

// unattendedEnv is the runtime-only environment of plan 50 section 9.2: every confirmation and the passphrase file.
// The provider-backstop attestation (PF-29) is by file, as the fixture policy enables a provider that needs it.
func (i *inst) unattendedEnv(extra ...map[string]string) map[string]string {
	i.t.Helper()
	_, d8 := pvPlanDigest(i.t)
	env := merge(i.passphraseFile(), map[string]string{
		"SC_CONFIRM_STORAGE": clusterName, "SC_CONFIRM_PLAN": d8, "SC_CONFIRM_BUDGET": budgetMonthly,
		"SC_CONFIRM_BACKSTOPS": i.backstopFile(),
	})
	return merge(append([]map[string]string{env}, extra...)...)
}

func (i *inst) envOnlyArgs(a ...string) []string {
	return append([]string{"--env-file", i.envFile}, a...)
}

// pvLogRecords reads the structured run logs under .bootstrap/runs, oldest run first.
func pvLogRecords(t testing.TB, i *inst) []map[string]any {
	t.Helper()
	dirs, _ := filepath.Glob(filepath.Join(i.repo, ".bootstrap", "runs", "*"))
	var out []map[string]any
	for _, d := range dirs {
		b, err := os.ReadFile(filepath.Join(d, "log.jsonl"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var rec map[string]any
			if json.Unmarshal([]byte(line), &rec) != nil {
				t.Fatalf("run log line is not JSON: %s", line)
			}
			out = append(out, rec)
		}
	}
	return out
}

// pvPromptCount reads the prompt counter of the last run that wrote one.
func pvPromptCount(t testing.TB, i *inst) (int, bool) {
	t.Helper()
	n, found := 0, false
	for _, rec := range pvLogRecords(t, i) {
		if rec["event"] != "prompts" {
			continue
		}
		f, _ := rec["fields"].(map[string]any)
		c, ok := f["count"].(float64)
		if !ok {
			t.Fatalf("prompts record has no numeric fields.count: %v", rec)
		}
		n, found = int(c), true
	}
	return n, found
}

// 50-AC-31 (BOOT-1): SC_CONFIRM_STORAGE, SC_CONFIRM_PLAN, SC_CONFIRM_BUDGET and SC_RECOVERY_PASSPHRASE_FILE, no TTY:
// the stubbed pipeline runs to its end with exit 0 and the prompt counter in the run log is 0. The fake provider
// created through the provider interface exactly once (50-AC-4 groundwork).
func TestUnattended_AllConfirmationsByEnvironment_ZeroPrompts_50AC31(t *testing.T) {
	i := newInst(t)
	code, out := i.run("", i.unattendedEnv(), i.envOnlyArgs())
	requireExit(t, code, 0, out)

	n, found := pvPromptCount(t, i)
	if !found {
		t.Fatal(`the run log has no {"event":"prompts"} record: the prompt counter is missing (50-AC-31)`)
	}
	if n != 0 {
		t.Errorf("prompt counter = %d, want 0", n)
	}

	j := pvRead(t, i)
	var names []string
	for _, p := range j.phases() {
		names = append(names, p.Name)
	}
	want := append([]string{"preflight", "plan", "provision"}, pvStubbedPhases...)
	if !pvOrdered(names, want...) {
		t.Errorf("journal phases = %v, want %v in that order", names, want)
	}
	for _, w := range want {
		if p, ok := j.phase(w); !ok || p.Status != "succeeded" {
			t.Errorf("phase %s = %+v (found %v), want succeeded", w, p.Status, ok)
		}
	}
	if j.Installation.State != "transferred" {
		t.Errorf("installation.state = %q at the end of the stubbed pipeline, want transferred (plan 50 section 14.6)", j.Installation.State)
	}
	if got := i.createCount(); got != 1 {
		t.Errorf("fake provider create counter = %d, want 1", got)
	}
	for _, k := range []string{"storage-footprint", "model-budget"} {
		if cf, ok := j.confirmation(k); !ok || cf.Method != "flag" {
			t.Errorf("confirmation %s = %+v (found %v), want method flag", k, cf, ok)
		}
	}
	full, _ := pvPlanDigest(t)
	cf, ok := j.confirmation("paid-footprint", "existing-nodes")
	if !ok || cf.Method != "flag" || cf.SubjectDigest != full {
		t.Errorf("plan confirmation = %+v (found %v), want method flag and subject_digest %s", cf, ok, full)
	}
	for _, c := range j.Confirmations {
		if c.Method != "flag" {
			t.Errorf("confirmation %s has method %q in an unattended run", c.Kind, c.Method)
		}
	}
	// The state account is configured, so verify has no unprotected-state warning to report (50-AC-32).
	if pvVerifyMentions(t, i, "OBJSTORE_STATE_UNPROTECTED") {
		t.Errorf("verify reports OBJSTORE_STATE_UNPROTECTED on an install whose state account is protected")
	}
}

// 50-AC-31: the counter counts prompts. An interactive run that is asked for the passphrase twice and for the typed
// storage confirmation reports 3.
func TestPromptCounter_CountsInteractiveAnswers_50AC31(t *testing.T) {
	i := newInst(t)
	stdin := twice(goodPass) + "storage " + clusterName + "\n"
	code, out := i.run(stdin, stopAfter("plan.storage-apply"), i.args())
	requireExit(t, code, 0, out)
	n, found := pvPromptCount(t, i)
	if !found || n != 3 {
		t.Errorf("prompt counter = %d (found %v), want 3 (passphrase twice, typed storage confirmation)", n, found)
	}
}

// Plan 50 section 14.3 steps 10 and 11, plan 03 section 14.7 and 03-AC-6: without the plan confirmation a
// non-interactive run exits 5 with the create counter at 0; a wrong D8 also exits 5; the right D8 (flag) creates once.
func TestPlanConfirm_NonInteractiveNeedsTheRightD8_50AC31(t *testing.T) {
	i := newInst(t)
	_, d8 := pvPlanDigest(t)
	full, _ := pvPlanDigest(t)
	envNoPlan := i.unattendedEnv()
	delete(envNoPlan, "SC_CONFIRM_PLAN")
	envNoPlan = merge(envNoPlan, stopAfter("provision"))

	code, out := i.run("", envNoPlan, i.envOnlyArgs("--non-interactive"))
	requireExit(t, code, 5, out)
	if got := i.createCount(); got != 0 {
		t.Errorf("create counter = %d after the missing plan confirmation, want 0", got)
	}
	if !strings.Contains(out, d8) {
		t.Errorf("the plan footprint with its digest (D8 %s) was not printed before the refusal:\n%s", d8, out)
	}
	j := pvRead(t, i)
	if s, ok := j.step("plan.compute-plan"); !ok || s.Status != "succeeded" {
		t.Errorf("step plan.compute-plan = %+v (found %v), want succeeded: the plan is computed before it is confirmed", s.Status, ok)
	}
	if s, ok := j.step("plan.confirm"); !ok || s.Status != "failed" {
		t.Errorf("step plan.confirm = %+v (found %v), want failed", s.Status, ok)
	}
	if p, ok := j.phase("provision"); !ok || p.Status != "pending" {
		t.Errorf("phase provision = %+v (found %v), want pending: provision must not start unconfirmed", p.Status, ok)
	}
	if _, ok := j.confirmation("paid-footprint", "existing-nodes"); ok {
		t.Errorf("the journal records a plan confirmation that was never given")
	}

	code, out = i.run("", merge(envNoPlan, map[string]string{"SC_CONFIRM_PLAN": "deadbeef"}), i.envOnlyArgs("--non-interactive"))
	requireExit(t, code, 5, out)
	if got := i.createCount(); got != 0 {
		t.Errorf("create counter = %d after a wrong D8, want 0", got)
	}

	code, out = i.run("", envNoPlan, i.envOnlyArgs("--non-interactive", "--confirm-plan", d8))
	requireExit(t, code, 0, out)
	if got := i.createCount(); got != 1 {
		t.Errorf("create counter = %d after the right D8, want 1", got)
	}
	cf, ok := pvRead(t, i).confirmation("paid-footprint", "existing-nodes")
	if !ok || cf.Method != "flag" || cf.SubjectDigest != full {
		t.Errorf("plan confirmation = %+v (found %v), want method flag and the full plan digest %s", cf, ok, full)
	}
}

// Plan 50 section 14.3 step order and section 14.4: compute-plan and confirm follow bundle-confirm; provision applies
// through the fake provider, then commits inventory.yaml and the SC_NODE_IP_* keys as `node inventory`.
func TestProvision_CommitsNodeInventory_50AC29(t *testing.T) {
	i := newInst(t)
	code, out := i.run("", merge(i.unattendedEnv(), stopAfter("provision")), i.envOnlyArgs("--non-interactive"))
	requireExit(t, code, 0, out)

	j := pvRead(t, i)
	if j.Installation.State != "provisioned" {
		t.Errorf("installation.state = %q after provision, want provisioned", j.Installation.State)
	}
	if got := i.createCount(); got != 1 {
		t.Errorf("fake provider create counter = %d, want 1", got)
	}
	var steps []string
	if p, ok := j.phase("plan"); ok {
		for _, s := range p.Steps {
			steps = append(steps, s.Name)
		}
	}
	if !pvOrdered(steps, "plan.bundle-confirm", "plan.compute-plan", "plan.confirm") {
		t.Errorf("plan phase steps = %v, want plan.bundle-confirm, plan.compute-plan, plan.confirm in that order", steps)
	}
	for _, n := range []string{"plan.compute-plan", "plan.confirm"} {
		if s, ok := j.step(n); !ok || s.Status != "succeeded" {
			t.Errorf("step %s = %+v (found %v), want succeeded", n, s.Status, ok)
		}
	}
	for _, n := range pvStubbedPhases {
		if p, ok := j.phase(n); ok && p.Status != "pending" {
			t.Errorf("phase %s is %s after SC_TEST_STOP_AFTER=provision, want pending", n, p.Status)
		}
	}

	commits := pvCommits(i, pvNodeInventorySubject)
	if len(commits) != 1 {
		t.Fatalf("commits %q = %v, want exactly one", pvNodeInventorySubject, commits)
	}
	sha := commits[0]
	if got := i.git("log", "-1", "--format=%(trailers:key=SC-Install-Uid,valueonly)", sha); strings.TrimSpace(got) != j.InstallUID {
		t.Errorf("node inventory commit trailer SC-Install-Uid = %q, want %q", strings.TrimSpace(got), j.InstallUID)
	}
	ph, ok := j.phase("provision")
	if !ok || ph.Status != "succeeded" {
		t.Fatalf("phase provision = %+v (found %v), want succeeded", ph.Status, ok)
	}
	refd := false
	for _, r := range ph.OutputRefs {
		if r.Kind == "git-commit" && r.Ref == sha {
			refd = true
		}
	}
	if !refd {
		t.Errorf("provision output_refs = %+v, want the git-commit %s", ph.OutputRefs, sha)
	}

	files := strings.Fields(i.git("show", "--name-only", "--format=", sha))
	allowed := map[string]bool{pvInventoryPath: true, pvClusterVarsPath: true, "gitops/clusters/" + clusterName + "/config/cluster.yaml": true}
	have := map[string]bool{}
	for _, f := range files {
		have[f] = true
		if !allowed[f] {
			t.Errorf("node inventory commit touches %s; plan 01 section 6.5 allows inventory.yaml, config/cluster.yaml and vars/cluster-vars.yaml", f)
		}
	}
	for _, f := range []string{pvInventoryPath, pvClusterVarsPath} {
		if !have[f] {
			t.Errorf("node inventory commit lacks %s: %v", f, files)
		}
	}

	ip := pvNodeIP(t, i)
	inv := i.git("show", "HEAD:"+pvInventoryPath)
	if !strings.Contains(inv, "node-01") || !strings.Contains(inv, ip) || strings.Contains(inv, "node-02") {
		t.Errorf("inventory.yaml does not list exactly node-01 with %s:\n%s", ip, inv)
	}
	vars := pvClusterVars(t, i, "HEAD")
	for n := 1; n <= 5; n++ {
		k := "SC_NODE_IP_" + string(rune('0'+n))
		if vars[k] != ip {
			t.Errorf("%s = %q, want %q (slot 1 is node-01's internal_ip; unused slots repeat it)", k, vars[k], ip)
		}
	}
	// Without a scenario the fake node still reports a usable resolver: every slot holds an IP address.
	for _, k := range []string{"SC_DNS_UPSTREAM_1", "SC_DNS_UPSTREAM_2", "SC_DNS_UPSTREAM_3"} {
		if _, err := netip.ParseAddr(vars[k]); err != nil {
			t.Errorf("%s = %q is not an IP address", k, vars[k])
		}
	}
	if c := pvCommits(i, pvInstallSubject); len(c) != 1 {
		t.Errorf("installation config commits = %v, want exactly one", c)
	}
	for _, l := range strings.Split(i.git("status", "--porcelain", "-uall"), "\n") {
		if l != "" && !strings.Contains(l, "provider-backstops.yaml") {
			t.Errorf("provision left the work tree dirty: %s (everything it writes under gitops/clusters is committed)", l)
		}
	}
}

// 50-AC-35 (PF-28 / plan 18 section 9.2): after provision on a fake node whose discovery reports the resolvers
// 192.0.2.53 and 192.0.2.54, cluster-vars has SC_DNS_UPSTREAM_1=192.0.2.53, _2=192.0.2.54, _3=192.0.2.53. The install
// commit was seeded with the toolbox resolver; provision rewrites the keys from discovery.
func TestProvision_DiscoveredResolversRewriteDNSUpstream_50AC35(t *testing.T) {
	for _, c := range []struct {
		name      string
		resolvers []string
		want      [3]string
	}{
		{"two", []string{"192.0.2.53", "192.0.2.54"}, [3]string{"192.0.2.53", "192.0.2.54", "192.0.2.53"}},
		{"one", []string{"192.0.2.53"}, [3]string{"192.0.2.53", "192.0.2.53", "192.0.2.53"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			i := newInst(t)
			pvScenario(t, i, c.resolvers...)
			code, out := i.run("", merge(i.unattendedEnv(), stopAfter("provision")), i.envOnlyArgs("--non-interactive"))
			requireExit(t, code, 0, out)

			vars := pvClusterVars(t, i, "HEAD")
			got := [3]string{vars["SC_DNS_UPSTREAM_1"], vars["SC_DNS_UPSTREAM_2"], vars["SC_DNS_UPSTREAM_3"]}
			if got != c.want {
				t.Errorf("SC_DNS_UPSTREAM_1..3 = %v, want %v", got, c.want)
			}
			if c.name == "two" {
				install := pvCommits(i, pvInstallSubject)
				if len(install) != 1 {
					t.Fatalf("installation config commits = %v", install)
				}
				seed := pvClusterVars(t, i, install[0])
				if seed["SC_DNS_UPSTREAM_1"] == "192.0.2.53" {
					t.Errorf("the installation config commit already holds the discovered resolver; install-identity is seeded with the toolbox resolver and provision rewrites it")
				}
				if _, has := seed["SC_NODE_IP_1"]; has {
					t.Errorf("the installation config commit holds SC_NODE_IP_1; provision owns SC_NODE_IP_* (BOOT-9)")
				}
			}
		})
	}
}

// 50-AC-29 (BOOT-9): after provision a rerun creates no new commit and cluster-vars SC_NODE_IP_1 (and the discovered
// resolvers) are unchanged. install-identity owns its files and never re-renders over provision's keys; neither the
// plain rerun nor `--from-phase plan` (which re-checks install-identity against reality) touches them.
func TestOwnership_RerunAfterProvisionCreatesNoCommit_50AC29(t *testing.T) {
	i := newInst(t)
	pvScenario(t, i, "192.0.2.53", "192.0.2.54")
	env := merge(i.unattendedEnv(), stopAfter("provision"))
	args := i.envOnlyArgs("--non-interactive")
	code, out := i.run("", env, args)
	requireExit(t, code, 0, out)

	if i.git("status", "--porcelain", "-uall") != "" {
		// The backstop record belongs to the operator's tree (as in review2_test B2).
		i.git("add", "-A")
		i.git("-c", "user.name=fixture", "-c", "user.email=fixture@example.net", "commit", "-q", "-m", "operator: provider backstops")
	}
	head := i.git("rev-parse", "HEAD")
	count := i.git("rev-list", "--count", "HEAD")
	before, err := os.ReadFile(filepath.Join(i.repo, filepath.FromSlash(pvClusterVarsPath)))
	if err != nil {
		t.Fatal(err)
	}
	ip := pvNodeIP(t, i)
	if v := pvClusterVars(t, i, "HEAD"); v["SC_NODE_IP_1"] != ip {
		t.Fatalf("SC_NODE_IP_1 = %q after provision, want %q", v["SC_NODE_IP_1"], ip)
	}
	confirmations := len(pvRead(t, i).Confirmations)

	for _, rerun := range []struct {
		name string
		args []string
	}{
		{"plain-rerun", args},
		{"from-phase-plan", i.envOnlyArgs("--non-interactive", "--from-phase", "plan")},
	} {
		var puts *pvPutLog
		code, out, puts = i.runRecording(env, rerun.args)
		if code != 0 {
			t.Fatalf("%s: exit %d, want 0:\n%s", rerun.name, code, out)
		}
		pvJournalPutsConditional(t, rerun.name, puts)
		if got := i.git("rev-parse", "HEAD"); got != head {
			t.Errorf("%s: HEAD moved from %s to %s: a rerun after provision creates no commit", rerun.name, head, got)
		}
		if got := i.git("rev-list", "--count", "HEAD"); got != count {
			t.Errorf("%s: commit count %s, want %s", rerun.name, got, count)
		}
		if st := i.git("status", "--porcelain", "-uall"); st != "" {
			t.Errorf("%s: the rerun dirtied the work tree:\n%s", rerun.name, st)
		}
		after, _ := os.ReadFile(filepath.Join(i.repo, filepath.FromSlash(pvClusterVarsPath)))
		if !bytes.Equal(before, after) {
			t.Errorf("%s: cluster-vars changed on rerun:\n--- before\n%s--- after\n%s", rerun.name, before, after)
		}
		v := pvClusterVars(t, i, "HEAD")
		if v["SC_NODE_IP_1"] != ip || v["SC_DNS_UPSTREAM_2"] != "192.0.2.54" {
			t.Errorf("%s: SC_NODE_IP_1=%q (want %q) SC_DNS_UPSTREAM_2=%q (want 192.0.2.54)", rerun.name, v["SC_NODE_IP_1"], ip, v["SC_DNS_UPSTREAM_2"])
		}
		if got := i.createCount(); got != 1 {
			t.Errorf("%s: create counter = %d, want 1: provision is not repeated", rerun.name, got)
		}
		if got := len(pvRead(t, i).Confirmations); got != confirmations {
			t.Errorf("%s: confirmations %d -> %d: the same inputs are not confirmed again", rerun.name, confirmations, got)
		}
		if c := pvCommits(i, pvNodeInventorySubject); len(c) != 1 {
			t.Errorf("%s: node inventory commits = %v, want exactly one", rerun.name, c)
		}
	}
}

// 50-AC-10 (provision leg, S6a review N5): three wrong re-entered passphrases exit 8; provision never started. The
// pipeline lists the real provision phase, still pending with no attempt, the plan was neither computed nor
// confirmed, the fake provider created nothing and no inventory commit exists.
func TestBundleConfirmFailure_ProvisionNeverStarts_50AC10(t *testing.T) {
	i := newInst(t)
	_, d8 := pvPlanDigest(t)
	stdin := twice(goodPass) + "wrong one entry\nwrong two entry\nwrong three entry\n"
	code, out := i.run(stdin, map[string]string{"SC_CONFIRM_PLAN": d8}, i.args("--confirm-storage", clusterName))
	requireExit(t, code, 8, out)

	if n := i.createCount(); n != 0 {
		t.Errorf("fake provider create counter = %d, want 0", n)
	}
	j := pvRead(t, i)
	p, ok := j.phase("provision")
	if !ok {
		t.Fatalf("the journal does not list the provision phase; the pipeline must include it for 'never started' to mean anything: %v", j.phases())
	}
	if p.Status != "pending" || p.Attempt != 0 || len(p.OutputRefs) != 0 {
		t.Errorf("phase provision = status %s attempt %d outputs %v, want pending, 0 attempts, no outputs", p.Status, p.Attempt, p.OutputRefs)
	}
	for _, s := range p.Steps {
		if s.Status != "pending" {
			t.Errorf("provision step %s is %s", s.Name, s.Status)
		}
	}
	for _, n := range []string{"plan.compute-plan", "plan.confirm"} {
		if s, ok := j.step(n); ok && s.Status != "pending" {
			t.Errorf("step %s is %s after the failed bundle confirmation, want pending (it runs only after plan.bundle-confirm)", n, s.Status)
		}
	}
	if _, ok := j.confirmation("paid-footprint", "existing-nodes"); ok {
		t.Errorf("a plan confirmation was recorded before the bundle was confirmed")
	}
	if c := pvCommits(i, pvNodeInventorySubject); len(c) != 0 {
		t.Errorf("node inventory commits = %v, want none", c)
	}
	if j.Installation.State != "new" {
		t.Errorf("installation.state = %q, want new", j.Installation.State)
	}
}

// pvVerifyMentions reports whether the verify phase reported needle: a run log record of phase verify, or the report.
func pvVerifyMentions(t testing.TB, i *inst, needle string) bool {
	t.Helper()
	for _, rec := range pvLogRecords(t, i) {
		if rec["phase"] != "verify" {
			continue
		}
		if b, _ := json.Marshal(rec); strings.Contains(string(b), needle) {
			return true
		}
	}
	reports, _ := filepath.Glob(filepath.Join(i.repo, ".bootstrap", "runs", "*", "report.txt"))
	for _, r := range reports {
		if b, err := os.ReadFile(r); err == nil && strings.Contains(string(b), needle) {
			return true
		}
	}
	return false
}

// 50-AC-32 (verify leg): with S3_ALLOW_UNPROTECTED_STATE=true the stubbed verify phase reports the warning (the
// protected case is checked in the unattended test above).
func TestVerify_ReportsUnprotectedStateWarning_50AC32(t *testing.T) {
	i := newInst(t)
	i.dropStateAccount()
	i.setVars(map[string]string{"S3_ALLOW_UNPROTECTED_STATE": "true"})
	code, out := i.run("", i.unattendedEnv(), i.envOnlyArgs("--non-interactive"))
	requireExit(t, code, 0, out)
	if _, ok := pvRead(t, i).confirmation("accept-state-unprotected"); !ok {
		t.Errorf("the journal does not record accept-state-unprotected")
	}
	if !pvVerifyMentions(t, i, "OBJSTORE_STATE_UNPROTECTED") {
		t.Errorf("verify did not report OBJSTORE_STATE_UNPROTECTED (log record of phase verify or report.txt)")
	}
}

// --- N2 of S6a review 1 (plan 50 section 17.3): the engine's S3 journal PUT is conditional once the conformance run
// recorded If-Match support. The reruns of the ownership test record every S3 write.

type pvPut struct {
	Bucket, Key, IfMatch string
}

type pvPutLog struct {
	mu   sync.Mutex
	puts []pvPut
}

func (l *pvPutLog) add(p pvPut) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.puts = append(l.puts, p)
}

type pvRecAccount struct {
	install.Account
	log *pvPutLog
}

func (a pvRecAccount) Bucket(name string) tofustate.Store {
	return pvRecStore{Store: a.Account.Bucket(name), name: name, log: a.log}
}

type pvRecStore struct {
	tofustate.Store
	name string
	log  *pvPutLog
}

func (s pvRecStore) Put(ctx context.Context, key string, data []byte, ifMatch string) (string, error) {
	s.log.add(pvPut{s.name, key, ifMatch})
	return s.Store.Put(ctx, key, data, ifMatch)
}

func (s pvRecStore) PutObject(ctx context.Context, key string, data []byte, o s3client.PutOptions) (s3client.PutResult, error) {
	s.log.add(pvPut{s.name, key, o.IfMatch})
	return s.Store.PutObject(ctx, key, data, o)
}

// runRecording is run with every S3 write recorded.
func (i *inst) runRecording(extraEnv map[string]string, args []string) (int, string, *pvPutLog) {
	i.t.Helper()
	l := &pvPutLog{}
	d := i.deps("")
	d.NewS3 = func(cfg s3client.Config) (install.Account, error) {
		a, err := i.world.open(cfg)
		if err != nil {
			return nil, err
		}
		return pvRecAccount{Account: a, log: l}, nil
	}
	env := map[string]string{}
	for k, v := range i.env {
		env[k] = v
	}
	for k, v := range extraEnv {
		env[k] = v
	}
	var out, errb bytes.Buffer
	code := install.Run(args, env, &out, &errb, d)
	return code, out.String() + errb.String(), l
}

// pvJournalPutsConditional (N2): in a run that starts after plan.state-conformance recorded If-Match support, every
// PUT of the S3 journal carries the previous ETag.
func pvJournalPutsConditional(t *testing.T, name string, l *pvPutLog) {
	t.Helper()
	key := clusterName + "/journal/journal.json"
	n := 0
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range l.puts {
		if p.Key != key {
			continue
		}
		n++
		if p.IfMatch == "" {
			t.Errorf("%s: a journal PUT in %s carries no If-Match although the conformance run recorded support (N2, plan 50 section 17.3)", name, p.Bucket)
			return
		}
	}
	if n == 0 {
		t.Errorf("%s: the rerun wrote no journal to S3; the test cannot judge the PUTs", name)
	}
}
