package installstate2_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"filippo.io/age"

	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/bootstrap/install"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

// Review 2 of S7 (F08), non-blocking findings N-c and N-d.
//
// N-c (06-AC-6, plan 06 section 20.3): `rotate tofu-passphrase` must never report success without rotating. After a
// journal-loss adoption of a bundle that is an already-completed rotation (every module already opens with its current
// passphrase alone), the rerun starts a NEW rotation (passphrase P3, bundle v+2) or fails with a named code. Adopting a local
// bundle v+1 also checks its manifest (bundle_version v+1, previous_bundle_sha256 = the current bundle's sha256, trigger
// "rotation") and rewrites a stale LATEST.json.
//
// N-d (plan 50 section 7.1 and 17.4): `state verify` surfaces RUN_LOCK_RELEASE_FAILED with a non-zero exit when it cannot
// release the run lock, the way ExecuteOp does.

var namedCodeRe = regexp.MustCompile(`\b[A-Z][A-Z0-9]*(_[A-Z0-9]+)+\b`)

// journalCopies holds both copies of the journal (local file and S3 object).
type journalCopies struct {
	local  []byte
	remote []byte
}

func (f *stFix) saveJournal() journalCopies {
	f.t.Helper()
	local, err := os.ReadFile(filepath.Join(f.i.repo, ".bootstrap", "journal.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	remote, ok := f.i.world.read(bucketName("tofu-state"), f.journalObjectKey())
	if !ok {
		f.t.Fatal("fixture: no journal object in the tofu-state bucket")
	}
	return journalCopies{local, remote}
}

func (f *stFix) restoreJournal(j journalCopies) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.i.repo, ".bootstrap", "journal.json"), j.local, 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := f.i.world.write(stateKeyID, bucketName("tofu-state"), f.journalObjectKey(), j.remote); err != nil {
		f.t.Fatal(err)
	}
}

func (f *stFix) bundlePath(version int) string {
	return filepath.Join(filepath.Dir(f.i.journal().RecoveryBundle.LocalPath), fmt.Sprintf("sc-recovery-%s-v%d.tar.age", clusterName, version))
}

func (f *stFix) opensAlone(module, pass string, gen int) bool {
	_, _, err := f.fake.open(f.current(module), tofustate.Encryption{Current: pass, Generation: gen})
	return err == nil
}

func (f *stFix) tofuPair(path string) (cur, prev string) {
	f.t.Helper()
	m, files := f.openBundle(path)
	for _, e := range m.Entries {
		if e.Class == "SC-TOFU-ENCRYPTION" && e.Instance == "current" {
			cur = string(files[e.Path])
		}
		if e.Class == "SC-TOFU-ENCRYPTION" && e.Instance == "previous" {
			prev = string(files[e.Path])
		}
	}
	return cur, prev
}

// 06-AC-6 (review 2 N-c): the journal and the unsynced S3 journal are lost after a rotation that completed (bundle v2 on disk
// and uploaded, every module already rewritten). The rerun adopts v2 as the newest bundle. Every module already opens with
// P2 alone, so there is nothing left to finish; the command must then rotate again (P3 in bundle v3) or fail with a named
// code, and must not print the rotated message while it generated nothing.
func TestRotateTofuPassphrase_AfterAdoptingCompletedRotation_DoesNotClaimRotatedWithoutRotating_06AC6(t *testing.T) {
	t.Parallel()
	f := newStateFix(t)
	v1 := f.i.journal().RecoveryBundle
	before := f.saveJournal()

	code, out := f.rotateWith(f.fake, nil)
	requireExit(t, code, 0, out)
	p2, p1 := f.tofuPair(f.bundlePath(v1.Generation + 1))
	if p1 != f.pass1 || p2 == "" || p2 == p1 {
		t.Fatal("fixture: the first rotation did not seal (P2, P1)")
	}
	for _, mod := range stModules {
		if !f.opensAlone(mod, p2, 2) {
			t.Fatalf("fixture: %s does not open with P2 alone after the first rotation", mod)
		}
	}
	f.restoreJournal(before) // the journal-loss: the sealed rotation is only on disk and in the recovery bucket

	code, out = f.rotateWith(f.fake, nil)
	v3 := f.bundlePath(v1.Generation + 2)
	if code == 0 {
		if _, err := os.Stat(v3); err != nil {
			t.Fatalf("the rerun exits 0 and reports success but generated nothing (no bundle v%d on disk):\n%s", v1.Generation+2, out)
		}
		if _, ok := f.i.world.read(bucketName("recovery"), stBundleObject(v1.Generation+2)); !ok {
			t.Errorf("the rerun exits 0 but bundle v%d is not in the recovery bucket", v1.Generation+2)
		}
		p3, prev := f.tofuPair(v3)
		if p3 == "" || p3 == p2 || prev != p2 {
			t.Errorf("bundle v%d: current is new = %v, previous is P2 = %v, want both true", v1.Generation+2, p3 != "" && p3 != p2, prev == p2)
		}
		for _, mod := range stModules {
			if !f.opensAlone(mod, p3, 3) {
				t.Errorf("%s does not open with the new passphrase alone at generation 3", mod)
			}
		}
		return
	}
	if !namedCodeRe.MatchString(out) {
		t.Errorf("the rerun fails without a named code:\n%s", out)
	}
	for _, mod := range stModules {
		if !f.opensAlone(mod, p2, 2) {
			t.Errorf("%s no longer opens with P2 alone after the failed rerun", mod)
		}
	}
}

// retag rewrites the bundle at path with a manifest changed by mutate, sealed again with stock age at a low work factor.
func (f *stFix) retag(path string, mutate func(m map[string]any)) {
	f.t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatal(err)
	}
	o, err := identity.Open(data, goodPass)
	if err != nil {
		f.t.Fatal(err)
	}
	var buf bytes.Buffer
	rcp, _ := age.NewScryptRecipient(goodPass)
	rcp.SetWorkFactor(10)
	aw, err := age.Encrypt(&buf, rcp)
	if err != nil {
		f.t.Fatal(err)
	}
	tw := tar.NewWriter(aw)
	for _, af := range o.Files {
		body := af.Data
		if af.Path == "manifest.json" {
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil {
				f.t.Fatal(err)
			}
			mutate(m)
			if body, err = json.MarshalIndent(m, "", "  "); err != nil {
				f.t.Fatal(err)
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: af.Path, Mode: af.Mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			f.t.Fatal(err)
		}
		tw.Write(body)
	}
	tw.Close()
	if err := aw.Close(); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *stFix) wantAdoptionConflict(why string, mutate func(m map[string]any)) {
	t := f.t
	t.Helper()
	v1 := f.i.journal().RecoveryBundle
	_, _, v2 := f.crashedRotation(true)
	f.retag(v2, mutate)
	tampered, _ := os.ReadFile(v2)

	code, out := f.rotateWith(f.fake, nil)
	if code == 0 {
		t.Fatalf("the rotation adopted a local bundle v%d whose manifest %s:\n%s", v1.Generation+1, why, out)
	}
	if !strings.Contains(out, stRotationConflictCode) {
		t.Errorf("the failure does not carry the code %s (manifest %s):\n%s", stRotationConflictCode, why, out)
	}
	if n := f.fake.count("refresh", ""); n != 0 {
		t.Errorf("%d refresh-only applies ran although the local bundle is foreign", n)
	}
	if _, ok := f.i.world.read(bucketName("recovery"), stBundleObject(v1.Generation+1)); ok {
		t.Error("the foreign bundle was uploaded")
	}
	if got, _ := os.ReadFile(v2); !bytes.Equal(got, tampered) {
		t.Error("the foreign local bundle was overwritten")
	}
}

// 06-AC-6 (review 2 N-c): adopting a local v+1 checks bundle_version == v+1.
func TestRotateTofuPassphrase_AdoptionRefusesWrongBundleVersion_06AC6(t *testing.T) {
	t.Parallel()
	f := newStateFix(t)
	v1 := f.i.journal().RecoveryBundle
	f.wantAdoptionConflict("claims another bundle_version", func(m map[string]any) { m["bundle_version"] = v1.Generation + 2 })
}

// 06-AC-6 (review 2 N-c): adopting a local v+1 checks previous_bundle_sha256 == the current bundle's sha256.
func TestRotateTofuPassphrase_AdoptionRefusesWrongPreviousSha_06AC6(t *testing.T) {
	t.Parallel()
	f := newStateFix(t)
	f.wantAdoptionConflict("does not chain to the current bundle", func(m map[string]any) {
		m["previous_bundle_sha256"] = strings.Repeat("0", 64)
	})
}

// 06-AC-6 (review 2 N-c): adopting a local v+1 checks trigger == "rotation".
func TestRotateTofuPassphrase_AdoptionRefusesOtherTrigger_06AC6(t *testing.T) {
	t.Parallel()
	f := newStateFix(t)
	f.wantAdoptionConflict("has another trigger", func(m map[string]any) {
		m["reason"] = map[string]any{"trigger": "initial-bootstrap"}
	})
}

// 06-AC-6 (review 2 N-c): a stale bundles/LATEST.json (still naming v1) is rewritten to point at the adopted v+1.
func TestRotateTofuPassphrase_AdoptionRewritesStaleLatest_06AC6(t *testing.T) {
	t.Parallel()
	f := newStateFix(t)
	v1 := f.i.journal().RecoveryBundle
	_, _, v2 := f.crashedRotation(true)
	latest := filepath.Join(filepath.Dir(v1.LocalPath), "LATEST.json")
	stale, _ := json.Marshal(map[string]any{"generation": v1.Generation, "file": filepath.Base(v1.LocalPath), "sha256": v1.Sha256})
	if err := os.WriteFile(latest, append(stale, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out := f.rotateWith(f.fake, nil)
	requireExit(t, code, 0, out)

	raw, err := os.ReadFile(latest)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Generation int    `json:"generation"`
		File       string `json:"file"`
		Sha256     string `json:"sha256"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(v2)
	sum := sha256.Sum256(data)
	if got.Generation != v1.Generation+1 || got.File != filepath.Base(v2) || got.Sha256 != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Errorf("LATEST.json after the adoption says %s, want generation %d, file %s, sha256:%x", raw, v1.Generation+1, filepath.Base(v2), sum)
	}
}

// --- N-d: the run lock of state verify cannot be released

// failLockRelease wraps an Account so that deleting the run-lock object fails.
type failLockRelease struct{ install.Account }

func (a failLockRelease) Bucket(name string) tofustate.Store {
	s := a.Account.Bucket(name)
	if name == bucketName("tofu-state") {
		return lockStuckStore{Store: s}
	}
	return s
}

type lockStuckStore struct{ tofustate.Store }

func (s lockStuckStore) Delete(ctx context.Context, key, ifMatch string) error {
	if strings.HasSuffix(key, "locks/bootstrap.lock") {
		return errors.New("injected: the run lock object cannot be deleted")
	}
	return s.Store.Delete(ctx, key, ifMatch)
}

// Plan 50 section 7.1 and 17.4 (review 2 N-d): when `state verify` cannot release the run lock it exits non-zero with
// RUN_LOCK_RELEASE_FAILED, as ExecuteOp does, instead of exiting 0 with the error swallowed.
func TestStateVerify_RunLockReleaseFails_ExitsNonZeroNamed_Plan50(t *testing.T) {
	t.Parallel()
	f := newStateFix(t)
	d := f.i.deps("")
	d.State = f.fake
	d.NewS3 = func(c s3client.Config) (install.Account, error) {
		a, err := f.i.world.open(c)
		if err != nil {
			return nil, err
		}
		return failLockRelease{Account: a}, nil
	}
	var out, errb bytes.Buffer
	code := install.Run([]string{"--env-file", f.i.envFile, "--non-interactive", "state", "verify"},
		merge(f.i.env, f.i.unattendedEnv()), &out, &errb, d)
	all := out.String() + errb.String()
	if !strings.Contains(all, "RUN_LOCK_RELEASE_FAILED") {
		t.Errorf("the output does not carry RUN_LOCK_RELEASE_FAILED:\n%s", all)
	}
	if code != 1 {
		t.Errorf("exit %d, want 1 (the run lock could not be released):\n%s", code, all)
	}
}
