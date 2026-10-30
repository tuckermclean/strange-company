package tofustate_test

// Layer-1 tests of F08 slice S8b3 (no network, no real tofu): the production install.ExecTofu and install.ExecStateTofu are
// run against a recording stand-in for the tofu binary. It logs every invocation (arguments and the TF_VAR_*, AWS_* and CA
// variables of its environment) to $TMPDIR/rec.log, prints an empty plan for `show -json` and exits 0.
//
// The state-account contract the tests fix (plans are silent on the variable names; S6a review-1 N1): with a second account
// the module gets BOTH accounts' coordinates through TF_VAR_*: endpoint, region, path_style, access_key, secret_key for the
// default provider and state_endpoint, state_region, state_path_style, state_access_key, state_secret_key for the aliased
// provider `aws.state`, in one apply. AWS_* stays the credential of the S3 backend (the state account's when the backend
// is the remote object).

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/install"
	"strange.company/sc/internal/tofustate"
)

var storagePurposes = []string{"tofu-state", "recovery", "bootstrap-seed", "pg-backups", "longhorn-backups", "etcd-snapshots",
	"forgejo-objects", "repo-backups", "evidence", "telemetry"}

const (
	sPrefix  = "s8b3lab"
	sCluster = "lab-one"
)

// repoRoot is the repository root (the directory with plans/06-opentofu-state.md).
func repoRoot(t *testing.T) string {
	t.Helper()
	d, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(d, "plans", "06-opentofu-state.md")); err == nil {
			return d
		}
		if p := filepath.Dir(d); p != d {
			d = p
			continue
		}
		t.Fatal("repository root not found")
	}
}

func bucketSpecs(stateAccount bool) []install.BucketSpec {
	var out []install.BucketSpec
	for _, p := range storagePurposes {
		out = append(out, install.BucketSpec{Name: sPrefix + "-" + p, StateAccount: stateAccount && (p == "tofu-state" || p == "recovery")})
	}
	return out
}

const recorderScript = `#!/bin/sh
{
  echo "ARGS $*"
  env | grep -E '^(TF_VAR_|AWS_|SSL_CERT_FILE)' | sort | sed 's/^/ENV /'
  echo "END"
} >> "$TMPDIR/rec.log"
case "$*" in
  *"show -json"*) echo '{"resource_changes":[]}' ;;
esac
exit 0
`

type recCall struct {
	args string
	env  map[string]string
}

func (c recCall) verb() string {
	for _, f := range strings.Fields(c.args) {
		if !strings.HasPrefix(f, "-chdir=") {
			return f
		}
	}
	return ""
}

type recorder struct {
	t    *testing.T
	dir  string
	bin  string
	repo string
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "tofu")
	if err := os.WriteFile(bin, []byte(recorderScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return &recorder{t: t, dir: dir, bin: bin, repo: repoRoot(t)}
}

func (r *recorder) env(extra map[string]string) map[string]string {
	e := map[string]string{"PATH": os.Getenv("PATH"), "HOME": r.dir, "TMPDIR": r.dir}
	for k, v := range extra {
		e[k] = v
	}
	return e
}

func (r *recorder) calls() []recCall {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, "rec.log"))
	if err != nil {
		r.t.Fatalf("the fake tofu was never run: %v", err)
	}
	var out []recCall
	var cur *recCall
	for _, l := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(l, "ARGS "):
			out = append(out, recCall{args: strings.TrimPrefix(l, "ARGS "), env: map[string]string{}})
			cur = &out[len(out)-1]
		case strings.HasPrefix(l, "ENV ") && cur != nil:
			k, v, _ := strings.Cut(strings.TrimPrefix(l, "ENV "), "=")
			cur.env[k] = v
		}
	}
	return out
}

func (r *recorder) verbs(verb string) []recCall {
	var out []recCall
	for _, c := range r.calls() {
		if c.verb() == verb {
			out = append(out, c)
		}
	}
	return out
}

func enc1() tofustate.Encryption {
	return tofustate.Encryption{Current: passphrase(3), Generation: 1}
}

// S7 review N5: every `init` of the production ExecStateTofu carries -lockfile=readonly (the provider versions are pinned
// by the module and the lock file, never re-resolved).
func TestExecStateTofu_InitIsLockfileReadonly_S8b3N5(t *testing.T) {
	t.Parallel()
	cases := map[string]func(x *install.ExecStateTofu) error{
		"Decrypt": func(x *install.ExecStateTofu) error {
			_, err := x.Decrypt(context.Background(), "compute", []byte("{}"), enc1())
			return err
		},
		"PlanCopy": func(x *install.ExecStateTofu) error {
			_, err := x.PlanCopy(context.Background(), "compute", []byte("{}"), enc1())
			return err
		},
		"RefreshOnly": func(x *install.ExecStateTofu) error {
			return x.RefreshOnly(context.Background(), "compute", install.RemoteState{Bucket: "lab-one-tofu-state", Key: "lab-one/compute/terraform.tfstate",
				S3: install.TofuS3{Endpoint: "http://127.0.0.1:9", Region: "us-east-1", AccessKey: "ak", SecretKey: "sk", PathStyle: true}}, enc1())
		},
	}
	for name, run := range cases {
		run := run
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRecorder(t)
			x := &install.ExecStateTofu{Repo: r.repo, Env: r.env(map[string]string{"SC_TEST_MODE": "1"}), Bin: r.bin}
			if err := run(x); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			inits := r.verbs("init")
			if len(inits) == 0 {
				t.Fatal("tofu init was never run")
			}
			for _, c := range inits {
				if !strings.Contains(" "+c.args+" ", " -lockfile=readonly ") {
					t.Errorf("init without -lockfile=readonly: %s", c.args)
				}
			}
		})
	}
}

// S7 review N5: the compute PlanCopy of the production wiring gets TF_VAR_nodes (node name to internal IP, from the
// provider inventory), so the plan against the state copy counts drift rather than a destroy of every node. The wiring is
// sess.stateVars; see state_vars_s8b3_test.go in the install package.

func storageSpecFor(t *testing.T, main install.TofuS3, state *install.TofuS3) install.StorageSpec {
	t.Helper()
	dir := t.TempDir()
	return install.StorageSpec{WorkDir: filepath.Join(dir, "work"), LocalState: filepath.Join(dir, "storage.local.tfstate"),
		Prefix: sPrefix, Cluster: sCluster, Buckets: bucketSpecs(state != nil), Main: main, State: state, Passphrase: passphrase(3), Generation: 1}
}

var (
	mainS3  = install.TofuS3{Endpoint: "http://main.example:9000", Region: "us-east-1", AccessKey: "MAINKEY", SecretKey: "MAINSECRET", PathStyle: true}
	stateS3 = install.TofuS3{Endpoint: "http://state.example:9001", Region: "eu-west-1", AccessKey: "STATEKEY", SecretKey: "STATESECRET", PathStyle: false}
)

// requireAccountVars checks the two accounts reach the right providers on one invocation.
func requireAccountVars(t *testing.T, c recCall, backendKey string) {
	t.Helper()
	want := map[string]string{
		"TF_VAR_endpoint": mainS3.Endpoint, "TF_VAR_region": mainS3.Region, "TF_VAR_access_key": mainS3.AccessKey, "TF_VAR_secret_key": mainS3.SecretKey,
		"TF_VAR_state_endpoint": stateS3.Endpoint, "TF_VAR_state_region": stateS3.Region, "TF_VAR_state_path_style": "false",
		"TF_VAR_state_access_key": stateS3.AccessKey, "TF_VAR_state_secret_key": stateS3.SecretKey,
	}
	for k, v := range want {
		if c.env[k] != v {
			t.Errorf("tofu %s: %s = %q, want %q", c.verb(), k, c.env[k], v)
		}
	}
	if backendKey != "" && c.env["AWS_ACCESS_KEY_ID"] != backendKey {
		t.Errorf("tofu %s: AWS_ACCESS_KEY_ID (the backend credential) = %q, want %q", c.verb(), c.env["AWS_ACCESS_KEY_ID"], backendKey)
	}
}

// S6a review-1 N1: in state-account mode the storage module is applied ONCE, with the aliased provider for the state
// account; the state account's endpoint and credentials reach the alias and the main endpoint and credentials are not
// reused for it. (Before: two -target passes over one provider block, the second with the main TF_VAR_endpoint.)
func TestExecTofu_StateAccountApply_OneApplyAliasedProvider_S8b3N1(t *testing.T) {
	t.Parallel()
	r := newRecorder(t)
	x := &install.ExecTofu{Repo: r.repo, Env: r.env(nil), Bin: r.bin}
	st := stateS3
	if err := x.ApplyStorage(context.Background(), storageSpecFor(t, mainS3, &st)); err != nil {
		t.Fatalf("ApplyStorage: %v", err)
	}
	if n := len(r.verbs("apply")); n != 1 {
		t.Errorf("%d tofu apply runs, want exactly 1 (one apply with the aliased provider)", n)
	}
	for _, c := range r.calls() {
		if strings.Contains(c.args, "-target") {
			t.Errorf("tofu %s still targets resources: %s", c.verb(), c.args)
		}
		requireAccountVars(t, c, "")
		if c.env["AWS_ACCESS_KEY_ID"] == stateS3.AccessKey {
			t.Errorf("tofu %s: the state credential is the process-wide AWS_ACCESS_KEY_ID, so the main provider would use it", c.verb())
		}
	}
}

// The same for the buckets' import (abandon-identity rebuild): every import and the init run with both accounts.
func TestExecTofu_StateAccountImport_BothAccounts_S8b3N1(t *testing.T) {
	t.Parallel()
	r := newRecorder(t)
	x := &install.ExecTofu{Repo: r.repo, Env: r.env(nil), Bin: r.bin}
	st := stateS3
	spec := storageSpecFor(t, mainS3, &st)
	if err := os.WriteFile(spec.LocalState, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	remote := install.RemoteState{Bucket: "lab-one-tofu-state", Key: "lab-one/storage/terraform.tfstate", S3: stateS3}
	if err := x.ImportStorage(context.Background(), spec, remote); err != nil {
		t.Fatalf("ImportStorage: %v", err)
	}
	imports := r.verbs("import")
	if len(imports) != 4*len(storagePurposes) {
		t.Errorf("%d tofu import runs, want %d (four resources per bucket)", len(imports), 4*len(storagePurposes))
	}
	for _, c := range r.calls() {
		requireAccountVars(t, c, "")
	}
}

// Changes against the remote state: the S3 backend is the state account's (AWS_*), and the main buckets are still
// refreshed with the main credential through TF_VAR_access_key (S6a review-1 N1: "Changes() refresh with state creds fails
// on main buckets").
func TestExecTofu_StateAccountChangesRemote_MainCredsForProvider_S8b3N1(t *testing.T) {
	t.Parallel()
	r := newRecorder(t)
	x := &install.ExecTofu{Repo: r.repo, Env: r.env(nil), Bin: r.bin}
	st := stateS3
	spec := storageSpecFor(t, mainS3, &st)
	remote := install.RemoteState{Bucket: "lab-one-tofu-state", Key: "lab-one/storage/terraform.tfstate", S3: stateS3}
	if _, err := x.Changes(context.Background(), spec, &remote); err != nil {
		t.Fatalf("Changes: %v", err)
	}
	for _, c := range r.calls() {
		requireAccountVars(t, c, stateS3.AccessKey)
	}
}

// Without a state account nothing of the alias is set: the aliased provider is configured from empty variables.
func TestExecTofu_NoStateAccount_NoAliasVars_S8b3N1(t *testing.T) {
	t.Parallel()
	r := newRecorder(t)
	x := &install.ExecTofu{Repo: r.repo, Env: r.env(nil), Bin: r.bin}
	if err := x.ApplyStorage(context.Background(), storageSpecFor(t, mainS3, nil)); err != nil {
		t.Fatalf("ApplyStorage: %v", err)
	}
	for _, c := range r.calls() {
		for k := range c.env {
			if strings.HasPrefix(k, "TF_VAR_state_") {
				t.Errorf("tofu %s: %s is set without a state account", c.verb(), k)
			}
		}
		if c.env["TF_VAR_access_key"] != mainS3.AccessKey {
			t.Errorf("tofu %s: TF_VAR_access_key = %q, want the main key (the default provider takes explicit credentials)", c.verb(), c.env["TF_VAR_access_key"])
		}
	}
	if n := len(r.verbs("apply")); n != 1 {
		t.Errorf("%d tofu apply runs, want 1", n)
	}
}

// Plan 50 section 9.2 / tier E: SC_LAB_S3_CA_FILE (test mode only) is handed to tofu as AWS_CA_BUNDLE, which adds the lab CA
// for the S3 backend and the aws provider without replacing the system roots the registry needs (SSL_CERT_FILE would).
func TestExecTofu_LabCAFile_PassedAsAWSCABundle_S8b3(t *testing.T) {
	t.Parallel()
	r := newRecorder(t)
	ca := filepath.Join(r.dir, "lab-ca.pem")
	x := &install.ExecTofu{Repo: r.repo, Env: r.env(map[string]string{"SC_TEST_MODE": "1", "SC_LAB_S3_CA_FILE": ca}), Bin: r.bin}
	if err := x.ApplyStorage(context.Background(), storageSpecFor(t, mainS3, nil)); err != nil {
		t.Fatalf("ApplyStorage: %v", err)
	}
	for _, c := range r.calls() {
		if c.env["AWS_CA_BUNDLE"] != ca {
			t.Errorf("tofu %s: AWS_CA_BUNDLE = %q, want %q", c.verb(), c.env["AWS_CA_BUNDLE"], ca)
		}
		if c.env["SSL_CERT_FILE"] != "" {
			t.Errorf("tofu %s: SSL_CERT_FILE = %q would replace the system roots", c.verb(), c.env["SSL_CERT_FILE"])
		}
	}
	// Outside test mode the file is ignored (the variable is test-only).
	r2 := newRecorder(t)
	x2 := &install.ExecTofu{Repo: r2.repo, Env: r2.env(map[string]string{"SC_LAB_S3_CA_FILE": ca}), Bin: r2.bin}
	if err := x2.ApplyStorage(context.Background(), storageSpecFor(t, mainS3, nil)); err != nil {
		t.Fatalf("ApplyStorage: %v", err)
	}
	for _, c := range r2.calls() {
		if c.env["AWS_CA_BUNDLE"] != "" {
			t.Errorf("tofu %s: AWS_CA_BUNDLE = %q outside SC_TEST_MODE=1", c.verb(), c.env["AWS_CA_BUNDLE"])
		}
	}
}

// --- the static check of infra/tofu/storage/main.tf (protected; the implementer's change arrives through the operator)

var blockRe = regexp.MustCompile(`(?m)^(provider|resource|variable|output|locals|terraform)\b[^\n]*\{`)

// blocks splits an HCL file at its top-level blocks (every block header starts a line, every block ends before the next).
func blocks(src string) []string {
	idx := blockRe.FindAllStringIndex(src, -1)
	var out []string
	for i, m := range idx {
		end := len(src)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		out = append(out, src[m[0]:end])
	}
	return out
}

// S6a review-1 N1: infra/tofu/storage declares a second `provider "aws"` with alias "state", fed by the state_* variables,
// the default provider takes explicit credentials, and the four per-bucket resource types exist once more on aws.state.
func TestStorageModule_DeclaresStateAccountProviderAlias_S8b3N1(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "infra", "tofu", "storage", "main.tf"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	var def, alias string
	for _, bl := range blocks(src) {
		if !strings.HasPrefix(bl, `provider "aws"`) {
			continue
		}
		if regexp.MustCompile(`(?m)^\s*alias\s*=\s*"state"`).MatchString(bl) {
			alias = bl
		} else {
			def = bl
		}
	}
	if alias == "" {
		t.Fatal(`no provider "aws" block with alias = "state"`)
	}
	for _, want := range []string{"var.state_endpoint", "var.state_access_key", "var.state_secret_key", "var.state_region", "var.state_path_style"} {
		if !strings.Contains(alias, want) {
			t.Errorf("the aliased provider does not use %s", want)
		}
	}
	for _, bad := range []string{"var.endpoint", "var.access_key", "var.secret_key"} {
		if regexp.MustCompile(regexp.QuoteMeta(bad) + `\b`).MatchString(alias) {
			t.Errorf("the aliased provider reuses the main %s", bad)
		}
	}
	for _, want := range []string{"var.endpoint", "var.access_key", "var.secret_key"} {
		if !strings.Contains(def, want) {
			t.Errorf("the default provider does not use %s", want)
		}
	}
	for _, v := range []string{"access_key", "secret_key", "state_endpoint", "state_region", "state_path_style", "state_access_key", "state_secret_key"} {
		if !regexp.MustCompile(`(?m)^variable "` + v + `"`).MatchString(src) {
			t.Errorf("variable %q is not declared", v)
		}
	}
	for _, v := range []string{"secret_key", "state_secret_key"} {
		for _, bl := range blocks(src) {
			if strings.HasPrefix(bl, `variable "`+v+`"`) && !regexp.MustCompile(`(?m)^\s*sensitive\s*=\s*true`).MatchString(bl) {
				t.Errorf("variable %q is not sensitive", v)
			}
		}
	}
	onAlias := map[string]bool{}
	for _, bl := range blocks(src) {
		m := regexp.MustCompile(`^resource "(aws_s3_bucket[a-z_]*)"`).FindStringSubmatch(bl)
		if m != nil && regexp.MustCompile(`(?m)^\s*provider\s*=\s*aws\.state\s*$`).MatchString(bl) {
			onAlias[m[1]] = true
		}
	}
	for _, typ := range []string{"aws_s3_bucket", "aws_s3_bucket_versioning", "aws_s3_bucket_public_access_block", "aws_s3_bucket_lifecycle_configuration"} {
		if !onAlias[typ] {
			t.Errorf("no %s resource uses provider = aws.state", typ)
		}
	}
}
