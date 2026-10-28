package identity

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"filippo.io/age"

	"strange.company/sc/internal/clock"
)

const (
	defaultWorkFactor = 20
	minWorkFactor     = 18
	maxWorkFactor     = 22
	maxBundleBytes    = 64 << 20
)

// SealInput is everything Seal needs.
type SealInput struct {
	Cluster        string
	Version        int
	Passphrase     string
	Set            *Set
	Clock          clock.Clock
	ToolVersion    string
	ToolboxImage   string
	SourceCommit   string
	PreviousSHA256 string
	Trigger        string
	// WorkFactor is the age scrypt log2(N); zero means 20 (plan 35).
	WorkFactor int
}

// Sealed is a sealed bundle.
type Sealed struct {
	Data     []byte
	SHA256   string
	Manifest []byte
}

var (
	sha256Re = regexp.MustCompile(`^[a-f0-9]{64}$`)
	commitRe = regexp.MustCompile(`^[a-f0-9]{40}$`)
)

var entryFormats = map[string]bool{
	"age-identity": true, "text": true, "env": true, "kubeconfig": true, "ssh-private-key": true,
	"json": true, "yaml": true, "pem": true,
}

type entryJSON struct {
	Class      string `json:"class"`
	Instance   string `json:"instance,omitempty"`
	Path       string `json:"path"`
	Format     string `json:"format"`
	Sha256     string `json:"sha256"`
	SizeBytes  int    `json:"size_bytes"`
	Generation int    `json:"generation"`
	RotatedAt  string `json:"rotated_at"`
}

type manifestJSON struct {
	SchemaVersion string `json:"schema_version"`
	Kind          string `json:"kind"`
	BundleVersion int    `json:"bundle_version"`
	InstallID     string `json:"install_id"`
	InstallUID    string `json:"install_uid,omitempty"`
	CreatedAt     string `json:"created_at"`
	CreatedBy     struct {
		Tool         string `json:"tool"`
		ToolVersion  string `json:"tool_version"`
		ToolboxImage string `json:"toolbox_image"`
	} `json:"created_by"`
	SourceCommit         string  `json:"source_commit"`
	PreviousBundleSha256 *string `json:"previous_bundle_sha256"`
	Reason               struct {
		Trigger string `json:"trigger"`
	} `json:"reason"`
	Encryption struct {
		Scheme           string `json:"scheme"`
		ScryptWorkFactor int    `json:"scrypt_work_factor"`
		ArchiveFormat    string `json:"archive_format"`
	} `json:"encryption"`
	AgeRecipients struct {
		Cluster  string `json:"cluster"`
		Recovery string `json:"recovery"`
	} `json:"age_recipients"`
	Entries []entryJSON `json:"entries"`
}

// entryFile is the archive file name of a bundled secret.
func entryFile(s Secret) string {
	switch s.Format {
	case "age-identity":
		return strings.ToLower(s.Class[strings.LastIndex(s.Class, "-")+1:]) + ".agekey"
	case "env":
		return "credentials.env"
	case "json":
		return "credentials.json"
	case "yaml":
		return "value.yaml"
	case "pem":
		return "value.pem"
	case "kubeconfig":
		return "kubeconfig"
	case "ssh-private-key":
		return "private-key"
	}
	return "value.txt"
}

func entryPath(s Secret) string {
	p := "entries/" + s.Class
	if s.Instance != "" {
		p += "/" + s.Instance
	}
	return p + "/" + entryFile(s)
}

// releaseScryptMemory returns the key-derivation buffer to the system right after the derivation: age's scrypt
// recipient holds 128*r*2^logN bytes (1 GiB at work factor 20) and the garbage collector would otherwise keep it
// while the next derivation allocates its own.
func releaseScryptMemory() { debug.FreeOSMemory() }

// Seal builds the tar archive (manifest.json first, then the entries in path order, mode 0600, mtime 0) and
// encrypts it with an age scrypt recipient.
func Seal(in SealInput) (*Sealed, error) {
	if in.Set == nil || in.Clock == nil {
		return nil, fail("SEAL_INPUT_INVALID", "Seal needs a secret set and a clock")
	}
	wf := in.WorkFactor
	if wf == 0 {
		wf = defaultWorkFactor
	}
	switch {
	case in.Version < 1:
		return nil, fail("SEAL_INPUT_INVALID", "bundle_version must be at least 1")
	case in.Version == 1 && in.PreviousSHA256 != "":
		return nil, fail("SEAL_INPUT_INVALID", "bundle version 1 has no previous bundle")
	case in.Version > 1 && !sha256Re.MatchString(in.PreviousSHA256):
		return nil, fail("SEAL_INPUT_INVALID", "bundle version %d needs the sha256 of the previous bundle", in.Version)
	case wf < minWorkFactor || wf > maxWorkFactor:
		return nil, fail("SEAL_INPUT_INVALID", "the scrypt work factor %d is outside %d..%d", wf, minWorkFactor, maxWorkFactor)
	case !commitRe.MatchString(in.SourceCommit):
		return nil, fail("SEAL_INPUT_INVALID", "source_commit is not a 40-character commit id")
	case in.Trigger == "":
		return nil, fail("SEAL_INPUT_INVALID", "the bundle needs a reason trigger")
	}
	if err := CheckPassphrase(in.Passphrase, in.Cluster); err != nil {
		return nil, err
	}
	created := in.Clock.Now().UTC().Format(time.RFC3339)

	var files []ArchiveFile
	var entries []entryJSON
	haveCluster := false
	for _, s := range in.Set.Secrets {
		if !s.InBundle {
			continue
		}
		if len(bytes.TrimSpace(s.Value)) == 0 {
			return nil, fail("SEAL_INPUT_INVALID", "the secret %s/%s is empty", s.Class, s.Instance)
		}
		if s.Format == "" {
			s.Format = "text"
		}
		if !entryFormats[s.Format] {
			return nil, fail("SEAL_INPUT_INVALID", "the secret %s/%s has the unknown bundle format %q", s.Class, s.Instance, s.Format)
		}
		if s.Class == "SC-AGE-CLUSTER" {
			haveCluster = true
		}
		sum := sha256.Sum256(s.Value)
		gen := s.Generation
		if gen == 0 {
			gen = 1
		}
		p := entryPath(s)
		files = append(files, ArchiveFile{Path: p, Data: s.Value, Mode: 0o600})
		entries = append(entries, entryJSON{
			Class: s.Class, Instance: s.Instance, Path: p, Format: s.Format, Sha256: hex.EncodeToString(sum[:]),
			SizeBytes: len(s.Value), Generation: gen, RotatedAt: created,
		})
	}
	if !haveCluster {
		return nil, fail("SEAL_INPUT_INVALID", "the bundle needs the SC-AGE-CLUSTER identity")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	var m manifestJSON
	m.SchemaVersion, m.Kind, m.BundleVersion, m.InstallID, m.CreatedAt = "v1alpha1", "RecoveryBundleManifest", in.Version, in.Cluster, created
	m.InstallUID = in.Set.InstallUID
	m.CreatedBy.Tool, m.CreatedBy.ToolVersion, m.CreatedBy.ToolboxImage = "sc-bootstrap", in.ToolVersion, in.ToolboxImage
	m.SourceCommit = in.SourceCommit
	if in.PreviousSHA256 != "" {
		prev := in.PreviousSHA256
		m.PreviousBundleSha256 = &prev
	}
	m.Reason.Trigger = in.Trigger
	m.Encryption.Scheme, m.Encryption.ScryptWorkFactor, m.Encryption.ArchiveFormat = "age-scrypt", wf, "tar"
	m.AgeRecipients.Cluster, m.AgeRecipients.Recovery = in.Set.ClusterRecipient, in.Set.RecoveryRecipient
	m.Entries = entries
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fail("SEAL_FAILED", "the manifest cannot be encoded: %v", err)
	}

	var out bytes.Buffer
	rcp, err := age.NewScryptRecipient(in.Passphrase)
	if err != nil {
		return nil, fail("SEAL_FAILED", "the passphrase recipient cannot be created: %v", err)
	}
	rcp.SetWorkFactor(wf)
	aw, err := age.Encrypt(&out, rcp)
	releaseScryptMemory()
	if err != nil {
		return nil, fail("SEAL_FAILED", "age encryption failed: %v", err)
	}
	tw := tar.NewWriter(aw)
	all := append([]ArchiveFile{{Path: "manifest.json", Data: manifest, Mode: 0o600}}, files...)
	for _, f := range all {
		hdr := &tar.Header{Name: f.Path, Mode: 0o600, Size: int64(len(f.Data)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fail("SEAL_FAILED", "archive write failed: %v", err)
		}
		if _, err := tw.Write(f.Data); err != nil {
			return nil, fail("SEAL_FAILED", "archive write failed: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fail("SEAL_FAILED", "archive write failed: %v", err)
	}
	if err := aw.Close(); err != nil {
		return nil, fail("SEAL_FAILED", "age encryption failed: %v", err)
	}
	sum := sha256.Sum256(out.Bytes())
	return &Sealed{Data: out.Bytes(), SHA256: hex.EncodeToString(sum[:]), Manifest: manifest}, nil
}

// ArchiveFile is one member of the bundle archive.
type ArchiveFile struct {
	Path    string
	Data    []byte
	Mode    int64
	ModTime int64
}

// Opened is a decrypted bundle.
type Opened struct {
	Manifest []byte
	Files    []ArchiveFile
}

// Open decrypts a bundle and reads its archive in order. A wrong passphrase is BUNDLE_PASSPHRASE_WRONG; anything
// else that does not decrypt or parse is BUNDLE_CORRUPT. No error repeats the passphrase.
func Open(data []byte, passphrase string) (*Opened, error) {
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, fail("BUNDLE_PASSPHRASE_WRONG", "the passphrase cannot open the recovery bundle")
	}
	id.SetMaxWorkFactor(maxWorkFactor)
	r, err := age.Decrypt(bytes.NewReader(data), id)
	releaseScryptMemory()
	if err != nil {
		var nm *age.NoIdentityMatchError
		if errors.As(err, &nm) || errors.Is(err, age.ErrIncorrectIdentity) {
			return nil, fail("BUNDLE_PASSPHRASE_WRONG", "the passphrase cannot open the recovery bundle")
		}
		return nil, fail("BUNDLE_CORRUPT", "the recovery bundle is not an age file this tool can open")
	}
	plain, err := io.ReadAll(io.LimitReader(r, maxBundleBytes+1))
	if err != nil {
		return nil, fail("BUNDLE_CORRUPT", "the recovery bundle payload is damaged")
	}
	if int64(len(plain)) > maxBundleBytes {
		return nil, fail("BUNDLE_CORRUPT", "the recovery bundle payload exceeds the size limit")
	}
	tr := tar.NewReader(bytes.NewReader(plain))
	o := &Opened{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fail("BUNDLE_CORRUPT", "the recovery bundle archive is damaged")
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, fail("BUNDLE_CORRUPT", "the recovery bundle archive is damaged")
		}
		o.Files = append(o.Files, ArchiveFile{Path: hdr.Name, Data: b, Mode: hdr.Mode, ModTime: hdr.ModTime.Unix()})
		if hdr.Name == "manifest.json" {
			o.Manifest = b
		}
	}
	if o.Manifest == nil {
		return nil, fail("BUNDLE_CORRUPT", "the recovery bundle has no manifest.json")
	}
	return o, nil
}

// writeFile writes data to dir/name through a temporary file in dir (mode 0600, fsync, then rename, or an exclusive
// link when exclusive). When the target exists with identical bytes nothing changes; with other bytes the call
// fails with existsCode.
func writeFile(dir, name string, data []byte, existsCode string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fail("WRITE_FAILED", "cannot create %s: %v", dir, err)
	}
	target := filepath.Join(dir, name)
	same := func() (bool, error) {
		cur, err := os.ReadFile(target)
		if err != nil {
			return false, err
		}
		return bytes.Equal(cur, data), nil
	}
	if ok, err := same(); err == nil {
		if ok {
			return target, nil
		}
		return "", fail(existsCode, "%s exists with different content", target)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fail("WRITE_FAILED", "cannot read %s: %v", target, err)
	}
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return "", fail("WRITE_FAILED", "cannot create a temporary file in %s: %v", dir, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", fail("WRITE_FAILED", "cannot write %s: %v", target, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", fail("WRITE_FAILED", "cannot sync %s: %v", target, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fail("WRITE_FAILED", "cannot close %s: %v", target, err)
	}
	// os.Link fails when the target appeared meanwhile, so a concurrent writer is never overwritten.
	if err := os.Link(tmp.Name(), target); err != nil {
		if ok, serr := same(); serr == nil && ok {
			return target, nil
		}
		if errors.Is(err, fs.ErrExist) {
			return "", fail(existsCode, "%s exists with different content", target)
		}
		return "", fail("WRITE_FAILED", "cannot create %s: %v", target, err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return target, nil
}

// WriteBundle writes the bundle atomically below blackstartDir/bundles; bundle versions are immutable.
func WriteBundle(blackstartDir, cluster string, version int, data []byte) (path string, err error) {
	return writeFile(filepath.Join(blackstartDir, "bundles"), fmt.Sprintf("sc-recovery-%s-v%d.tar.age", cluster, version), data, "BUNDLE_VERSION_EXISTS")
}

// WriteRecoveryIdentity writes recovery-identity.txt once (ADR-0012); the file is the SC-AGE-RECOVERY identity.
func WriteRecoveryIdentity(blackstartDir string, set *Set) (path string, err error) {
	rec := set.Find("SC-AGE-RECOVERY", "")
	if rec == nil || len(rec.Value) == 0 {
		return "", fail("RECOVERY_IDENTITY_MISSING", "the secret set has no SC-AGE-RECOVERY identity")
	}
	return writeFile(blackstartDir, "recovery-identity.txt", rec.Value, "RECOVERY_IDENTITY_EXISTS")
}
