package lint_test

import (
	"regexp"
	"strings"
	"testing"
)

// F03 S4 (plan 52 section 8.4, plan 02 sections 22 and 23): .github/workflows/e2e.yaml runs the tier-E evidence
// for 02-AC-4 (pull/build equivalence), 02-AC-5 (engine matrix) and 02-AC-12 (k3d), each as its own job selected
// by the path filter and awaited by e2e-gate. Permissions {contents: read} and pinned `uses:` for every job are
// asserted generically by TestWorkflow_Permissions_52AC1 and TestWorkflow_NoSecretsAndPinnedUses_52AC1.

type e2eJob struct {
	id      string
	ac      string
	scripts []string // each must appear in a step
	extra   []*regexp.Regexp
}

var s4Jobs = []e2eJob{
	{
		id: "toolbox-podman", ac: "02-AC-5 rootless Podman on Ubuntu",
		scripts: []string{"sh test/e2e/wrapper/wrapper-real.sh"},
		extra:   []*regexp.Regexp{regexp.MustCompile(`SC_CONTAINER_ENGINE[=:]\s*"?podman"?`)},
	},
	{
		id: "toolbox-selinux", ac: "02-AC-5 rootless Podman, SELinux enforcing, Fedora VM",
		scripts: []string{"test/e2e/selinux/run"},
		extra:   []*regexp.Regexp{regexp.MustCompile(`/dev/kvm`), regexp.MustCompile(`qemu`)},
	},
	{
		id: "toolbox-pull-equivalence", ac: "02-AC-4",
		scripts: []string{"sh test/e2e/wrapper/pull-equivalence.sh"},
	},
	{
		id: "e2e-k3d", ac: "02-AC-12 (plan 52 e2e-k3d)",
		scripts: []string{"test/e2e/k3d/run"},
	},
}

func TestWorkflow_E2EEngineMatrixJobs_02AC4_02AC5_02AC12(t *testing.T) {
	w := mustWorkflow(t, "e2e.yaml")
	jobs := jobsOf(w)
	for _, j := range s4Jobs {
		lines, ok := jobs[j.id]
		if !ok {
			t.Errorf("e2e.yaml lacks job %q (%s)", j.id, j.ac)
			continue
		}
		body := strings.Join(lines, "\n")
		if !strings.Contains(body, "runs-on: ubuntu-24.04") {
			t.Errorf("job %s (%s) must run on ubuntu-24.04", j.id, j.ac)
		}
		if !regexp.MustCompile(`needs:\s*(\[\s*)?changes\b`).MatchString(body) {
			t.Errorf("job %s must declare needs: changes (the path filter)", j.id)
		}
		if !regexp.MustCompile(`if:.*needs\.changes\.outputs\.[a-z-]+\s*==\s*'true'`).MatchString(body) {
			t.Errorf("job %s must be selected by a needs.changes.outputs.<x> == 'true' condition", j.id)
		}
		if !regexp.MustCompile(`timeout-minutes:\s*\d+`).MatchString(body) {
			t.Errorf("job %s must set timeout-minutes", j.id)
		}
		for _, s := range j.scripts {
			if !strings.Contains(body, s) {
				t.Errorf("job %s (%s) has no step running %q", j.id, j.ac, s)
			}
		}
		for _, re := range j.extra {
			if !re.MatchString(body) {
				t.Errorf("job %s (%s): pattern %s not found", j.id, j.ac, re)
			}
		}
		if strings.Contains(body, "sudo") && !strings.Contains(body, "persist-credentials: false") {
			t.Errorf("job %s uses sudo and must check out with persist-credentials: false", j.id)
		}
	}
}

// e2e-gate waits for every tier-E job and fails when one of them failed or was cancelled.
func TestWorkflow_E2EGateAwaitsS4Jobs_52AC1_02AC5(t *testing.T) {
	jobs := jobsOf(mustWorkflow(t, "e2e.yaml"))
	gate := strings.Join(jobs["e2e-gate"], "\n")
	for _, j := range s4Jobs {
		if !regexp.MustCompile(`needs:[^\n]*\b` + regexp.QuoteMeta(j.id) + `\b|(?m)^\s*-\s*` + regexp.QuoteMeta(j.id) + `\s*$`).MatchString(gate) {
			t.Errorf("e2e-gate must list %q in needs", j.id)
		}
		if !strings.Contains(gate, "needs."+j.id+".result") {
			t.Errorf("e2e-gate must check needs.%s.result", j.id)
		}
	}
}

// The path filter selects the new jobs when their scripts change.
func TestWorkflow_E2EPathFilterSelectsS4Scripts_52AC1(t *testing.T) {
	changes := strings.Join(jobsOf(mustWorkflow(t, "e2e.yaml"))["changes"], "\n")
	for _, want := range []string{"test/e2e/selinux/", "test/e2e/k3d/"} {
		if !strings.Contains(changes, want) {
			t.Errorf("the changes job's path filter lacks %q", want)
		}
	}
}
