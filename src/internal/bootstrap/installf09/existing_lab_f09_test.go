package installf09_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// F09 S9b (plan 05 sections 14.4 and 14.6, 05-AC-6): the harness's configuration, PROFILE=lab-small with the generated
// host key attested through EXISTING_NODE_OVERRIDES, makes provider-output.json report host_key_source cloud-init.

// 05-AC-6: PROFILE=lab-small plus an attested host key yields host_key_source cloud-init in provider-output.json.
func TestExisting_LabProfileAttestedHostKey_ProviderOutputCloudInit_05AC6(t *testing.T) {
	t.Parallel()
	i, env := newExisting(t)
	i.setVars(map[string]string{"PROFILE": "lab-small",
		"EXISTING_NODE_OVERRIDES": "node-01:host_key=" + exFingerprints(t)[0]})

	code, out := i.run(clusterName+"\n", env, i.envOnlyArgs())
	requireExit(t, code, 0, out)

	files, _ := filepath.Glob(filepath.Join(i.repo, ".bootstrap", "runs", "*", "provider-output.json"))
	if len(files) != 1 {
		t.Fatalf("provider-output.json files under .bootstrap/runs = %v, want exactly one", files)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var po struct {
		Provider struct {
			Capabilities struct {
				HostKeySource string `json:"host_key_source"`
			} `json:"capabilities"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(raw, &po); err != nil {
		t.Fatal(err)
	}
	if got := po.Provider.Capabilities.HostKeySource; got != "cloud-init" {
		t.Errorf("provider-output.json host_key_source = %q, want cloud-init", got)
	}
}
