package resume_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// S8a review-3 of F08 (plan 50 sections 7.1, 7.2 and 14.3, BOOT-6, 50-AC-18): before plan.storage-apply the tofu-state
// bucket does not exist. The store is reachable then and no remote journal can exist, so abandon-identity must not
// treat the missing bucket as "object store unreachable" (exit 7).

// B4a: the journal was lost after plan.install-identity left the bundle and recovery-identity.txt behind; the store is
// reachable but has no bucket yet. The orphans move aside and the next run starts a new identity.
func TestAbandonIdentity_OrphansBeforeStorageApplyWithReachableStoreMoveAside_50AC18_S8aR3B4(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	code, out := i.run(twice(goodPass), stopAfter("plan.install-identity"), i.args("--confirm-storage", clusterName))
	requireExit(t, code, 0, out)
	s := &stale{inst: i, uid: i.journal().InstallUID, cdir: filepath.Join(i.blackstart, clusterName), journalKey: clusterName + "/journal/journal.json"}
	if _, ok := s.world.read(bucketName("tofu-state"), s.journalKey); ok {
		t.Fatal("fixture: a remote journal exists before plan.storage-apply")
	}
	if err := os.Remove(filepath.Join(i.repo, ".bootstrap", "journal.json")); err != nil {
		t.Fatal(err)
	}
	if len(s.localBundles()) == 0 {
		t.Fatal("fixture: no leftover bundle files")
	}
	if _, err := os.Stat(filepath.Join(s.cdir, "recovery-identity.txt")); err != nil {
		t.Fatalf("fixture: no recovery-identity.txt: %v", err)
	}

	code, out = s.abandon(nil, "")
	if code != 0 {
		t.Errorf("exit %d, want 0 (reachable store without a bucket: no remote journal can exist):\n%s", code, out)
	}
	if strings.Contains(out, "ABANDON_REMOTE_UNREACHABLE") {
		t.Errorf("a missing bucket was reported as an unreachable store:\n%s", out)
	}
	if left := s.localBundles(); len(left) != 0 {
		t.Errorf("bundle files remain: %v", left)
	}
	if _, err := os.Stat(filepath.Join(s.cdir, "recovery-identity.txt")); err == nil {
		t.Errorf("recovery-identity.txt remains in %s", s.cdir)
	}
	if m, _ := filepath.Glob(filepath.Join(s.cdir, "abandoned", "orphaned-*", "recovery-identity.txt")); len(m) == 0 {
		t.Errorf("recovery-identity.txt is not kept under %s/abandoned/orphaned-*", s.cdir)
	}
	if m, _ := filepath.Glob(filepath.Join(s.cdir, "abandoned", "orphaned-*", "bundles", "*")); len(m) == 0 {
		t.Errorf("the bundle files are not kept under %s/abandoned/orphaned-*/bundles", s.cdir)
	}
	s.requireNewIdentityPlanned(t)
}

// B4b: a fresh host (nothing local, no bucket, reachable store) has no identity to abandon: exit 2, not 7.
func TestAbandonIdentity_FreshHostWithReachableStoreWithoutBucketHasNoIdentity_50AC18_S8aR3B4(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	s := &stale{inst: i, cdir: filepath.Join(i.blackstart, clusterName), journalKey: clusterName + "/journal/journal.json"}
	code, out := s.abandon(nil, "")
	if code != 2 || !strings.Contains(out, "ABANDON_NO_IDENTITY") {
		t.Errorf("exit %d, want 2 with ABANDON_NO_IDENTITY:\n%s", code, out)
	}
	if strings.Contains(out, "ABANDON_REMOTE_UNREACHABLE") {
		t.Errorf("a missing bucket was reported as an unreachable store:\n%s", out)
	}
}
