package installstate_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/bootstrap/cli"
	"strange.company/sc/internal/bootstrap/install"
	"strange.company/sc/internal/provider/fake"
)

// The fake install of S6a (plan 50 section 22): a scratch Git repository with fixture policy files, an
// in-memory S3 world with two accounts, a fake OpenTofu, the fake provider, and the real preflight, engine,
// identity and commit code behind install.Run. No network, no wall clock, no container, no weed.

const (
	clusterName = "lab-one"
	bucketPfx   = "sc-lab-one"

	mainKeyID   = "fixture-secret-s3-id-0006"
	mainSecret  = "fixture-secret-s3-key-0006"
	stateKeyID  = "fixture-secret-state-id-0006"
	stateSecret = "fixture-secret-state-key-0006"

	goodPass = "correct horse battery staple lab"

	budgetMonthly = "137.50"
	budgetDaily   = "11.25"
	anthropicMon  = "100.00"
)

// bucketPurposes is the plan 14 section 9.1 bucket set B1-B10.
var bucketPurposes = []string{
	"tofu-state", "recovery", "bootstrap-seed", "pg-backups", "longhorn-backups", "etcd-snapshots",
	"forgejo-objects", "repo-backups", "evidence", "telemetry",
}

// stateAccountPurposes are B1 and B2: in shared mode they live in the second account (plan 14 14-R-2a).
var stateAccountPurposes = []string{"tofu-state", "recovery"}

func bucketName(purpose string) string { return bucketPfx + "-" + purpose }

func allBucketNames() []string {
	var out []string
	for _, p := range bucketPurposes {
		out = append(out, bucketName(p))
	}
	sort.Strings(out)
	return out
}

func stateBucketNames() []string {
	var out []string
	for _, p := range stateAccountPurposes {
		out = append(out, bucketName(p))
	}
	sort.Strings(out)
	return out
}

func mainBucketNames() []string {
	skip := map[string]bool{}
	for _, n := range stateBucketNames() {
		skip[n] = true
	}
	var out []string
	for _, n := range allBucketNames() {
		if !skip[n] {
			out = append(out, n)
		}
	}
	return out
}

func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "plans", "50-bootstrap-orchestrator.md")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root not found")
		}
		dir = parent
	}
}

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

// --- OpenSSH key fixture (PF-06 needs a real key).

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

// inst is one scratch installation.
type inst struct {
	t          testing.TB
	dir        string
	repo       string
	blackstart string
	envFile    string
	key        string
	vars       map[string]string // the bootstrap.env content
	env        map[string]string // the process environment of sc-bootstrap
	clk        *tickClock
	world      *s3World
	tofu       *fakeTofu
}

func baseVars(key string) map[string]string {
	return map[string]string{
		"CLUSTER_NAME": clusterName, "BASE_DOMAIN": "lab.example.net", "ADMIN_EMAIL": "ops@example.net",
		"PROVIDER": "fake", "EXPECTED_NODE_COUNT": "1", "MAX_MONTHLY_COST": "0",
		"INGRESS_MODE": "direct", "DNS_PROVIDER": "manual", "ACME_SOLVER": "http01",
		"S3_ENDPOINT": "https://s3.main.example.net", "S3_REGION": "us-east-1", "S3_CREDENTIAL_MODE": "shared",
		"S3_ACCESS_KEY_ID": mainKeyID, "S3_SECRET_ACCESS_KEY": mainSecret,
		"S3_STATE_ENDPOINT": "https://s3.state.example.net", "S3_STATE_ACCESS_KEY_ID": stateKeyID, "S3_STATE_SECRET_ACCESS_KEY": stateSecret,
		"GITHUB_MIRROR_REPO": "example-org/sc-lab", "GITHUB_MIRROR_TOKEN": "fixture-secret-gh-mirror-0006",
		"GITHUB_PROBE_TOKEN": "fixture-secret-gh-probe-0006", "GITHUB_MIRROR_READ_TOKEN": "fixture-secret-gh-read-0006",
		"NOTIFY_TRANSPORT": "webhook", "NOTIFY_WEBHOOK_URL": "fixture-secret-webhook-0006",
		"ANTHROPIC_API_KEY":    "fixture-secret-anthropic-key-0006",
		"SSH_PRIVATE_KEY_PATH": key, "SSH_PUBLIC_KEY_PATH": key + ".pub",
	}
}

func (i *inst) git(args ...string) string {
	i.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", i.repo}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(i.dir, "home"), "GIT_CONFIG_NOSYSTEM=1"}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		i.t.Fatalf("git %v: %v\n%s", args, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

// newInst builds a fresh fake installation in shared credential mode with a second S3 account for the state
// buckets (the protected configuration of 50-AC-32).
func newInst(t testing.TB) *inst {
	t.Helper()
	dir := t.TempDir()
	i := &inst{t: t, dir: dir, repo: filepath.Join(dir, "repo"), blackstart: filepath.Join(dir, "blackstart")}
	if err := os.MkdirAll(filepath.Join(dir, "home"), 0o755); err != nil {
		t.Fatal(err)
	}
	emb := write(t, filepath.Join(dir, "emb.yaml"), toolLock, 0o644)
	write(t, filepath.Join(i.repo, "toolbox", "tools.lock.yaml"), toolLock, 0o644)
	reg, err := os.ReadFile(filepath.Join(repoRoot(t), "contracts", "secret-classes.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(i.repo, "contracts", "secret-classes.yaml"), string(reg), 0o644)
	write(t, filepath.Join(i.repo, ".gitignore"), ".bootstrap/\n", 0o644)
	i.key = writeEd25519(t, filepath.Join(dir, "ssh"), "id_ed25519")

	mr := schemaExample(t, "model-routing.schema.yaml")
	sub(mr, "spec", "providers", "deepseek")["enabled"] = false
	i.writePolicy("model-routing.yaml", mr)
	bp := schemaExample(t, "budget-policy.schema.yaml")
	sub(bp, "spec", "global")["monthlyUsd"] = budgetMonthly
	sub(bp, "spec", "global")["dailyUsd"] = budgetDaily
	sub(bp, "spec", "providers", "anthropic")["monthlyUsd"] = anthropicMon
	i.writePolicy("budgets.yaml", bp)

	i.git("init", "-q", "-b", "main")
	i.git("add", "-A")
	i.git("-c", "user.name=fixture", "-c", "user.email=fixture@example.net", "commit", "-q", "-m", "fixture repository")

	i.env = map[string]string{
		"SC_EMBEDDED_LOCK": emb, "SC_REPO_ROOT": i.repo, "HOME": filepath.Join(dir, "home"),
		"SC_BLACKSTART_DIR": i.blackstart, "SC_TEST_MODE": "1",
	}
	i.clk = newTickClock()
	i.world = newS3World(i.clk, mainKeyID, stateKeyID)
	i.tofu = &fakeTofu{w: i.world}
	i.vars = baseVars(i.key)
	i.writeEnv()
	return i
}

func (i *inst) writePolicy(name string, doc map[string]any) {
	b, err := yaml.Marshal(doc)
	if err != nil {
		i.t.Fatal(err)
	}
	write(i.t, filepath.Join(i.repo, "policy", name), string(b), 0o644)
}

// setVars changes the bootstrap.env content (a "" value drops the key) and rewrites the file.
func (i *inst) setVars(set map[string]string) {
	for k, v := range set {
		if v == "" {
			delete(i.vars, k)
		} else {
			i.vars[k] = v
		}
	}
	i.writeEnv()
}

func (i *inst) writeEnv() {
	var keys []string
	for k := range i.vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s=%s\n", k, i.vars[k])
	}
	i.envFile = write(i.t, filepath.Join(i.dir, "bootstrap.env"), sb.String(), 0o600)
}

// dropStateAccount removes S3_STATE_* from bootstrap.env.
func (i *inst) dropStateAccount() {
	i.setVars(map[string]string{"S3_STATE_ENDPOINT": "", "S3_STATE_ACCESS_KEY_ID": "", "S3_STATE_SECRET_ACCESS_KEY": ""})
}

func (i *inst) backstopFile() string {
	b, err := yaml.Marshal(map[string]any{"providers": map[string]any{"anthropic": map[string]any{"limitUsd": 125, "consoleMfaEnabled": true}}})
	if err != nil {
		i.t.Fatal(err)
	}
	return write(i.t, filepath.Join(i.dir, "backstop-ok.yaml"), string(b), 0o600)
}

// args builds a command line: the global flags that confirm the budget and the provider backstops (the preflight
// confirmations are not under test here), then the given arguments.
func (i *inst) args(a ...string) []string {
	return append([]string{"--env-file", i.envFile, "--confirm-budget", budgetMonthly, "--confirm-backstops", i.backstopFile()}, a...)
}

func (i *inst) deps(stdin string) install.Deps {
	return install.Deps{
		CLI: cli.Deps{
			ProbeModelKey: func(string, string) error { return nil },
			Resolve:       func(string) ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("203.0.113.9")}, nil },
		},
		Clock:      i.clk,
		Stdin:      strings.NewReader(stdin),
		NewS3:      i.world.open,
		Tofu:       i.tofu,
		LockProbe:  goodProbe{},
		Encrypt:    fakeEncrypt,
		WorkFactor: 18,
	}
}

// run executes install.Run and returns the exit code and stdout+stderr. extraEnv is added to the process environment.
func (i *inst) run(stdin string, extraEnv map[string]string, args []string) (int, string) {
	i.t.Helper()
	env := map[string]string{}
	for k, v := range i.env {
		env[k] = v
	}
	for k, v := range extraEnv {
		env[k] = v
	}
	var out, errb bytes.Buffer
	code := install.Run(args, env, &out, &errb, i.deps(stdin))
	return code, out.String() + errb.String()
}

// passphraseFile writes the SC_RECOVERY_PASSPHRASE_FILE of an unattended run.
func (i *inst) passphraseFile() map[string]string {
	p := write(i.t, filepath.Join(i.dir, "passphrase"), goodPass+"\n", 0o600)
	return map[string]string{"SC_RECOVERY_PASSPHRASE_FILE": p}
}

func twice(p string) string { return p + "\n" + p + "\n" }

func (i *inst) fakeProviderDir() string { return filepath.Join(i.repo, ".bootstrap", "fake-provider") }

func (i *inst) createCount() int {
	i.t.Helper()
	n, err := fake.CreateCount(i.fakeProviderDir())
	if err != nil {
		i.t.Fatalf("fake provider create counter: %v", err)
	}
	return n
}

// --- journal

type jStep struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  *struct {
		Code string `json:"code"`
	} `json:"error"`
	OutputRefs []struct {
		Kind   string `json:"kind"`
		Ref    string `json:"ref"`
		Sha256 string `json:"sha256"`
	} `json:"output_refs"`
}

type journal struct {
	InstallUID   string `json:"install_uid"`
	Installation struct {
		State string `json:"state"`
	} `json:"installation"`
	RecoveryBundle struct {
		Status      string `json:"status"`
		Generation  int    `json:"generation"`
		Sha256      string `json:"sha256"`
		LocalPath   string `json:"local_path"`
		S3URI       string `json:"s3_uri"`
		S3VersionID string `json:"s3_version_id"`
	} `json:"recovery_bundle"`
	Pipelines []struct {
		Pipeline string `json:"pipeline"`
		Phases   []struct {
			Name   string  `json:"name"`
			Status string  `json:"status"`
			Steps  []jStep `json:"steps"`
		} `json:"phases"`
	} `json:"pipelines"`
	Confirmations []jConfirm `json:"confirmations"`
}

type jConfirm struct {
	Kind          string `json:"kind"`
	SubjectDigest string `json:"subject_digest"`
	ConfirmedAt   string `json:"confirmed_at"`
	Method        string `json:"method"`
}

func parseJournal(t testing.TB, b []byte) journal {
	t.Helper()
	var j journal
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatalf("journal does not parse: %v\n%s", err, b)
	}
	return j
}

// journal reads the local journal; missing is a test failure.
func (i *inst) journal() journal {
	i.t.Helper()
	b, err := os.ReadFile(filepath.Join(i.repo, ".bootstrap", "journal.json"))
	if err != nil {
		i.t.Fatalf("no local journal: %v", err)
	}
	return parseJournal(i.t, b)
}

// journalIfAny reads the local journal when there is one.
func (i *inst) journalIfAny() (journal, bool) {
	b, err := os.ReadFile(filepath.Join(i.repo, ".bootstrap", "journal.json"))
	if err != nil {
		return journal{}, false
	}
	return parseJournal(i.t, b), true
}

func (j journal) step(name string) (jStep, bool) {
	for _, p := range j.Pipelines {
		for _, ph := range p.Phases {
			for _, s := range ph.Steps {
				if s.Name == name {
					return s, true
				}
			}
		}
	}
	return jStep{}, false
}

func (j journal) confirmation(kind string) (jConfirm, bool) {
	for _, c := range j.Confirmations {
		if c.Kind == kind {
			return c, true
		}
	}
	return jConfirm{}, false
}

func parseTime(t testing.TB, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("timestamp %q: %v", s, err)
	}
	return ts
}

// treeText returns every byte under dir (skipping .git) as one string: the secrets-in-files scan.
func treeText(t testing.TB, dir string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		b, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		sb.Write(b)
		sb.WriteByte('\n')
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

// installCommits lists the subjects of the installation config commits in the scratch repository.
func (i *inst) installCommits() []string {
	var out []string
	for _, l := range strings.Split(i.git("log", "--format=%s"), "\n") {
		if l == "bootstrap("+clusterName+"): installation config" {
			out = append(out, l)
		}
	}
	return out
}

func sha256Hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
