package existing_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/provider"
	"strange.company/sc/internal/provider/discovery"
	"strange.company/sc/internal/provider/existing"
)

// F09 S9b (plan 05 sections 7.1 and 14.6, 05-AC-6): the lab generates the host keys and injects them through cloud-init,
// so with a lab-labeled effective profile (infra/profiles/lab-*.yaml, label strange.company/lab: "true") and every
// node attested with the generated key's fingerprint (EXISTING_NODE_OVERRIDES host_key), the output reports
// host_key_source "cloud-init". An unattested lab node cannot claim it (the key was not checked against the generated
// one), and a non-lab profile with attested keys stays operator-supplied.

const labProfile = "../../../../infra/profiles/lab-small.yaml"

func hostKeySourceFor(t *testing.T, profileFile string, attest bool) string {
	t.Helper()
	node, tr := fixture(t)
	if attest {
		var ed string
		for _, k := range tr.keys {
			if strings.Contains(k, " ssh-ed25519 ") {
				ed = k
			}
		}
		if ed == "" {
			t.Fatal("fixture lacks an ed25519 key")
		}
		node.HostKeyFingerprint = discovery.Fingerprints([]string{ed})[0]
	}
	p := existing.New(existing.Config{Nodes: []discovery.Node{node}, ProfileFile: profileFile,
		InstallUID: func() string { return uidA }, Transport: tr})
	if _, err := p.Plan(context.Background(), provider.Desired{Cluster: "lab-one", NodeCount: 1}); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	doc, err := p.OutputDocument("lab-one", time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), "0", "USD")
	if err != nil {
		t.Fatalf("OutputDocument: %v", err)
	}
	var po struct {
		Provider struct {
			Capabilities struct {
				HostKeySource string `json:"host_key_source"`
			} `json:"capabilities"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(doc, &po); err != nil {
		t.Fatal(err)
	}
	return po.Provider.Capabilities.HostKeySource
}

// 05-AC-6: lab profile and attested generated keys report cloud-init.
func TestOutputDocument_LabProfileAttestedKeys_ReportsCloudInit_05AC6(t *testing.T) {
	if got := hostKeySourceFor(t, labProfile, true); got != "cloud-init" {
		t.Errorf("host_key_source = %q, want cloud-init", got)
	}
}

// 05-AC-6: a lab node whose key is not attested is trust-on-first-use, never cloud-init.
func TestOutputDocument_LabProfileUnattestedKeys_StaysTofu_05AC6(t *testing.T) {
	if got := hostKeySourceFor(t, labProfile, false); got != "tofu" {
		t.Errorf("host_key_source = %q, want tofu", got)
	}
}

// 05-AC-6 (unchanged behavior): a production profile with attested keys is operator-supplied.
func TestOutputDocument_ProductionProfileAttestedKeys_StaysOperatorSupplied_05AC6(t *testing.T) {
	if got := hostKeySourceFor(t, profile, true); got != "operator-supplied" {
		t.Errorf("host_key_source = %q, want operator-supplied", got)
	}
}
