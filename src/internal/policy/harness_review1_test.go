package policy

import (
	"os"
	"path/filepath"
	"testing"
)

func harnessIssueCodes(t *testing.T, dir string) map[string]bool {
	t.Helper()
	_, issues := Check(td("harness-credential", dir), opts(t, false))
	got := map[string]bool{}
	for _, i := range issues {
		got[i.Code] = true
	}
	if len(issues) == 0 {
		t.Fatalf("%s: fixture accepted, want rejection", dir)
	}
	return got
}

// S1-F2: HARNESS_PROVIDER_CREDENTIAL_FIELD is reported when any offending extra property of a harness entry is
// credential-like, even if another extra property is not.
func TestHarnessRegistry_CredentialBesideOtherProperty_S1F2(t *testing.T) {
	got := harnessIssueCodes(t, "credential-and-other")
	if !got[CodeHarnessCredentialField] {
		t.Errorf("codes = %v, want %s", keys(got), CodeHarnessCredentialField)
	}
}

// S1-F2: non-credential names (authors) and a misplaced entry-level tokenDelivery stay generic schema faults.
func TestHarnessRegistry_NonCredentialNamesNotCredentialField_S1F2(t *testing.T) {
	for _, dir := range []string{"authors", "misplaced-token-delivery"} {
		t.Run(dir, func(t *testing.T) {
			got := harnessIssueCodes(t, dir)
			if got[CodeHarnessCredentialField] {
				t.Errorf("codes = %v, must not contain %s", keys(got), CodeHarnessCredentialField)
			}
			if !got["POLICY_SCHEMA_INVALID"] {
				t.Errorf("codes = %v, want POLICY_SCHEMA_INVALID", keys(got))
			}
		})
	}
}

// S1-F1: only fake-deadline-exceeded carries fake.disableWorkerTimeout: true (plan 28 section 14.4).
func TestHarnessRegistry_DisableWorkerTimeoutOnlyDeadlineExceeded_S1F1(t *testing.T) {
	reg, ok := dig(t, readYAML(t, filepath.Join(repoRoot(t), "policy", "harnesses.yaml")), "spec", "harnesses").(map[string]any)
	if !ok {
		t.Fatal("no spec.harnesses")
	}
	if _, err := os.Stat(filepath.Join(repoRoot(t), "policy", "harnesses.yaml")); err != nil {
		t.Fatal(err)
	}
	for name, h := range reg {
		fk, _ := h.(map[string]any)["fake"].(map[string]any)
		got := fk != nil && fk["disableWorkerTimeout"] == true
		want := name == "fake-deadline-exceeded"
		if got != want {
			t.Errorf("%s: fake.disableWorkerTimeout true = %v, want %v", name, got, want)
		}
	}
}
