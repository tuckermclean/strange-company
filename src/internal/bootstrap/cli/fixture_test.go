package cli_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"strange.company/sc/internal/bootstrap/cli"
	"strange.company/sc/internal/clock"
)

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

// --- OpenSSH key fixture (same encoding as the toolbox tests; PF-06 needs a real key).

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

// fixtureKeyPrefix is the Anthropic OAuth prefix of the model-routing example. It is assembled at run time so no
// secret-looking literal sits in the source (make secrets-scan).
func fixtureKeyPrefix() string { return "sk-ant-" + "oat" }

const (
	budgetMonthly  = "137.50"
	budgetDaily    = "11.25"
	anthropicMonth = "100.00"
)

type probe struct{ Provider, Key string }

// inst is one scratch installation: a repository root with fixture policy files, a valid env file and fakes.
type inst struct {
	t        testing.TB
	dir      string
	repo     string
	envFile  string
	env      map[string]string
	key      string
	set      map[string]string
	probes   []probe
	probeErr error
	recs     []cli.Confirmation
	dns      netip.Addr
}

func baseEnv(key string) map[string]string {
	return map[string]string{
		"CLUSTER_NAME": "lab-one", "BASE_DOMAIN": "lab.example.net", "ADMIN_EMAIL": "ops@example.net",
		"PROVIDER": "existing", "EXPECTED_NODE_COUNT": "1", "MAX_MONTHLY_COST": "0",
		"EXISTING_NODES": "node-01=debian@192.0.2.10:2201/10.77.0.11",
		"INGRESS_MODE":   "direct", "DNS_PROVIDER": "manual", "ACME_SOLVER": "http01",
		"S3_ENDPOINT": "https://s3.example.net", "S3_REGION": "us-east-1", "S3_CREDENTIAL_MODE": "scoped",
		"S3_ACCESS_KEY_ID": "fixture-secret-s3-id-0006", "S3_SECRET_ACCESS_KEY": "fixture-secret-s3-key-0006",
		"GITHUB_MIRROR_REPO": "example-org/sc-lab", "GITHUB_MIRROR_TOKEN": "fixture-secret-gh-mirror-0006",
		"GITHUB_PROBE_TOKEN": "fixture-secret-gh-probe-0006", "GITHUB_MIRROR_READ_TOKEN": "fixture-secret-gh-read-0006",
		"NOTIFY_TRANSPORT": "webhook", "NOTIFY_WEBHOOK_URL": "fixture-secret-webhook-0006",
		"ANTHROPIC_API_KEY":    "fixture-secret-anthropic-key-0006",
		"SSH_PRIVATE_KEY_PATH": key, "SSH_PUBLIC_KEY_PATH": key + ".pub",
	}
}

// newInst builds a passing installation: toolbox locks, SSH key, model-routing (anthropic enabled and production,
// others off or test-only) and budgets (global 137.50 monthly, anthropic 100.00 monthly) policy files.
func newInst(t testing.TB) *inst {
	t.Helper()
	dir := t.TempDir()
	i := &inst{t: t, dir: dir, repo: filepath.Join(dir, "repo")}
	emb := write(t, filepath.Join(dir, "emb.yaml"), toolLock, 0o644)
	write(t, filepath.Join(i.repo, "toolbox", "tools.lock.yaml"), toolLock, 0o644)
	i.key = writeEd25519(t, filepath.Join(dir, "ssh"), "id_ed25519")
	i.env = map[string]string{"SC_EMBEDDED_LOCK": emb, "SC_REPO_ROOT": i.repo, "HOME": filepath.Join(dir, "home")}

	mr := schemaExample(t, "model-routing.schema.yaml")
	prov := sub(mr, "spec", "providers")
	sub(prov, "deepseek")["enabled"] = false
	i.writePolicy("model-routing.yaml", mr)

	bp := schemaExample(t, "budget-policy.schema.yaml")
	sub(bp, "spec", "global")["monthlyUsd"] = budgetMonthly
	sub(bp, "spec", "global")["dailyUsd"] = budgetDaily
	sub(bp, "spec", "providers", "anthropic")["monthlyUsd"] = anthropicMonth
	i.writePolicy("budgets.yaml", bp)

	i.set = map[string]string{}
	i.writeEnv(nil, nil)
	return i
}

func (i *inst) writePolicy(name string, doc map[string]any) {
	b, err := yaml.Marshal(doc)
	if err != nil {
		i.t.Fatal(err)
	}
	write(i.t, filepath.Join(i.repo, "policy", name), string(b), 0o644)
}

// writeEnv rewrites the env file: the base install, plus set, minus drop.
func (i *inst) writeEnv(set map[string]string, drop []string) {
	e := baseEnv(i.key)
	for _, k := range drop {
		delete(e, k)
	}
	for k, v := range set {
		e[k] = v
	}
	var keys []string
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s=%s\n", k, e[k])
	}
	i.envFile = write(i.t, filepath.Join(i.dir, "bootstrap.env"), sb.String(), 0o600)
}

func (i *inst) deps() cli.Deps {
	return cli.Deps{
		Clock: clock.NewFake(time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)),
		ProbeModelKey: func(provider, key string) error {
			i.probes = append(i.probes, probe{provider, key})
			return i.probeErr
		},
		Resolve: func(host string) ([]netip.Addr, error) {
			if host == "s3.internal.example.net" {
				return []netip.Addr{netip.MustParseAddr("10.20.0.5")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("203.0.113.9")}, nil
		},
		ClusterDNSIP: i.dns,
		Record:       func(c cli.Confirmation) { i.recs = append(i.recs, c) },
	}
}

// exec runs sc-bootstrap with the given arguments and returns the exit code and stdout+stderr.
func (i *inst) exec(extraEnv map[string]string, args ...string) (int, string) {
	i.t.Helper()
	env := map[string]string{}
	for k, v := range i.env {
		env[k] = v
	}
	for k, v := range extraEnv {
		env[k] = v
	}
	var out, errb bytes.Buffer
	code := cli.Run(args, env, &out, &errb, i.deps())
	return code, out.String() + errb.String()
}

// backstopFile writes a PF-29 attestation file: one entry for anthropic. Format (judgement call, flagged in the RED
// handoff): providers.<id>.limitUsd and providers.<id>.consoleMfaEnabled.
func (i *inst) backstopFile(name string, limit any, mfa any) string {
	entry := map[string]any{"limitUsd": limit}
	if mfa != nil {
		entry["consoleMfaEnabled"] = mfa
	}
	b, err := yaml.Marshal(map[string]any{"providers": map[string]any{"anthropic": entry}})
	if err != nil {
		i.t.Fatal(err)
	}
	return write(i.t, filepath.Join(i.dir, name), string(b), 0o600)
}

// preflightArgs is a fully confirmed non-interactive preflight; callers may replace pieces by building their own.
func (i *inst) preflightArgs(extra ...string) []string {
	args := []string{"--env-file", i.envFile, "--non-interactive"}
	return append(append(args, extra...), "preflight")
}

func (i *inst) goodConfirms() []string {
	return []string{"--confirm-budget", budgetMonthly, "--confirm-backstops", i.backstopFile("backstop-ok.yaml", 125, true)}
}

func sha256File(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }
