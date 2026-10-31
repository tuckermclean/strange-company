// Command engine is the test double of ./bootstrap for the crash matrix (plan 50 section 22). It runs the real
// install pipeline (install.Run: real preflight, engine, journal, S3 client, identity, commit and fake provider
// code) as a process of its own, so that SC_TEST_CRASH_AT can SIGKILL it. Only the four collaborators the
// in-process fake install replaces are replaced here too: the preflight probes, the C7 lock probe, sops and
// OpenTofu. The S3 side is the real client talking to the SeaweedFS emulator that the test process runs.
package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"strange.company/sc/internal/bootstrap/cli"
	"strange.company/sc/internal/bootstrap/install"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

func main() {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	deps := install.Deps{
		CLI: cli.Deps{
			ProbeModelKey: func(string, string) error { return nil },
			Resolve:       func(string) ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("203.0.113.9")}, nil },
		},
		NewS3:      install.NewS3Account,
		Tofu:       &fileTofu{env: env},
		LockProbe:  goodProbe{},
		Encrypt:    fakeEncrypt,
		WorkFactor: 18,
	}
	os.Exit(install.Run(os.Args[1:], env, os.Stdout, os.Stderr, deps))
}

// goodProbe is the C7 probe of an OpenTofu that honors a hand-written lock file.
type goodProbe struct{}

func (goodProbe) Probe(_ context.Context, p tofustate.ProbeParams) (tofustate.ProbeResult, error) {
	return tofustate.ProbeResult{ExitCode: 1, Stderr: "Error: Error acquiring the state lock\n\nLock Info:\n  ID:        " + p.LockID + "\n  Operation: OperationTypePlan\n"}, nil
}

// fakeEncrypt stands in for sops: the ciphertext carries a digest of the plaintext and the recipients only.
func fakeEncrypt(plain []byte, recipients []string) ([]byte, error) {
	if len(recipients) == 0 {
		return nil, errors.New("no recipients")
	}
	sum := md5.Sum(append(bytes.Clone(plain), []byte(strings.Join(recipients, ","))...))
	return []byte("sops:\n  fixture-ciphertext: " + hex.EncodeToString(sum[:]) + "\n  recipients: [" + strings.Join(recipients, ", ") + "]\n"), nil
}

// fileTofu plays the storage root module against the real S3 endpoint: ApplyStorage creates (and versions) the
// buckets the way the aws provider does after a refresh (a bucket the state account already owns is left alone) and
// leaves the encrypted local state next to its backup; Migrate writes the remote state object as an OpenTofu
// encryption envelope. Every bucket creation is also appended to $CRASH_TEST_LOG_DIR/tofu.log.
type fileTofu struct {
	env map[string]string
	mu  sync.Mutex
}

var _ install.Tofu = (*fileTofu)(nil)

func (f *fileTofu) logf(format string, a ...any) {
	dir := f.env["CRASH_TEST_LOG_DIR"]
	if dir == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = os.MkdirAll(dir, 0o755)
	fh, err := os.OpenFile(filepath.Join(dir, "tofu.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer fh.Close()
	fmt.Fprintf(fh, format+"\n", a...)
}

func (f *fileTofu) client(s install.TofuS3) (*s3client.Client, error) {
	hc := &http.Client{}
	if ca := f.env["SC_LAB_S3_CA_FILE"]; ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(pem)
		hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	}
	return s3client.New(s3client.Config{Endpoint: s.Endpoint, Region: s.Region, AccessKey: s.AccessKey, SecretKey: s.SecretKey, PathStyle: s.PathStyle, HTTP: hc})
}

func addrOf(bucket string) []string {
	id := strings.ReplaceAll(bucket, "-", "_")
	return []string{"aws_s3_bucket." + id, "aws_s3_bucket_versioning." + id}
}

func (f *fileTofu) PlanStorage(_ context.Context, s install.StorageSpec) (string, error) {
	f.logf("plan")
	return fmt.Sprintf("fake tofu plan: %d buckets to add", len(s.Buckets)), nil
}

func (f *fileTofu) ApplyStorage(ctx context.Context, s install.StorageSpec) error {
	f.logf("apply")
	var addrs []string
	for _, b := range s.Buckets {
		acct := s.Main
		if b.StateAccount {
			if s.State == nil {
				return errors.New("fake tofu: a state-account bucket without a state account")
			}
			acct = *s.State
		}
		c, err := f.client(acct)
		if err != nil {
			return err
		}
		if err := c.HeadBucket(ctx, b.Name); err != nil {
			if err := c.CreateBucket(ctx, b.Name); err != nil {
				return fmt.Errorf("fake tofu: creating %s: %w", b.Name, err)
			}
			f.logf("create %s", b.Name)
		}
		if err := c.SetVersioning(ctx, b.Name, true); err != nil {
			return fmt.Errorf("fake tofu: versioning %s: %w", b.Name, err)
		}
		addrs = append(addrs, addrOf(b.Name)...)
	}
	sort.Strings(addrs)
	body, _ := json.Marshal(envelope(addrs))
	if err := os.MkdirAll(filepath.Dir(s.LocalState), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(s.LocalState, body, 0o600); err != nil {
		return err
	}
	return os.WriteFile(s.LocalState+".backup", body, 0o600)
}

// envelope wraps the resource list the way OpenTofu wraps an encrypted state (the engine's status check reads the header).
func envelope(addrs []string) map[string]any {
	raw, _ := json.Marshal(addrs)
	return map[string]any{"serial": 1, "lineage": "crash-matrix-lineage", "meta": map[string]any{},
		"encrypted_data": base64.StdEncoding.EncodeToString(raw), "encryption_version": "v0"}
}

func addrsOf(raw []byte) ([]string, error) {
	var doc struct {
		Data string `json:"encrypted_data"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	b, err := base64.StdEncoding.DecodeString(doc.Data)
	if err != nil {
		return nil, err
	}
	var out []string
	return out, json.Unmarshal(b, &out)
}

func (f *fileTofu) StateList(ctx context.Context, s install.StorageSpec, remote *install.RemoteState) ([]string, error) {
	if remote == nil {
		b, err := os.ReadFile(s.LocalState)
		if err != nil {
			return nil, fmt.Errorf("fake tofu: no local state: %w", err)
		}
		return addrsOf(b)
	}
	c, err := f.client(remote.S3)
	if err != nil {
		return nil, err
	}
	o, err := c.Bucket(remote.Bucket).Get(ctx, remote.Key)
	if err != nil {
		return nil, fmt.Errorf("fake tofu: no remote state at %s/%s: %w", remote.Bucket, remote.Key, err)
	}
	return addrsOf(o.Data)
}

func (f *fileTofu) Migrate(ctx context.Context, s install.StorageSpec, remote install.RemoteState) error {
	f.logf("migrate")
	b, err := os.ReadFile(s.LocalState)
	if err != nil {
		return fmt.Errorf("fake tofu: no local state to migrate: %w", err)
	}
	c, err := f.client(remote.S3)
	if err != nil {
		return err
	}
	_, err = c.Bucket(remote.Bucket).PutObject(ctx, remote.Key, b, s3client.PutOptions{})
	return err
}

func (f *fileTofu) Changes(context.Context, install.StorageSpec, *install.RemoteState) (int, error) {
	return 0, nil
}
