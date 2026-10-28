package identity_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/contracts"
)

// S7 of F08. Operator decision 2026-10-27: the recovery bundle manifest carries the installation's `install_uid` (an
// optional field of contracts/recovery-bundle.schema.yaml; `install_id` stays the cluster name). The bundle writer puts
// the uid into the manifest, readers take it from there. Plan 06 section 20.3 and 06-AC-6: the rotated bundle lists
// SC-TOFU-ENCRYPTION as the instances `current` and `previous`, each entry with its own generation.

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func sealWith(t testing.TB, set *identity.Set) *identity.Sealed {
	t.Helper()
	sealed, err := identity.Seal(identity.SealInput{
		Cluster: cluster, Version: 1, Passphrase: goodPass, Set: set,
		Clock:       clock.NewFake(time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)),
		ToolVersion: "0.1.0", ToolboxImage: "local-build:sha256:" + strings.Repeat("a", 64),
		SourceCommit: strings.Repeat("b", 40), Trigger: "initial-bootstrap", WorkFactor: 18,
	})
	if err != nil || sealed == nil {
		t.Fatalf("Seal: %v", err)
	}
	return sealed
}

func manifestDoc(t testing.TB, manifest []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	return m
}

func validManifestDoc(t testing.TB, doc map[string]any) error {
	t.Helper()
	schemas, err := contracts.CompileSchemas(filepath.Join(repoRoot(t), "contracts"))
	if err != nil {
		t.Fatal(err)
	}
	sc := schemas["recovery-bundle"]
	if sc == nil {
		t.Fatal("no recovery-bundle schema")
	}
	b, _ := json.Marshal(doc)
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var inst any
	if err := dec.Decode(&inst); err != nil {
		t.Fatal(err)
	}
	return sc.Validate(inst)
}

// The contract: install_uid is an optional string of the manifest.
func TestRecoveryBundleSchema_InstallUIDOptional_InstallUID(t *testing.T) {
	_, sealed := sealedV1(t)
	base := manifestDoc(t, sealed.Manifest)
	delete(base, "install_uid")
	if err := validManifestDoc(t, base); err != nil {
		t.Fatalf("a manifest without install_uid no longer validates: %v", err)
	}
	with := manifestDoc(t, sealed.Manifest)
	with["install_uid"] = "1b4e28ba-2fa1-4d3b-a3fc-2c0e5b5f1e3a"
	if err := validManifestDoc(t, with); err != nil {
		t.Errorf("a manifest with an install_uid does not validate: %v", err)
	}
	for name, bad := range map[string]any{"an empty string": "", "a number": 7, "an object": map[string]any{"a": 1}} {
		d := manifestDoc(t, sealed.Manifest)
		d["install_uid"] = bad
		if err := validManifestDoc(t, d); err == nil {
			t.Errorf("a manifest whose install_uid is %s validates", name)
		}
	}
}

// The writer: Seal puts Set.InstallUID into the manifest (and the manifest still validates); without a uid the key is absent.
func TestSeal_ManifestCarriesInstallUID_InstallUID(t *testing.T) {
	set := genSet(t)
	set.InstallUID = "1b4e28ba-2fa1-4d3b-a3fc-2c0e5b5f1e3a"
	sealed := sealWith(t, set)
	o := open(t, sealed.Data, goodPass)
	m := manifestDoc(t, o.Manifest)
	if got, _ := m["install_uid"].(string); got != set.InstallUID {
		t.Errorf("manifest install_uid = %v, want %s", m["install_uid"], set.InstallUID)
	}
	if got, _ := m["install_id"].(string); got != cluster {
		t.Errorf("manifest install_id = %v, want the cluster name %s", m["install_id"], cluster)
	}
	manifestSchemaValidate(t, o.Manifest)

	set2 := genSet(t)
	set2.InstallUID = ""
	o2 := open(t, sealWith(t, set2).Data, goodPass)
	if _, ok := manifestDoc(t, o2.Manifest)["install_uid"]; ok {
		t.Errorf("a set without an install_uid wrote install_uid into the manifest")
	}
	manifestSchemaValidate(t, o2.Manifest)
}

// The reader: SetFromBundle with no uid argument takes it from the manifest.
func TestSetFromBundle_TakesInstallUIDFromManifest_InstallUID(t *testing.T) {
	set := genSet(t)
	set.InstallUID = "5f0c2b9e-7a41-4d8e-8b3a-9c1d2e3f4a5b"
	o := open(t, sealWith(t, set).Data, goodPass)
	got, err := identity.SetFromBundle(loadReg(t), "", o)
	if err != nil || got == nil {
		t.Fatalf("SetFromBundle: %v", err)
	}
	if got.InstallUID != set.InstallUID {
		t.Errorf("InstallUID = %q, want the manifest's %q", got.InstallUID, set.InstallUID)
	}
	if !uuidRe.MatchString(got.InstallUID) {
		t.Errorf("InstallUID %q is not a UUID", got.InstallUID)
	}
}

// 06-AC-6 (bundle): an entry's generation is the secret's own (zero means 1), and SC-TOFU-ENCRYPTION may be bundled as the
// instances `current` (generation 2) and `previous` (generation 1) with their own paths.
func TestSeal_EntryGenerationAndTofuInstances_06AC6(t *testing.T) {
	set := genSet(t)
	var keep []identity.Secret
	for _, s := range set.Secrets {
		if s.Class != "SC-TOFU-ENCRYPTION" {
			keep = append(keep, s)
		}
	}
	p1, p2 := "first-tofu-passphrase-0000000000000000001", "second-tofu-passphrase-000000000000000002"
	set.Secrets = append(keep,
		identity.Secret{Class: "SC-TOFU-ENCRYPTION", Instance: "current", Format: "text", Value: []byte(p2), InBundle: true, Generation: 2},
		identity.Secret{Class: "SC-TOFU-ENCRYPTION", Instance: "previous", Format: "text", Value: []byte(p1), InBundle: true, Generation: 1},
	)
	o := open(t, sealWith(t, set).Data, goodPass)
	manifestSchemaValidate(t, o.Manifest)
	var m struct {
		Entries []struct {
			Class      string `json:"class"`
			Instance   string `json:"instance"`
			Path       string `json:"path"`
			Generation int    `json:"generation"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(o.Manifest, &m); err != nil {
		t.Fatal(err)
	}
	gens := map[string]int{}
	paths := map[string]string{}
	for _, e := range m.Entries {
		if e.Class == "SC-TOFU-ENCRYPTION" {
			gens[e.Instance], paths[e.Instance] = e.Generation, e.Path
			continue
		}
		if e.Generation != 1 {
			t.Errorf("%s/%s has generation %d, want 1 for a secret without a generation", e.Class, e.Instance, e.Generation)
		}
	}
	if len(gens) != 2 || gens["current"] != 2 || gens["previous"] != 1 {
		t.Fatalf("SC-TOFU-ENCRYPTION generations by instance = %v, want current 2 and previous 1", gens)
	}
	for inst, want := range map[string]string{"current": p2, "previous": p1} {
		var got []byte
		for _, f := range o.Files {
			if f.Path == paths[inst] {
				got = f.Data
			}
		}
		if string(got) != want {
			t.Errorf("archive file %s (instance %s) does not hold that instance's passphrase", paths[inst], inst)
		}
	}
}
