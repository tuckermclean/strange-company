package installf09_test

import (
	"strings"
	"testing"
)

// 03-AC-10 (plan 03 14.6, S5b attempt 2): a catalogue entry whose commitment is empty or does not parse is
// COST_INCOMPLETE (exit 4) before anything is created, not priced as one month.
func TestCost_UnparseableCommitmentExits4_03AC10(t *testing.T) {
	t.Parallel()
	for name, commitment := range map[string]string{"week": "P1W", "lowercase": "P1y", "no-P": "1M", "empty": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			i := newInst(t)
			s5Profiles(t, i)
			i.setVars(map[string]string{"MAX_MONTHLY_COST": "25"})
			i.useScenario("paid")
			s5bCatalogue(t, i, func(_, entry map[string]any) { entry["commitment"] = commitment })
			code, out := s5Provision(t, i, nil)
			requireExit(t, code, 4, out)
			if !strings.Contains(out, "COST_INCOMPLETE") {
				t.Errorf("output lacks COST_INCOMPLETE:\n%s", out)
			}
			if got := i.createCount(); got != 0 {
				t.Errorf("create counter = %d after the refusal, want 0", got)
			}
		})
	}
}

// 03-AC-11 (plan 03 9.2, PF-03, fail closed like commandPaid): in the run, PROVIDER=ovh is not built (no adapter, so the
// run has no provider yet), but every provider except fake and existing is paid: on a lab-labeled profile the run's
// preflight reports LAB_SUBSTRATE_PAID_REFUSED (exit 3), before PROVIDER_NOT_IMPLEMENTED.
func TestRun_UnbuiltPaidProviderOnLabProfile_RefusedAtPF03_03AC11(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	s5Profiles(t, i)
	i.setVars(map[string]string{"PROVIDER": "ovh", "PROVIDER_REGION": "gra", "MACHINE_CLASS": "vps-2025-model2",
		"OVH_APPLICATION_KEY": "fixture-ak", "OVH_APPLICATION_SECRET": "fixture-secret-as-0001", "OVH_CONSUMER_KEY": "fixture-secret-ck-0002"})
	env := merge(i.unattendedEnv(), map[string]string{"SC_TEST_MODE": ""})
	delete(env, "SC_TEST_STOP_AFTER")
	code, out := i.run("", env, i.envOnlyArgs("--non-interactive", "--profile", "lab-small"))
	requireExit(t, code, 3, out)
	for _, w := range []string{"PF-03", "LAB_SUBSTRATE_PAID_REFUSED"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %s:\n%s", w, out)
		}
	}
	if strings.Contains(out, "PROVIDER_NOT_IMPLEMENTED") {
		t.Errorf("the run reached PROVIDER_NOT_IMPLEMENTED instead of failing PF-03:\n%s", out)
	}
}
