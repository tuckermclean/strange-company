package identity_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/contracts"
)

func manifestSchemaValidate(t testing.TB, manifest []byte) {
	t.Helper()
	schemas, err := contracts.CompileSchemas(filepath.Join(repoRoot(t), "contracts"))
	if err != nil {
		t.Fatal(err)
	}
	sc := schemas["recovery-bundle"]
	if sc == nil {
		t.Fatal("no recovery-bundle schema")
	}
	dec := json.NewDecoder(bytes.NewReader(manifest))
	dec.UseNumber()
	var inst any
	if err := dec.Decode(&inst); err != nil {
		t.Fatalf("manifest is not JSON: %v\n%s", err, manifest)
	}
	if err := sc.Validate(inst); err != nil {
		t.Errorf("manifest does not validate against recovery-bundle.schema.yaml: %v", err)
	}
}

func open(t testing.TB, data []byte, pass string) *identity.Opened {
	t.Helper()
	o, err := identity.Open(data, pass)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if o == nil {
		t.Fatal("Open returned nothing")
	}
	return o
}

// Plan 50 section 14.3 / plan 35 Sealer: the bundle is an age file with a scrypt recipient, the SHA256 is that of the
// bytes, and the manifest validates against contracts/recovery-bundle.schema.yaml.
func TestSeal_AgeScryptBundleWithValidManifest_50AC10(t *testing.T) {
	set, sealed := sealedV1(t)
	if !bytes.HasPrefix(sealed.Data, []byte("age-encryption.org/v1\n-> scrypt ")) {
		t.Errorf("bundle does not start with an age scrypt header: %q", sealed.Data[:min(len(sealed.Data), 40)])
	}
	sum := sha256.Sum256(sealed.Data)
	if sealed.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("Sealed.SHA256 %q is not the sha256 of Data", sealed.SHA256)
	}
	o := open(t, sealed.Data, goodPass)
	if !bytes.Equal(o.Manifest, sealed.Manifest) {
		t.Error("the manifest inside the archive differs from Sealed.Manifest")
	}
	manifestSchemaValidate(t, o.Manifest)
	var m struct {
		BundleVersion int     `json:"bundle_version"`
		InstallID     string  `json:"install_id"`
		Previous      *string `json:"previous_bundle_sha256"`
		Reason        struct {
			Trigger string `json:"trigger"`
		} `json:"reason"`
		Encryption struct {
			Scheme string `json:"scheme"`
			WF     int    `json:"scrypt_work_factor"`
		} `json:"encryption"`
		Recipients struct {
			Cluster  string `json:"cluster"`
			Recovery string `json:"recovery"`
		} `json:"age_recipients"`
		SourceCommit string `json:"source_commit"`
		CreatedAt    string `json:"created_at"`
	}
	if err := json.Unmarshal(o.Manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.BundleVersion != 1 || m.InstallID != cluster || m.Previous != nil || m.Reason.Trigger != "initial-bootstrap" {
		t.Errorf("manifest header %+v", m)
	}
	if m.Encryption.Scheme != "age-scrypt" || m.Encryption.WF != 18 {
		t.Errorf("encryption %+v, want age-scrypt at the requested work factor 18", m.Encryption)
	}
	if m.Recipients.Cluster != set.ClusterRecipient || m.Recipients.Recovery != set.RecoveryRecipient {
		t.Errorf("manifest recipients %+v do not match the generated set", m.Recipients)
	}
	if m.CreatedAt != "2026-10-27T09:00:00Z" {
		t.Errorf("created_at %q, want the injected clock's time", m.CreatedAt)
	}
}

// Plan 35: work factor 20 unless told otherwise.
func TestSeal_DefaultWorkFactorIs20_50AC10(t *testing.T) {
	set := genSet(t)
	sealed, err := identity.Seal(identity.SealInput{
		Cluster: cluster, Version: 1, Passphrase: goodPass, Set: set,
		Clock:       clock.NewFake(time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)),
		ToolVersion: "0.1.0", ToolboxImage: "local-build:sha256:" + strings.Repeat("a", 64),
		SourceCommit: strings.Repeat("b", 40), Trigger: "initial-bootstrap",
	})
	if err != nil || sealed == nil {
		t.Fatalf("Seal: %v", err)
	}
	var m struct {
		Encryption struct {
			WF int `json:"scrypt_work_factor"`
		} `json:"encryption"`
	}
	if err := json.Unmarshal(sealed.Manifest, &m); err != nil || m.Encryption.WF != 20 {
		t.Errorf("work factor %d (%v), want 20", m.Encryption.WF, err)
	}
	manifestSchemaValidate(t, sealed.Manifest)
}

// contracts/recovery-bundle.schema.yaml semantic checks: every entry digest equals the sha256 of its file, no entry
// is empty, every class with inRecoveryBundle has an entry, the manifest never holds a value.
func TestSeal_EntriesMatchFilesAndCoverBundleClasses_50AC10(t *testing.T) {
	set, sealed := sealedV1(t)
	o := open(t, sealed.Data, goodPass)
	var m struct {
		Entries []struct {
			Class      string `json:"class"`
			Instance   string `json:"instance"`
			Path       string `json:"path"`
			Sha256     string `json:"sha256"`
			Size       int    `json:"size_bytes"`
			Generation int    `json:"generation"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(o.Manifest, &m); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, f := range o.Files {
		files[f.Path] = f.Data
	}
	seen := map[string]bool{}
	for _, e := range m.Entries {
		data, ok := files[e.Path]
		if !ok {
			t.Errorf("entry %s: no file %s in the archive", e.Class, e.Path)
			continue
		}
		sum := sha256.Sum256(data)
		if e.Sha256 != hex.EncodeToString(sum[:]) || e.Size != len(data) || e.Size < 1 {
			t.Errorf("entry %s: sha256/size do not match the file", e.Path)
		}
		if e.Generation != 1 {
			t.Errorf("entry %s generation %d, want 1 for bundle v1", e.Path, e.Generation)
		}
		seen[e.Class+"/"+e.Instance] = true
	}
	for _, s := range set.Secrets {
		if s.InBundle != seen[s.Class+"/"+s.Instance] {
			t.Errorf("%s/%s: InBundle=%v but manifest entry present=%v", s.Class, s.Instance, s.InBundle, seen[s.Class+"/"+s.Instance])
		}
	}
	if !seen["SC-INFRA-PROVIDER/"] {
		t.Error("the operator-supplied SC-INFRA-PROVIDER secret was not sealed")
	}
	if !seen["SC-RUNNER-ADMIN/breakglass"] {
		t.Error("SC-RUNNER-ADMIN/breakglass has no entry")
	}
	for _, s := range set.Secrets {
		if bytes.Contains(o.Manifest, bytes.TrimSpace(s.Value)) {
			t.Errorf("the manifest contains the value of %s/%s", s.Class, s.Instance)
		}
	}
}

// ADR-0012: the offline recovery identity is not in the bundle (a bundle that held it would defeat it), and the
// passphrase is not either.
func TestSeal_RecoveryIdentityAndPassphraseNotInBundle_50AC10(t *testing.T) {
	set, sealed := sealedV1(t)
	o := open(t, sealed.Data, goodPass)
	rec := set.Find("SC-AGE-RECOVERY", "")
	if rec == nil {
		t.Fatal("no SC-AGE-RECOVERY")
	}
	for _, f := range o.Files {
		if bytes.Contains(f.Data, bytes.TrimSpace(rec.Value)) {
			t.Errorf("%s contains the recovery identity", f.Path)
		}
		if bytes.Contains(f.Data, []byte(goodPass)) {
			t.Errorf("%s contains the passphrase", f.Path)
		}
	}
}

// The SC-AGE-CLUSTER entry's public key equals age_recipients.cluster (recovery-bundle contract).
func TestSeal_ClusterEntryPublicKeyMatchesRecipient_50AC10(t *testing.T) {
	set, sealed := sealedV1(t)
	o := open(t, sealed.Data, goodPass)
	found := false
	for _, f := range o.Files {
		if strings.HasPrefix(f.Path, "entries/SC-AGE-CLUSTER/") {
			found = true
			if !bytes.Contains(f.Data, []byte("# public key: "+set.ClusterRecipient)) {
				t.Errorf("%s does not carry the cluster recipient", f.Path)
			}
		}
	}
	if !found {
		t.Error("no entries/SC-AGE-CLUSTER/ file")
	}
}

// Plan 35 Sealer: manifest first, then entries sorted, mode 0600, mtime 0 (reproducible archive).
func TestSeal_ArchiveLayoutIsReproducible_50AC10(t *testing.T) {
	_, sealed := sealedV1(t)
	o := open(t, sealed.Data, goodPass)
	if len(o.Files) < 2 || o.Files[0].Path != "manifest.json" {
		t.Fatalf("first archive member %v, want manifest.json", o.Files)
	}
	var rest []string
	for _, f := range o.Files[1:] {
		rest = append(rest, f.Path)
		if !strings.HasPrefix(f.Path, "entries/") && !strings.HasPrefix(f.Path, "public/") {
			t.Errorf("unexpected archive member %s", f.Path)
		}
		if path.Clean(f.Path) != f.Path || strings.Contains(f.Path, "..") {
			t.Errorf("unclean archive path %q", f.Path)
		}
	}
	if !sort.StringsAreSorted(rest) {
		t.Errorf("archive members are not sorted: %v", rest)
	}
	for _, f := range o.Files {
		if f.Mode&0o777 != 0o600 {
			t.Errorf("%s mode %o, want 0600", f.Path, f.Mode&0o777)
		}
		if f.ModTime != 0 {
			t.Errorf("%s mtime %d, want 0", f.Path, f.ModTime)
		}
	}
}

// Plan 50 section 14.3 / ADR-0012: a wrong passphrase and a damaged bundle do not open.
func TestOpen_WrongPassphraseAndTamperingFail_50AC10(t *testing.T) {
	_, sealed := sealedV1(t)
	o, err := identity.Open(sealed.Data, goodPass+" wrong")
	if err == nil {
		t.Fatalf("a wrong passphrase opened the bundle: %v", o)
	}
	var ie *identity.Error
	if !errors.As(err, &ie) || ie.Code != "BUNDLE_PASSPHRASE_WRONG" {
		t.Errorf("error %v, want identity.Error BUNDLE_PASSPHRASE_WRONG", err)
	}
	if strings.Contains(err.Error(), goodPass) {
		t.Error("the error repeats the passphrase")
	}
	bad := append([]byte(nil), sealed.Data...)
	bad[len(bad)-3] ^= 0xff
	if _, err := identity.Open(bad, goodPass); err == nil {
		t.Error("a bundle with a flipped payload byte opened")
	}
	if _, err := identity.Open([]byte("not an age file"), goodPass); err == nil {
		t.Error("garbage opened")
	}
}

// contracts/recovery-bundle.schema.yaml: bundle_version is monotonic, previous_bundle_sha256 is the sha256 of the
// previous object (null for version 1), and the work factor is at least 18.
func TestSeal_RejectsInvalidInputs_50AC10(t *testing.T) {
	set, _ := sealedV1(t)
	base := func() identity.SealInput {
		return identity.SealInput{
			Cluster: cluster, Version: 1, Passphrase: goodPass, Set: set,
			Clock:       clock.NewFake(time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)),
			ToolVersion: "0.1.0", ToolboxImage: "local-build:sha256:" + strings.Repeat("a", 64),
			SourceCommit: strings.Repeat("b", 40), Trigger: "initial-bootstrap", WorkFactor: 18,
		}
	}
	cases := map[string]func(*identity.SealInput){
		"version 0":                    func(i *identity.SealInput) { i.Version = 0 },
		"version 2 without a previous": func(i *identity.SealInput) { i.Version = 2 },
		"version 1 with a previous":    func(i *identity.SealInput) { i.PreviousSHA256 = strings.Repeat("c", 64) },
		"work factor below 18":         func(i *identity.SealInput) { i.WorkFactor = 10 },
		"weak passphrase":              func(i *identity.SealInput) { i.Passphrase = "short" },
		"passphrase has the cluster":   func(i *identity.SealInput) { i.Passphrase = "some words with lab-one inside them" },
		"no cluster age identity": func(i *identity.SealInput) {
			var keep []identity.Secret
			for _, s := range set.Secrets {
				if s.Class != "SC-AGE-CLUSTER" {
					keep = append(keep, s)
				}
			}
			cp := *set
			cp.Secrets = keep
			i.Set = &cp
		},
		"empty secret": func(i *identity.SealInput) {
			cp := *set
			cp.Secrets = append(append([]identity.Secret(nil), set.Secrets...), identity.Secret{Class: "SC-DNS-API", Format: "text", InBundle: true})
			i.Set = &cp
		},
	}
	for name, mut := range cases {
		in := base()
		mut(&in)
		if s, err := identity.Seal(in); err == nil {
			t.Errorf("%s: Seal succeeded (%v)", name, s != nil)
		}
	}
}

// Version 2 chains to version 1 by digest and bumps the manifest version.
func TestSeal_Version2ChainsToPrevious_50AC10(t *testing.T) {
	set, v1 := sealedV1(t)
	v2, err := identity.Seal(identity.SealInput{
		Cluster: cluster, Version: 2, Passphrase: goodPass, Set: set,
		Clock:       clock.NewFake(time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)),
		ToolVersion: "0.1.0", ToolboxImage: "local-build:sha256:" + strings.Repeat("a", 64),
		SourceCommit: strings.Repeat("b", 40), PreviousSHA256: v1.SHA256, Trigger: "manual", WorkFactor: 18,
	})
	if err != nil || v2 == nil {
		t.Fatalf("Seal v2: %v", err)
	}
	var m struct {
		Version  int     `json:"bundle_version"`
		Previous *string `json:"previous_bundle_sha256"`
	}
	if err := json.Unmarshal(v2.Manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.Version != 2 || m.Previous == nil || *m.Previous != v1.SHA256 {
		t.Errorf("manifest %+v, want version 2 chained to %s", m, v1.SHA256)
	}
	manifestSchemaValidate(t, v2.Manifest)
}

// Plan 50 section 14.3: write the bundle atomically (tmp, fsync, rename) below /blackstart/<cluster>/bundles/ and
// recovery-identity.txt once (ADR-0012).
func TestWriteBundle_AtomicAndExclusiveIdentity_50AC10(t *testing.T) {
	set, sealed := sealedV1(t)
	dir := t.TempDir()
	p, err := identity.WriteBundle(dir, cluster, 1, sealed.Data)
	if err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	if want := filepath.Join(dir, "bundles", "sc-recovery-"+cluster+"-v1.tar.age"); p != want {
		t.Errorf("path %q, want %q", p, want)
	}
	got, err := os.ReadFile(p)
	if err != nil || !bytes.Equal(got, sealed.Data) {
		t.Errorf("the bundle on disk differs (%v)", err)
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("bundle mode %v (%v), want 0600", st.Mode(), err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "bundles"))
	if len(entries) != 1 {
		t.Errorf("bundles/ holds %d files, want only the bundle (no temporary file left)", len(entries))
	}
	if _, err := identity.WriteBundle(dir, cluster, 1, sealed.Data); err != nil {
		t.Errorf("rewriting identical bytes failed: %v", err)
	}
	if _, err := identity.WriteBundle(dir, cluster, 1, append(append([]byte(nil), sealed.Data...), 0)); err == nil {
		t.Error("overwriting version 1 with different bytes succeeded")
	}

	ip, err := identity.WriteRecoveryIdentity(dir, set)
	if err != nil {
		t.Fatalf("WriteRecoveryIdentity: %v", err)
	}
	if want := filepath.Join(dir, "recovery-identity.txt"); ip != want {
		t.Errorf("identity path %q, want %q", ip, want)
	}
	b, _ := os.ReadFile(ip)
	if rec := set.Find("SC-AGE-RECOVERY", ""); rec == nil || !bytes.Equal(b, rec.Value) {
		t.Error("recovery-identity.txt does not hold the SC-AGE-RECOVERY identity")
	}
	if st, err := os.Stat(ip); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("identity mode %v (%v), want 0600", st.Mode(), err)
	}
	if _, err := identity.WriteRecoveryIdentity(dir, set); err != nil {
		t.Errorf("rewriting the same identity failed: %v", err)
	}
	other := genSet(t)
	if _, err := identity.WriteRecoveryIdentity(dir, other); err == nil {
		t.Error("a different identity replaced recovery-identity.txt (written once)")
	}
}
