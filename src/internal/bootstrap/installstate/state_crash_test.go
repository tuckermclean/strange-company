package installstate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"strange.company/sc/internal/bootstrap/install"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

// Review 1 of S7 (F08): the crash windows of `rotate tofu-passphrase` (06-AC-6; plan 06 section 20.3, plan 50 section 17
// idempotency and crash semantics) and the run lock of `state verify` (plan 50 section 7.1, `state` row: run lock "yes
// (except status, versions)").
//
// The crash windows are built the way a killed process leaves them, with no timers:
//   - window A, killed after the journal checkpoint "created" and before the upload: the first run's recovery-bucket write
//     is made to fail, so the process stops at exactly that point and the bundle v+1 exists only on disk;
//   - window B, killed after the bundle files were written and before the journal checkpoint: the same run, then both
//     copies of the journal (local file and S3 object) are put back to their state before the rotation.
//
// Decision the plans leave open (recorded in the handoff): a local bundle v+1 that is not the (new passphrase, old current)
// pair at generations N+1/N is refused with TOFU_ROTATION_BUNDLE_CONFLICT before any state is rewritten.

const stRotationConflictCode = "TOFU_ROTATION_BUNDLE_CONFLICT"

// failRecoveryWrites wraps an Account so that, while armed, every write to the recovery bucket fails: the process "dies"
// there.
type failRecoveryWrites struct {
	install.Account
	armed *atomic.Bool
}

func (a failRecoveryWrites) Bucket(name string) tofustate.Store {
	s := a.Account.Bucket(name)
	if name == bucketName("recovery") && a.armed.Load() {
		return deadStore{Store: s}
	}
	return s
}

type deadStore struct{ tofustate.Store }

var errDied = errors.New("injected crash: the process died before the recovery bucket write")

func (deadStore) PutObject(context.Context, string, []byte, s3client.PutOptions) (s3client.PutResult, error) {
	return s3client.PutResult{}, errDied
}
func (deadStore) PutIfAbsent(context.Context, string, []byte) (string, error) { return "", errDied }
func (deadStore) Put(context.Context, string, []byte, string) (string, error) { return "", errDied }

// orderedFake is the fake OpenTofu that notes, at every refresh-only apply, whether the recovery bucket already held the
// bundle version the rotation is going to.
type orderedFake struct {
	*stFake
	hasBundle func() bool
	atRefresh []bool
}

func (o *orderedFake) RefreshOnly(ctx context.Context, module string, remote install.RemoteState, e tofustate.Encryption) error {
	o.mu.Lock()
	o.atRefresh = append(o.atRefresh, o.hasBundle())
	o.mu.Unlock()
	return o.stFake.RefreshOnly(ctx, module, remote, e)
}

func stBundleObject(version int) string {
	return clusterName + "/bundles/sc-recovery-" + clusterName + fmt.Sprintf("-v%d.tar.age", version)
}

// rotateWith runs `rotate tofu-passphrase` with the given fake OpenTofu and S3 opener.
func (f *stFix) rotateWith(state install.StateTofu, newS3 func(s3client.Config) (install.Account, error)) (int, string) {
	f.t.Helper()
	d := f.i.deps("")
	d.State = state
	if newS3 != nil {
		d.NewS3 = newS3
	}
	var out, errb bytes.Buffer
	code := install.Run([]string{"--env-file", f.i.envFile, "--non-interactive", "rotate", "tofu-passphrase"},
		merge(f.i.env, f.i.unattendedEnv()), &out, &errb, d)
	return code, out.String() + errb.String()
}

func (f *stFix) journalObjectKey() string { return clusterName + "/journal/journal.json" }

// crashedRotation runs a rotation that dies at the recovery-bucket upload (window A). With rewind, the journal is then put
// back to what it was before the rotation (window B). It returns the path of the sealed bundle v+1.
func (f *stFix) crashedRotation(rewind bool) (p1, p2, v2Path string) {
	f.t.Helper()
	jpath := filepath.Join(f.i.repo, ".bootstrap", "journal.json")
	local0, err := os.ReadFile(jpath)
	if err != nil {
		f.t.Fatal(err)
	}
	remote0, ok := f.i.world.read(bucketName("tofu-state"), f.journalObjectKey())
	if !ok {
		f.t.Fatal("fixture: no journal object in the tofu-state bucket")
	}
	v1 := f.i.journal().RecoveryBundle
	armed := &atomic.Bool{}
	armed.Store(true)
	code, out := f.rotateWith(f.fake, func(c s3client.Config) (install.Account, error) {
		a, err := f.i.world.open(c)
		if err != nil {
			return nil, err
		}
		return failRecoveryWrites{Account: a, armed: armed}, nil
	})
	if code == 0 {
		f.t.Fatalf("fixture: the rotation succeeded although the recovery bucket was unreachable:\n%s", out)
	}
	if n := f.fake.count("refresh", ""); n != 0 {
		f.t.Fatalf("fixture: %d refresh-only applies ran before the upload", n)
	}
	v2Path = filepath.Join(filepath.Dir(v1.LocalPath), fmt.Sprintf("sc-recovery-%s-v%d.tar.age", clusterName, v1.Generation+1))
	if _, err := os.Stat(v2Path); err != nil {
		f.t.Fatalf("fixture: the sealed bundle v%d is not on disk: %v", v1.Generation+1, err)
	}
	if _, ok := f.i.world.read(bucketName("recovery"), stBundleObject(v1.Generation+1)); ok {
		f.t.Fatal("fixture: the bundle reached the recovery bucket")
	}
	if rewind {
		if err := os.WriteFile(jpath, local0, 0o600); err != nil {
			f.t.Fatal(err)
		}
		if err := f.i.world.write(stateKeyID, bucketName("tofu-state"), f.journalObjectKey(), remote0); err != nil {
			f.t.Fatal(err)
		}
		if jb := f.i.journal().RecoveryBundle; jb.Generation != v1.Generation || jb.Status != v1.Status {
			f.t.Fatalf("fixture: the rewound journal says %+v", jb)
		}
	} else if jb := f.i.journal().RecoveryBundle; jb.Status != "created" || jb.Generation != v1.Generation+1 {
		f.t.Fatalf("fixture: the journal after the crash says %+v, want status created at generation %d", jb, v1.Generation+1)
	}
	m, files := f.openBundle(v2Path)
	for _, e := range m.Entries {
		if e.Class == "SC-TOFU-ENCRYPTION" && e.Instance == "current" {
			p2 = string(files[e.Path])
		}
		if e.Class == "SC-TOFU-ENCRYPTION" && e.Instance == "previous" {
			p1 = string(files[e.Path])
		}
	}
	if p1 != f.pass1 || p2 == "" || p2 == p1 {
		f.t.Fatal("fixture: the sealed bundle does not list (P2, P1)")
	}
	return p1, p2, v2Path
}

// checkRotatedWith asserts that every module opens with p2 alone at generation 2 and that the refresh-only applies ran with
// P2 enforced and P1 as the fallback.
func (f *stFix) checkRotatedWith(fake *stFake, p1, p2 string) {
	f.t.Helper()
	for _, mod := range stModules {
		if _, _, err := f.fake.open(f.current(mod), tofustate.Encryption{Current: p2, Generation: 2}); err != nil {
			f.t.Errorf("%s: state pull with only P2 fails: %v", mod, err)
		}
		if _, _, err := f.fake.open(f.current(mod), tofustate.Encryption{Current: p1, Generation: 1}); err == nil {
			f.t.Errorf("%s: state pull with only P1 succeeds", mod)
		}
	}
	for _, c := range fake.snapshot() {
		if c.Op == "refresh" && c.Enc != (tofustate.Encryption{Current: p2, Generation: 2, Previous: p1}) {
			f.t.Errorf("%s: refresh-only apply ran with %+v, want the bundle's P2 as generation 2 and P1 as the fallback", c.Module, c.Enc)
		}
	}
}

// 06-AC-6, window A (plan 06 section 20.3, plan 50 section 17): a rotation that sealed bundle v+1 (journal "created") and
// died before uploading it is finished by the rerun, and the rerun uploads and records v+1 before it rewrites any state
// with the new passphrase.
func TestRotateTofuPassphrase_RerunAfterCrashBeforeUpload_UploadsBundleFirst_06AC6(t *testing.T) {
	t.Parallel()
	f := newStateFix(t)
	v1 := f.i.journal().RecoveryBundle
	p1, p2, v2Path := f.crashedRotation(false)
	sealed, err := os.ReadFile(v2Path)
	if err != nil {
		t.Fatal(err)
	}

	of := &orderedFake{stFake: f.fake, hasBundle: func() bool {
		_, ok := f.i.world.read(bucketName("recovery"), stBundleObject(v1.Generation+1))
		return ok
	}}
	code, out := f.rotateWith(of, nil)
	requireExit(t, code, 0, out)

	if len(of.atRefresh) == 0 {
		t.Fatal("the rerun rewrote no state")
	}
	for n, had := range of.atRefresh {
		if !had {
			t.Errorf("refresh-only apply %d ran while the recovery bucket lacked bundle v%d: a state was rewritten with a passphrase that only exists on this host", n+1, v1.Generation+1)
		}
	}
	up, ok := f.i.world.read(bucketName("recovery"), stBundleObject(v1.Generation+1))
	if !ok || !bytes.Equal(up, sealed) {
		t.Errorf("the recovery bucket holds bundle v%d = %v, want the bundle sealed before the crash", v1.Generation+1, ok)
	}
	jb := f.i.journal().RecoveryBundle
	if jb.Generation != v1.Generation+1 || jb.Status != "uploaded" || jb.S3URI != "s3://"+bucketName("recovery")+"/"+stBundleObject(v1.Generation+1) || jb.S3VersionID == "" {
		t.Errorf("journal bundle after the rerun %+v, want v%d uploaded with its S3 URI and version id", jb, v1.Generation+1)
	}
	if _, ok := f.i.world.read(bucketName("recovery"), stBundleObject(v1.Generation+2)); ok {
		t.Errorf("a bundle v%d was uploaded: the rerun must finish the sealed rotation, not start another", v1.Generation+2)
	}
	f.checkRotatedWith(f.fake, p1, p2)
}

// 06-AC-6, window B: the process died after writing bundle v+1 and LATEST.json and before the journal checkpoint. The rerun
// adopts that bundle (its pair is the new passphrase and the old current at generations N+1 and N) instead of failing for
// ever with BUNDLE_VERSION_EXISTS, uploads it first and finishes the rotation with that passphrase.
func TestRotateTofuPassphrase_RerunAfterCrashBeforeCheckpoint_AdoptsLocalBundle_06AC6(t *testing.T) {
	t.Parallel()
	f := newStateFix(t)
	v1 := f.i.journal().RecoveryBundle
	p1, p2, v2Path := f.crashedRotation(true)
	sealed, err := os.ReadFile(v2Path)
	if err != nil {
		t.Fatal(err)
	}

	of := &orderedFake{stFake: f.fake, hasBundle: func() bool {
		_, ok := f.i.world.read(bucketName("recovery"), stBundleObject(v1.Generation+1))
		return ok
	}}
	code, out := f.rotateWith(of, nil)
	if strings.Contains(out, "BUNDLE_VERSION_EXISTS") {
		t.Errorf("the rerun fails with BUNDLE_VERSION_EXISTS although the local bundle is the sealed rotation:\n%s", out)
	}
	requireExit(t, code, 0, out)

	after, err := os.ReadFile(v2Path)
	if err != nil || !bytes.Equal(after, sealed) {
		t.Errorf("the local bundle v%d changed (read error %v): the rerun must adopt it", v1.Generation+1, err)
	}
	up, ok := f.i.world.read(bucketName("recovery"), stBundleObject(v1.Generation+1))
	if !ok || !bytes.Equal(up, sealed) {
		t.Errorf("the recovery bucket holds bundle v%d = %v, want the adopted bundle", v1.Generation+1, ok)
	}
	for n, had := range of.atRefresh {
		if !had {
			t.Errorf("refresh-only apply %d ran while the recovery bucket lacked bundle v%d", n+1, v1.Generation+1)
		}
	}
	jb := f.i.journal().RecoveryBundle
	if jb.Generation != v1.Generation+1 || jb.Status != "uploaded" || jb.LocalPath != v2Path {
		t.Errorf("journal bundle after the rerun %+v, want v%d uploaded at %s", jb, v1.Generation+1, v2Path)
	}
	f.checkRotatedWith(f.fake, p1, p2)
}

// 06-AC-6, window B with a bundle that is not the sealed rotation: a local bundle v+1 whose pair is not (new passphrase,
// old current) at generations N+1/N is refused with a named code, before any state is rewritten and before anything is
// uploaded.
func TestRotateTofuPassphrase_RerunWithForeignLocalBundle_FailsNamed_06AC6(t *testing.T) {
	t.Parallel()
	f := newStateFix(t)
	v1 := f.i.journal().RecoveryBundle
	// a bundle v+1 that lists no rotation at all: the current bundle's own bytes
	old, err := os.ReadFile(v1.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	v2 := filepath.Join(filepath.Dir(v1.LocalPath), fmt.Sprintf("sc-recovery-%s-v%d.tar.age", clusterName, v1.Generation+1))
	if err := os.WriteFile(v2, old, 0o600); err != nil {
		t.Fatal(err)
	}

	code, out := f.rotateWith(f.fake, nil)
	if code == 0 {
		t.Fatalf("the rotation succeeded over a foreign local bundle v%d:\n%s", v1.Generation+1, out)
	}
	if !strings.Contains(out, stRotationConflictCode) {
		t.Errorf("the failure does not carry the code %s:\n%s", stRotationConflictCode, out)
	}
	if n := f.fake.count("refresh", ""); n != 0 {
		t.Errorf("%d refresh-only applies ran although the local bundle is foreign", n)
	}
	if _, ok := f.i.world.read(bucketName("recovery"), stBundleObject(v1.Generation+1)); ok {
		t.Error("the foreign bundle was uploaded")
	}
	for _, mod := range stModules {
		if _, _, err := f.fake.open(f.current(mod), tofustate.Encryption{Current: f.pass1, Generation: 1}); err != nil {
			t.Errorf("%s no longer opens with the original passphrase: %v", mod, err)
		}
	}
	if got, _ := os.ReadFile(v2); !bytes.Equal(got, old) {
		t.Error("the foreign local bundle was overwritten")
	}
}

// --- state verify takes the run lock (plan 50 section 7.1, section 17.4, exit 6 RUN_LOCK_HELD)

func (f *stFix) holdRunLock() {
	f.t.Helper()
	body, _ := json.Marshal(map[string]string{
		"run_id": "01JFAKEOTHERRUN0000000000", "command": "rotate", "user": "other", "host": "another-host.example.net",
		"install_uid": f.i.journal().InstallUID, "started_at": "2026-10-27T09:00:00Z", "lease_expires_at": "2026-10-27T21:00:00Z",
	})
	if err := f.i.world.write(stateKeyID, bucketName("tofu-state"), clusterName+"/locks/bootstrap.lock", body); err != nil {
		f.t.Fatal(err)
	}
}

// Plan 50 section 7.1 (`state` row: run lock yes except status and versions), section 7.2 and 17.4: while another run holds
// the run lock, `state verify` exits 6 with RUN_LOCK_HELD, runs no OpenTofu and writes no restore-test result, so a rotation
// in flight cannot make it report a false failure.
func TestStateVerify_RunLockHeld_Exits6WritesNoResult_Plan50(t *testing.T) {
	t.Parallel()
	f := newStateFix(t)
	f.holdRunLock()
	code, out := f.run("", nil, "--non-interactive", "state", "verify")
	requireExit(t, code, 6, out)
	if !strings.Contains(out, "RUN_LOCK_HELD") {
		t.Errorf("the failure does not carry RUN_LOCK_HELD:\n%s", out)
	}
	if objs := f.i.world.objects(bucketName("tofu-state"), stRestoreTests); len(objs) != 0 {
		t.Errorf("a restore-test result was written while the run lock was held: %v", objs)
	}
	if n := len(f.fake.snapshot()); n != 0 {
		t.Errorf("OpenTofu ran %d times while the run lock was held", n)
	}
}

// The same for an install with no OpenTofu module: there is no journal, and the lock still applies.
func TestStateVerify_NoModuleInstall_RunLockHeld_Exits6_Plan50(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	i.setVars(map[string]string{"PROVIDER": "existing", "S3_BUCKET_MODE": "existing", "EXISTING_NODES": "node-01=ops@192.0.2.10/10.0.0.11"})
	if err := i.world.createBucket(stateKeyID, bucketName("tofu-state")); err != nil {
		t.Fatal(err)
	}
	body := `{"run_id":"01JFAKEOTHERRUN0000000000","command":"state restore","user":"other","host":"another-host.example.net","install_uid":"","started_at":"2026-10-27T09:00:00Z","lease_expires_at":"2026-10-27T21:00:00Z"}`
	if err := i.world.write(stateKeyID, bucketName("tofu-state"), clusterName+"/locks/bootstrap.lock", []byte(body)); err != nil {
		t.Fatal(err)
	}
	fake := &stFake{w: i.world}
	d := i.deps("")
	d.State = fake
	env := merge(i.env, map[string]string{"SC_CONFIRM_BUDGET": budgetMonthly, "SC_CONFIRM_BACKSTOPS": i.backstopFile()})
	var out, errb bytes.Buffer
	code := install.Run(i.envOnlyArgs("--non-interactive", "state", "verify"), env, &out, &errb, d)
	requireExit(t, code, 6, out.String()+errb.String())
	if !strings.Contains(out.String()+errb.String(), "RUN_LOCK_HELD") {
		t.Errorf("the failure does not carry RUN_LOCK_HELD:\n%s", out.String()+errb.String())
	}
	if objs := i.world.objects(bucketName("tofu-state"), stRestoreTests); len(objs) != 0 {
		t.Errorf("a restore-test result was written while the run lock was held: %v", objs)
	}
}
