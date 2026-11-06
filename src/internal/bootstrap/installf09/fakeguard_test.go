package installf09_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/provider/fake"
	"strange.company/sc/internal/provider/guard"
)

// F09 slice S4 (plan 03 sections 14.4, 14.7, 14.8, 22): the fake provider feeds a synthetic plan JSON through the
// guard; scenarios come from test/fixtures/fake-provider/*.yaml, copied to <.bootstrap/fake-provider>/scenario.yaml.

func (i *inst) useScenario(name string) {
	i.t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(i.t), "test", "fixtures", "fake-provider", name+".yaml"))
	if err != nil {
		i.t.Fatalf("scenario fixture: %v", err)
	}
	write(i.t, filepath.Join(i.fakeProviderDir(), "scenario.yaml"), string(b), 0o600)
}

func (i *inst) unattendedProvision(extra map[string]string, args ...string) (int, string) {
	return i.run("", merge(i.unattendedEnv(), merge(stopAfter("provision"), extra)), i.envOnlyArgs(append([]string{"--non-interactive"}, args...)...))
}

// 03-AC-5: an unlisted resource type in the fake plan exits 4 with the guard code; nothing is created.
func TestFakeGuard_UnlistedResourceExits4_03AC5(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	i.useScenario("unlisted-resource")
	_, d8 := pvPlanDigest(t)
	code, out := i.unattendedProvision(nil, "--confirm-plan", d8)
	requireExit(t, code, 4, out)
	if !strings.Contains(out, "GUARD_UNEXPECTED_RESOURCE") {
		t.Errorf("03-AC-5: output lacks GUARD_UNEXPECTED_RESOURCE:\n%s", out)
	}
	if got := i.createCount(); got != 0 {
		t.Errorf("03-AC-5: create counter = %d after the guard refusal, want 0", got)
	}
}

// 03-AC-6: the confirmation D8 is the guard digest of the fake plan JSON; no confirmation or a wrong one exits 5 with
// the counter at 0, the right one creates once.
func TestFakeGuard_ConfirmationIsGuardD8_03AC6(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	gb, err := os.ReadFile(filepath.Join(root, "infra", "tofu", "compute", "fake", "guard.yaml"))
	if err != nil {
		t.Fatalf("03-AC-6: the fake compute module has no guard.yaml: %v", err)
	}
	g, err := guard.ParseGuard(gb)
	if err != nil {
		t.Fatalf("03-AC-6: fake guard.yaml: %v", err)
	}
	hex, err := g.Digest(fake.PlanJSON(clusterName, 1))
	if err != nil {
		t.Fatalf("03-AC-6: guard digest of the fake plan JSON: %v", err)
	}
	d8 := guard.D8(hex)

	i := newInst(t)
	env := i.unattendedEnv()
	delete(env, "SC_CONFIRM_PLAN")

	code, out := i.run("", merge(env, stopAfter("provision")), i.envOnlyArgs("--non-interactive"))
	requireExit(t, code, 5, out)
	if !strings.Contains(out, d8) {
		t.Errorf("03-AC-6: the printed plan lacks the guard D8 %s:\n%s", d8, out)
	}
	if got := i.createCount(); got != 0 {
		t.Errorf("03-AC-6: counter = %d without --confirm-plan, want 0", got)
	}
	code, out = i.run("", merge(env, stopAfter("provision")), i.envOnlyArgs("--non-interactive", "--confirm-plan", "deadbeef"))
	requireExit(t, code, 5, out)
	if got := i.createCount(); got != 0 {
		t.Errorf("03-AC-6: counter = %d after a wrong D8, want 0", got)
	}
	code, out = i.run("", merge(env, stopAfter("provision")), i.envOnlyArgs("--non-interactive", "--confirm-plan", d8))
	requireExit(t, code, 0, out)
	if got := i.createCount(); got != 1 {
		t.Errorf("03-AC-6: counter = %d after the right D8, want 1", got)
	}
	cf, ok := pvRead(t, i).confirmation("paid-footprint", "existing-nodes")
	if !ok || cf.SubjectDigest != "sha256:"+hex {
		t.Errorf("03-AC-6: plan confirmation = %+v (found %v), want subject_digest sha256:%s", cf, ok, hex)
	}
}

// 03-AC-7: the fake orders, then the run dies before the node is recorded; the rerun refuses with
// ORPHAN_PAID_RESOURCE (exit 4) and does not order again.
func TestFakeOrphan_RerunExits4WithoutSecondOrder_03AC7(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	i.useScenario("orphan")
	_, d8 := pvPlanDigest(t)
	code, out := i.unattendedProvision(nil, "--confirm-plan", d8)
	if code == 0 {
		t.Errorf("03-AC-7: the run with the orphan scenario exited 0, want it to die after the order:\n%s", out)
	}
	if got := i.createCount(); got != 1 {
		t.Errorf("03-AC-7: counter = %d after the order, want 1", got)
	}
	code, out = i.unattendedProvision(nil, "--confirm-plan", d8)
	requireExit(t, code, 4, out)
	if !strings.Contains(out, "ORPHAN_PAID_RESOURCE") {
		t.Errorf("03-AC-7: rerun output lacks ORPHAN_PAID_RESOURCE:\n%s", out)
	}
	if got := i.createCount(); got != 1 {
		t.Errorf("03-AC-7: counter = %d after the rerun, want 1 (no duplicate order)", got)
	}
}
