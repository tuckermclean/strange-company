// Package identity is plan.install-identity of plan 50 section 14.3: secret generation per plan 35 class, the
// passphrase policy and handling, the age-encrypted recovery bundle and its manifest, the SOPS and cluster-vars
// render, and the plan.bundle-confirm step.
package identity

import (
	"context"
	"net/netip"

	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/bootstrap/redact"
	"strange.company/sc/internal/clock"
)

// Error is a refusal with a stable code.
//
// SC-STUB(F08 50-AC-10)
type Error struct {
	Code    string
	Message string
}

// Error implements error.
//
// SC-STUB(F08 50-AC-10)
func (e *Error) Error() string { return "" }

// Class is one row of contracts/secret-classes.yaml.
//
// SC-STUB(F08 50-AC-10)
type Class struct {
	ID               string
	Actor            string
	Format           string
	InRecoveryBundle bool
	Instances        []Instance
}

// Instance is a registered instance of a class.
//
// SC-STUB(F08 50-AC-10)
type Instance struct {
	Name             string
	InRecoveryBundle bool
}

// Register is the parsed secret-class register.
//
// SC-STUB(F08 50-AC-10)
type Register struct {
	Classes []Class
}

// LoadRegister parses contracts/secret-classes.yaml.
//
// SC-STUB(F08 50-AC-10)
func LoadRegister(data []byte) (*Register, error) { return nil, nil }

// Secret is one generated or supplied secret value.
//
// SC-STUB(F08 50-AC-10)
type Secret struct {
	Class    string
	Instance string
	Format   string
	Value    []byte
	InBundle bool
}

// Set is everything plan.install-identity generates.
//
// SC-STUB(F08 50-AC-10)
type Set struct {
	InstallUID        string
	ClusterRecipient  string
	RecoveryRecipient string
	Secrets           []Secret
}

// Find returns the secret of class and instance, or nil.
//
// SC-STUB(F08 50-AC-10)
func (s *Set) Find(class, instance string) *Secret { return nil }

// Generate creates the install UUID and every secret plan 35 marks as created by plan.install-identity.
//
// SC-STUB(F08 50-AC-10)
func Generate(reg *Register, cluster string) (*Set, error) { return nil, nil }

// CheckPassphrase applies the passphrase policy of plan 50 section 14.3.
//
// SC-STUB(F08 50-AC-10)
func CheckPassphrase(pass, cluster string) error { return nil }

// ReadPassphraseFile reads SC_RECOVERY_PASSPHRASE_FILE.
//
// SC-STUB(F08 50-AC-10)
func ReadPassphraseFile(path string) (string, error) { return "", nil }

// NewPassphrase prompts twice, requires both entries to match and applies the policy.
//
// SC-STUB(F08 50-AC-10)
func NewPassphrase(prompt func() (string, error), cluster string) (string, error) { return "", nil }

// RegisterSecrets registers every secret value and the passphrase with the redactor.
//
// SC-STUB(F08 50-AC-10)
func RegisterSecrets(red *redact.Redactor, set *Set, passphrase string) {}

// SealInput is everything Seal needs.
//
// SC-STUB(F08 50-AC-10)
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
	WorkFactor     int
}

// Sealed is a sealed bundle.
//
// SC-STUB(F08 50-AC-10)
type Sealed struct {
	Data     []byte
	SHA256   string
	Manifest []byte
}

// Seal builds the tar archive and encrypts it with an age scrypt recipient.
//
// SC-STUB(F08 50-AC-10)
func Seal(in SealInput) (*Sealed, error) { return nil, nil }

// ArchiveFile is one member of the bundle archive.
//
// SC-STUB(F08 50-AC-10)
type ArchiveFile struct {
	Path    string
	Data    []byte
	Mode    int64
	ModTime int64
}

// Opened is a decrypted bundle.
//
// SC-STUB(F08 50-AC-10)
type Opened struct {
	Manifest []byte
	Files    []ArchiveFile
}

// Open decrypts a bundle and reads its archive in order.
//
// SC-STUB(F08 50-AC-10)
func Open(data []byte, passphrase string) (*Opened, error) { return nil, nil }

// WriteBundle writes the bundle atomically below blackstartDir/bundles.
//
// SC-STUB(F08 50-AC-10)
func WriteBundle(blackstartDir, cluster string, version int, data []byte) (path string, err error) {
	return "", nil
}

// WriteRecoveryIdentity writes recovery-identity.txt once.
//
// SC-STUB(F08 50-AC-10)
func WriteRecoveryIdentity(blackstartDir string, set *Set) (path string, err error) { return "", nil }

// File is a repository file produced by a render.
//
// SC-STUB(F08 50-AC-34)
type File struct {
	Path string
	Data []byte
}

// Recipients are the two public age recipients.
//
// SC-STUB(F08 50-AC-34)
type Recipients struct {
	Cluster  string
	Recovery string
}

// Encryptor encrypts a plaintext YAML document to the recipients (production: sops).
//
// SC-STUB(F08 50-AC-34)
type Encryptor func(plain []byte, recipients []string) ([]byte, error)

// RenderSOPS renders the generated secrets as SOPS files.
//
// SC-STUB(F08 50-AC-34)
func RenderSOPS(cluster string, set *Set, rcp Recipients, enc Encryptor) ([]File, error) {
	return nil, nil
}

// SOPSRules adds the creation rules of the cluster to an existing .sops.yaml.
//
// SC-STUB(F08 50-AC-34)
func SOPSRules(cluster string, rcp Recipients, existing []byte) ([]byte, error) { return nil, nil }

// ClusterVarsInput is what the cluster-vars render needs.
//
// SC-STUB(F08 50-AC-34)
type ClusterVarsInput struct {
	Vars          map[string]string
	ServiceCIDR   string
	PodCIDR       string
	S3Endpoint    string
	S3PrivateCIDR string
	Resolve       func(host string) ([]netip.Addr, error)
	DNSUpstreams  []string
	PinnedIPs     []netip.Addr
}

// RenderClusterVars renders vars/cluster-vars.yaml (ConfigMap flux-system/cluster-vars).
//
// SC-STUB(F08 50-AC-34)
func RenderClusterVars(in ClusterVarsInput) ([]byte, error) { return nil, nil }

// BundleJournal is the journal's recovery_bundle record (engine.Engine implements it).
//
// SC-STUB(F08 50-AC-10)
type BundleJournal interface {
	RecoveryBundle() engine.RecoveryBundleState
	SetRecoveryBundle(engine.RecoveryBundleState)
}

// BundleConfirmConfig configures plan.bundle-confirm.
//
// SC-STUB(F08 50-AC-10)
type BundleConfirmConfig struct {
	Journal  BundleJournal
	Download func(ctx context.Context) ([]byte, error)
	Prompt   func(ctx context.Context, attempt int) (string, error)
	Clock    clock.Clock
	Attempts int
}

// NewBundleConfirm returns the plan.bundle-confirm step.
//
// SC-STUB(F08 50-AC-10)
func NewBundleConfirm(cfg BundleConfirmConfig) engine.Unit { return nil }
