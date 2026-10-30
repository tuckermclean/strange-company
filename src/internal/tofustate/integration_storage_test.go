//go:build integration

package tofustate_test

// Tier-E tests of F08 slice S8b3 (plan 06 section 22 "full storage apply, conformance, migrate, compute init, import
// rebuild"; plan 50 section 7.1): the production install.ExecTofu with the real `tofu` and the real hashicorp/aws provider
// (pinned in infra/tofu/storage/main.tf) applies the storage module against SeaweedFS. The aws plugin is downloaded from
// the registry, so these tests need network; TF_PLUGIN_CACHE_DIR keeps one copy for the whole run.
//
//	cd src && go test -count=1 -tags integration -run Integration ./internal/tofustate/...

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/bootstrap/install"
	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/s3emu"
	"strange.company/sc/internal/tofustate"
)

// pluginCache is one provider cache shared by every test of the run.
func pluginCache(t *testing.T) string {
	t.Helper()
	d := os.Getenv("TF_PLUGIN_CACHE_DIR")
	if d == "" {
		d = filepath.Join(repoRoot(t), ".cache", "tofu-plugins")
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

// weedAuth is a SeaweedFS S3 gateway with authentication on (one identity), started directly because s3emu.Start
// starts the open one.
type weedAuth struct {
	endpoint string
	cmd      *exec.Cmd
}

func startWeedAuth(t *testing.T, access, secret string) *weedAuth {
	t.Helper()
	weed := binPath(t, "SC_WEED_BIN", "weed")
	dir := t.TempDir()
	cfg := map[string]any{"identities": []any{map[string]any{
		"name":        "admin",
		"credentials": []any{map[string]string{"accessKey": access, "secretKey": secret}},
		"actions":     []string{"Admin", "Read", "Write", "List", "Tagging"},
	}}}
	b, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(dir, "s3.json")
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	ports, err := s3emu.FreePorts(5)
	if err != nil {
		t.Fatal(err)
	}
	p := func(i int) string { return strconv.Itoa(ports[i]) }
	cmd := exec.Command(weed, "server", "-s3", "-ip=127.0.0.1", "-dir="+dir, "-s3.config="+cfgPath, "-s3.port="+p(0),
		"-master.port="+p(1), "-volume.port="+p(2), "-filer.port="+p(3), "-iam.port="+p(4), "-s3.port.iceberg=0", "-s3.port.lance=0",
		"-master.volumeSizeLimitMB=64", "-volume.max=16")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting weed: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	w := &weedAuth{endpoint: "http://127.0.0.1:" + p(0), cmd: cmd}
	c := w.client(t, access, secret)
	ctx := context.Background()
	for i := 0; ; i++ {
		if _, err := c.ListBuckets(ctx); err == nil {
			return w
		}
		if i > 900 {
			t.Fatalf("the auth-on weed never answered:\n%.1500s", out.String())
		}
		if err := (clock.Real{}).Sleep(ctx, 100*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
}

func (w *weedAuth) client(t *testing.T, access, secret string) *s3client.Client {
	t.Helper()
	c, err := s3client.New(s3client.Config{Endpoint: w.endpoint, Region: "us-east-1", AccessKey: access, SecretKey: secret, PathStyle: true, Clock: clock.Real{}})
	if err != nil || c == nil {
		t.Fatalf("s3client.New: %v", err)
	}
	return c
}

// emulatorOverrideHeader documents the one deviation of the tier-E storage tests from production.
const emulatorOverrideHeader = `# Test-only (F08 S8b3, tier E). SeaweedFS 4.48 answers PutPublicAccessBlock and GetPublicAccessBlock with
# 501 NotImplemented, so hashicorp/aws 6.69.0 cannot apply aws_s3_bucket_public_access_block against the emulator.
# Operator decision 2026-10-29 ("Test-only override"): the tier-E tests apply a scratch copy of infra/tofu/storage in
# which this override file removes the public access block (for_each = toset([])). The production module is
# unchanged. The block is therefore NOT exercised in tier E; it is proven only at P01 / OC-10 (plan 06 section 167,
# plan 14 section 118). TestIntegration_WeedAnswers501ForPublicAccessBlock_S8b3 notices when weed implements the API.
`

// emulatorRepo returns a scratch repository root holding a copy of infra/tofu/storage (files only, as production
// ExecTofu.prepare copies them) plus emulator_override.tf, which empties every aws_s3_bucket_public_access_block
// resource of the module (found by scanning main.tf, so a resource added for a second account is covered).
func emulatorRepo(t *testing.T) string {
	t.Helper()
	src := filepath.Join(repoRoot(t), "infra", "tofu", "storage")
	dst := filepath.Join(t.TempDir(), "infra", "tofu", "storage")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(e.Name(), ".tf") {
			for _, m := range regexp.MustCompile(`(?m)^resource "aws_s3_bucket_public_access_block" "([^"]+)"`).FindAllStringSubmatch(string(b), -1) {
				names = append(names, m[1])
			}
		}
	}
	if len(names) == 0 {
		t.Fatal("the storage module declares no aws_s3_bucket_public_access_block: the emulator override is stale")
	}
	ov := emulatorOverrideHeader
	for _, n := range names {
		ov += "\nresource \"aws_s3_bucket_public_access_block\" \"" + n + "\" {\n  for_each = toset([])\n}\n"
	}
	if err := os.WriteFile(filepath.Join(dst, "emulator_override.tf"), []byte(ov), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(dst)))
}

// skipPABImports is the second half of the emulator override: ImportStorage imports four resources per bucket, one of
// them the public access block, which the override removed from the configuration (and which weed cannot read: 501). The
// script makes `tofu import aws_s3_bucket_public_access_block...` a no-op and runs the real tofu for everything else.
func skipPABImports(t *testing.T, real string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tofu")
	script := "#!/bin/sh\ncase \"$*\" in\n  *\" import \"*aws_s3_bucket_public_access_block*) exit 0 ;;\nesac\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

type storageFix struct {
	t    *testing.T
	exec *install.ExecTofu
	spec install.StorageSpec
}

func newStorageFix(t *testing.T, main install.TofuS3, env map[string]string) *storageFix {
	t.Helper()
	dir := t.TempDir()
	e := map[string]string{"PATH": os.Getenv("PATH"), "HOME": t.TempDir(), "TF_PLUGIN_CACHE_DIR": pluginCache(t)}
	for k, v := range env {
		e[k] = v
	}
	return &storageFix{t: t, exec: &install.ExecTofu{Repo: emulatorRepo(t), Env: e, Bin: skipPABImports(t, binPath(t, "SC_TOFU_BIN", "tofu"))},
		spec: install.StorageSpec{WorkDir: filepath.Join(dir, "work"), LocalState: filepath.Join(dir, "storage.local.tfstate"),
			Prefix: sPrefix, Cluster: sCluster, Buckets: bucketSpecs(false), Main: main, Passphrase: passphrase(3), Generation: 1}}
}

func (f *storageFix) remote(s3 install.TofuS3) install.RemoteState {
	return install.RemoteState{Bucket: sPrefix + "-tofu-state", Key: tofustate.StateKey(sCluster, "storage"), S3: s3}
}

// buckets returns the storage buckets (of the ten purposes) that exist in the account behind c. It probes each bucket
// with a List instead of ListBuckets: an open weed lists no bucket that was created by another identity.
func buckets(t *testing.T, c *s3client.Client) []string {
	t.Helper()
	var l []string
	for _, p := range storagePurposes {
		name := sPrefix + "-" + p
		if _, err := c.Bucket(name).List(context.Background(), ""); err == nil {
			l = append(l, name)
		}
	}
	sort.Strings(l)
	return l
}

func wantBuckets() []string {
	var l []string
	for _, p := range storagePurposes {
		l = append(l, sPrefix+"-"+p)
	}
	sort.Strings(l)
	return l
}

func (e *itEnv) tofuS3() install.TofuS3 {
	return install.TofuS3{Endpoint: e.emu.Endpoint(), Region: "us-east-1", AccessKey: "fixture-access-key", SecretKey: "fixture-secret-key-0000", PathStyle: true}
}

// pullWith runs `tofu state pull` against the remote storage state with exactly the given passphrase.
func pullWith(t *testing.T, f *storageFix, remote install.RemoteState, pass string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	be, err := tofustate.RenderBackendS3(tofustate.Backend{Bucket: remote.Bucket, Key: remote.Key, Region: remote.S3.Region,
		Endpoint: remote.S3.Endpoint, PathStyle: remote.S3.PathStyle, SkipS3Checksum: remote.SkipS3Checksum})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "_backend.tf"), []byte(be), 0o600); err != nil {
		t.Fatal(err)
	}
	// The state names the aws provider, so init installs it: pin the version the module pins (the cache already holds it).
	pin := "terraform {\n  required_providers {\n    aws = {\n      source  = \"hashicorp/aws\"\n      version = \"6.69.0\"\n    }\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "versions.tf"), []byte(pin), 0o600); err != nil {
		t.Fatal(err)
	}
	enc, err := tofustate.EncryptionEnv(tofustate.Encryption{Current: pass, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TF_IN_AUTOMATION=1", "TF_PLUGIN_CACHE_DIR=" + pluginCache(t),
		"AWS_ACCESS_KEY_ID=" + remote.S3.AccessKey, "AWS_SECRET_ACCESS_KEY=" + remote.S3.SecretKey, "AWS_REGION=" + remote.S3.Region, enc}
	cmd := exec.Command(f.exec.Bin, "-chdir="+dir, "init", "-input=false", "-no-color")
	cmd.Env = env
	// OpenTofu reads the state during init, so a wrong passphrase already fails there: that counts as a failed pull.
	if out, err := cmd.CombinedOutput(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return string(out), ee.ExitCode()
		}
		t.Fatalf("tofu init for the pull: %v\n%s", err, out)
	}
	var out bytes.Buffer
	cmd = exec.Command(f.exec.Bin, "-chdir="+dir, "state", "pull", "-no-color")
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return out.String(), ee.ExitCode()
		}
		t.Fatal(err)
	}
	return out.String(), 0
}

// S8b3: the real aws provider applies infra/tofu/storage against SeaweedFS; migrate-state moves the encrypted local
// state to the S3 backend and the remote list equals the local one (06-AC-5, 06-AC-4).
func TestIntegration_StorageApplyAndMigrate_S8b3(t *testing.T) {
	e := newIT(t)
	f := newStorageFix(t, e.tofuS3(), nil)
	ctx := context.Background()
	if err := f.exec.ApplyStorage(ctx, f.spec); err != nil {
		t.Fatalf("ApplyStorage: %v", err)
	}
	if got := buckets(t, e.client); !containsAll(got, wantBuckets()) {
		t.Fatalf("buckets = %v, want all of %v", got, wantBuckets())
	}
	local, err := f.exec.StateList(ctx, f.spec, nil)
	if err != nil || len(local) == 0 {
		t.Fatalf("StateList(local) = %v, %v", local, err)
	}
	remote := f.remote(f.spec.Main)
	remote.Bucket = sPrefix + "-tofu-state"
	if err := f.exec.Migrate(ctx, f.spec, remote); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	got, err := f.exec.StateList(ctx, f.spec, &remote)
	if err != nil || strings.Join(got, "\n") != strings.Join(local, "\n") {
		t.Errorf("remote state list = %v (%v), want the local %v", got, err, local)
	}
	o, err := e.client.Bucket(remote.Bucket).Get(ctx, remote.Key)
	if err != nil {
		t.Fatalf("raw remote state: %v", err)
	}
	if _, err := tofustate.ParseEnvelope(o.Data); err != nil {
		t.Errorf("the migrated object is not an encryption envelope: %v", err)
	}
	if out, code := pullWith(t, f, remote, passphrase(11)); code == 0 {
		t.Errorf("state pull with a wrong passphrase succeeded:\n%.200s", out)
	}
	if out, code := pullWith(t, f, remote, f.spec.Passphrase); code != 0 {
		t.Errorf("state pull with the right passphrase failed (exit %d):\n%.400s", code, out)
	}
}

func containsAll(have, want []string) bool {
	set := map[string]bool{}
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

// 50-AC-18 / M1 / S8b1 N-1 at tier E: after abandon-identity the storage state is rebuilt by ImportStorage under the new
// passphrase and migrated over the object that still holds the old passphrase's state; `tofu state pull` then works with
// the new passphrase and fails with the old one.
func TestIntegration_ImportStorageUnderNewPassphrase_S8b3M1(t *testing.T) {
	e := newIT(t)
	f := newStorageFix(t, e.tofuS3(), nil)
	ctx := context.Background()
	if err := f.exec.ApplyStorage(ctx, f.spec); err != nil {
		t.Fatalf("ApplyStorage: %v", err)
	}
	remote := f.remote(f.spec.Main)
	if err := f.exec.Migrate(ctx, f.spec, remote); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	oldPass, newPass := f.spec.Passphrase, passphrase(21)

	// The new identity: a fresh work dir, no local state, the new passphrase; the buckets exist.
	g := newStorageFix(t, e.tofuS3(), nil)
	g.spec.Passphrase = newPass
	if err := os.WriteFile(g.spec.LocalState, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := g.exec.ImportStorage(ctx, g.spec, remote); err != nil {
		t.Fatalf("ImportStorage: %v", err)
	}
	n, err := g.exec.Changes(ctx, g.spec, nil)
	if err != nil || n != 0 {
		t.Fatalf("Changes after the import = %d, %v; want 0 (the imported state matches the module)", n, err)
	}
	if err := g.exec.Migrate(ctx, g.spec, remote); err != nil {
		t.Fatalf("Migrate over the old-passphrase object: %v", err)
	}
	if out, code := pullWith(t, g, remote, newPass); code != 0 {
		t.Errorf("state pull with the new passphrase failed (exit %d):\n%.400s", code, out)
	}
	if out, code := pullWith(t, g, remote, oldPass); code == 0 {
		t.Errorf("state pull with the abandoned passphrase succeeded:\n%.200s", out)
	}
}

// 06-AC-2 with authentication on: the re-signing proxy strips If-None-Match and signs again; C2 fails with
// PF_S3_LOCK_UNSUPPORTED and the storage prefix stays empty.
func TestIntegration_ResigningProxyAuthOnWeed_S8b3(t *testing.T) {
	const ak, sk = "fixture-auth-access-1", "fixture-auth-secret-0001"
	w := startWeedAuth(t, ak, sk)
	c := w.client(t, ak, sk)
	ctx := context.Background()
	if err := c.CreateBucket(ctx, itBucket); err != nil {
		t.Fatalf("CreateBucket on the auth-on weed: %v", err)
	}
	if err := c.SetVersioning(ctx, itBucket, true); err != nil {
		t.Fatal(err)
	}
	// Without the proxy the auth-on weed does honour If-None-Match.
	rep, err := tofustate.Conformance(ctx, tofustate.ConformanceParams{Store: c.Bucket(itBucket), Cluster: "lab-one", RunID: "20260304T050607Z-ee01", Clock: clock.Real{}})
	if err != nil || len(rep.Fatal) != 0 {
		t.Fatalf("direct Conformance on the auth-on weed: fatal %v, %v", rep.Fatal, err)
	}
	p, err := s3emu.NewResigningProxy(w.endpoint, s3emu.Credentials{AccessKey: ak, SecretKey: sk, Region: "us-east-1"})
	if err != nil || p == nil {
		t.Fatalf("NewResigningProxy: %v", err)
	}
	t.Cleanup(p.Close)
	via, err := s3client.New(s3client.Config{Endpoint: p.URL(), Region: "us-east-1", AccessKey: ak, SecretKey: sk, PathStyle: true, Clock: clock.Real{}})
	if err != nil || via == nil {
		t.Fatal(err)
	}
	rep, err = tofustate.Conformance(ctx, tofustate.ConformanceParams{Store: via.Bucket(itBucket), Cluster: "lab-one", RunID: "20260304T050607Z-ee02", Clock: clock.Real{}})
	if err != nil {
		t.Fatalf("Conformance through the proxy: %v", err)
	}
	if !hasFatal(rep, "PF_S3_LOCK_UNSUPPORTED") {
		t.Errorf("Fatal = %v, want PF_S3_LOCK_UNSUPPORTED (the proxy signed again, so the failure is the missing precondition, not 403)", rep.Fatal)
	}
	keys, err := c.Bucket(itBucket).List(ctx, "lab-one/storage/")
	if err != nil || len(keys) != 0 {
		t.Errorf("objects under lab-one/storage/: %v, %v; want none", keys, err)
	}
}

// A TLS front in front of the open weed, trusted only through SC_LAB_S3_CA_FILE (plan 50 section 9.2): the production
// ExecTofu hands the file to tofu and the aws provider.
func TestIntegration_TLSFrontTrustedThroughCAFile_S8b3(t *testing.T) {
	e := newIT(t)
	up, _ := url.Parse(e.emu.Endpoint())
	front := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(up))
	t.Cleanup(front.Close)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: front.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	main := e.tofuS3()
	main.Endpoint = front.URL
	// The front's certificate is untrusted without the CA file.
	pool := x509.NewCertPool()
	if _, err := (&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}).Get(front.URL); err == nil {
		t.Fatal("the TLS front is trusted without the CA file: the test proves nothing")
	}
	// AWS_MAX_ATTEMPTS and the context bound the run: an untrusted certificate is retried by the provider for a long time.
	f := newStorageFix(t, main, map[string]string{"SC_TEST_MODE": "1", "SC_LAB_S3_CA_FILE": caFile, "AWS_MAX_ATTEMPTS": "2"})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := f.exec.ApplyStorage(ctx, f.spec); err != nil {
		t.Fatalf("ApplyStorage through the TLS front with SC_LAB_S3_CA_FILE: %v", err)
	}
	if got := buckets(t, e.client); !containsAll(got, wantBuckets()) {
		t.Errorf("buckets = %v, want all of %v", got, wantBuckets())
	}
}

// S6a review-1 N1 at tier E: with a second account (a second weed), the aliased provider creates tofu-state and recovery
// there with the state credentials; the other eight buckets exist only in the main account; a plan against the remote
// state (state credentials for the backend) shows no changes, so the main buckets are refreshed with the main credential.
func TestIntegration_StateAccountAlias_S8b3(t *testing.T) {
	e := newIT(t)
	const sak, ssk = "fixture-state-access-1", "fixture-state-secret-0001"
	w2 := startWeedAuth(t, sak, ssk)
	state := install.TofuS3{Endpoint: w2.endpoint, Region: "us-east-1", AccessKey: sak, SecretKey: ssk, PathStyle: true}
	f := newStorageFix(t, e.tofuS3(), nil)
	f.spec.State = &state
	f.spec.Buckets = bucketSpecs(true)
	ctx := context.Background()
	if err := f.exec.ApplyStorage(ctx, f.spec); err != nil {
		t.Fatalf("ApplyStorage with a state account: %v", err)
	}
	mainB, stateB := buckets(t, e.client), buckets(t, w2.client(t, sak, ssk))
	for _, p := range storagePurposes {
		name := sPrefix + "-" + p
		inState := p == "tofu-state" || p == "recovery"
		if has(mainB, name) == inState {
			t.Errorf("bucket %s: in the main account = %v, want %v", name, has(mainB, name), !inState)
		}
		if has(stateB, name) != inState {
			t.Errorf("bucket %s: in the state account = %v, want %v", name, has(stateB, name), inState)
		}
	}
	remote := f.remote(state)
	if err := f.exec.Migrate(ctx, f.spec, remote); err != nil {
		t.Fatalf("Migrate to the state account: %v", err)
	}
	n, err := f.exec.Changes(ctx, f.spec, &remote)
	if err != nil || n != 0 {
		t.Errorf("Changes against the remote state = %d, %v; want 0", n, err)
	}
}

func has(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// Probe for the emulator override above: SeaweedFS 4.48 does not implement PutPublicAccessBlock. When this fails, weed
// implements it and the emulator override (emulator_override.tf in emulatorRepo) can be dropped, so the tier-E tests apply
// the production module unchanged.
func TestIntegration_WeedAnswers501ForPublicAccessBlock_S8b3(t *testing.T) {
	e := newIT(t)
	if err := e.client.CreateBucket(context.Background(), sPrefix+"-pab-probe"); err != nil {
		t.Fatal(err)
	}
	body := `<PublicAccessBlockConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><BlockPublicAcls>true</BlockPublicAcls>` +
		`<IgnorePublicAcls>true</IgnorePublicAcls><BlockPublicPolicy>true</BlockPublicPolicy><RestrictPublicBuckets>true</RestrictPublicBuckets></PublicAccessBlockConfiguration>`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, e.emu.Endpoint()+"/"+sPrefix+"-pab-probe?publicAccessBlock", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("PutPublicAccessBlock answered %d, want 501 NotImplemented: weed now implements it, so the emulator override "+
			"(emulator_override.tf in emulatorRepo) can be dropped and the production module applied unchanged", resp.StatusCode)
	}
}
