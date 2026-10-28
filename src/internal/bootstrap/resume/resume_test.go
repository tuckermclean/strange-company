package resume_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
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

// S8a of F08: the resume semantics of the fake install (plan 50 sections 7.1, 14.1, 14.3, 15, 16, 17.1 to 17.4 and 22),
// through install.Run over the same fake world as the install package tests (an in-memory S3 with two accounts, a fake
// OpenTofu, the fake provider, injected clock). The package is separate from install so that these end-to-end runs
// have a go test timeout budget of their own.
//
// Names the plans leave open and these tests fix (recorded in the handoff):
//   - the stubbed post-provision phases of test mode keep their state in <repo>/.bootstrap/stub-cluster/:
//     root-kustomization.yaml (spec.sourceRef.kind: Bucket after seed, GitRepository after flux-bootstrap) is what the
//     engine's freeze probe reads (plan 50 section 17.1 rule 5); scenario.yaml (failOnce: [<phase>]) makes a stub phase
//     fail its first call with STUB_FAILED_ONCE (exit 1), like the fake provider's scenario.yaml;
//   - the stub phases of a test-mode run are, after provision, seed-prep, configure, seed, forge-init, flux-bootstrap,
//     sso-init, ci-init, verify, control-transfer (plan 50 section 5 table order);
//   - abandon-identity reads the typed confirmation "abandon <cluster> <first 8 characters of install_uid>" from the
//     input; the exit code of ABANDON_REFUSED_COMPUTE_CONFIRMED is not fixed by the plans and is not asserted beyond
//     "not zero".

var runIDRe = regexp.MustCompile(`[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4}`)

func stopAfter(step string) map[string]string { return map[string]string{"SC_TEST_STOP_AFTER": step} }

func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func requireExit(t *testing.T, got, want int, out string) {
	t.Helper()
	if got != want {
		t.Fatalf("exit %d, want %d:\n%s", got, want, out)
	}
}

// planDigest is the digest of the fake provider's plan for the fixture installation (plan 03 section 14.7: D8 is its
// first 8 hex characters).
func planDigest(t testing.TB) (full, d8 string) {
	t.Helper()
	p, err := fake.New(map[string]string{"SC_TEST_MODE": "1"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := p.Plan(context.Background(), provider.Desired{Cluster: clusterName, NodeCount: 1})
	if err != nil || plan == nil || !strings.HasPrefix(plan.Digest, "sha256:") {
		t.Fatalf("fake provider plan = %+v, %v", plan, err)
	}
	return plan.Digest, strings.TrimPrefix(plan.Digest, "sha256:")[:8]
}

// unattendedEnv is the runtime-only environment of plan 50 section 9.2: every confirmation and the passphrase file.
func (i *inst) unattendedEnv(extra ...map[string]string) map[string]string {
	i.t.Helper()
	_, d8 := planDigest(i.t)
	env := merge(i.passphraseFile(), map[string]string{
		"SC_CONFIRM_STORAGE": clusterName, "SC_CONFIRM_PLAN": d8, "SC_CONFIRM_BUDGET": budgetMonthly,
		"SC_CONFIRM_BACKSTOPS": i.backstopFile(),
	})
	return merge(append([]map[string]string{env}, extra...)...)
}

func (i *inst) envOnlyArgs(a ...string) []string {
	return append([]string{"--env-file", i.envFile}, a...)
}

// runWith is run with a change to the injected collaborators.
func (i *inst) runWith(mod func(*install.Deps), stdin string, extraEnv map[string]string, args []string) (int, string) {
	i.t.Helper()
	env := merge(i.env, extraEnv)
	var out, errb bytes.Buffer
	d := i.deps(stdin)
	if mod != nil {
		mod(&d)
	}
	code := install.Run(args, env, &out, &errb, d)
	return code, out.String() + errb.String()
}

func (j journal) phase(name string) (jPhase, bool) {
	for _, p := range j.Pipelines {
		for _, ph := range p.Phases {
			if ph.Name == name {
				return ph, true
			}
		}
	}
	return jPhase{}, false
}

func (j journal) phaseStatus(name string) string {
	ph, ok := j.phase(name)
	if !ok {
		return "(absent)"
	}
	return ph.Status
}

func (j journal) stepStatus(name string) string {
	s, ok := j.step(name)
	if !ok {
		return "(absent)"
	}
	return s.Status
}

func (i *inst) lockObject() (raw []byte, ok bool) {
	return i.world.read(bucketName("tofu-state"), clusterName+"/locks/bootstrap.lock")
}

// --- the stub cluster of the test-mode phases

func (i *inst) stubDir() string { return filepath.Join(i.repo, ".bootstrap", "stub-cluster") }

func writeStubScenario(t testing.TB, i *inst, failOnce ...string) {
	t.Helper()
	b, err := yaml.Marshal(map[string]any{"apiVersion": "strange.company/v1alpha1", "kind": "StubClusterScenario", "failOnce": failOnce})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(i.stubDir(), "scenario.yaml"), string(b), 0o600)
}

// stubRootKind is spec.sourceRef.kind of the stub root Kustomization ("" when there is none).
func stubRootKind(t testing.TB, i *inst) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(i.stubDir(), "root-kustomization.yaml"))
	if err != nil {
		return ""
	}
	var doc struct {
		Spec struct {
			SourceRef struct {
				Kind string `yaml:"kind"`
			} `yaml:"sourceRef"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("root-kustomization.yaml does not parse: %v", err)
	}
	return doc.Spec.SourceRef.Kind
}

func sha256Of(b []byte) string { s := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(s[:]) }

// --- 50-AC-5: two concurrent runs

// gateTofu holds the run inside plan.state-migrate: reached closes when the run is there, release lets it go on.
type gateTofu struct {
	install.Tofu
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gateTofu) Migrate(ctx context.Context, s install.StorageSpec, r install.RemoteState) error {
	g.once.Do(func() { close(g.reached) })
	select {
	case <-g.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return g.Tofu.Migrate(ctx, s, r)
}

// 50-AC-5: two concurrent runs against the same fake install: exactly one exits 6, and the lock object content names
// the run that holds it (the other run's id, from the loser's side). The holder is parked inside plan.state-migrate,
// after the tofu-state bucket exists and the remote run lock object was written; the second run starts then. No timer:
// the test waits on the gate.
func TestConcurrentRuns_ExactlyOneExits6_LockObjectNamesTheHolder_50AC5(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	gate := &gateTofu{Tofu: i.tofu, reached: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate.release) }) }
	t.Cleanup(release)

	type result struct {
		code int
		out  string
	}
	done := make(chan result, 1)
	args := i.args("--confirm-storage", clusterName)
	go func() {
		code, out := i.runWith(func(d *install.Deps) { d.Tofu = gate }, twice(goodPass)+goodPass+"\n", stopAfter("plan.bundle-confirm"), args)
		done <- result{code, out}
	}()
	select {
	case <-gate.reached:
	case r := <-done:
		t.Fatalf("the first run ended with exit %d before it reached plan.state-migrate:\n%s", r.code, r.out)
	}

	raw, ok := i.lockObject()
	if !ok {
		release()
		<-done
		t.Fatalf("while the first run is inside plan.state-migrate there is no lock object at %s/locks/bootstrap.lock", clusterName)
	}
	var held struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(raw, &held); err != nil || !runIDRe.MatchString(held.RunID) {
		release()
		<-done
		t.Fatalf("the lock object does not name a run id: %v\n%s", err, raw)
	}

	codeB, outB := i.run("", stopAfter("plan.bundle-confirm"), args)
	if codeB != 6 {
		t.Errorf("the second run exited %d, want 6 (run lock held):\n%s", codeB, outB)
	}
	if !strings.Contains(outB, held.RunID) {
		t.Errorf("the second run's output does not name the holder %s:\n%s", held.RunID, outB)
	}
	if now, _ := i.lockObject(); !bytes.Equal(now, raw) {
		t.Errorf("the lock object changed while the second run was refused:\nbefore %s\nafter  %s", raw, now)
	}

	release()
	a := <-done
	if a.code != 0 {
		t.Fatalf("the first run exited %d, want 0 (exactly one of two runs exits 6):\n%s", a.code, a.out)
	}
	if _, ok := i.lockObject(); ok {
		t.Errorf("the lock object remains after the holder finished")
	}
	j := i.journal()
	found := false
	for _, r := range j.Runs {
		if r.RunID == held.RunID {
			found = true
			if r.ExitCode == nil || *r.ExitCode != 0 {
				t.Errorf("journal run %s exit_code = %v, want 0", r.RunID, r.ExitCode)
			}
		}
	}
	if !found || len(j.Runs) != 1 {
		t.Errorf("journal runs = %+v, want exactly the holder's run %s (the refused run never wrote a journal entry)", j.Runs, held.RunID)
	}
	if c := i.installCommits(); len(c) != 1 {
		t.Errorf("installation config commits = %v, want exactly one", c)
	}
}

// --- 50-AC-17: stale lock

type killed struct{}

// crashAccount makes the process "die" right after the run lock object was written to the tofu-state bucket (inside
// plan.storage-apply, where the engine's remembered lock is bound to the new bucket): a panic nobody recovers but the
// test, so no release, no journal write and no cleanup follow, as after SIGKILL.
type crashAccount struct{ install.Account }

func (a crashAccount) Bucket(name string) tofustate.Store {
	s := a.Account.Bucket(name)
	if name == bucketName("tofu-state") {
		return crashStore{Store: s}
	}
	return s
}

type crashStore struct{ tofustate.Store }

func (c crashStore) PutIfAbsent(ctx context.Context, key string, data []byte) (string, error) {
	etag, err := c.Store.PutIfAbsent(ctx, key, data)
	if err == nil && strings.HasSuffix(key, "locks/bootstrap.lock") {
		panic(killed{})
	}
	return etag, err
}

// runUntilKilled runs the command line and reports whether the process "died".
func (i *inst) runUntilKilled(stdin string, extraEnv map[string]string, args []string) (died bool) {
	i.t.Helper()
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(killed); !ok {
				panic(r)
			}
			died = true
		}
	}()
	i.runWith(func(d *install.Deps) {
		d.NewS3 = func(c s3client.Config) (install.Account, error) {
			a, err := i.world.open(c)
			if err != nil {
				return nil, err
			}
			return crashAccount{Account: a}, nil
		}
	}, stdin, extraEnv, args)
	return false
}

// 50-AC-17 (BOOT-5): after SIGKILL during plan.storage-apply, a rerun on the same host exits 0 and the journal shows
// stale-lock-takeover; a rerun with a different host in the lock object exits 6 and changes nothing. One crash serves
// both reruns: the refused rerun changes nothing, then the lock object gets its own host back.
func TestStaleLock_KilledInStorageApply_SameHostTakesOver_OtherHostExits6_50AC17(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	env := i.unattendedEnv()
	args := i.envOnlyArgs("--non-interactive")

	if !i.runUntilKilled("", env, args) {
		t.Fatal("the first run was not killed: it never wrote the run lock object to the tofu-state bucket")
	}
	// The kernel frees the dead process's file lock; the test's stand-in is a fresh lock file.
	if err := os.Remove(filepath.Join(i.repo, ".bootstrap", "run.lock")); err != nil {
		t.Fatal(err)
	}
	orig, ok := i.lockObject()
	if !ok {
		t.Fatal("a killed run leaves its lock object behind; there is none")
	}
	var holder struct {
		RunID string `json:"run_id"`
		Host  string `json:"host"`
	}
	if err := json.Unmarshal(orig, &holder); err != nil || holder.Host == "" || !runIDRe.MatchString(holder.RunID) {
		t.Fatalf("lock object %s: %v", orig, err)
	}
	if _, applies, _ := i.tofu.counts(); applies != 1 {
		t.Fatalf("storage applies = %d before the rerun, want 1", applies)
	}

	// Different host: exit 6, nothing changes.
	foreign := bytes.Replace(orig, []byte(`"host":"`+holder.Host+`"`), []byte(`"host":"some-other-host"`), 1)
	if bytes.Equal(foreign, orig) {
		t.Fatalf("fixture: cannot rewrite the host in %s", orig)
	}
	if err := i.world.write(stateKeyID, bucketName("tofu-state"), clusterName+"/locks/bootstrap.lock", foreign); err != nil {
		t.Fatal(err)
	}
	journalBefore, _ := os.ReadFile(filepath.Join(i.repo, ".bootstrap", "journal.json"))
	code, out := i.run("", env, args)
	if code != 6 {
		t.Fatalf("rerun with a lock object of another host: exit %d, want 6:\n%s", code, out)
	}
	if !strings.Contains(out, "RUN_LOCK_HELD") {
		t.Errorf("rerun with a foreign lock does not report RUN_LOCK_HELD:\n%s", out)
	}
	if now, _ := i.lockObject(); !bytes.Equal(now, foreign) {
		t.Errorf("the foreign lock object was modified by the refused rerun")
	}
	if journalAfter, _ := os.ReadFile(filepath.Join(i.repo, ".bootstrap", "journal.json")); !bytes.Equal(journalBefore, journalAfter) {
		t.Errorf("the refused rerun wrote the journal")
	}
	if _, applies, _ := i.tofu.counts(); applies != 1 {
		t.Errorf("storage applies = %d after the refused rerun, want 1", applies)
	}

	// Same host: automatic takeover.
	if err := i.world.write(stateKeyID, bucketName("tofu-state"), clusterName+"/locks/bootstrap.lock", orig); err != nil {
		t.Fatal(err)
	}
	code, out = i.run("", env, args)
	if code != 0 {
		t.Fatalf("same-host rerun: exit %d, want 0 (automatic takeover of the stale lock):\n%s", code, out)
	}
	j := i.journal()
	var takeovers []jConfirm
	for _, c := range j.Confirmations {
		if c.Kind == "stale-lock-takeover" {
			takeovers = append(takeovers, c)
		}
	}
	if len(takeovers) != 1 {
		t.Fatalf("confirmations %+v, want exactly one stale-lock-takeover", j.Confirmations)
	}
	if takeovers[0].SubjectDigest != sha256Of(orig) {
		t.Errorf("stale-lock-takeover subject_digest = %s, want the digest of the dead run's lock object %s", takeovers[0].SubjectDigest, sha256Of(orig))
	}
	if takeovers[0].RunID == holder.RunID || !runIDRe.MatchString(takeovers[0].RunID) {
		t.Errorf("stale-lock-takeover run_id = %q, want the id of the taking-over run (not the dead run %s)", takeovers[0].RunID, holder.RunID)
	}
	if _, ok := i.lockObject(); ok {
		t.Errorf("the lock object remains after the rerun finished")
	}
	if j.Installation.State != "transferred" {
		t.Errorf("installation.state = %q after the rerun, want transferred", j.Installation.State)
	}
	if _, applies, _ := i.tofu.counts(); applies != 1 {
		t.Errorf("storage applies = %d in total, want 1: the rerun converges without repeating the apply", applies)
	}
	if n := i.createCount(); n != 1 {
		t.Errorf("fake provider create counter = %d, want 1", n)
	}
	if c := i.installCommits(); len(c) != 1 {
		t.Errorf("installation config commits = %v, want exactly one", c)
	}
}

// --- 50-AC-16 and S6b R1: freeze

// 50-AC-16 (BOOT-2): fake install with stubbed post-transfer phases where verify fails once: the rerun reports seed,
// forge-init and flux-bootstrap skipped, the stub root sourceRef stays GitRepository, and --from-phase seed exits 2
// with PHASE_FROZEN. The rerun is also the "provision consumed the confirmation" rerun of S6b R1: plan.compute-plan is
// Done once the state is at least provisioned, so the plan is not recomputed and no phase reports drift because its
// plan digest is missing; provision hashes the recorded confirmation digest.
func TestFreeze_VerifyFailsOnce_RerunSkipsFrozenPhases_FromPhaseSeedRefused_50AC16(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	writeStubScenario(t, i, "verify")
	env := i.unattendedEnv()
	args := i.envOnlyArgs("--non-interactive")

	code, out := i.run("", env, args)
	requireExit(t, code, 1, out)
	if !strings.Contains(out, "STUB_FAILED_ONCE") {
		t.Fatalf("the first run does not report the stub verify failure STUB_FAILED_ONCE:\n%s", out)
	}
	j1 := i.journal()
	for _, p := range []string{"seed", "forge-init", "flux-bootstrap"} {
		if got := j1.phaseStatus(p); got != "succeeded" {
			t.Errorf("first run: phase %s = %s, want succeeded", p, got)
		}
	}
	if got := j1.phaseStatus("verify"); got != "failed" {
		t.Errorf("first run: phase verify = %s, want failed", got)
	}
	if j1.Installation.State != "flux-sourced" {
		t.Errorf("first run: installation.state = %q, want flux-sourced", j1.Installation.State)
	}
	if got := stubRootKind(t, i); got != "GitRepository" {
		t.Fatalf("first run: stub root sourceRef.kind = %q after flux-bootstrap, want GitRepository", got)
	}
	prov1, _ := j1.phase("provision")
	cp1, _ := j1.step("plan.compute-plan")

	code, out = i.run("", env, args)
	requireExit(t, code, 0, out)
	if strings.Contains(out, "drifted") {
		t.Errorf("the rerun reports drift (a phase hashed a plan digest that was not recorded):\n%s", out)
	}
	j2 := i.journal()
	for _, p := range []string{"seed", "forge-init", "flux-bootstrap"} {
		if got := j2.phaseStatus(p); got != "skipped" {
			t.Errorf("rerun: phase %s = %s, want skipped (frozen)", p, got)
		}
	}
	for _, p := range []string{"verify", "control-transfer"} {
		if got := j2.phaseStatus(p); got != "succeeded" {
			t.Errorf("rerun: phase %s = %s, want succeeded", p, got)
		}
	}
	if j2.Installation.State != "transferred" {
		t.Errorf("rerun: installation.state = %q, want transferred", j2.Installation.State)
	}
	if got := stubRootKind(t, i); got != "GitRepository" {
		t.Errorf("rerun: stub root sourceRef.kind = %q, want GitRepository (never back to the seed source)", got)
	}
	// S6b R1
	if got := j2.stepStatus("plan.compute-plan"); got != "skipped" {
		t.Errorf("rerun: step plan.compute-plan = %s, want skipped (its check is Done once the state is at least provisioned)", got)
	}
	if cp2, _ := j2.step("plan.compute-plan"); cp2.Attempt != cp1.Attempt {
		t.Errorf("rerun: plan.compute-plan attempt %d -> %d: the plan was computed again after provision", cp1.Attempt, cp2.Attempt)
	}
	for _, p := range []string{"plan", "provision"} {
		if got := j2.phaseStatus(p); got != "skipped" {
			t.Errorf("rerun: phase %s = %s, want skipped", p, got)
		}
	}
	if prov2, _ := j2.phase("provision"); prov1.InputDigest == nil || prov2.InputDigest == nil || *prov1.InputDigest != *prov2.InputDigest {
		t.Errorf("provision input_digest changed between the runs: %v -> %v (it hashes the recorded confirmation digest)", prov1.InputDigest, prov2.InputDigest)
	}
	if n := i.createCount(); n != 1 {
		t.Errorf("fake provider create counter = %d, want 1", n)
	}

	code, out = i.run("", env, i.envOnlyArgs("--non-interactive", "--from-phase", "seed"))
	if code != 2 || !strings.Contains(out, "PHASE_FROZEN") {
		t.Errorf("--from-phase seed after the source switch: exit %d, want 2 with PHASE_FROZEN:\n%s", code, out)
	}
	j3 := i.journal()
	for _, p := range []string{"seed", "forge-init", "flux-bootstrap"} {
		if got := j3.phaseStatus(p); got != "skipped" {
			t.Errorf("after the refused --from-phase seed: phase %s = %s, want skipped", p, got)
		}
	}
	if got := stubRootKind(t, i); got != "GitRepository" {
		t.Errorf("after the refused --from-phase seed: stub root sourceRef.kind = %q, want GitRepository", got)
	}
}

// --- S6b R2 and R3, and the compute-confirmed half of 50-AC-18

// uid8 is the first 8 characters of the install uid (plan 50 section 7.1, abandon-identity).
func uid8(uid string) string { return uid[:8] }

// S6b R2 (SC_TEST_STOP_AFTER=plan.confirm ends the pipeline after that step, before provision), 50-AC-18 second half
// (after a paid-footprint confirmation abandon-identity exits with ABANDON_REFUSED_COMPUTE_CONFIRMED and changes
// nothing) and S6b R3 (the stub phases seed-prep and configure exist, and the install reaches state configured). One
// installation, three steps in order.
func TestStopAfterPlanConfirm_AbandonRefused_ThenConfigured_50AC18_S6bR2_S6bR3(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	full, _ := planDigest(t)
	args := i.envOnlyArgs("--non-interactive")

	code, out := i.run("", i.unattendedEnv(stopAfter("plan.confirm")), args)
	requireExit(t, code, 0, out)
	j := i.journal()
	uid := j.InstallUID
	if j.Installation.State != "planned" {
		t.Errorf("after SC_TEST_STOP_AFTER=plan.confirm installation.state = %q, want planned", j.Installation.State)
	}
	if got := j.stepStatus("plan.confirm"); got != "succeeded" {
		t.Errorf("step plan.confirm = %s, want succeeded", got)
	}
	if got := j.phaseStatus("provision"); got == "succeeded" || got == "running" || got == "failed" {
		t.Errorf("phase provision = %s after SC_TEST_STOP_AFTER=plan.confirm, want it not started", got)
	}
	if n := i.createCount(); n != 0 {
		t.Errorf("fake provider create counter = %d after SC_TEST_STOP_AFTER=plan.confirm, want 0", n)
	}
	if cf, ok := j.confirmation("paid-footprint"); !ok || cf.SubjectDigest != full {
		t.Errorf("paid-footprint confirmation = %+v (found %v), want subject_digest %s", cf, ok, full)
	}
	if c := pvCommits(i, "bootstrap("+clusterName+"): node inventory"); len(c) != 0 {
		t.Errorf("node inventory commits = %v after SC_TEST_STOP_AFTER=plan.confirm, want none", c)
	}

	// 50-AC-18: refused once a paid-footprint confirmation exists, whatever is typed.
	journalBefore, _ := os.ReadFile(filepath.Join(i.repo, ".bootstrap", "journal.json"))
	code, out = i.run("abandon "+clusterName+" "+uid8(uid)+"\n", nil, i.args("abandon-identity"))
	if code == 0 || !strings.Contains(out, "ABANDON_REFUSED_COMPUTE_CONFIRMED") {
		t.Errorf("abandon-identity after a paid-footprint confirmation: exit %d, want a refusal with ABANDON_REFUSED_COMPUTE_CONFIRMED:\n%s", code, out)
	}
	if journalAfter, _ := os.ReadFile(filepath.Join(i.repo, ".bootstrap", "journal.json")); !bytes.Equal(journalBefore, journalAfter) {
		t.Errorf("the refused abandon-identity changed the local journal")
	}
	if got := i.journal().InstallUID; got != uid {
		t.Errorf("install_uid after the refused abandon-identity = %s, want %s", got, uid)
	}
	if _, ok := i.world.read(bucketName("tofu-state"), clusterName+"/journal/abandoned-"+uid+".json"); ok {
		t.Errorf("the refused abandon-identity archived the journal")
	}

	// S6b R3: provision, seed-prep and configure; the pipeline stops after configure in state configured.
	code, out = i.run("", i.unattendedEnv(stopAfter("configure")), args)
	requireExit(t, code, 0, out)
	j = i.journal()
	if j.Installation.State != "configured" {
		t.Errorf("after SC_TEST_STOP_AFTER=configure installation.state = %q, want configured", j.Installation.State)
	}
	for _, p := range []string{"provision", "seed-prep", "configure"} {
		if got := j.phaseStatus(p); got != "succeeded" {
			t.Errorf("phase %s = %s, want succeeded", p, got)
		}
	}
	if got := j.phaseStatus("seed"); got == "succeeded" || got == "skipped" || got == "running" || got == "failed" {
		t.Errorf("phase seed = %s after SC_TEST_STOP_AFTER=configure, want it not started", got)
	}
	if n := i.createCount(); n != 1 {
		t.Errorf("fake provider create counter = %d, want 1", n)
	}
	if got := stubRootKind(t, i); got != "" {
		t.Errorf("stub root sourceRef.kind = %q after configure, want none (seed has not run)", got)
	}
}

// --- 50-AC-18 and NB1: abandon identity

// 50-AC-18 (BOOT-6): fake install, three wrong passphrase confirmations (exit 8), abandon-identity, rerun with a new
// passphrase reaches planned with the same buckets, a new install_uid, and journal/abandoned-<old-uid>.json present.
// NB1: the local bundle files of the abandoned identity are moved aside, so the rerun does not adopt them again and
// fail the confirmation with exit 8 forever. The bundle objects of the old identity in the recovery bucket stay as
// they are (bundle versions are immutable).
func TestAbandonIdentity_NewIdentityReusesTheBuckets_50AC18_NB1(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	code, out := i.run(twice(goodPass)+"wrong one entry\nwrong two entry\nwrong three entry\n", nil, i.args("--confirm-storage", clusterName))
	requireExit(t, code, 8, out)

	j0 := i.journal()
	oldUID := j0.InstallUID
	oldBundle, err := os.ReadFile(j0.RecoveryBundle.LocalPath)
	if err != nil {
		t.Fatalf("the abandoned identity has no local bundle: %v", err)
	}
	bucketsBefore := i.world.allBuckets(bucketPfx)
	if len(bucketsBefore) != len(bucketPurposes) {
		t.Fatalf("buckets before the abandon = %v, want %d", bucketsBefore, len(bucketPurposes))
	}
	oldObjects := map[string]string{}
	for _, k := range i.world.objects(bucketName("recovery"), "") {
		if !strings.HasSuffix(k, ".tar.age") {
			continue
		}
		b, _ := i.world.read(bucketName("recovery"), k)
		oldObjects[k] = sha256Of(b)
	}
	if len(oldObjects) == 0 {
		t.Fatal("fixture: the recovery bucket holds no bundle")
	}

	code, out = i.run("abandon "+clusterName+" "+uid8(oldUID)+"\n", nil, i.args("abandon-identity"))
	requireExit(t, code, 0, out)

	archive, ok := i.world.read(bucketName("tofu-state"), clusterName+"/journal/abandoned-"+oldUID+".json")
	if !ok {
		t.Fatalf("no journal/abandoned-%s.json in the tofu-state bucket (have %v)", oldUID, i.world.objects(bucketName("tofu-state"), clusterName+"/journal/"))
	}
	if got := parseJournal(t, archive).InstallUID; got != oldUID {
		t.Errorf("the archived journal has install_uid %s, want %s", got, oldUID)
	}
	if b, ok := i.world.read(bucketName("tofu-state"), clusterName+"/journal/journal.json"); ok && parseJournal(t, b).InstallUID == oldUID {
		t.Errorf("the remote journal still belongs to the abandoned identity")
	}
	if lj, ok := i.journalIfAny(); ok && lj.InstallUID == oldUID {
		t.Errorf("the local journal still belongs to the abandoned identity")
	}
	bundlesDir := filepath.Join(i.blackstart, clusterName, "bundles")
	left, _ := filepath.Glob(filepath.Join(bundlesDir, "*.tar.age"))
	if len(left) != 0 {
		t.Errorf("local bundle files remain in %s after abandon-identity: %v (they must be moved aside)", bundlesDir, left)
	}
	if !fileWithContentUnder(t, filepath.Join(i.blackstart, clusterName), oldBundle) {
		t.Errorf("the abandoned bundle is gone from %s (move it aside, never delete it)", filepath.Join(i.blackstart, clusterName))
	}
	if got := i.world.allBuckets(bucketPfx); strings.Join(got, ",") != strings.Join(bucketsBefore, ",") {
		t.Errorf("buckets after abandon-identity = %v, want %v", got, bucketsBefore)
	}

	const newPass = "second passphrase for the lab cluster"
	_, d8 := planDigest(t)
	code, out = i.run(twice(newPass)+newPass+"\n", merge(stopAfter("plan"), map[string]string{"SC_CONFIRM_PLAN": d8}), i.args("--confirm-storage", clusterName))
	requireExit(t, code, 0, out)

	j1 := i.journal()
	if j1.Installation.State != "planned" {
		t.Errorf("installation.state = %q after the rerun with the new passphrase, want planned", j1.Installation.State)
	}
	if j1.InstallUID == "" || j1.InstallUID == oldUID {
		t.Errorf("install_uid = %q after the rerun, want a new one (old %s)", j1.InstallUID, oldUID)
	}
	if got := i.world.allBuckets(bucketPfx); strings.Join(got, ",") != strings.Join(bucketsBefore, ",") {
		t.Errorf("buckets after the rerun = %v, want the same %v", got, bucketsBefore)
	}
	if _, ok := i.world.read(bucketName("tofu-state"), clusterName+"/journal/abandoned-"+oldUID+".json"); !ok {
		t.Errorf("journal/abandoned-%s.json is gone after the rerun", oldUID)
	}
	for k, want := range oldObjects {
		if b, ok := i.world.read(bucketName("recovery"), k); !ok || sha256Of(b) != want {
			t.Errorf("recovery bucket object %s of the abandoned identity changed or vanished", k)
		}
	}
	if j1.RecoveryBundle.Status != "confirmed" {
		t.Errorf("recovery_bundle.status = %q after the rerun, want confirmed", j1.RecoveryBundle.Status)
	}
	if b, err := os.ReadFile(j1.RecoveryBundle.LocalPath); err != nil || bytes.Equal(b, oldBundle) {
		t.Errorf("the new identity's local bundle %q is missing or is the abandoned one (%v)", j1.RecoveryBundle.LocalPath, err)
	}
	if body := i.git("log", "--format=%B"); !strings.Contains(body, "SC-Install-Uid: "+j1.InstallUID) {
		t.Errorf("no commit carries the trailer SC-Install-Uid: %s", j1.InstallUID)
	}
}

// fileWithContentUnder reports whether any file below dir holds exactly want.
func fileWithContentUnder(t testing.TB, dir string, want []byte) bool {
	t.Helper()
	found := false
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found {
			return nil
		}
		if b, rerr := os.ReadFile(p); rerr == nil && bytes.Equal(b, want) {
			found = true
		}
		return nil
	})
	return found
}

// pvCommits lists the commit ids whose subject is exactly subject.
func pvCommits(i *inst, subject string) []string {
	var out []string
	for _, l := range strings.Split(i.git("log", "--format=%H %s"), "\n") {
		if sha, s, ok := strings.Cut(l, " "); ok && s == subject {
			out = append(out, sha)
		}
	}
	return out
}
