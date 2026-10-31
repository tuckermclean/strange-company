//go:build integration

package tofustate_test

// Tier-E tests (plan 06 section 22, plan 52 D2): they start the real SeaweedFS `weed` as a subprocess and run the
// real `tofu`. `make test-integration` runs them inside the ci image with
//
//	cd src && go test -count=1 -tags integration -run Integration ./internal/tofustate/...
//
// `make verify` never builds them (no `integration` tag). SC_WEED_BIN and SC_TOFU_BIN override the binary names.
// A missing binary fails the test: this lane exists to run them, so a skip would hide a broken image.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/s3emu"
	"strange.company/sc/internal/tofustate"
)

const itBucket = "lab-one-tofu-state"

func binPath(t *testing.T, env, def string) string {
	t.Helper()
	name := os.Getenv(env)
	if name == "" {
		name = def
	}
	p, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("%s: %v (set %s, or run through `make test-integration` in the ci image)", def, err, env)
	}
	return p
}

type itEnv struct {
	emu    *s3emu.Emulator
	client *s3client.Client
	bucket *s3client.Bucket
	tofu   string
	keys   []string // AWS_* entries for the tofu process
}

func newIT(t *testing.T) *itEnv {
	t.Helper()
	weed := binPath(t, "SC_WEED_BIN", "weed")
	tofu := binPath(t, "SC_TOFU_BIN", "tofu")
	ctx := context.Background()
	emu, err := s3emu.Start(ctx, s3emu.Options{Binary: weed, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("s3emu.Start: %v", err)
	}
	t.Cleanup(func() { _ = emu.Stop() })
	ak, sk := "fixture-access-key", "fixture-secret-key-0000"
	c, err := s3client.New(s3client.Config{Endpoint: emu.Endpoint(), Region: "us-east-1", AccessKey: ak, SecretKey: sk, PathStyle: true, Clock: clock.Real{}})
	if err != nil || c == nil {
		t.Fatalf("s3client.New: %v", err)
	}
	if err := c.CreateBucket(ctx, itBucket); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if err := c.SetVersioning(ctx, itBucket, true); err != nil {
		t.Fatalf("SetVersioning: %v", err)
	}
	return &itEnv{emu: emu, client: c, bucket: c.Bucket(itBucket), tofu: tofu,
		keys: []string{"AWS_ACCESS_KEY_ID=" + ak, "AWS_SECRET_ACCESS_KEY=" + sk, "AWS_REGION=us-east-1"}}
}

// env is a clean process environment: PATH, a private HOME, the S3 keys and exactly the given extras.
func (e *itEnv) env(t *testing.T, extra ...string) []string {
	t.Helper()
	out := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TF_IN_AUTOMATION=1"}
	out = append(out, e.keys...)
	return append(out, extra...)
}

func (e *itEnv) backend(key string) tofustate.Backend {
	return tofustate.Backend{Bucket: itBucket, Key: key, Region: "us-east-1", Endpoint: e.emu.Endpoint(), PathStyle: true}
}

func (e *itEnv) run(t *testing.T, dir string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(e.tofu, append([]string{"-chdir=" + dir}, args...)...)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err == nil {
		return out.String(), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return out.String(), ee.ExitCode()
	}
	t.Fatalf("running tofu %v: %v", args, err)
	return "", -1
}

func mustRun(t *testing.T, out string, code int, what string) {
	t.Helper()
	if code != 0 {
		t.Fatalf("%s failed (exit %d):\n%s", what, code, out)
	}
}

// module writes a terraform_data root module whose backend is the rendered s3 backend, and applies it.
func (e *itEnv) module(t *testing.T, cluster, mod, input string, enc tofustate.Encryption) (dir string, env []string) {
	t.Helper()
	dir = t.TempDir()
	be, err := tofustate.RenderBackendS3(e.backend(tofustate.StateKey(cluster, mod)))
	if err != nil {
		t.Fatalf("RenderBackendS3: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "_backend.tf"), []byte(be), 0o600); err != nil {
		t.Fatal(err)
	}
	e.setInput(t, dir, input)
	tfenc, err := tofustate.EncryptionEnv(enc)
	if err != nil {
		t.Fatalf("EncryptionEnv: %v", err)
	}
	env = e.env(t, tfenc)
	out, code := e.run(t, dir, env, "init", "-input=false", "-no-color")
	mustRun(t, out, code, "tofu init")
	out, code = e.run(t, dir, env, "apply", "-auto-approve", "-input=false", "-no-color", "-lock-timeout=60s")
	mustRun(t, out, code, "tofu apply")
	return dir, env
}

func (e *itEnv) setInput(t *testing.T, dir, input string) {
	t.Helper()
	src := "resource \"terraform_data\" \"node\" {\n  input = \"" + input + "\"\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
}

func repoOut(t *testing.T) string {
	t.Helper()
	d, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(d, "plans", "06-opentofu-state.md")); err == nil {
			return filepath.Join(d, "out", "integration")
		}
		if p := filepath.Dir(d); p != d {
			d = p
			continue
		}
		t.Fatal("repository root not found")
	}
}

// 06-AC-1: against the SeaweedFS emulator C1-C8 pass with the real tofu in C7, and s3-conformance.json shows fatal: [].
func TestIntegration_ConformanceC1toC8_06AC1(t *testing.T) {
	e := newIT(t)
	probe := tofustate.ExecLockProbe{Tofu: e.tofu, Dir: t.TempDir(), Backend: e.backend("placeholder"), Env: e.env(t)}
	rep, err := tofustate.Conformance(context.Background(), tofustate.ConformanceParams{
		Store: e.bucket, Cluster: "lab-one", RunID: "20260304T050607Z-ab12", Clock: clock.Real{}, Probe: probe,
	})
	if err != nil {
		t.Fatalf("Conformance: %v", err)
	}
	if len(rep.Fatal) != 0 {
		t.Errorf("Fatal = %v", rep.Fatal)
	}
	if len(rep.Tests) != 8 {
		t.Fatalf("%d rows, want 8: %+v", len(rep.Tests), rep.Tests)
	}
	for _, r := range rep.Tests {
		if r.Outcome != "pass" {
			t.Errorf("%s %s: %s (%s)", r.ID, r.Name, r.Outcome, r.Code)
		}
	}
	path := filepath.Join(repoOut(t), "s3-conformance.json")
	if err := tofustate.WriteReport(path, rep); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var doc struct {
		Fatal []string `json:"fatal"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Fatal == nil || len(doc.Fatal) != 0 {
		t.Errorf("%s: fatal = %v (%v), want an empty array", path, doc.Fatal, err)
	}
	left, _ := e.bucket.ListVersions(context.Background(), "lab-one/conformance/")
	if len(left) != 0 {
		t.Errorf("%d object versions left under the conformance prefix", len(left))
	}
}

// 06-AC-2 (library leg): behind the If-None-Match-stripping proxy, C2 fails with PF_S3_LOCK_UNSUPPORTED.
func TestIntegration_StrippingProxyFailsC2_06AC2(t *testing.T) {
	e := newIT(t)
	p, err := s3emu.NewStrippingProxy(e.emu.Endpoint())
	if err != nil || p == nil {
		t.Fatalf("NewStrippingProxy: %v", err)
	}
	t.Cleanup(p.Close)
	c, err := s3client.New(s3client.Config{Endpoint: p.URL(), Region: "us-east-1", AccessKey: "fixture-access-key", SecretKey: "fixture-secret-key-0000", PathStyle: true, Clock: clock.Real{}})
	if err != nil || c == nil {
		t.Fatal(err)
	}
	rep, err := tofustate.Conformance(context.Background(), tofustate.ConformanceParams{Store: c.Bucket(itBucket), Cluster: "lab-one", RunID: "20260304T050607Z-cd34", Clock: clock.Real{}})
	if err != nil {
		t.Fatalf("Conformance: %v", err)
	}
	if !hasFatal(rep, "PF_S3_LOCK_UNSUPPORTED") {
		t.Errorf("Fatal = %v, want PF_S3_LOCK_UNSUPPORTED", rep.Fatal)
	}
	keys, err := e.bucket.List(context.Background(), "lab-one/storage/")
	if err != nil || len(keys) != 0 {
		t.Errorf("objects under lab-one/storage/: %v, %v; want none", keys, err)
	}
}

// 06-AC-4: the stored state is the OpenTofu encryption envelope, not a tfstate; without TF_ENCRYPTION it cannot be read;
// the rotation fallback is S7 (06-AC-6).
func TestIntegration_EncryptionEnforced_06AC4(t *testing.T) {
	e := newIT(t)
	p1, p2 := passphrase(3), passphrase(11)
	dir, _ := e.module(t, "lab-one", "compute", "one", tofustate.Encryption{Current: p1})

	o, err := e.bucket.Get(context.Background(), tofustate.StateKey("lab-one", "compute"))
	if err != nil {
		t.Fatalf("raw state object: %v", err)
	}
	if _, err := tofustate.ParseEnvelope(o.Data); err != nil {
		t.Errorf("the stored object is not an encryption envelope: %v\n%.200s", err, o.Data)
	}
	var doc map[string]any
	if err := json.Unmarshal(o.Data, &doc); err != nil {
		t.Fatalf("raw object is not JSON: %v", err)
	}
	if _, ok := doc["resources"]; ok {
		t.Error("the raw object has a `resources` key: it is a plaintext tfstate")
	}
	if _, ok := doc["encrypted_data"]; !ok {
		t.Error("the raw object has no `encrypted_data` key")
	}
	if bytes.Contains(o.Data, []byte(p1)) {
		t.Error("the passphrase appears in the stored object")
	}

	enc := func(e1 tofustate.Encryption) []string {
		v, err := tofustate.EncryptionEnv(e1)
		if err != nil {
			t.Fatal(err)
		}
		return e.env(t, v)
	}
	pull := func(env []string) (string, int) { return e.run(t, dir, env, "state", "pull", "-no-color") }

	if out, code := pull(e.env(t, "TF_ENCRYPTION=")); code == 0 {
		t.Errorf("tofu state pull succeeded with an empty TF_ENCRYPTION:\n%.300s", out)
	}
	if out, code := pull(e.env(t)); code == 0 {
		t.Errorf("tofu state pull succeeded with no TF_ENCRYPTION:\n%.300s", out)
	}
	if out, code := pull(enc(tofustate.Encryption{Current: p2})); code == 0 {
		t.Errorf("tofu state pull succeeded with the wrong passphrase:\n%.300s", out)
	}
	if out, code := pull(enc(tofustate.Encryption{Current: p1})); code != 0 {
		t.Errorf("tofu state pull failed with the right passphrase (exit %d):\n%s", code, out)
	}
	be, _ := tofustate.RenderBackendS3(e.backend("x/compute/terraform.tfstate"))
	chk := t.TempDir()
	_ = os.WriteFile(filepath.Join(chk, "_backend.tf"), []byte(be), 0o600)
	if out, code := e.run(t, chk, e.env(t), "fmt", "-check", "-diff"); code != 0 {
		t.Errorf("the rendered backend is not tofu-fmt clean (exit %d):\n%s", code, out)
	}
}

// 06-AC-3 (library leg): a lock object held by someone else makes tofu fail with the state lock error, and the
// library reads the holder and recognizes the message.
func TestIntegration_LockContention_06AC3(t *testing.T) {
	e := newIT(t)
	p1 := passphrase(3)
	dir, env := e.module(t, "lab-one", "compute", "one", tofustate.Encryption{Current: p1})
	ctx := context.Background()
	lock := `{"ID":"11111111-2222-3333-4444-555555555555","Operation":"OperationTypeApply","Info":"","Who":"other@host","Version":"1.13.1","Created":"2026-03-04T05:06:07.000000001Z","Path":"` + itBucket + `/lab-one/compute/terraform.tfstate"}`
	if _, err := e.bucket.PutIfAbsent(ctx, tofustate.LockKey("lab-one", "compute"), []byte(lock)); err != nil {
		t.Fatalf("writing the lock object: %v", err)
	}
	st, err := tofustate.ReadLock(ctx, e.bucket, "lab-one", "compute")
	if err != nil || !st.Present || st.Info.ID != "11111111-2222-3333-4444-555555555555" || st.Info.Who != "other@host" {
		t.Fatalf("ReadLock = %+v, %v", st, err)
	}
	out, code := e.run(t, dir, env, "plan", "-lock-timeout=0s", "-input=false", "-no-color")
	if code == 0 {
		t.Fatalf("tofu plan succeeded although the state is locked:\n%s", out)
	}
	if !strings.Contains(out, "Error acquiring the state lock") || !tofustate.IsLockError(out) {
		t.Errorf("stderr lacks \"Error acquiring the state lock\":\n%s", out)
	}
	if id := tofustate.LockIDFromStderr(out); id != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("LockIDFromStderr = %q", id)
	}
}

// 06-AC-8 (library leg): a garbage upload is detected, the previous version is selected and restored as a new version,
// and tofu plan shows no changes.
func TestIntegration_RestoreDrill_06AC8(t *testing.T) {
	e := newIT(t)
	ctx := context.Background()
	p1 := passphrase(3)
	dir, env := e.module(t, "lab-one", "compute", "one", tofustate.Encryption{Current: p1})
	e.setInput(t, dir, "two")
	out, code := e.run(t, dir, env, "apply", "-auto-approve", "-input=false", "-no-color")
	mustRun(t, out, code, "second tofu apply")

	key := tofustate.StateKey("lab-one", "compute")
	if _, err := e.bucket.PutObject(ctx, key, []byte("GARBAGE"), s3client.PutOptions{}); err != nil {
		t.Fatalf("uploading garbage: %v", err)
	}
	cur, err := e.bucket.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tofustate.ParseEnvelope(cur.Data); err == nil {
		t.Fatal("ParseEnvelope accepted the garbage object")
	}
	if out, code := e.run(t, dir, env, "plan", "-input=false", "-no-color"); code == 0 {
		t.Fatalf("tofu plan succeeded on a garbage state:\n%s", out)
	}
	vs, err := tofustate.ListVersions(ctx, e.bucket, "lab-one", "compute", 0)
	if err != nil || len(vs) < 3 || !vs[0].Current {
		t.Fatalf("ListVersions = %+v, %v; want at least 3 versions, newest current", vs, err)
	}
	validate := func(raw []byte) error { _, err := tofustate.ParseEnvelope(raw); return err }
	sel, err := tofustate.SelectRestoreVersion(vs, func(v tofustate.Version) error {
		o, err := e.bucket.GetVersion(ctx, key, v.ID)
		if err != nil {
			return err
		}
		return validate(o.Data)
	})
	if err != nil {
		t.Fatalf("SelectRestoreVersion: %v", err)
	}
	before := len(vs)
	res, err := tofustate.Restore(ctx, e.bucket, "lab-one", "compute", sel.ID, validate)
	if err != nil || res.NoOp || res.NewVersionID == "" {
		t.Fatalf("Restore = %+v, %v", res, err)
	}
	after, _ := tofustate.ListVersions(ctx, e.bucket, "lab-one", "compute", 0)
	if len(after) != before+1 {
		t.Errorf("version count %d -> %d, want +1", before, len(after))
	}
	out, code = e.run(t, dir, env, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	if code != 0 {
		t.Errorf("tofu plan after the restore exited %d, want 0 (no changes):\n%s", code, out)
	}
}
