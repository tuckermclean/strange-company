package install_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CD-76 (a), plan 50 section 9.2 as amended: in test mode with a lab-labeled substrate PF-15..PF-20, PF-26 and PF-29
// report `skip (lab)` and the preflight does not fail on them, even when policy/model-routing.yaml and
// policy/budgets.yaml (plans 31 and 33) do not exist yet. Enabler of 05-AC-6.

var labSkipRows = []string{"PF-15", "PF-16", "PF-17", "PF-18", "PF-19", "PF-20", "PF-26", "PF-29"}

// labInst is a fixture installation with the lab-small profile and without the two policy files the lab cannot judge.
func labInst(t *testing.T, profile string) *inst {
	t.Helper()
	i := newInst(t)
	i.setVars(map[string]string{"PROFILE": profile})
	for _, f := range []string{"model-routing.yaml", "budgets.yaml"} {
		if err := os.Remove(filepath.Join(i.repo, "policy", f)); err != nil {
			t.Fatal(err)
		}
	}
	return i
}

// row returns the output line of a preflight row ("" when the row is absent).
func row(out, id string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, id+" ") {
			return l
		}
	}
	return ""
}

func requireSkippedLab(t *testing.T, out string) {
	t.Helper()
	for _, id := range labSkipRows {
		l := row(out, id)
		if !strings.HasPrefix(l, id+" skip") || !strings.Contains(l, "(lab)") {
			t.Errorf("%s row = %q, want `%s skip` with a \"(lab)\" reason", id, l, id)
		}
	}
}

func requireNoLabSkip(t *testing.T, out string) {
	t.Helper()
	for _, id := range labSkipRows {
		if l := row(out, id); strings.Contains(l, "skip") || strings.Contains(l, "(lab)") {
			t.Errorf("%s row = %q, want it evaluated (no lab skip)", id, l)
		}
	}
}

// CD-76 (a): the preflight command skips the eight rows and exits 0 without the policy files.
func TestPreflight_LabTestMode_SkipsPF15to20_26_29_CD76a(t *testing.T) {
	t.Parallel()
	i := labInst(t, "lab-small")
	code, out := i.run("", nil, i.envOnlyArgs("preflight"))
	requireExit(t, code, 0, out)
	requireSkippedLab(t, out)
}

// CD-76 (a): the install pipeline's own preflight phase does the same (the run that 05-AC-6 drives).
func TestRun_LabTestMode_PreflightSkipsPF15to20_26_29_CD76a(t *testing.T) {
	t.Parallel()
	i := labInst(t, "lab-small")
	code, out := i.run("", merge(i.passphraseFile(), stopAfter("preflight")), i.envOnlyArgs("--non-interactive"))
	requireExit(t, code, 0, out)
	requireSkippedLab(t, out)
}

// CD-76 (a): without test mode the rows are evaluated as before; the absent policy files fail PF-19 and PF-26.
func TestPreflight_LabWithoutTestMode_EvaluatesRows_CD76a(t *testing.T) {
	t.Parallel()
	i := labInst(t, "lab-small")
	// the fake provider itself needs test mode; the existing provider does not
	i.setVars(map[string]string{"PROVIDER": "existing", "EXISTING_NODES": "node-01=debian@192.0.2.10", "EXPECTED_NODE_COUNT": "1"})
	code, out := i.run("", map[string]string{"SC_TEST_MODE": ""}, i.envOnlyArgs("preflight"))
	requireExit(t, code, 3, out)
	requireNoLabSkip(t, out)
	if l := row(out, "PF-26"); !strings.Contains(l, "POLICY_FILE_MISSING") {
		t.Errorf("PF-26 row = %q, want POLICY_FILE_MISSING", l)
	}
}

// CD-76 (a): a profile without the lab label is not skipped even in test mode.
func TestPreflight_NonLabProfileTestMode_EvaluatesRows_CD76a(t *testing.T) {
	t.Parallel()
	i := labInst(t, "self-hosting-minimum")
	code, out := i.run("", nil, i.envOnlyArgs("preflight"))
	requireExit(t, code, 3, out)
	requireNoLabSkip(t, out)
	if l := row(out, "PF-26"); !strings.Contains(l, "POLICY_FILE_MISSING") {
		t.Errorf("PF-26 row = %q, want POLICY_FILE_MISSING", l)
	}
}

// CD-76 (a): a paid provider gets no lab skip (test mode itself is refused, 50-AC-7).
func TestPreflight_LabPaidProviderTestMode_NoSkip_CD76a(t *testing.T) {
	t.Parallel()
	i := labInst(t, "lab-small")
	i.setVars(map[string]string{"PROVIDER": "ovh"})
	code, out := i.run("", nil, i.envOnlyArgs("preflight"))
	if code == 0 {
		t.Errorf("exit 0, want a refusal:\n%s", out)
	}
	requireNoLabSkip(t, out)
}
