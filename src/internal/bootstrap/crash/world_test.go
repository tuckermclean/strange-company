package crash_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/provider"
	"strange.company/sc/internal/provider/fake"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/s3emu"
)

// The world of the crash matrix (plan 50 section 22, BOOT-5): the real install pipeline runs as a subprocess
// (testdata/engine, built once without -race), so that SC_TEST_CRASH_AT can deliver a real SIGKILL to it. Everything
// that must outlive the killed process lives in the test process or in a process of its own:
//
//   - S3 is the SeaweedFS emulator (ADR-0205, `weed` is in the ci image that `make test` runs in; the package fails,
//     not skips, when it is missing), one per scenario because an emulator allows 16 volumes;
//   - the configuration requires an https:// S3_ENDPOINT, so the subprocess talks to a TLS front served from this
//     process (SC_LAB_S3_CA_FILE names its certificate, plan 50 section 9.2). The front counts every CreateBucket
//     request per bucket: "no duplicate bucket creations" is judged from the wire, not from what the engine reports;
//   - the fake provider's state (.bootstrap/fake-provider) and the stub cluster (.bootstrap/stub-cluster) are the
//     "cloud" and the "cluster" of the fake install; they are files of the workspace, and a new machine is given
//     a copy of them.

const (
	clusterName = "lab-one"

	mainKeyID   = "fixture-secret-s3-id-0008"
	mainSecret  = "fixture-secret-s3-key-0008"
	stateKeyID  = "fixture-secret-state-id-0008"
	stateSecret = "fixture-secret-state-key-0008"

	goodPass = "correct horse battery staple lab"

	budgetMonthly = "137.50"
	budgetDaily   = "11.25"
	anthropicMon  = "100.00"
)

var bucketPurposes = []string{
	"tofu-state", "recovery", "bootstrap-seed", "pg-backups", "longhorn-backups", "etcd-snapshots",
	"forgejo-objects", "repo-backups", "evidence", "telemetry",
}

type worldState struct {
	once sync.Once
	err  error

	tmp       string
	engineBin string
	weedBin   string
	nextID    atomic.Int32
}

var theWorld worldState

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found")
		}
		dir = parent
	}
}

func repoRoot(t testing.TB) string {
	t.Helper()
	m, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(m)
}

// getWorld builds the engine under test on first use; a failure there fails the test that needed it.
func getWorld(t testing.TB) *worldState {
	t.Helper()
	w := &theWorld
	w.once.Do(func() { w.err = w.start() })
	if w.err != nil {
		t.Fatalf("crash matrix infrastructure: %v", w.err)
	}
	return w
}

func (w *worldState) start() error {
	var err error
	if w.tmp, err = os.MkdirTemp("", "crash-matrix-"); err != nil {
		return err
	}
	// The engine under test: testdata/engine wraps install.Run (see its package comment).
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	w.engineBin = filepath.Join(w.tmp, "engine")
	build := exec.Command("go", "build", "-o", w.engineBin, "./internal/bootstrap/crash/testdata/engine")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("go build of the engine under test: %v\n%s", err, out)
	}
	name := os.Getenv("SC_WEED_BIN")
	if name == "" {
		name = "weed"
	}
	if w.weedBin, err = exec.LookPath(name); err != nil {
		return fmt.Errorf("weed: %v (the crash matrix needs the SeaweedFS emulator of the ci image; set SC_WEED_BIN to override)", err)
	}
	return nil
}

func (w *worldState) shutdown() {
	if w.tmp != "" {
		_ = os.RemoveAll(w.tmp)
	}
}

// startS3 starts an emulator of its own for one scenario, with the TLS front in front of it. The emulator allows 16
// volumes and gives every bucket's first write several, so one emulator cannot serve the buckets of many scenarios.
func (s *scenario) startS3() {
	s.t.Helper()
	data := filepath.Join(s.dir, "weed")
	if err := os.MkdirAll(data, 0o755); err != nil {
		s.t.Fatal(err)
	}
	// The free ports are looked up before the emulator binds them, so two starts at the same moment can collide: try again.
	var emu *s3emu.Emulator
	var err error
	for try := 1; try <= 3; try++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		emu, err = s3emu.Start(ctx, s3emu.Options{Binary: s.w.weedBin, DataDir: data})
		cancel()
		if err == nil {
			break
		}
	}
	if err != nil {
		s.t.Fatalf("starting the SeaweedFS emulator: %v", err)
	}
	s.t.Cleanup(func() { _ = emu.Stop() })
	s.s3side = &s3side{emu: emu}
	up, err := url.Parse(emu.Endpoint())
	if err != nil {
		s.t.Fatal(err)
	}
	rp := httputil.NewSingleHostReverseProxy(up)
	s.creates = map[string]int{}
	s.front = httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.RawQuery == "" {
			if b := strings.Trim(r.URL.Path, "/"); b != "" && !strings.Contains(b, "/") {
				s.mu.Lock()
				s.creates[b]++
				s.mu.Unlock()
			}
		}
		rp.ServeHTTP(rw, r)
	}))
	s.front.StartTLS()
	s.t.Cleanup(s.front.Close)
	s.caFile = filepath.Join(s.dir, "front-ca.pem")
	write(s.t, s.caFile, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.front.Certificate().Raw})), 0o644)
}

func (s *scenario) createsOf(bucket string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.creates[bucket]
}

// httpClient trusts the front's certificate.
func (s *scenario) httpClient() *http.Client {
	pool := x509.NewCertPool()
	pool.AddCert(s.front.Certificate())
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
}

// --- fixture files

func write(t testing.TB, path, content string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func sshString(b []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(b)))
	return append(out, b...)
}

func writeEd25519(t testing.TB, dir, name string) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blob := append(sshString([]byte("ssh-ed25519")), sshString(pub)...)
	p := []byte{1, 2, 3, 4, 1, 2, 3, 4}
	p = append(p, sshString([]byte("ssh-ed25519"))...)
	p = append(p, sshString(pub)...)
	p = append(p, sshString(priv)...)
	p = append(p, sshString([]byte("fixture"))...)
	for i := byte(1); len(p)%8 != 0; i++ {
		p = append(p, i)
	}
	var body []byte
	body = append(body, "openssh-key-v1\x00"...)
	body = append(body, sshString([]byte("none"))...)
	body = append(body, sshString([]byte("none"))...)
	body = append(body, sshString(nil)...)
	body = binary.BigEndian.AppendUint32(body, 1)
	body = append(body, sshString(blob)...)
	body = append(body, sshString(p)...)
	b64 := base64.StdEncoding.EncodeToString(body)
	var sb strings.Builder
	sb.WriteString("-----BEGIN OPENSSH PRIVATE KEY-----\n")
	for len(b64) > 70 {
		sb.WriteString(b64[:70] + "\n")
		b64 = b64[70:]
	}
	sb.WriteString(b64 + "\n-----END OPENSSH PRIVATE KEY-----\n")
	path := write(t, filepath.Join(dir, name), sb.String(), 0o600)
	write(t, path+".pub", "ssh-ed25519 "+base64.StdEncoding.EncodeToString(blob)+" fixture\n", 0o644)
	return path
}

const toolLock = `apiVersion: strange.company/v1alpha1
kind: ToolboxLock
metadata:
  name: fixture
spec:
  tools:
    - name: faketool
      set: runtime
      version: "1.2.3"
      url: http://127.0.0.1:1/unused
      sha256:
        amd64: "0000000000000000000000000000000000000000000000000000000000000000"
        arm64: "0000000000000000000000000000000000000000000000000000000000000000"
      install: {to: /usr/local/bin/faketool}
      versionCommand: [sh, -c, "echo v1.2.3"]
      versionRegex: '^v{version}$'
`

// schemaExample reads examples[0] of a contract, so the policy fixtures are valid by construction.
func schemaExample(t testing.TB, contract string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "contracts", contract))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Examples []map[string]any `yaml:"examples"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil || len(doc.Examples) == 0 {
		t.Fatalf("%s: no examples (%v)", contract, err)
	}
	return doc.Examples[0]
}

func sub(m map[string]any, path ...string) map[string]any {
	for _, p := range path {
		m = m[p].(map[string]any)
	}
	return m
}

// --- one scenario: one workspace on one "machine", one bucket prefix

type scenario struct {
	t          testing.TB
	w          *worldState
	dir        string
	repo       string
	blackstart string
	logDir     string
	envFile    string
	key        string
	prefix     string
	vars       map[string]string
	env        map[string]string // the process environment of the engine under test

	*s3side
}

// s3side is the object store of an installation: it outlives every machine the installation runs on.
type s3side struct {
	emu     *s3emu.Emulator
	front   *httptest.Server
	caFile  string
	mu      sync.Mutex
	creates map[string]int // CreateBucket requests seen by the TLS front, per bucket
}

func (s *scenario) bucket(purpose string) string { return s.prefix + "-" + purpose }

func (s *scenario) git(args ...string) string {
	s.t.Helper()
	return gitIn(s.t, s.repo, args...)
}

func gitIn(t testing.TB, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(filepath.Dir(repo), "home"), "GIT_CONFIG_NOSYSTEM=1"}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

// newScenario builds a fresh fake installation: a scratch Git repository with fixture policy files and a bootstrap.env
// for provider fake against the shared emulator, in shared credential mode with a second S3 key for the state buckets.
func newScenario(t testing.TB) *scenario {
	t.Helper()
	w := getWorld(t)
	s := &scenario{t: t, w: w, dir: t.TempDir()}
	s.repo = filepath.Join(s.dir, "repo")
	s.blackstart = filepath.Join(s.dir, "blackstart")
	s.logDir = filepath.Join(s.dir, "log")
	s.prefix = fmt.Sprintf("sc-p%03d", w.nextID.Add(1))
	s.startS3()
	if err := os.MkdirAll(filepath.Join(s.dir, "home"), 0o755); err != nil {
		t.Fatal(err)
	}
	emb := write(t, filepath.Join(s.dir, "emb.yaml"), toolLock, 0o644)
	write(t, filepath.Join(s.repo, "toolbox", "tools.lock.yaml"), toolLock, 0o644)
	reg, err := os.ReadFile(filepath.Join(repoRoot(t), "contracts", "secret-classes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(s.repo, "contracts", "secret-classes.yaml"), string(reg), 0o644)
	write(t, filepath.Join(s.repo, ".gitignore"), ".bootstrap/\n", 0o644)
	s.key = writeEd25519(t, filepath.Join(s.dir, "ssh"), "id_ed25519")

	mr := schemaExample(t, "model-routing.schema.yaml")
	sub(mr, "spec", "providers", "deepseek")["enabled"] = false
	s.writePolicy("model-routing.yaml", mr)
	bp := schemaExample(t, "budget-policy.schema.yaml")
	sub(bp, "spec", "global")["monthlyUsd"] = budgetMonthly
	sub(bp, "spec", "global")["dailyUsd"] = budgetDaily
	sub(bp, "spec", "providers", "anthropic")["monthlyUsd"] = anthropicMon
	s.writePolicy("budgets.yaml", bp)

	s.git("init", "-q", "-b", "main")
	s.git("add", "-A")
	s.git("-c", "user.name=fixture", "-c", "user.email=fixture@example.net", "commit", "-q", "-m", "fixture repository")

	s.env = map[string]string{
		"SC_EMBEDDED_LOCK": emb, "SC_REPO_ROOT": s.repo, "HOME": filepath.Join(s.dir, "home"),
		"SC_BLACKSTART_DIR": s.blackstart, "SC_TEST_MODE": "1", "SC_LAB_S3_CA_FILE": s.caFile,
		"CRASH_TEST_LOG_DIR": s.logDir,
	}
	s.vars = map[string]string{
		"CLUSTER_NAME": clusterName, "BASE_DOMAIN": "lab.example.net", "ADMIN_EMAIL": "ops@example.net",
		"PROVIDER": "fake", "EXPECTED_NODE_COUNT": "1", "MAX_MONTHLY_COST": "0",
		"INGRESS_MODE": "direct", "DNS_PROVIDER": "manual", "ACME_SOLVER": "http01",
		"S3_ENDPOINT": s.front.URL, "S3_REGION": "us-east-1", "S3_CREDENTIAL_MODE": "shared", "S3_FORCE_PATH_STYLE": "true",
		"S3_BUCKET_PREFIX": s.prefix, "S3_PRIVATE_CIDR": "127.0.0.0/8",
		"S3_ACCESS_KEY_ID": mainKeyID, "S3_SECRET_ACCESS_KEY": mainSecret,
		"S3_STATE_ENDPOINT": s.front.URL, "S3_STATE_ACCESS_KEY_ID": stateKeyID, "S3_STATE_SECRET_ACCESS_KEY": stateSecret,
		"GITHUB_MIRROR_REPO": "example-org/sc-lab", "GITHUB_MIRROR_TOKEN": "fixture-secret-gh-mirror-0008",
		"GITHUB_PROBE_TOKEN": "fixture-secret-gh-probe-0008", "GITHUB_MIRROR_READ_TOKEN": "fixture-secret-gh-read-0008",
		"NOTIFY_TRANSPORT": "webhook", "NOTIFY_WEBHOOK_URL": "fixture-secret-webhook-0008",
		"ANTHROPIC_API_KEY":    "fixture-secret-anthropic-key-0008",
		"SSH_PRIVATE_KEY_PATH": s.key, "SSH_PUBLIC_KEY_PATH": s.key + ".pub",
	}
	s.writeEnv()
	return s
}

func (s *scenario) writePolicy(name string, doc map[string]any) {
	b, err := yaml.Marshal(doc)
	if err != nil {
		s.t.Fatal(err)
	}
	write(s.t, filepath.Join(s.repo, "policy", name), string(b), 0o644)
}

func (s *scenario) writeEnv() {
	var keys []string
	for k := range s.vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s=%s\n", k, s.vars[k])
	}
	s.envFile = write(s.t, filepath.Join(s.dir, "bootstrap.env"), sb.String(), 0o600)
}

func (s *scenario) backstopFile() string {
	b, err := yaml.Marshal(map[string]any{"providers": map[string]any{"anthropic": map[string]any{"limitUsd": 125, "consoleMfaEnabled": true}}})
	if err != nil {
		s.t.Fatal(err)
	}
	return write(s.t, filepath.Join(s.dir, "backstop-ok.yaml"), string(b), 0o600)
}

// unattendedEnv is the runtime-only environment of plan 50 section 9.2: every confirmation and the passphrase file.
// The plan confirmation names the first 8 hex characters of the fake provider's plan digest (plan 03 section 14.7).
func (s *scenario) unattendedEnv(extra ...map[string]string) map[string]string {
	s.t.Helper()
	pass := write(s.t, filepath.Join(s.dir, "passphrase"), goodPass+"\n", 0o600)
	out := map[string]string{
		"SC_RECOVERY_PASSPHRASE_FILE": pass, "SC_CONFIRM_STORAGE": clusterName, "SC_CONFIRM_PLAN": s.planD8(),
		"SC_CONFIRM_BUDGET": budgetMonthly, "SC_CONFIRM_BACKSTOPS": s.backstopFile(),
	}
	for _, m := range extra {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func (s *scenario) planD8() string {
	s.t.Helper()
	return planD8Of(s.t)
}

func (s *scenario) runArgs(a ...string) []string {
	return append([]string{"--env-file", s.envFile}, a...)
}

// --- running the engine under test

type result struct {
	exit   int            // exit code; -1 when the process was killed by a signal
	signal syscall.Signal // the fatal signal, 0 when the process exited
	out    string         // stdout followed by stderr
	stdout string
}

func (r result) killedByKill() bool { return r.signal == syscall.SIGKILL }

// run executes the engine under test with the given extra environment and arguments.
func (s *scenario) run(stdin string, extraEnv map[string]string, args []string) result {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.w.engineBin, args...)
	cmd.Dir = s.dir
	env := []string{"PATH=" + os.Getenv("PATH")}
	merged := map[string]string{}
	for k, v := range s.env {
		merged[k] = v
	}
	for k, v := range extraEnv {
		merged[k] = v
	}
	for k, v := range merged {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := result{out: out.String() + errb.String(), stdout: out.String()}
	if err == nil {
		return res
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		s.t.Fatalf("starting the engine under test: %v", err)
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		res.exit, res.signal = -1, ws.Signal()
		return res
	}
	res.exit = ee.ExitCode()
	return res
}

func (s *scenario) requireExit(res result, want int, what string) {
	s.t.Helper()
	if res.exit != want || res.signal != 0 {
		s.t.Fatalf("%s: exit %d (signal %v), want exit %d:\n%s", what, res.exit, res.signal, want, tail(res.out, 3000))
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}

// --- the observables

func (s *scenario) journalPath() string { return filepath.Join(s.repo, ".bootstrap", "journal.json") }

func (s *scenario) journalBytes() ([]byte, bool) {
	b, err := os.ReadFile(s.journalPath())
	return b, err == nil
}

func (s *scenario) journal() map[string]any {
	s.t.Helper()
	b, ok := s.journalBytes()
	if !ok {
		s.t.Fatalf("no local journal at %s", s.journalPath())
	}
	var j map[string]any
	if err := json.Unmarshal(b, &j); err != nil {
		s.t.Fatalf("the local journal does not parse: %v", err)
	}
	return j
}

func (s *scenario) createCount() int {
	s.t.Helper()
	n, err := fake.CreateCount(filepath.Join(s.repo, ".bootstrap", "fake-provider"))
	if err != nil {
		s.t.Fatalf("fake provider create counter: %v", err)
	}
	return n
}

func commitSubjects(t testing.TB, repo string) []string {
	t.Helper()
	out := gitIn(t, repo, "log", "--format=%s")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func countSubject(subjects []string, want string) int {
	n := 0
	for _, l := range subjects {
		if l == want {
			n++
		}
	}
	return n
}

func (s *scenario) installSubject() string {
	return "bootstrap(" + clusterName + "): installation config"
}

// client is an S3 client with the main or the state key (the test's own view of the emulator).
func (s *scenario) client(state bool) *s3client.Client {
	s.t.Helper()
	id, secret := mainKeyID, mainSecret
	if state {
		id, secret = stateKeyID, stateSecret
	}
	c, err := s3client.New(s3client.Config{Endpoint: s.front.URL, Region: "us-east-1", AccessKey: id, SecretKey: secret, PathStyle: true, HTTP: s.httpClient()})
	if err != nil {
		s.t.Fatal(err)
	}
	return c
}

func (s *scenario) lockKey() string { return clusterName + "/locks/bootstrap.lock" }

// lockObject reads the run lock object from the tofu-state bucket.
func (s *scenario) lockObject() ([]byte, bool) {
	s.t.Helper()
	o, err := s.client(true).Bucket(s.bucket("tofu-state")).Get(context.Background(), s.lockKey())
	if err != nil {
		return nil, false
	}
	return o.Data, true
}

func (s *scenario) listKeys(purpose, prefix string) []string {
	s.t.Helper()
	keys, err := s.client(purpose == "tofu-state" || purpose == "recovery").Bucket(s.bucket(purpose)).List(context.Background(), prefix)
	if err != nil {
		return nil
	}
	sort.Strings(keys)
	return keys
}

// --- the plan digest of the fixture installation

func planD8Of(t testing.TB) string {
	t.Helper()
	p, err := fake.New(map[string]string{"SC_TEST_MODE": "1"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := p.Plan(context.Background(), provider.Desired{Cluster: clusterName, NodeCount: 1})
	if err != nil || plan == nil || !strings.HasPrefix(plan.Digest, "sha256:") {
		t.Fatalf("fake provider plan = %+v, %v", plan, err)
	}
	return strings.TrimPrefix(plan.Digest, "sha256:")[:8]
}
