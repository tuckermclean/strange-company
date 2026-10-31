package install

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/provider/fake"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

// s3Account is the production Account over the standard-library S3 client.
type s3Account struct{ c *s3client.Client }

var _ Account = (*s3Account)(nil)

// NewS3Account is the production Deps.NewS3.
func NewS3Account(cfg s3client.Config) (Account, error) {
	c, err := s3client.New(cfg)
	if err != nil {
		return nil, err
	}
	return &s3Account{c: c}, nil
}

func (a *s3Account) HeadBucket(ctx context.Context, name string) error {
	return a.c.HeadBucket(ctx, name)
}
func (a *s3Account) Versioning(ctx context.Context, name string) (bool, error) {
	return a.c.Versioning(ctx, name)
}
func (a *s3Account) ListBuckets(ctx context.Context) ([]string, error) { return a.c.ListBuckets(ctx) }
func (a *s3Account) Bucket(name string) tofustate.Store                { return a.c.Bucket(name) }

// SopsEncrypt is the production Deps.Encrypt: it runs the sops binary with the recipients given explicitly. A
// Kubernetes Secret manifest keeps its metadata readable (only data and stringData are encrypted), as the
// creation rules of .sops.yaml say; an off-cluster map is encrypted whole.
func SopsEncrypt(plain []byte, recipients []string) ([]byte, error) {
	if len(recipients) == 0 {
		return nil, errors.New("sops: no recipients")
	}
	args := []string{"--encrypt", "--age", strings.Join(recipients, ","), "--input-type", "yaml", "--output-type", "yaml"}
	if bytes.HasPrefix(plain, []byte("apiVersion:")) {
		args = append(args, "--encrypted-regex", "^(data|stringData)$")
	}
	args = append(args, "/dev/stdin")
	cmd := exec.Command("sops", args...)
	cmd.Stdin = bytes.NewReader(plain)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("sops: %w: %s", err, msg)
	}
	return out.Bytes(), nil
}

// ExecTofu is the production Tofu: it runs the tofu binary in a work directory copied from infra/tofu/storage, with
// the backend and the state encryption supplied as plan 06 section 7.3 describes. Everything the module needs reaches
// it through TF_VAR_* and AWS_* in the environment of the subprocess; the passphrase never touches a file or argv.
type ExecTofu struct {
	// Repo is the repository root that holds infra/tofu/storage.
	Repo string
	// Env is the process environment of sc-bootstrap (PATH, HOME, proxy and plugin-cache settings are passed on).
	Env map[string]string
	// Bin is the tofu binary (default "tofu").
	Bin string
}

var _ Tofu = (*ExecTofu)(nil)

func (t *ExecTofu) bin() string {
	if t.Bin != "" {
		return t.Bin
	}
	return "tofu"
}

// tofuEnv builds the environment of a tofu subprocess.
func tofuEnv(env map[string]string, s3 TofuS3, enc tofustate.Encryption) []string {
	out := []string{"TF_IN_AUTOMATION=1", "TF_INPUT=0", "CHECKPOINT_DISABLE=1"}
	for _, k := range []string{"PATH", "HOME", "TMPDIR", "TF_PLUGIN_CACHE_DIR", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
		if v := env[k]; v != "" {
			out = append(out, k+"="+v)
		} else if v := os.Getenv(k); v != "" && (k == "PATH" || k == "HOME") {
			out = append(out, k+"="+v)
		}
	}
	// SC_LAB_S3_CA_FILE (test mode only, plan 50 section 9.2) adds the lab CA for the S3 backend and the aws provider;
	// AWS_CA_BUNDLE extends the system roots, where SSL_CERT_FILE would replace them.
	if env["SC_TEST_MODE"] == "1" && env["SC_LAB_S3_CA_FILE"] != "" {
		out = append(out, "AWS_CA_BUNDLE="+env["SC_LAB_S3_CA_FILE"])
	}
	if s3.AccessKey != "" {
		out = append(out, "AWS_ACCESS_KEY_ID="+s3.AccessKey, "AWS_SECRET_ACCESS_KEY="+s3.SecretKey)
	}
	if enc.Current != "" {
		if e, err := tofustate.EncryptionEnv(enc); err == nil {
			out = append(out, e)
		}
	}
	return out
}

// specEncryption is the state encryption a storage spec asks for. The key provider is named after the passphrase's
// generation from the first run on (plan 06 section 7.3), so a later rotation can fall back to it.
func specEncryption(s StorageSpec) tofustate.Encryption {
	gen := s.Generation
	if gen < 1 {
		gen = 1
	}
	if s.Passphrase == "" {
		return tofustate.Encryption{}
	}
	return tofustate.Encryption{Current: s.Passphrase, Generation: gen, Previous: s.Previous}
}

// varsEnv are the TF_VAR_* of the storage module: the main account for the default provider and, with a state account,
// the second account for the aliased provider aws.state (empty otherwise: the alias then follows the main account).
func varsEnv(s StorageSpec) []string {
	v := []string{
		"TF_VAR_bucket_prefix=" + s.Prefix, "TF_VAR_cluster=" + s.Cluster, "TF_VAR_region=" + s.Main.Region,
		"TF_VAR_endpoint=" + s.Main.Endpoint, "TF_VAR_path_style=" + strconv.FormatBool(s.Main.PathStyle),
		"TF_VAR_access_key=" + s.Main.AccessKey, "TF_VAR_secret_key=" + s.Main.SecretKey,
	}
	if st := s.State; st != nil {
		v = append(v, "TF_VAR_state_endpoint="+st.Endpoint, "TF_VAR_state_region="+st.Region,
			"TF_VAR_state_path_style="+strconv.FormatBool(st.PathStyle),
			"TF_VAR_state_access_key="+st.AccessKey, "TF_VAR_state_secret_key="+st.SecretKey)
	}
	return v
}

// storageResource is the address of a bucket resource of infra/tofu/storage: the buckets of the state account (B1
// tofu-state and B2 recovery) are the "state" resources on the aliased provider, the others the "this" resources.
func storageResource(typ, purpose string) string {
	name := "this"
	if purpose == "tofu-state" || purpose == "recovery" {
		name = "state"
	}
	return fmt.Sprintf("%s.%s[%q]", typ, name, purpose)
}

var storageResourceTypes = []string{"aws_s3_bucket", "aws_s3_bucket_versioning", "aws_s3_bucket_public_access_block", "aws_s3_bucket_lifecycle_configuration"}

func (t *ExecTofu) run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, t.bin(), append([]string{"-chdir=" + dir}, args...)...)
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 1500 {
			msg = msg[len(msg)-1500:]
		}
		return out.String(), fmt.Errorf("tofu %s: %w: %s", args[0], err, msg)
	}
	return out.String(), nil
}

// prepare copies infra/tofu/storage into a fresh work directory and writes _backend.tf.
func (t *ExecTofu) prepare(spec StorageSpec, backendHCL string) error {
	src := filepath.Join(t.Repo, "infra", "tofu", "storage")
	if err := os.RemoveAll(spec.WorkDir); err != nil {
		return err
	}
	if err := os.MkdirAll(spec.WorkDir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("the storage module is missing: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(spec.WorkDir, e.Name()), b, 0o600); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(spec.WorkDir, "_backend.tf"), []byte(backendHCL), 0o600)
}

func (t *ExecTofu) localBackend(spec StorageSpec) (string, error) {
	return tofustate.RenderBackendLocal(spec.LocalState)
}

func remoteBackend(remote RemoteState) (string, error) {
	return tofustate.RenderBackendS3(tofustate.Backend{Bucket: remote.Bucket, Key: remote.Key, Region: remote.S3.Region,
		Endpoint: remote.S3.Endpoint, PathStyle: remote.S3.PathStyle, SkipS3Checksum: remote.SkipS3Checksum})
}

func (t *ExecTofu) env(spec StorageSpec, s3 TofuS3) []string {
	return append(tofuEnv(t.Env, s3, specEncryption(spec)), varsEnv(spec)...)
}

func (t *ExecTofu) init(ctx context.Context, spec StorageSpec, env []string, extra ...string) error {
	_, err := t.run(ctx, spec.WorkDir, env, append([]string{"init", "-input=false", "-no-color", "-lockfile=readonly"}, extra...)...)
	return err
}

// PlanStorage plans the module against a local backend and returns the plan summary line.
func (t *ExecTofu) PlanStorage(ctx context.Context, spec StorageSpec) (string, error) {
	hcl, err := t.localBackend(spec)
	if err != nil {
		return "", err
	}
	if err := t.prepare(spec, hcl); err != nil {
		return "", err
	}
	env := t.env(spec, spec.Main)
	if err := t.init(ctx, spec, env); err != nil {
		return "", err
	}
	out, err := t.run(ctx, spec.WorkDir, env, "plan", "-input=false", "-no-color", "-lock-timeout=60s")
	if err != nil {
		return "", err
	}
	return planSummary(out), nil
}

func planSummary(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "Plan:") || strings.HasPrefix(l, "No changes") {
			return strings.TrimSpace(l)
		}
	}
	return "see the plan output"
}

// resourceTypes of the guard: infra/tofu/storage/guard.yaml.
type guardFile struct {
	Spec struct {
		Resources []struct {
			Type           string   `yaml:"type"`
			AllowedActions []string `yaml:"allowedActions"`
			MaxCount       int      `yaml:"maxCount"`
		} `yaml:"resources"`
	} `yaml:"spec"`
}

// guardPlan refuses a plan that holds a resource type or an action guard.yaml does not allow (plan 06 section 14.2).
func (t *ExecTofu) guardPlan(planJSON string) error {
	b, err := os.ReadFile(filepath.Join(t.Repo, "infra", "tofu", "storage", "guard.yaml"))
	if err != nil {
		return fmt.Errorf("the storage guard is missing: %w", err)
	}
	var g guardFile
	if err := yaml.Unmarshal(b, &g); err != nil {
		return fmt.Errorf("the storage guard is not YAML: %w", err)
	}
	allowed := map[string]map[string]bool{}
	max := map[string]int{}
	for _, r := range g.Spec.Resources {
		allowed[r.Type] = map[string]bool{}
		for _, a := range r.AllowedActions {
			allowed[r.Type][a] = true
		}
		max[r.Type] = r.MaxCount
	}
	var plan struct {
		ResourceChanges []struct {
			Address string `json:"address"`
			Type    string `json:"type"`
			Change  struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
		return fmt.Errorf("the plan is not JSON: %w", err)
	}
	count := map[string]int{}
	for _, rc := range plan.ResourceChanges {
		acts := rc.Change.Actions
		if len(acts) == 1 && (acts[0] == "no-op" || acts[0] == "read") {
			continue
		}
		perm, ok := allowed[rc.Type]
		if !ok {
			return fmt.Errorf("guard: %s (%s) is not a resource type the storage module may plan", rc.Address, rc.Type)
		}
		for _, a := range acts {
			if !perm[a] {
				return fmt.Errorf("guard: %s would be %s, which guard.yaml does not allow", rc.Address, strings.Join(acts, "+"))
			}
		}
		count[rc.Type]++
		if max[rc.Type] > 0 && count[rc.Type] > max[rc.Type] {
			return fmt.Errorf("guard: more than %d %s resources in the plan", max[rc.Type], rc.Type)
		}
	}
	return nil
}

// ApplyStorage plans, guards and applies the module against an encrypted local state. With a state account the
// module is still applied once: the aliased provider aws.state carries the second account's coordinates (TF_VAR_state_*)
// and creates the state buckets B1 and B2 there, the default provider the rest with the main account.
func (t *ExecTofu) ApplyStorage(ctx context.Context, spec StorageSpec) error {
	hcl, err := t.localBackend(spec)
	if err != nil {
		return err
	}
	if err := t.prepare(spec, hcl); err != nil {
		return err
	}
	env := t.env(spec, spec.Main)
	if err := t.init(ctx, spec, env); err != nil {
		return err
	}
	const planFile = "storage.tfplan"
	if _, err := t.run(ctx, spec.WorkDir, env, "plan", "-input=false", "-no-color", "-lock-timeout=60s", "-out="+planFile); err != nil {
		return err
	}
	js, err := t.run(ctx, spec.WorkDir, env, "show", "-json", planFile)
	if err != nil {
		return err
	}
	if err := t.guardPlan(js); err != nil {
		return err
	}
	_, err = t.run(ctx, spec.WorkDir, env, "apply", "-input=false", "-no-color", "-lock-timeout=60s", planFile)
	return err
}

// ImportStorage rebuilds the storage state of existing buckets (plan 06 `state import --module storage`): a fresh
// encrypted local state is filled with `tofu import` of every resource of every bucket (both accounts reach the module through
// TF_VAR_*, as in ApplyStorage). The caller checks the plan and migrates it to the remote object,
// replacing the state encrypted under another passphrase.
func (t *ExecTofu) ImportStorage(ctx context.Context, spec StorageSpec, remote RemoteState) error {
	for _, f := range localStateFiles(spec) {
		if err := os.Remove(f); err != nil {
			return err
		}
	}
	hcl, err := t.localBackend(spec)
	if err != nil {
		return err
	}
	if err := t.prepare(spec, hcl); err != nil {
		return err
	}
	env := t.env(spec, spec.Main)
	if err := t.init(ctx, spec, env); err != nil {
		return err
	}
	for _, b := range spec.Buckets {
		p := strings.TrimPrefix(b.Name, spec.Prefix+"-")
		for _, typ := range storageResourceTypes {
			if _, err := t.run(ctx, spec.WorkDir, env, "import", "-input=false", "-no-color", "-lock-timeout=60s", storageResource(typ, p), b.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

// StateList lists the addresses of the local state (remote nil) or of the remote object.
func (t *ExecTofu) StateList(ctx context.Context, spec StorageSpec, remote *RemoteState) ([]string, error) {
	hcl, s3, err := t.backendFor(spec, remote)
	if err != nil {
		return nil, err
	}
	if err := t.prepare(spec, hcl); err != nil {
		return nil, err
	}
	env := t.env(spec, s3)
	if err := t.init(ctx, spec, env); err != nil {
		return nil, err
	}
	out, err := t.run(ctx, spec.WorkDir, env, "state", "list")
	if err != nil {
		return nil, err
	}
	var addrs []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			addrs = append(addrs, l)
		}
	}
	sort.Strings(addrs)
	return addrs, nil
}

func (t *ExecTofu) backendFor(spec StorageSpec, remote *RemoteState) (string, TofuS3, error) {
	if remote == nil {
		hcl, err := t.localBackend(spec)
		return hcl, spec.Main, err
	}
	hcl, err := remoteBackend(*remote)
	return hcl, remote.S3, err
}

// Migrate moves the local state to the remote backend: init against the local backend, then rewrite _backend.tf to
// s3 and run init -migrate-state -force-copy (plan 06 section 14.4).
func (t *ExecTofu) Migrate(ctx context.Context, spec StorageSpec, remote RemoteState) error {
	hcl, err := t.localBackend(spec)
	if err != nil {
		return err
	}
	if err := t.prepare(spec, hcl); err != nil {
		return err
	}
	if err := t.init(ctx, spec, t.env(spec, spec.Main)); err != nil {
		return err
	}
	rhcl, err := remoteBackend(remote)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(spec.WorkDir, "_backend.tf"), []byte(rhcl), 0o600); err != nil {
		return err
	}
	err = t.init(ctx, spec, t.env(spec, remote.S3), "-migrate-state", "-force-copy")
	if err == nil || !strings.Contains(err.Error(), "decryption failed") {
		return err
	}
	// The remote object holds a state encrypted under a passphrase this run does not have: an identity that was abandoned
	// and re-imported (S8b1 N-1; the old passphrase is lost by design, so no fallback key provider can open it). The
	// locally rebuilt state replaces it. The bucket is versioned, so the old object stays as a noncurrent version.
	if derr := t.deleteRemote(ctx, remote); derr != nil {
		return fmt.Errorf("%w (replacing the remote state failed: %v)", err, derr)
	}
	return t.init(ctx, spec, t.env(spec, remote.S3), "-migrate-state", "-force-copy")
}

// deleteRemote deletes the current version of the remote state object.
func (t *ExecTofu) deleteRemote(ctx context.Context, remote RemoteState) error {
	cfg := s3client.Config{Endpoint: remote.S3.Endpoint, Region: remote.S3.Region, AccessKey: remote.S3.AccessKey,
		SecretKey: remote.S3.SecretKey, PathStyle: remote.S3.PathStyle, Clock: clock.Real{}}
	if ca := t.Env["SC_LAB_S3_CA_FILE"]; ca != "" && t.Env["SC_TEST_MODE"] == "1" {
		pemBytes, err := os.ReadFile(ca)
		if err != nil {
			return err
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		pool.AppendCertsFromPEM(pemBytes)
		cfg.HTTP = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	}
	c, err := s3client.New(cfg)
	if err != nil {
		return err
	}
	return c.Bucket(remote.Bucket).Delete(ctx, remote.Key, "")
}

// Changes counts the resource changes a plan shows against the backend.
func (t *ExecTofu) Changes(ctx context.Context, spec StorageSpec, remote *RemoteState) (int, error) {
	hcl, s3, err := t.backendFor(spec, remote)
	if err != nil {
		return 0, err
	}
	if err := t.prepare(spec, hcl); err != nil {
		return 0, err
	}
	env := t.env(spec, s3)
	if err := t.init(ctx, spec, env); err != nil {
		return 0, err
	}
	if _, err := t.run(ctx, spec.WorkDir, env, "plan", "-input=false", "-no-color", "-lock-timeout=60s", "-out=changes.tfplan"); err != nil {
		return 0, err
	}
	js, err := t.run(ctx, spec.WorkDir, env, "show", "-json", "changes.tfplan")
	if err != nil {
		return 0, err
	}
	var plan struct {
		ResourceChanges []struct {
			Change struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal([]byte(js), &plan); err != nil {
		return 0, err
	}
	n := 0
	for _, rc := range plan.ResourceChanges {
		if a := rc.Change.Actions; len(a) == 1 && (a[0] == "no-op" || a[0] == "read") {
			continue
		}
		n++
	}
	return n, nil
}

// startFakeProvider binds the fake provider to .bootstrap/fake-provider; it is refused outside SC_TEST_MODE=1.
func (s *sess) startFakeProvider() error {
	p, err := fake.New(s.env, filepath.Join(s.boot, "fake-provider"))
	if err != nil {
		return err
	}
	s.prov = p
	return nil
}
