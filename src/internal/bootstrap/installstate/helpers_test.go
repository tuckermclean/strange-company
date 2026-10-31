package installstate_test

// Helpers copied unchanged from the install package's plan_test.go and provision_test.go (the install tests were
// split into two packages to fit the go test timeout; plan 52).

import (
	"context"
	"strings"
	"testing"

	"strange.company/sc/internal/provider"
	"strange.company/sc/internal/provider/fake"
)

// stopAfter ends the pipeline successfully after a step (SC_TEST_STOP_AFTER, plan 50 section 9.2).
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
