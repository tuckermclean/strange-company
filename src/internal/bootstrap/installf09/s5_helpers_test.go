package installf09_test

import (
	"regexp"
	"testing"
)

// Copies of helpers that stayed behind in installf09b with the moved cost tests (installf09 split, 2026-11-04).

var s5Digest = regexp.MustCompile(`Plan digest: sha256:([0-9a-f]{64})`)

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
