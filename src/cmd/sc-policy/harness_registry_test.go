package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 28-AC-1: `sc-policy check` (the body of `make policy-check`) rejects each registry fixture, printing its own
// distinct code, and accepts the committed policy/harnesses.yaml. Schema-level fixtures may also print the generic
// POLICY_SCHEMA_INVALID (F04 26-AC-1); the invariant-level ones (repo-config-not-ignored,
// worker-timeout-production) print exactly their own code. No other code is allowed.
func TestCheck_HarnessRegistryFixtures_28AC1(t *testing.T) {
	invariantLevel := map[string]bool{"repo-config-not-ignored": true, "worker-timeout-production": true}
	cases := []struct{ dir, code string }{
		{"tag-image", "HARNESS_IMAGE_NOT_DIGEST_PINNED"},
		{"reserved-hermes", "HARNESS_NAME_RESERVED"},
		{"credential-field", "HARNESS_PROVIDER_CREDENTIAL_FIELD"},
		{"unknown-scenario", "HARNESS_FAKE_SCENARIO_UNKNOWN"},
		{"repo-config-not-ignored", "HARNESS_REPO_CONFIG_NOT_IGNORED"},
		{"worker-timeout-production", "HARNESS_WORKER_TIMEOUT_NOT_TEST_ONLY"},
		{"telemetry-prompts", "HARNESS_TELEMETRY_CONTENT_CAPTURE"},
	}
	for _, c := range cases {
		t.Run(c.dir, func(t *testing.T) {
			code, out, errs := runCmd("check", "--dir", filepath.Join(pol, "harness-registry", c.dir))
			if code == 0 {
				t.Fatalf("exit 0; stdout %q", out)
			}
			found := false
			for _, l := range strings.Split(out+errs, "\n") {
				if m := line.FindStringSubmatch(l); m != nil {
					switch {
					case m[1] == c.code:
						found = true
					case m[1] == "POLICY_SCHEMA_INVALID" && !invariantLevel[c.dir]:
					default:
						t.Errorf("unexpected code %s in line %q", m[1], l)
					}
				}
			}
			if !found {
				t.Errorf("no %s line in output:\n%s%s", c.code, out, errs)
			}
		})
	}
}

// 28-AC-1: `check --root <repo>` exits 0 with the committed policy/harnesses.yaml present.
func TestCheck_CommittedHarnessRegistryAccepted_28AC1(t *testing.T) {
	if _, err := os.Stat(filepath.Join(root(t), "policy", "harnesses.yaml")); err != nil {
		t.Fatalf("policy/harnesses.yaml must be committed: %v", err)
	}
	code, out, errs := runCmd("check", "--root", root(t))
	if code != 0 {
		t.Fatalf("exit %d; stdout %q stderr %q", code, out, errs)
	}
}
