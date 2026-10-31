package abandon_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"strange.company/sc/internal/bootstrap/install"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

// S8b1 review 2 of F08 (plan 50 sections 7.1 and 14.3, plan 06 section 20.4): findings B-1, B-2 and the precedence of
// the journal over the host-local first-generation marker (N-2).

// changesFailTofu is a flexTofu whose Changes fails with a transient error for the first failChanges calls made after
// a Migrate happened (the remote plan right after the migration).
type changesFailTofu struct {
	*flexTofu
	cmu         sync.Mutex
	failChanges int
}

func (c *changesFailTofu) Changes(ctx context.Context, s install.StorageSpec, r *install.RemoteState) (int, error) {
	c.cmu.Lock()
	fail := c.failChanges > 0 && r != nil && c.migrateCount() > 0
	if fail {
		c.failChanges--
	}
	c.cmu.Unlock()
	if fail {
		return 0, errors.New("fake tofu: transient failure of the remote plan")
	}
	return c.flexTofu.Changes(ctx, s, r)
}

// B-1 (plan 50 section 7.1, plan 06 section 20.4): plan.state-migrate that succeeded through its CHECK (the local and
// the remote state equal; the local state deleted) still counts as "ever succeeded". A later StateList failure that is
// not about the passphrase must not turn into a re-import, neither on that run nor on the next one.
func TestStateMigrate_SuccessThroughTheCheckStillCountsAsEstablished_50AC18_S8b1R2B1(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	inner := newFlexTofu(i.tofu)
	tf := &changesFailTofu{flexTofu: inner, failChanges: 1}
	e := &established{inst: i, tf: inner, with: func(d *install.Deps) { d.Tofu = tf }, st: bucketName("tofu-state"), key: tofustate.StateKey(clusterName, "storage")}

	// (a) Migrate succeeds, the remote plan fails: the step fails and the local state is left
	code, out := i.runWith(e.with, twice(goodPass)+goodPass+"\n", stopAfter("plan.state-migrate"), i.args("--confirm-storage", clusterName))
	if code == 0 {
		t.Fatalf("fixture: the run whose remote plan failed exited 0:\n%s", out)
	}
	if m := inner.migrateCount(); m != 1 {
		t.Fatalf("fixture: %d Migrate calls in the first run, want 1", m)
	}
	// (b) the check finds local and remote equal and deletes the local state
	code, out = e.resume(goodPass + "\n")
	requireExit(t, code, 0, out)
	if got := i.journal().stepStatus("plan.state-migrate"); got != "succeeded" {
		t.Fatalf("fixture: plan.state-migrate is %s after the rerun, want succeeded", got)
	}
	migrates := inner.migrateCount()
	before, _ := e.world.read(e.st, e.key)
	if len(before) == 0 {
		t.Fatal("fixture: no remote storage state")
	}

	// (c) and (d): the store answers 503 on StateList
	inner.configure(func(f *flexTofu) { f.listErr = errors.New("fake tofu: 503 Service Unavailable") })
	code, out = e.resume(goodPass + "\n")
	e.requireNotReimported(t, code, out, before)
	code, out = e.resume(goodPass + "\n")
	e.requireNotReimported(t, code, out, before)
	if m := inner.migrateCount(); m != migrates {
		t.Errorf("Migrate was called %d more times while the store failed", m-migrates)
	}

	// the store recovers: the run completes without an import
	inner.configure(func(f *flexTofu) { f.listErr = nil })
	code, out = e.resume(goodPass + "\n")
	requireExit(t, code, 0, out)
	if n := inner.attemptCount(); n != 0 {
		t.Errorf("ImportStorage was called %d times after the store recovered", n)
	}
	if m := inner.migrateCount(); m != migrates {
		t.Errorf("Migrate was called %d more times after the store recovered", m-migrates)
	}
	if now, _ := e.world.read(e.st, e.key); string(now) != string(before) {
		t.Errorf("the remote storage state changed")
	}
}

// B-2 (plan 50 section 14.3, review-1 N-3): after abandon-identity a run whose recovery bucket listing fails exits 7
// and must leave nothing behind that makes the next run fail: no new install_uid in the journal without a recovery
// bundle. The rerun with the store reachable plans the new identity (first generation at least 2) instead of exiting
// 8 BUNDLE_MISSING.
func TestInstallIdentity_RecoveryListingFailureLeavesNoUidWithoutBundleAndTheRerunPlans_50AC18_S8b1R2B2(t *testing.T) {
	t.Parallel()
	s := newStale(t)
	code, out := s.abandon(nil, s.typed())
	requireExit(t, code, 0, out)
	mod := func(d *install.Deps) {
		open := d.NewS3
		d.NewS3 = func(c s3client.Config) (install.Account, error) {
			a, err := open(c)
			if err != nil {
				return nil, err
			}
			return listFailAccount{Account: a, err: &s3client.Error{Status: 503, Code: "ServiceUnavailable"}}, nil
		}
	}
	_, d8 := planDigest(t)
	env := merge(stopAfter("plan"), map[string]string{"SC_CONFIRM_PLAN": d8})
	stdin := twice(newPassS8b1R1) + newPassS8b1R1 + "\n"
	code, out = s.runWith(mod, stdin, env, s.args("--confirm-storage", clusterName))
	if code != 7 {
		t.Fatalf("exit %d, want 7 (the recovery bucket cannot be listed):\n%s", code, out)
	}
	if j, ok := s.journalIfAny(); ok && j.InstallUID != "" && j.InstallUID != "00000000-0000-4000-8000-000000000000" && j.RecoveryBundle.LocalPath == "" {
		t.Errorf("the journal carries install_uid %s without a recovery bundle after the failed identity step", j.InstallUID)
	}

	code, out = s.run(stdin, env, s.args("--confirm-storage", clusterName))
	if code != 0 {
		t.Fatalf("the rerun with the store reachable: exit %d, want 0 (planned with the new identity):\n%s", code, out)
	}
	j := s.journal()
	if j.Installation.State != "planned" || j.InstallUID == "" || j.InstallUID == s.uid {
		t.Errorf("after the rerun: state %q install_uid %q (old %s), want planned and a new uid", j.Installation.State, j.InstallUID, s.uid)
	}
	if g := j.RecoveryBundle.Generation; g < 2 {
		t.Errorf("the new identity's bundle is generation %d, want at least 2", g)
	}
}

// N-2 precedence (plan 50 section 14.3): the identity's first generation comes from the journal (the bundle file
// reference of plan.install-identity) first; the host-local <cdir>/first-generation marker is only a fallback. A stale
// marker that names a higher generation than the journal must not make plan.storage-credentials re-seal again once it
// has re-sealed the bundle (generation 3 over the journal's first generation 2).
func TestStorageCredentials_JournalFirstGenerationWinsOverAStaleMarker_50AC18_S8b1R2N2(t *testing.T) {
	t.Parallel()
	s := newStale(t)
	code, out := s.abandon(nil, s.typed())
	requireExit(t, code, 0, out)
	args := s.args("--confirm-storage", clusterName)
	code, out = s.run(twice(newPassS8b1R1)+newPassS8b1R1+"\n", stopAfter("plan.storage-credentials"), args)
	requireExit(t, code, 0, out)
	j := s.journal()
	if g := j.RecoveryBundle.Generation; g != 3 {
		t.Fatalf("fixture: after plan.storage-credentials the bundle is generation %d, want 3 (first generation 2 plus the re-seal)", g)
	}
	sha := j.RecoveryBundle.Sha256
	// a marker left by another history: it names generation 3 as the identity's first
	if err := os.WriteFile(filepath.Join(s.cdir, "first-generation"), []byte("3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out = s.run(newPassS8b1R1+"\n", stopAfter("plan.storage-credentials"), args)
	requireExit(t, code, 0, out)
	j = s.journal()
	if g := j.RecoveryBundle.Generation; g != 3 {
		t.Errorf("the bundle is generation %d after the rerun, want 3: the stale marker was trusted over the journal's first generation 2 and the credentials were sealed again", g)
	}
	if j.RecoveryBundle.Sha256 != sha {
		t.Errorf("the recovery bundle changed although the journal says it was already re-sealed")
	}
}
