package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/contracts"
)

const installUID = "6f1c2d3e-4b5a-4c6d-8e7f-0a1b2c3d4e5f"

// stubUnit is a unit with a counting Run (the fake provider arrives in a later slice: provision is a counting stub).
type stubUnit struct {
	name  string
	steps []engine.Unit
	run   func(ctx context.Context) ([]engine.OutputRef, error)

	mu   sync.Mutex
	runs int
}

func (u *stubUnit) Name() string                        { return u.name }
func (u *stubUnit) Inputs(context.Context) (any, error) { return map[string]any{"unit": u.name}, nil }
func (u *stubUnit) Check(context.Context) (engine.Check, error) {
	return engine.Check{State: engine.NotDone}, nil
}
func (u *stubUnit) Timeout() time.Duration { return 30 * time.Minute }
func (u *stubUnit) Retryable(error) bool   { return false }
func (u *stubUnit) Steps() []engine.Unit   { return u.steps }
func (u *stubUnit) Run(ctx context.Context) ([]engine.OutputRef, error) {
	u.mu.Lock()
	u.runs++
	u.mu.Unlock()
	if u.run != nil {
		return u.run(ctx)
	}
	return nil, nil
}
func (u *stubUnit) count() int { u.mu.Lock(); defer u.mu.Unlock(); return u.runs }

type confirmEnv struct {
	t   *testing.T
	dir string
	clk *clock.Fake
}

func newConfirmEnv(t *testing.T) *confirmEnv {
	return &confirmEnv{t: t, dir: filepath.Join(t.TempDir(), ".bootstrap"), clk: clock.NewFake(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC))}
}

func (v *confirmEnv) engine() *engine.Engine {
	e, err := engine.New(engine.Config{
		Dir: v.dir, Clock: v.clk, Cluster: cluster, InstallUID: installUID, Provider: "fake", Profile: "self-hosting-minimum",
		ConfigDigest: "sha256:aa11bb22cc33dd44ee55ff6600112233445566778899aabbccddeeff00112233",
		Command:      "run", User: "alice", Host: "host-a", Version: "v0.1.0",
		GitHead:       "4b825dc642cb6eb9a060e54bf8d69288fbee4904",
		ToolboxDigest: "sha256:5555555555555555555555555555555555555555555555555555555555555555", ToolboxSource: "pulled",
		Env: map[string]string{},
	})
	if err != nil {
		v.t.Fatal(err)
	}
	return e
}

// uploaded is the journal state plan.bundle-upload leaves behind (generation N+1 re-sealed by storage-credentials).
func uploaded(sha string) engine.RecoveryBundleState {
	return engine.RecoveryBundleState{
		Status: "uploaded", Generation: 2, Sha256: "sha256:" + sha,
		LocalPath: "/blackstart/lab-one/bundles/sc-recovery-lab-one-v2.tar.age",
		S3URI:     "s3://sc-lab-recovery/lab-one/bundles/sc-recovery-lab-one-v2.tar.age", S3VersionID: "3HL4kqtJlcpXroDTDmJ.rmSpXd3dIbrHY",
		CreatedAt: "2026-10-27T11:55:00Z",
	}
}

type confirmRun struct {
	res       engine.Result
	provision *stubUnit
	prompts   int
}

// runConfirm executes plan{ bundle-upload stub, plan.bundle-confirm } then a counting provision stub. answers are
// the passphrases the operator types, in order (the last repeats).
func (v *confirmEnv) runConfirm(answers []string, download func(context.Context) ([]byte, error), sha string) confirmRun {
	v.t.Helper()
	e := v.engine()
	upload := &stubUnit{name: "plan.bundle-upload", run: func(context.Context) ([]engine.OutputRef, error) {
		if e.RecoveryBundle().Status != "confirmed" {
			e.SetRecoveryBundle(uploaded(sha))
		}
		return nil, nil
	}}
	prompts := 0
	confirm := identity.NewBundleConfirm(identity.BundleConfirmConfig{
		Journal:  e,
		Download: download,
		Prompt: func(_ context.Context, attempt int) (string, error) {
			i := prompts
			prompts++
			if i >= len(answers) {
				i = len(answers) - 1
			}
			return answers[i], nil
		},
		Clock: v.clk,
	})
	if confirm == nil {
		v.t.Fatal("NewBundleConfirm returned no unit")
	}
	plan := &stubUnit{name: "plan", steps: []engine.Unit{upload, confirm}}
	provision := &stubUnit{name: "provision"}
	res := e.Execute(context.Background(), engine.Pipeline{Kind: "install", Phases: []engine.Phase{plan, provision}}, engine.Options{})
	return confirmRun{res: res, provision: provision, prompts: prompts}
}

func (v *confirmEnv) journal() map[string]any {
	v.t.Helper()
	b, err := os.ReadFile(filepath.Join(v.dir, "journal.json"))
	if err != nil {
		v.t.Fatalf("local journal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		v.t.Fatal(err)
	}
	return m
}

func (v *confirmEnv) bundleRecord() map[string]any {
	rb, _ := v.journal()["recovery_bundle"].(map[string]any)
	if rb == nil {
		v.t.Fatal("journal has no recovery_bundle")
	}
	return rb
}

func (v *confirmEnv) validateJournal() {
	v.t.Helper()
	schemas, err := contracts.CompileSchemas(filepath.Join(repoRoot(v.t), "contracts"))
	if err != nil {
		v.t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(v.dir, "journal.json"))
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var inst any
	if err := dec.Decode(&inst); err != nil {
		v.t.Fatal(err)
	}
	if err := schemas["bootstrap-journal"].Validate(inst); err != nil {
		v.t.Errorf("journal does not validate against bootstrap-journal.schema.yaml: %v", err)
	}
}

func downloadOf(sealed *identity.Sealed, calls *int) func(context.Context) ([]byte, error) {
	return func(context.Context) ([]byte, error) {
		*calls++
		return sealed.Data, nil
	}
}

// 50-AC-10 (unit part): plan.bundle-confirm with a wrong re-entered passphrase three times exits 8, the journal shows
// recovery_bundle.status = uploaded (not confirmed), and provision never starts.
func TestBundleConfirm_ThreeWrongPassphrasesExit8_50AC10(t *testing.T) {
	_, sealed := sealedV1(t)
	v := newConfirmEnv(t)
	dl := 0
	wrong := "this is not the passphrase at all"
	r := v.runConfirm([]string{wrong}, downloadOf(sealed, &dl), sealed.SHA256)
	if r.res.Exit != 8 {
		t.Fatalf("exit %d (%s %s), want 8", r.res.Exit, r.res.Code, r.res.Message)
	}
	if r.prompts != 3 {
		t.Errorf("prompted %d times, want exactly 3 attempts", r.prompts)
	}
	rb := v.bundleRecord()
	if rb["status"] != "uploaded" {
		t.Errorf("recovery_bundle.status %v, want uploaded", rb["status"])
	}
	if rb["confirmed_at"] != nil {
		t.Errorf("confirmed_at %v set although nothing was confirmed", rb["confirmed_at"])
	}
	if g, _ := rb["generation"].(float64); g != 2 {
		t.Errorf("recovery_bundle.generation %v, want 2 (kept)", rb["generation"])
	}
	if n := r.provision.count(); n != 0 {
		t.Errorf("provision ran %d times, want 0", n)
	}
	if j := v.journal(); strings.Contains(string(mustJSON(j)), wrong) || strings.Contains(r.res.Message, wrong) {
		t.Error("the typed passphrase reached the journal or the result message")
	}
	v.validateJournal()
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// Plan 50 BOOT-10 step 9: the S3 copy is downloaded and opened with the re-entered passphrase; the right one
// confirms (also on the third attempt) and the pipeline continues.
func TestBundleConfirm_RightPassphraseConfirms_50AC10(t *testing.T) {
	_, sealed := sealedV1(t)
	for name, answers := range map[string][]string{
		"first try":  {goodPass},
		"third try":  {"wrong one", "wrong two", goodPass},
		"second try": {"wrong one", goodPass},
	} {
		t.Run(name, func(t *testing.T) {
			v := newConfirmEnv(t)
			dl := 0
			r := v.runConfirm(answers, downloadOf(sealed, &dl), sealed.SHA256)
			if r.res.Exit != 0 {
				t.Fatalf("exit %d (%s %s), want 0", r.res.Exit, r.res.Code, r.res.Message)
			}
			if r.prompts != len(answers) {
				t.Errorf("prompted %d times, want %d", r.prompts, len(answers))
			}
			rb := v.bundleRecord()
			if rb["status"] != "confirmed" || rb["confirmed_at"] != "2026-10-27T12:00:00Z" {
				t.Errorf("recovery_bundle %v, want confirmed at the injected clock's time", rb)
			}
			if rb["s3_uri"] == nil || rb["sha256"] != "sha256:"+sealed.SHA256 {
				t.Errorf("confirm dropped the upload fields: %v", rb)
			}
			if dl == 0 {
				t.Error("the S3 copy was never downloaded")
			}
			if r.provision.count() != 1 {
				t.Errorf("provision ran %d times, want 1", r.provision.count())
			}
			v.validateJournal()
		})
	}
}

// BOOT-10 step 9: "the next run asks again": after exit 8 a rerun with the right passphrase confirms and continues.
func TestBundleConfirm_NextRunAsksAgain_50AC10(t *testing.T) {
	_, sealed := sealedV1(t)
	v := newConfirmEnv(t)
	dl := 0
	if r := v.runConfirm([]string{"wrong"}, downloadOf(sealed, &dl), sealed.SHA256); r.res.Exit != 8 {
		t.Fatalf("first run exit %d, want 8", r.res.Exit)
	}
	r := v.runConfirm([]string{goodPass}, downloadOf(sealed, &dl), sealed.SHA256)
	if r.res.Exit != 0 || r.prompts != 1 {
		t.Fatalf("rerun exit %d prompts %d (%s), want 0 and one prompt", r.res.Exit, r.prompts, r.res.Code)
	}
	if v.bundleRecord()["status"] != "confirmed" || r.provision.count() != 1 {
		t.Errorf("rerun did not confirm and continue: %v", v.bundleRecord())
	}
}

// Once confirmed, a rerun does not ask again (done check: recovery_bundle.status = confirmed).
func TestBundleConfirm_ConfirmedIsNotAskedAgain_50AC10(t *testing.T) {
	_, sealed := sealedV1(t)
	v := newConfirmEnv(t)
	dl := 0
	if r := v.runConfirm([]string{goodPass}, downloadOf(sealed, &dl), sealed.SHA256); r.res.Exit != 0 {
		t.Fatalf("first run exit %d", r.res.Exit)
	}
	r := v.runConfirm([]string{"never used"}, downloadOf(sealed, &dl), sealed.SHA256)
	if r.res.Exit != 0 || r.prompts != 0 {
		t.Errorf("rerun exit %d prompts %d, want 0 and no prompt", r.res.Exit, r.prompts)
	}
}

// Plan 50 section 7.2: exit 8 also means "recovery bundle missing": an S3 copy that cannot be downloaded, or that is
// not an openable bundle at all, does not confirm and does not start provision.
func TestBundleConfirm_MissingBundleExit8_50AC10(t *testing.T) {
	v := newConfirmEnv(t)
	_, sealed := sealedV1(t)
	r := v.runConfirm([]string{goodPass}, func(context.Context) ([]byte, error) { return nil, errors.New("NoSuchKey") }, sealed.SHA256)
	if r.res.Exit != 8 {
		t.Fatalf("exit %d (%s), want 8", r.res.Exit, r.res.Code)
	}
	if r.provision.count() != 0 {
		t.Error("provision ran")
	}
	if v.bundleRecord()["status"] != "uploaded" {
		t.Errorf("status %v, want uploaded", v.bundleRecord()["status"])
	}
}
