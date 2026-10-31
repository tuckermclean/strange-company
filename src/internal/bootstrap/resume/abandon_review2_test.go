package resume_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/install"
)

// S8a review-2 of F08 (plan 50 sections 7.1, 7.2 and 14.3, BOOT-6, 50-AC-18): abandon-identity never decides about
// local files while the remote journal, which may show a confirmed compute plan, cannot be consulted; a refusal
// that is known locally wins over "retry when reachable".

func requireUnreachableRefusal(t *testing.T, code int, out string) {
	t.Helper()
	if code != 7 || !strings.Contains(out, "ABANDON_REMOTE_UNREACHABLE") {
		t.Errorf("exit %d, want 7 with ABANDON_REMOTE_UNREACHABLE:\n%s", code, out)
	}
	if strings.Contains(out, "archived as") || strings.Contains(out, "generates a new identity") {
		t.Errorf("the output claims an abandon or a new identity although the remote was unreachable:\n%s", out)
	}
}

// B3: no local journal, no install_uid, the object store unreachable, leftover bundles and recovery-identity.txt. The
// remote journal may show a confirmed compute plan (a re-cloned repository), so the orphan path must not move
// anything aside.
func TestAbandonIdentity_OrphansWithUnreachableStoreRefuseAndMoveNothing_50AC18_S8aR2B3(t *testing.T) {
	t.Parallel()
	s := newStale(t)
	if err := os.Remove(filepath.Join(s.repo, ".bootstrap", "journal.json")); err != nil {
		t.Fatal(err)
	}
	if len(s.localBundles()) == 0 {
		t.Fatal("fixture: no leftover bundle files")
	}
	if _, err := os.Stat(filepath.Join(s.cdir, "recovery-identity.txt")); err != nil {
		t.Fatalf("fixture: no recovery-identity.txt: %v", err)
	}
	before := s.localFileSet()

	code, out := s.abandon(func(d *install.Deps) { d.NewS3 = unreachableS3 }, "")
	requireUnreachableRefusal(t, code, out)
	if left := s.localBundles(); len(left) == 0 {
		t.Errorf("the bundle files were moved although the remote journal could not be consulted")
	}
	if _, err := os.Stat(filepath.Join(s.cdir, "recovery-identity.txt")); err != nil {
		t.Errorf("recovery-identity.txt was moved although the remote journal could not be consulted")
	}
	if after := s.localFileSet(); after != before {
		t.Errorf("the refused abandon-identity changed the local files:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if _, ok := s.world.read(bucketName("tofu-state"), s.journalKey); !ok {
		t.Errorf("the remote journal is gone")
	}
}

// R2-N1: the remote journal exists from plan.storage-apply on, before plan.bundle-upload records S3URI and
// S3VersionID. With the store unreachable the abandon is refused with exit 7 and changes nothing.
func TestAbandonIdentity_StorageAppliedWithoutBundleUploadUnreachableRefuses_50AC18_S8aR2N1(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	code, out := i.run(twice(goodPass), stopAfter("plan.storage-apply"), i.args("--confirm-storage", clusterName))
	requireExit(t, code, 0, out)
	j := i.journal()
	if got := j.stepStatus("plan.storage-apply"); got != "succeeded" {
		t.Fatalf("fixture: step plan.storage-apply = %s, want succeeded", got)
	}
	if j.RecoveryBundle.S3URI != "" || j.RecoveryBundle.S3VersionID != "" {
		t.Fatalf("fixture: the bundle is already uploaded (%q %q)", j.RecoveryBundle.S3URI, j.RecoveryBundle.S3VersionID)
	}
	s := &stale{inst: i, uid: j.InstallUID, cdir: filepath.Join(i.blackstart, clusterName), journalKey: clusterName + "/journal/journal.json"}
	before := s.localFileSet()

	code, out = s.abandon(func(d *install.Deps) { d.NewS3 = unreachableS3 }, s.typed())
	requireUnreachableRefusal(t, code, out)
	if after := s.localFileSet(); after != before {
		t.Errorf("the refused abandon-identity changed the local files:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if _, ok := i.journalIfAny(); !ok {
		t.Errorf("the local journal was removed")
	}
}

// R2-N2: the local journal already shows a confirmed compute plan and the store is unreachable: the refusal known
// locally (exit 4 ABANDON_REFUSED_COMPUTE_CONFIRMED) wins over exit 7.
func TestAbandonIdentity_ComputeConfirmedWinsOverUnreachableStore_50AC18_S8aR2N2(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	code, out := i.run("", i.unattendedEnv(stopAfter("plan.confirm")), i.envOnlyArgs("--non-interactive"))
	requireExit(t, code, 0, out)
	j := i.journal()
	if _, ok := j.confirmation("paid-footprint"); !ok {
		t.Fatal("fixture: no paid-footprint confirmation in the local journal")
	}
	s := &stale{inst: i, uid: j.InstallUID, cdir: filepath.Join(i.blackstart, clusterName), journalKey: clusterName + "/journal/journal.json"}
	before := s.localFileSet()

	code, out = s.abandon(func(d *install.Deps) { d.NewS3 = unreachableS3 }, s.typed())
	if code != 4 || !strings.Contains(out, "ABANDON_REFUSED_COMPUTE_CONFIRMED") {
		t.Errorf("exit %d, want 4 with ABANDON_REFUSED_COMPUTE_CONFIRMED (known locally, not exit 7):\n%s", code, out)
	}
	if after := s.localFileSet(); after != before {
		t.Errorf("the refused abandon-identity changed the local files")
	}
}
