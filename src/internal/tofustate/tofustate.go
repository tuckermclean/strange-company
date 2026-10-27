// Package tofustate is the OpenTofu state library of sc-bootstrap (plan 06 section 8): backend and encryption
// rendering, the S3 conformance suite C1-C8, state envelope checks, lock inspection and version restore.
package tofustate

import (
	"context"
	"time"

	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/s3client"
)

// Store is the S3 surface the library needs; *s3client.Bucket implements it.
// SC-STUB(F08 06-AC-1)
type Store interface {
	store.S3
	PutObject(ctx context.Context, key string, data []byte, o s3client.PutOptions) (s3client.PutResult, error)
	ListVersions(ctx context.Context, prefix string) ([]s3client.ObjectInfo, error)
	GetVersion(ctx context.Context, key, versionID string) (store.Object, error)
	DeleteVersion(ctx context.Context, key, versionID string) error
	CopyVersion(ctx context.Context, key, versionID string) (s3client.PutResult, error)
	Head(ctx context.Context, key string) (s3client.ObjectInfo, error)
	ServerTime(ctx context.Context) (time.Time, error)
}

// Error is a state-library failure with a stable code (TOFU_STATE_INVALID, TOFU_VERSION_NOT_FOUND, ...).
// SC-STUB(F08 06-AC-8)
type Error struct {
	Code string
	Msg  string
}

// Backend is the S3 backend of one root module (plan 06 section 7.3).
// SC-STUB(F08 06-AC-4)
type Backend struct {
	Bucket         string
	Key            string
	Region         string
	Endpoint       string
	PathStyle      bool
	SkipS3Checksum bool
}

// Encryption is the state-encryption passphrase. The rotation fallback (section 20.3, 06-AC-6) is S7.
// SC-STUB(F08 06-AC-4)
type Encryption struct {
	Current string
}

// Envelope is the plaintext header of an encrypted OpenTofu state object.
// SC-STUB(F08 06-AC-4)
type Envelope struct {
	Serial            int64
	Lineage           string
	EncryptionVersion string
}

// LockInfo is the content of a <key>.tflock object as OpenTofu writes it.
// SC-STUB(F08 06-AC-3)
type LockInfo struct {
	ID        string
	Operation string
	Info      string
	Who       string
	Version   string
	Created   time.Time
	Path      string
}

// LockStatus says whether a module's state lock object exists, and who holds it.
// SC-STUB(F08 06-AC-3)
type LockStatus struct {
	Present bool
	Info    LockInfo
}

// Version is one stored version of a module's state object.
// SC-STUB(F08 06-AC-8)
type Version struct {
	ID       string
	Modified time.Time
	Size     int64
	Current  bool
}

// RestoreResult is the outcome of Restore.
// SC-STUB(F08 06-AC-8)
type RestoreResult struct {
	NewVersionID string
	NoOp         bool
}

// ConformanceParams are the inputs of Conformance.
// SC-STUB(F08 06-AC-1)
type ConformanceParams struct {
	Store     Store
	Cluster   string
	RunID     string
	Clock     clock.Clock
	Probe     LockProbe
	Accept409 bool
}

// LockProbe runs C7: OpenTofu against a hand-written lock object.
// SC-STUB(F08 06-AC-1)
type LockProbe interface {
	Probe(ctx context.Context, p ProbeParams) (ProbeResult, error)
}

// ProbeParams names the scratch backend key and the hand-written lock ID.
// SC-STUB(F08 06-AC-1)
type ProbeParams struct {
	Key    string
	LockID string
}

// ProbeResult is what the tofu process did.
// SC-STUB(F08 06-AC-1)
type ProbeResult struct {
	ExitCode int
	Stderr   string
}

// TestResult is one row of the conformance report.
// SC-STUB(F08 06-AC-1)
type TestResult struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Outcome   string `json:"outcome"`
	Code      string `json:"code,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
}

// Flags are the derived settings of a conformance run.
// SC-STUB(F08 06-AC-1)
type Flags struct {
	IfMatchSupported bool `json:"if_match_supported"`
	SkipS3Checksum   bool `json:"skip_s3_checksum"`
}

// Report is s3-conformance.json.
// SC-STUB(F08 06-AC-1)
type Report struct {
	RunID  string       `json:"run_id"`
	Prefix string       `json:"prefix"`
	Fatal  []string     `json:"fatal"`
	Tests  []TestResult `json:"tests"`
	Flags  Flags        `json:"flags"`
}

// ExecLockProbe is the real C7 probe: it runs the tofu binary in a scratch directory.
// SC-STUB(F08 06-AC-1)
type ExecLockProbe struct {
	Tofu    string
	Dir     string
	Backend Backend
	Env     []string
}

// Error returns the message.
// SC-STUB(F08 06-AC-8)
func (e *Error) Error() string { return "" }

// RenderBackendS3 renders _backend.tf for the s3 backend.
// SC-STUB(F08 06-AC-4)
func RenderBackendS3(b Backend) (string, error) { return "", nil }

// RenderBackendLocal renders _backend.tf for the local backend.
// SC-STUB(F08 06-AC-4)
func RenderBackendLocal(path string) (string, error) { return "", nil }

// RenderEncryption renders the TF_ENCRYPTION HCL.
// SC-STUB(F08 06-AC-4)
func RenderEncryption(e Encryption) (string, error) { return "", nil }

// EncryptionEnv renders the TF_ENCRYPTION=<hcl> environment entry.
// SC-STUB(F08 06-AC-4)
func EncryptionEnv(e Encryption) (string, error) { return "", nil }

// ParseEnvelope accepts only an OpenTofu encrypted state object.
// SC-STUB(F08 06-AC-4)
func ParseEnvelope(raw []byte) (Envelope, error) { return Envelope{}, nil }

// StateKey is <cluster>/<module>/terraform.tfstate.
// SC-STUB(F08 06-AC-3)
func StateKey(cluster, module string) string { return "" }

// LockKey is the state key plus .tflock.
// SC-STUB(F08 06-AC-3)
func LockKey(cluster, module string) string { return "" }

// ReadLock reads the module's lock object.
// SC-STUB(F08 06-AC-3)
func ReadLock(ctx context.Context, st Store, cluster, module string) (LockStatus, error) {
	return LockStatus{}, nil
}

// IsLockError reports whether tofu stderr says the state lock could not be acquired.
// SC-STUB(F08 06-AC-3)
func IsLockError(stderr string) bool { return false }

// LockIDFromStderr extracts the holder's lock ID from a lock error.
// SC-STUB(F08 06-AC-3)
func LockIDFromStderr(stderr string) string { return "" }

// ListVersions lists the versions of the module's state object (not of its lock object), newest first.
// SC-STUB(F08 06-AC-8)
func ListVersions(ctx context.Context, st Store, cluster, module string, limit int) ([]Version, error) {
	return nil, nil
}

// SelectRestoreVersion returns the newest non-current version for which ok returns nil.
// SC-STUB(F08 06-AC-8)
func SelectRestoreVersion(vs []Version, ok func(Version) error) (Version, error) {
	return Version{}, nil
}

// Restore validates version versionID and makes it current by a server-side copy.
// SC-STUB(F08 06-AC-8)
func Restore(ctx context.Context, st Store, cluster, module, versionID string, validate func(raw []byte) error) (RestoreResult, error) {
	return RestoreResult{}, nil
}

// Conformance runs C1-C8 under <cluster>/conformance/<run-id>/ and deletes every version it wrote.
// SC-STUB(F08 06-AC-1)
func Conformance(ctx context.Context, p ConformanceParams) (Report, error) { return Report{}, nil }

// Marshal renders the report as JSON; fatal is always an array.
// SC-STUB(F08 06-AC-1)
func (r Report) Marshal() ([]byte, error) { return nil, nil }

// WriteReport writes the report to path.
// SC-STUB(F08 06-AC-1)
func WriteReport(path string, r Report) error { return nil }

// Probe runs tofu plan -lock-timeout=0s against the scratch backend.
// SC-STUB(F08 06-AC-1)
func (p ExecLockProbe) Probe(ctx context.Context, q ProbeParams) (ProbeResult, error) {
	return ProbeResult{}, nil
}
