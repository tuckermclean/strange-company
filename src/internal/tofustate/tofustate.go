// Package tofustate is the OpenTofu state library of sc-bootstrap (plan 06 section 8): backend and encryption
// rendering, the S3 conformance suite C1-C8, state envelope checks, lock inspection and version restore.
package tofustate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/s3client"
)

// Store is the S3 surface the library needs; *s3client.Bucket implements it.
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

var _ Store = (*s3client.Bucket)(nil)

// Error is a state-library failure with a stable code (TOFU_STATE_INVALID, TOFU_VERSION_NOT_FOUND, ...).
type Error struct {
	Code string
	Msg  string
}

// Error returns the code and message.
func (e *Error) Error() string {
	if e.Msg == "" {
		return e.Code
	}
	return e.Code + ": " + e.Msg
}

// Backend is the S3 backend of one root module (plan 06 section 7.3).
type Backend struct {
	Bucket         string
	Key            string
	Region         string
	Endpoint       string
	PathStyle      bool
	SkipS3Checksum bool
}

// Encryption is the state-encryption passphrase. The rotation fallback (section 20.3, 06-AC-6) is S7.
type Encryption struct {
	Current string
}

// Envelope is the plaintext header of an encrypted OpenTofu state object.
type Envelope struct {
	Serial            int64
	Lineage           string
	EncryptionVersion string
}

// LockInfo is the content of a <key>.tflock object as OpenTofu writes it.
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
type LockStatus struct {
	Present bool
	Info    LockInfo
}

// Version is one stored version of a module's state object.
type Version struct {
	ID       string
	Modified time.Time
	Size     int64
	Current  bool
}

// RestoreResult is the outcome of Restore.
type RestoreResult struct {
	NewVersionID string
	NoOp         bool
}

// --- rendering

// hclSafe reports why s cannot sit inside an HCL quoted string without being interpreted ("" when it can).
func hclSafe(s string) string {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "control character"
		}
		if r == '"' || r == '\\' {
			return "quote or backslash"
		}
	}
	if strings.Contains(s, "${") || strings.Contains(s, "%{") {
		return "template sequence"
	}
	return ""
}

func renderErr(field, why string) error {
	return &Error{Code: "TOFU_RENDER_INVALID", Msg: field + ": " + why}
}

func checkField(field, v string) error {
	if v == "" {
		return renderErr(field, "empty")
	}
	if why := hclSafe(v); why != "" {
		return renderErr(field, why)
	}
	return nil
}

// RenderBackendS3 renders _backend.tf for the s3 backend.
func RenderBackendS3(b Backend) (string, error) {
	for _, f := range []struct{ n, v string }{{"bucket", b.Bucket}, {"key", b.Key}, {"region", b.Region}, {"endpoint", b.Endpoint}} {
		if err := checkField(f.n, f.v); err != nil {
			return "", err
		}
	}
	u, err := url.Parse(b.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", renderErr("endpoint", "must be an http or https URL")
	}
	return fmt.Sprintf(`terraform {
  backend "s3" {
    bucket                      = %q
    key                         = %q
    region                      = %q
    endpoints                   = { s3 = %q }
    use_path_style              = %t
    use_lockfile                = true
    skip_credentials_validation = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    skip_metadata_api_check     = true
    skip_s3_checksum            = %t
  }
}
`, b.Bucket, b.Key, b.Region, b.Endpoint, b.PathStyle, b.SkipS3Checksum), nil
}

// RenderBackendLocal renders _backend.tf for the local backend.
func RenderBackendLocal(path string) (string, error) {
	if err := checkField("path", path); err != nil {
		return "", err
	}
	return fmt.Sprintf("terraform {\n  backend \"local\" {\n    path = %q\n  }\n}\n", path), nil
}

const minPassphrase = 16

// RenderEncryption renders the TF_ENCRYPTION HCL.
func RenderEncryption(e Encryption) (string, error) {
	if len(e.Current) < minPassphrase {
		return "", renderErr("passphrase", fmt.Sprintf("shorter than %d characters", minPassphrase))
	}
	if why := hclSafe(e.Current); why != "" {
		return "", renderErr("passphrase", why)
	}
	return fmt.Sprintf(`key_provider "pbkdf2" "current" {
  passphrase = %q
}

method "aes_gcm" "current" {
  keys = key_provider.pbkdf2.current
}

state {
  method   = method.aes_gcm.current
  enforced = true
}

plan {
  method   = method.aes_gcm.current
  enforced = true
}
`, e.Current), nil
}

// EncryptionEnv renders the TF_ENCRYPTION=<hcl> environment entry.
func EncryptionEnv(e Encryption) (string, error) {
	h, err := RenderEncryption(e)
	if err != nil {
		return "", err
	}
	return "TF_ENCRYPTION=" + h, nil
}

// --- state envelope

// ParseEnvelope accepts only an OpenTofu encrypted state object.
func ParseEnvelope(raw []byte) (Envelope, error) {
	invalid := func(format string, a ...any) (Envelope, error) {
		return Envelope{}, &Error{Code: "TOFU_STATE_INVALID", Msg: fmt.Sprintf(format, a...)}
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return invalid("not a JSON object")
	}
	var data string
	if err := json.Unmarshal(doc["encrypted_data"], &data); err != nil || data == "" {
		return invalid("no encrypted_data: not an OpenTofu encryption envelope")
	}
	if _, ok := doc["resources"]; ok {
		return invalid("plaintext state fields next to encrypted_data")
	}
	var env Envelope
	if v, ok := doc["serial"]; ok {
		if err := json.Unmarshal(v, &env.Serial); err != nil {
			return invalid("serial is not a number")
		}
	}
	if v, ok := doc["lineage"]; ok {
		if err := json.Unmarshal(v, &env.Lineage); err != nil {
			return invalid("lineage is not a string")
		}
	}
	if v, ok := doc["encryption_version"]; ok {
		if err := json.Unmarshal(v, &env.EncryptionVersion); err != nil {
			return invalid("encryption_version is not a string")
		}
	}
	return env, nil
}

// --- keys and locks

var (
	moduleRe  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	clusterRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	lockIDRe  = regexp.MustCompile(`(?m)^\W*ID:\s+([0-9A-Za-z-]+)\s*$`)
)

// StateKey is <cluster>/<module>/terraform.tfstate.
func StateKey(cluster, module string) string { return cluster + "/" + module + "/terraform.tfstate" }

// LockKey is the state key plus .tflock.
func LockKey(cluster, module string) string { return StateKey(cluster, module) + ".tflock" }

func checkNames(cluster, module string) error {
	if !clusterRe.MatchString(cluster) {
		return &Error{Code: "TOFU_NAME_INVALID", Msg: fmt.Sprintf("cluster name %q", cluster)}
	}
	if !moduleRe.MatchString(module) {
		return &Error{Code: "TOFU_NAME_INVALID", Msg: fmt.Sprintf("module name %q", module)}
	}
	return nil
}

// ReadLock reads the module's lock object.
func ReadLock(ctx context.Context, st Store, cluster, module string) (LockStatus, error) {
	if err := checkNames(cluster, module); err != nil {
		return LockStatus{}, err
	}
	o, err := st.Get(ctx, LockKey(cluster, module))
	if store.Is(err, store.NotFound) {
		return LockStatus{}, nil
	}
	if err != nil {
		return LockStatus{}, err
	}
	var info LockInfo
	if err := json.Unmarshal(o.Data, &info); err != nil {
		return LockStatus{}, &Error{Code: "TOFU_LOCK_INVALID", Msg: "the lock object is not OpenTofu lock JSON"}
	}
	return LockStatus{Present: true, Info: info}, nil
}

const lockErrorText = "Error acquiring the state lock"

// IsLockError reports whether tofu stderr says the state lock could not be acquired.
func IsLockError(stderr string) bool { return strings.Contains(stderr, lockErrorText) }

// LockIDFromStderr extracts the holder's lock ID from a lock error.
func LockIDFromStderr(stderr string) string {
	if !IsLockError(stderr) {
		return ""
	}
	if m := lockIDRe.FindStringSubmatch(stderr); m != nil {
		return m[1]
	}
	return ""
}

// --- versions

// ListVersions lists the versions of the module's state object (not of its lock object), newest first.
func ListVersions(ctx context.Context, st Store, cluster, module string, limit int) ([]Version, error) {
	if err := checkNames(cluster, module); err != nil {
		return nil, err
	}
	key := StateKey(cluster, module)
	all, err := st.ListVersions(ctx, key)
	if err != nil {
		return nil, err
	}
	var out []Version
	for _, o := range all {
		if o.Key != key || o.IsDeleteMarker {
			continue
		}
		out = append(out, Version{ID: o.VersionID, Modified: o.LastModified, Size: o.Size, Current: o.IsLatest})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SelectRestoreVersion returns the newest non-current version for which ok returns nil.
func SelectRestoreVersion(vs []Version, ok func(Version) error) (Version, error) {
	var last error
	for _, v := range vs {
		if v.Current {
			continue
		}
		if err := ok(v); err != nil {
			last = err
			continue
		}
		return v, nil
	}
	msg := "no earlier version of the state object exists"
	if last != nil {
		msg = "no earlier version passes validation (last: " + last.Error() + ")"
	}
	return Version{}, &Error{Code: "TOFU_NO_RESTORABLE_VERSION", Msg: msg}
}

// Restore validates version versionID and makes it current by a server-side copy.
func Restore(ctx context.Context, st Store, cluster, module, versionID string, validate func(raw []byte) error) (RestoreResult, error) {
	if err := checkNames(cluster, module); err != nil {
		return RestoreResult{}, err
	}
	key := StateKey(cluster, module)
	old, err := st.GetVersion(ctx, key, versionID)
	if store.Is(err, store.NotFound) {
		return RestoreResult{}, &Error{Code: "TOFU_VERSION_NOT_FOUND", Msg: fmt.Sprintf("%s has no version %q", key, versionID)}
	}
	if err != nil {
		return RestoreResult{}, err
	}
	if validate != nil {
		if err := validate(old.Data); err != nil {
			return RestoreResult{}, &Error{Code: "TOFU_STATE_INVALID", Msg: "version " + versionID + " is not restorable: " + err.Error()}
		}
	}
	cur, err := st.Get(ctx, key)
	if err != nil && !store.Is(err, store.NotFound) {
		return RestoreResult{}, err
	}
	if err == nil && bytes.Equal(cur.Data, old.Data) {
		return RestoreResult{NoOp: true}, nil
	}
	res, err := st.CopyVersion(ctx, key, versionID)
	if err != nil {
		return RestoreResult{}, err
	}
	return RestoreResult{NewVersionID: res.VersionID}, nil
}

// --- conformance (plan 06 section 14.3)

// ConformanceParams are the inputs of Conformance.
type ConformanceParams struct {
	Store     Store
	Cluster   string
	RunID     string
	Clock     clock.Clock
	Probe     LockProbe
	Accept409 bool
	// Cleanup is the store that deletes the conformance object versions afterwards (plan 06 section 14.3: the
	// admin credential; the tofu-state credential holds no DeleteObjectVersion).
	// SC-STUB(F08 06-AC-1)
	Cleanup Store
}

// LockProbe runs C7: OpenTofu against a hand-written lock object.
type LockProbe interface {
	Probe(ctx context.Context, p ProbeParams) (ProbeResult, error)
}

// ProbeParams names the scratch backend key and the hand-written lock ID.
type ProbeParams struct {
	Key    string
	LockID string
}

// ProbeResult is what the tofu process did.
type ProbeResult struct {
	ExitCode int
	Stderr   string
}

// TestResult is one row of the conformance report.
type TestResult struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Outcome   string `json:"outcome"`
	Code      string `json:"code,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
}

// Flags are the derived settings of a conformance run.
type Flags struct {
	IfMatchSupported bool `json:"if_match_supported"`
	SkipS3Checksum   bool `json:"skip_s3_checksum"`
}

// Report is s3-conformance.json.
type Report struct {
	RunID  string       `json:"run_id"`
	Prefix string       `json:"prefix"`
	Fatal  []string     `json:"fatal"`
	Tests  []TestResult `json:"tests"`
	Flags  Flags        `json:"flags"`
	// CleanupFailures lists the objects or listings the cleanup could not delete; it does not change Fatal.
	// SC-STUB(F08 06-AC-1)
	CleanupFailures []string `json:"cleanup_failures,omitempty"`
}

const (
	codeConditionalPut = "S3_CONDITIONAL_PUT_UNSUPPORTED"
	codeLock           = "PF_S3_LOCK_UNSUPPORTED"
	codeVersioning     = "S3_VERSIONING_UNSUPPORTED"
	codeChecksum       = "S3_CHECKSUM_INCOMPATIBLE"
	codeLockfile       = "TOFU_LOCKFILE_NOT_HONORED"
	codeSkew           = "S3_CLOCK_SKEW"
	maxSkew            = 60 * time.Second
	c3Writers          = 8
)

type suite struct {
	ctx    context.Context
	p      ConformanceParams
	clk    clock.Clock
	prefix string
	rep    *Report
}

func (s *suite) fatal(code string) {
	for _, f := range s.rep.Fatal {
		if f == code {
			return
		}
	}
	s.rep.Fatal = append(s.rep.Fatal, code)
}

// row runs one test and records it; fn returns the outcome and, on failure, the code.
func (s *suite) row(id, name string, fn func() (outcome, code string)) string {
	t0 := s.clk.Now()
	outcome, code := fn()
	s.rep.Tests = append(s.rep.Tests, TestResult{ID: id, Name: name, Outcome: outcome, Code: code, LatencyMS: s.clk.Now().Sub(t0).Milliseconds()})
	if outcome == "fail" && code != "" {
		s.fatal(code)
	}
	return outcome
}

// refused reports whether err is the store's answer to a second conditional create.
func (s *suite) refused(err error) bool {
	if !store.Is(err, store.Exists) {
		return false
	}
	var se *s3client.Error
	if errors.As(err, &se) && se.Status == 409 && !s.p.Accept409 {
		return false
	}
	return true
}

// Conformance runs C1-C8 under <cluster>/conformance/<run-id>/ and deletes every version it wrote.
func Conformance(ctx context.Context, p ConformanceParams) (Report, error) {
	if p.Store == nil {
		return Report{}, errors.New("tofustate: Conformance needs a Store")
	}
	if !clusterRe.MatchString(p.Cluster) || p.RunID == "" || strings.ContainsAny(p.RunID, "/ \t\n") {
		return Report{}, errors.New("tofustate: Conformance needs a valid cluster name and run id")
	}
	clk := p.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	prefix := p.Cluster + "/conformance/" + p.RunID + "/"
	rep := Report{RunID: p.RunID, Prefix: prefix, Fatal: []string{}, Tests: []TestResult{}}
	s := &suite{ctx: ctx, p: p, clk: clk, prefix: prefix, rep: &rep}
	defer func() { s.cleanup() }()

	k1, k2, k3, k4, k5 := prefix+"k1", prefix+"k2", prefix+"k3", prefix+"k4", prefix+"k5"

	c1 := s.row("C1", "conditional create (If-None-Match: *) of an absent key", func() (string, string) {
		if _, err := p.Store.PutIfAbsent(ctx, k1, []byte("one")); err != nil {
			return "fail", codeConditionalPut
		}
		return "pass", ""
	})
	if c1 != "pass" {
		s.skip("C2", "second conditional create is refused with 412")
		s.skip("C3", "eight concurrent conditional creates: exactly one wins")
	} else {
		s.row("C2", "second conditional create is refused with 412", func() (string, string) {
			_, err := p.Store.PutIfAbsent(ctx, k1, []byte("two"))
			if !s.refused(err) {
				return "fail", codeLock
			}
			if o, err := p.Store.Get(ctx, k1); err != nil || string(o.Data) != "one" {
				return "fail", codeLock
			}
			return "pass", ""
		})
		s.row("C3", "eight concurrent conditional creates: exactly one wins", func() (string, string) {
			errs := make([]error, c3Writers)
			var wg sync.WaitGroup
			for i := range errs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, errs[i] = p.Store.PutIfAbsent(ctx, k2, []byte(fmt.Sprintf("writer-%d", i)))
				}()
			}
			wg.Wait()
			wins := 0
			for _, err := range errs {
				switch {
				case err == nil:
					wins++
				case !s.refused(err):
					return "fail", codeLock
				}
			}
			if wins != 1 {
				return "fail", codeLock
			}
			return "pass", ""
		})
	}

	s.row("C4", "overwrites create versions and old versions stay readable", func() (string, string) {
		first, err := p.Store.PutObject(ctx, k3, []byte("first"), s3client.PutOptions{})
		if err != nil || first.VersionID == "" {
			return "fail", codeVersioning
		}
		if _, err := p.Store.PutObject(ctx, k3, []byte("second"), s3client.PutOptions{}); err != nil {
			return "fail", codeVersioning
		}
		vs, err := p.Store.ListVersions(ctx, k3)
		n := 0
		for _, v := range vs {
			if v.Key == k3 {
				n++
			}
		}
		if err != nil || n < 2 {
			return "fail", codeVersioning
		}
		o, err := p.Store.GetVersion(ctx, k3, first.VersionID)
		if err != nil || string(o.Data) != "first" {
			return "fail", codeVersioning
		}
		return "pass", ""
	})

	s.row("C5", "If-Match: right ETag accepted, wrong ETag refused", func() (string, string) {
		r, err := p.Store.PutObject(ctx, k4, []byte("v1"), s3client.PutOptions{})
		if err != nil || r.ETag == "" {
			rep.Flags.IfMatchSupported = false
			return "warn", "S3_IF_MATCH_UNKNOWN"
		}
		r2, err := p.Store.PutObject(ctx, k4, []byte("v2"), s3client.PutOptions{IfMatch: r.ETag})
		if err != nil {
			rep.Flags.IfMatchSupported = false
			return "warn", "S3_IF_MATCH_UNSUPPORTED"
		}
		_, err = p.Store.PutObject(ctx, k4, []byte("v3"), s3client.PutOptions{IfMatch: "00000000000000000000000000000000-" + r2.ETag})
		if !store.Is(err, store.PreconditionFailed) {
			rep.Flags.IfMatchSupported = false
			return "warn", "S3_IF_MATCH_UNSUPPORTED"
		}
		rep.Flags.IfMatchSupported = true
		return "pass", ""
	})

	s.row("C6", "writes with and without SDK checksums", func() (string, string) {
		_, errSum := p.Store.PutObject(ctx, k5, []byte("checksummed"), s3client.PutOptions{Checksum: true})
		_, errPlain := p.Store.PutObject(ctx, k5, []byte("plain"), s3client.PutOptions{})
		switch {
		case errSum != nil && errPlain != nil:
			return "fail", codeChecksum
		case errSum != nil:
			rep.Flags.SkipS3Checksum = true
		}
		return "pass", ""
	})

	s.row("C7", "OpenTofu honors a hand-written lock file", func() (string, string) {
		if p.Probe == nil {
			return "skip", ""
		}
		return s.lockfile()
	})

	s.row("C8", "server clock within 60 seconds of the local clock", func() (string, string) {
		st, err := p.Store.ServerTime(ctx)
		if err != nil {
			return "fail", codeSkew
		}
		d := st.Sub(clk.Now())
		if d > maxSkew || d < -maxSkew {
			return "fail", codeSkew
		}
		return "pass", ""
	})

	if err := ctx.Err(); err != nil {
		return rep, err
	}
	return rep, nil
}

func (s *suite) skip(id, name string) {
	s.rep.Tests = append(s.rep.Tests, TestResult{ID: id, Name: name, Outcome: "skip"})
}

// lockfile is C7: a hand-written lock object, then tofu plan -lock-timeout=0s.
func (s *suite) lockfile() (string, string) {
	sum := sha256.Sum256([]byte(s.p.Cluster + "/" + s.p.RunID))
	h := hex.EncodeToString(sum[:16])
	id := h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	key := s.prefix + "tofu.tfstate"
	lock, _ := json.Marshal(LockInfo{ID: id, Operation: "OperationTypePlan", Info: "sc-bootstrap conformance C7", Who: "sc-bootstrap@conformance",
		Version: "1.13.1", Created: s.clk.Now().UTC(), Path: key})
	if _, err := s.p.Store.PutIfAbsent(s.ctx, key+".tflock", lock); err != nil {
		return "fail", codeLockfile
	}
	res, err := s.p.Probe.Probe(s.ctx, ProbeParams{Key: key, LockID: id})
	if err != nil || res.ExitCode == 0 || !IsLockError(res.Stderr) || !strings.Contains(res.Stderr, id) {
		return "fail", codeLockfile
	}
	return "pass", ""
}

// cleanup deletes every object version under the conformance prefix, whatever happened.
func (s *suite) cleanup() {
	ctx := context.WithoutCancel(s.ctx)
	vs, err := s.p.Store.ListVersions(ctx, s.prefix)
	if err != nil {
		return
	}
	for _, v := range vs {
		if v.VersionID == "" {
			_ = s.p.Store.Delete(ctx, v.Key, "")
			continue
		}
		_ = s.p.Store.DeleteVersion(ctx, v.Key, v.VersionID)
	}
}

// Marshal renders the report as JSON; fatal is always an array.
func (r Report) Marshal() ([]byte, error) {
	if r.Fatal == nil {
		r.Fatal = []string{}
	}
	if r.Tests == nil {
		r.Tests = []TestResult{}
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// WriteReport writes the report to path.
func WriteReport(path string, r Report) error {
	b, err := r.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// ExecLockProbe is the real C7 probe: it runs the tofu binary in a scratch directory.
type ExecLockProbe struct {
	Tofu    string
	Dir     string
	Backend Backend
	Env     []string
}

// Probe runs tofu plan -lock-timeout=0s against the scratch backend.
func (p ExecLockProbe) Probe(ctx context.Context, q ProbeParams) (ProbeResult, error) {
	be := p.Backend
	be.Key = q.Key
	hcl, err := RenderBackendS3(be)
	if err != nil {
		return ProbeResult{}, err
	}
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return ProbeResult{}, err
	}
	if err := os.WriteFile(filepath.Join(p.Dir, "_backend.tf"), []byte(hcl), 0o600); err != nil {
		return ProbeResult{}, err
	}
	run := func(args ...string) (ProbeResult, error) {
		cmd := exec.CommandContext(ctx, p.Tofu, append([]string{"-chdir=" + p.Dir}, args...)...)
		cmd.Env = p.Env
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		var ee *exec.ExitError
		switch {
		case err == nil:
			return ProbeResult{Stderr: stderr.String()}, nil
		case errors.As(err, &ee):
			return ProbeResult{ExitCode: ee.ExitCode(), Stderr: stderr.String()}, nil
		}
		return ProbeResult{}, fmt.Errorf("tofustate: running %s: %w", p.Tofu, err)
	}
	res, err := run("init", "-input=false", "-no-color")
	if err != nil || res.ExitCode != 0 {
		return res, err
	}
	return run("plan", "-lock-timeout=0s", "-input=false", "-no-color")
}
