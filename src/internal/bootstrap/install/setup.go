package install

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"strange.company/sc/internal/bootstrap/cli"
	"strange.company/sc/internal/bootstrap/config"
	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/bootstrap/redact"
	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/provider"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/version"
)

const zeroUID = "00000000-0000-4000-8000-000000000000"

// bucketPurposes is the plan 14 section 9.1 bucket set B1..B10, in that order.
var bucketPurposes = []string{
	"tofu-state", "recovery", "bootstrap-seed", "pg-backups", "longhorn-backups", "etcd-snapshots",
	"forgejo-objects", "repo-backups", "evidence", "telemetry",
}

// stateAccountPurposes are B1 and B2: with S3_STATE_* configured they live in the second account (14-R-2a).
var stateAccountPurposes = map[string]bool{"tofu-state": true, "recovery": true}

// lockDays is the Object Lock period per bucket of plan 14 section 9.1 (0: none).
var lockDays = map[string]int{"recovery": 365, "pg-backups": 14, "longhorn-backups": 14, "etcd-snapshots": 7, "repo-backups": 14}

// cred is one S3 credential with the endpoint it belongs to.
type cred struct {
	Endpoint, Region, AccessKey, SecretKey string
	PathStyle                              bool
}

func (c cred) tofu() TofuS3 {
	return TofuS3{Endpoint: c.Endpoint, Region: c.Region, AccessKey: c.AccessKey, SecretKey: c.SecretKey, PathStyle: c.PathStyle}
}

// objCfg is the object-storage part of the normalized configuration with the credentials resolved.
type objCfg struct {
	prefix, endpoint, region, bucketMode, credMode string
	pathStyle, allowUnprotected                    bool
	main                                           cred
	state                                          *cred
	instances                                      map[string]cred
}

// sess is one invocation of plan, run or status.
type sess struct {
	inv  cli.Invocation
	env  map[string]string
	out  io.Writer
	errw io.Writer
	deps Deps
	clk  clock.Clock
	in   *bufio.Reader

	repo, boot, blackstart string

	vars    map[string]string
	merged  map[string]string
	doc     map[string]any
	cluster string
	oc      objCfg

	red  *redact.Redactor
	eng  *engine.Engine
	lazy *lazyStore
	reg  *identity.Register
	set  *identity.Set
	pass string

	// bucketAbsent is set when tryAttach saw HeadBucket answer "not found" for the tofu-state bucket: the store is
	// reachable and no remote journal can exist (before plan.storage-apply).
	bucketAbsent bool

	// tofuGen and tofuPrev are the generation and fallback passphrase of the last tofuPassphrase call.
	tofuGen  int
	tofuPrev string

	// prov is the compute provider of the install (the fake provider in this build) and plan its latest plan.
	prov provider.Provider
	plan *provider.ProviderPlan
	// prompts counts the answers read from the operator in this run (50-AC-31).
	prompts int

	accts map[string]Account
}

func newSession(inv cli.Invocation, env map[string]string, stdout, stderr io.Writer, deps Deps) *sess {
	repo := env["SC_REPO_ROOT"]
	if repo == "" {
		repo = "."
	}
	bs := env["SC_BLACKSTART_DIR"]
	if bs == "" {
		bs = "/blackstart"
	}
	return &sess{
		inv: inv, env: env, out: stdout, errw: stderr, deps: deps, clk: deps.Clock, in: bufio.NewReader(deps.Stdin),
		repo: repo, boot: filepath.Join(repo, ".bootstrap"), blackstart: bs,
		red: redact.New(), lazy: &lazyStore{}, accts: map[string]Account{},
	}
}

func (s *sess) signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// printf writes a redacted line to stdout.
func (s *sess) printf(format string, a ...any) {
	fmt.Fprint(s.out, s.red.String(fmt.Sprintf(format, a...)))
}

// errorf writes a redacted line to stderr.
func (s *sess) errorf(format string, a ...any) {
	fmt.Fprint(s.errw, s.red.String(fmt.Sprintf(format, a...)))
}

func dig(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// localJournalUID reads install_uid from the local journal, "" when there is none yet.
func (s *sess) localJournalUID() string {
	b, err := os.ReadFile(filepath.Join(s.boot, "journal.json"))
	if err != nil {
		return ""
	}
	var j struct {
		UID string `json:"install_uid"`
	}
	if json.Unmarshal(b, &j) != nil || j.UID == zeroUID {
		return ""
	}
	return j.UID
}

// envFilePath is --env-file, or bootstrap.env in the input directory (plan 50 section 7.1).
func (s *sess) envFilePath() string {
	if s.inv.EnvFile != "" {
		return s.inv.EnvFile
	}
	in := s.env["SC_INPUT_DIR"]
	if in == "" {
		in = "/run/sc-input"
	}
	return filepath.Join(in, "bootstrap.env")
}

// load reads bootstrap.env and normalizes it. Preflight reports the problems of a configuration that does not load,
// with the plan 50 exit codes; load only needs a usable configuration.
func (s *sess) load() error {
	data, err := os.ReadFile(s.envFilePath())
	if err != nil {
		return err
	}
	vars, err := config.ParseEnv(data)
	if err != nil {
		return err
	}
	s.vars = vars
	s.merged = map[string]string{}
	for k, v := range vars {
		s.merged[k] = v
	}
	for k, v := range s.env {
		if v != "" && (strings.HasPrefix(k, "SC_TEST_") || strings.HasPrefix(k, "SC_LAB_S3_")) {
			s.merged[k] = v
		}
	}
	uid := s.localJournalUID()
	if uid == "" {
		uid = zeroUID
	}
	res, err := config.Normalize(s.merged, config.Options{Clock: s.clk, InstallUID: uid, Version: version.Version, Profile: s.inv.Profile})
	if err != nil {
		return err
	}
	s.doc = res.Doc
	s.cluster = str(dig(s.doc, "spec", "cluster", "name"))
	s.loadObjectStorage()
	s.registerEnvSecrets()
	return nil
}

func (s *sess) loadObjectStorage() {
	o := dig(s.doc, "spec", "objectStorage")
	pathStyle := dig(o, "forcePathStyle") == true
	oc := objCfg{
		prefix: str(dig(o, "bucketPrefix")), endpoint: str(dig(o, "endpoint")), region: str(dig(o, "region")),
		bucketMode: str(dig(o, "bucketMode")), credMode: str(dig(o, "credentialMode")), pathStyle: pathStyle,
		allowUnprotected: dig(o, "allowUnprotectedState") == true, instances: map[string]cred{},
	}
	oc.main = cred{Endpoint: oc.endpoint, Region: oc.region, PathStyle: pathStyle,
		AccessKey: s.merged["S3_ACCESS_KEY_ID"], SecretKey: s.merged["S3_SECRET_ACCESS_KEY"]}
	if st := dig(o, "stateAccount"); st != nil {
		oc.state = &cred{Endpoint: str(dig(st, "endpoint")), Region: oc.region, PathStyle: pathStyle,
			AccessKey: s.merged["S3_STATE_ACCESS_KEY_ID"], SecretKey: s.merged["S3_STATE_SECRET_ACCESS_KEY"]}
	}
	if list, ok := dig(o, "instanceCredentials").([]any); ok {
		for _, it := range list {
			name := str(dig(it, "instance"))
			up := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
			oc.instances[name] = cred{Endpoint: oc.endpoint, Region: oc.region, PathStyle: pathStyle,
				AccessKey: s.merged["S3_CRED_"+up+"_ACCESS_KEY_ID"], SecretKey: s.merged["S3_CRED_"+up+"_SECRET_ACCESS_KEY"]}
		}
	}
	s.oc = oc
}

// registerEnvSecrets hands every secret of bootstrap.env to the redactor before anything is logged.
func (s *sess) registerEnvSecrets() {
	for k, v := range s.vars {
		if v == "" || !config.IsSecretKey(k) {
			continue
		}
		class := "SC-BOOTSTRAP-ENV"
		if strings.HasPrefix(k, "S3_") {
			class = "SC-OBJECT-STORAGE"
		}
		s.red.Register(class, v)
	}
	for _, k := range []string{"S3_ACCESS_KEY_ID", "S3_STATE_ACCESS_KEY_ID"} {
		s.red.Register("SC-OBJECT-STORAGE", s.vars[k])
	}
}

func (s *sess) bucketName(purpose string) string { return s.oc.prefix + "-" + purpose }

func (s *sess) hasStateAccount() bool { return s.oc.state != nil }

// stateCred is the credential of B1 and B2: the second account when configured, else the main one.
func (s *sess) stateCred() cred {
	if s.oc.state != nil {
		return *s.oc.state
	}
	return s.oc.main
}

func (s *sess) credFor(purpose string) cred {
	if stateAccountPurposes[purpose] {
		return s.stateCred()
	}
	return s.oc.main
}

// bucketSpecs lists the ten buckets with the account each one is created in.
func (s *sess) bucketSpecs() []BucketSpec {
	var out []BucketSpec
	for _, p := range bucketPurposes {
		out = append(out, BucketSpec{Name: s.bucketName(p), StateAccount: s.hasStateAccount() && stateAccountPurposes[p]})
	}
	return out
}

func (s *sess) httpClient() *http.Client {
	c := &http.Client{Timeout: 60 * time.Second}
	if f := s.env["SC_LAB_S3_CA_FILE"]; f != "" && s.env["SC_TEST_MODE"] == "1" {
		if pem, err := os.ReadFile(f); err == nil {
			pool, perr := x509.SystemCertPool()
			if perr != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			pool.AppendCertsFromPEM(pem)
			c.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, Proxy: http.ProxyFromEnvironment}
		}
	}
	return c
}

// account opens (and caches) the S3 account of a credential.
func (s *sess) account(c cred) (Account, error) {
	key := c.Endpoint + "|" + c.AccessKey
	if a, ok := s.accts[key]; ok {
		return a, nil
	}
	a, err := s.deps.NewS3(s3client.Config{Endpoint: c.Endpoint, Region: c.Region, AccessKey: c.AccessKey, SecretKey: c.SecretKey,
		PathStyle: c.PathStyle, HTTP: s.httpClient(), Clock: s.clk})
	if err != nil {
		return nil, err
	}
	s.accts[key] = a
	return a, nil
}

// tryAttach binds the S3 journal and lock when the tofu-state bucket already exists and answers to the state
// credential: the resume case. It never fails the command.
func (s *sess) tryAttach(ctx context.Context) {
	if s.lazy.Attached() || s.oc.prefix == "" {
		return
	}
	acct, err := s.account(s.stateCred())
	if err != nil {
		return
	}
	name := s.bucketName("tofu-state")
	if herr := acct.HeadBucket(ctx, name); herr != nil {
		// only a definite "no such bucket" rules a remote journal out; dial errors, 403 and the rest stay unknown
		s.bucketAbsent = store.Is(herr, store.NotFound) && s.noSuchBucket(ctx, acct, name)
		return
	}
	_ = s.lazy.Attach(ctx, acct.Bucket(name), nil)
}

// noSuchBucket confirms an absent bucket with a body-bearing request: a HEAD 404 has no body, so a wrong endpoint, a
// proxy or a wildcard host cannot be told from a missing bucket by it. Only the error code NoSuchBucket counts.
func (s *sess) noSuchBucket(ctx context.Context, acct Account, name string) bool {
	_, err := acct.Bucket(name).List(ctx, "")
	var se *s3client.Error
	return errors.As(err, &se) && se.Status == 404 && se.Code == "NoSuchBucket"
}

// attach binds the journal store after plan.storage-apply created the bucket.
func (s *sess) attach(ctx context.Context) error {
	acct, err := s.account(s.stateCred())
	if err != nil {
		return err
	}
	uid := s.eng.InstallUID()
	return s.lazy.Attach(ctx, acct.Bucket(s.bucketName("tofu-state")), func(b []byte) []byte {
		if uid == "" {
			return b
		}
		return []byte(strings.ReplaceAll(string(b), zeroUID, uid))
	})
}

// engineConfig fills the engine configuration of this invocation.
func (s *sess) engineConfig(command string) engine.Config {
	user := s.env["USER"]
	if user == "" {
		user = "unknown"
	}
	digest := str(dig(s.doc, "metadata", "envDigest"))
	if digest != "" && !strings.HasPrefix(digest, "sha256:") {
		digest = "sha256:" + digest
	}
	m, markerOK := s.readMarker()
	cfg := engine.Config{
		Dir: s.boot, Store: s.lazy, IfMatch: markerOK && m.IfMatchSupported, Clock: s.clk, Cluster: s.cluster, InstallUID: s.localJournalUID(),
		Provider: str(dig(s.doc, "spec", "provider", "name")), Profile: str(dig(s.doc, "spec", "cluster", "profile")),
		ConfigDigest: digest, Command: command, User: user, Version: version.Version, GitHead: s.gitHead(context.Background()),
		ToolboxDigest: s.toolboxDigest(), Env: s.env, Redactor: s.red,
	}
	if s.env["SC_TEST_MODE"] == "1" {
		cfg.SourceSwitched = s.stubSourceSwitched
	}
	return cfg
}

func (s *sess) toolboxDigest() string {
	if d := s.env["SC_TOOLBOX_IMAGE_DIGEST"]; strings.HasPrefix(d, "sha256:") && len(d) == len("sha256:")+64 {
		return d
	}
	return ""
}

// git runs a read-only git command in the repository.
func (s *sess) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", s.repo}, args...)...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// gitHead is the HEAD commit, or the all-zero id outside a repository.
func (s *sess) gitHead(ctx context.Context) string {
	h, err := s.git(ctx, "rev-parse", "HEAD")
	if err != nil || len(h) != 40 {
		return strings.Repeat("0", 40)
	}
	return h
}

func sha256Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// dnsUpstream is the resolver cluster-vars is seeded with until provision rewrites it from discovery: SC_DNS_UPSTREAM_1
// from the environment, else the first nameserver of the toolbox resolver configuration.
func (s *sess) dnsUpstream() string {
	if v := s.env["SC_DNS_UPSTREAM_1"]; v != "" {
		return v
	}
	if b, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) == 2 && f[0] == "nameserver" {
				if a, err := netip.ParseAddr(f[1]); err == nil && !a.IsLoopback() {
					return a.String()
				}
			}
		}
	}
	return "1.1.1.1"
}

func (s *sess) resolve(host string) ([]netip.Addr, error) {
	if s.deps.CLI.Resolve != nil {
		return s.deps.CLI.Resolve(host)
	}
	return net.DefaultResolver.LookupNetIP(context.Background(), "ip", host)
}

// failure is a refusal with a plan 50 exit code, for the paths that run outside the engine.
type failure struct {
	code, msg string
	exit      int
}

func (f *failure) Error() string { return f.code + ": " + f.msg }

var errNoInput = errors.New("no input available")
