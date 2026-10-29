package abandon_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/install"
	"strange.company/sc/internal/tofustate"
)

// S8b1 review 3 of F08 (plan 50 section 14.3 and BOOT-19 "re-sealed at most once during an install run", plan 06
// section 20.4): the BOOT-19 regression of the journal-first generation, the version id of the short-path state
// reference and the placement of SetInstallUID.

const uidPlaceholderS8b1R3 = "00000000-0000-4000-8000-000000000000"

func recoveryObjectsS8b1R3(i *inst) map[string]string {
	out := map[string]string{}
	for _, k := range i.world.objects(bucketName("recovery"), "") {
		b, _ := i.world.read(bucketName("recovery"), k)
		out[k] = string(b)
	}
	return out
}

func identityFileRefS8b1R3(t *testing.T, j journal) string {
	t.Helper()
	st, ok := j.step("plan.install-identity")
	if !ok {
		t.Fatal("the journal has no plan.install-identity step")
	}
	for _, r := range st.OutputRefs {
		if r.Kind == "file" {
			return r.Ref
		}
	}
	t.Fatal("plan.install-identity has no file output reference")
	return ""
}

// Blocking: plan.install-identity runs again after plan.storage-credentials re-sealed the bundle (HEAD lost the
// installation commit). The bundle stays generation 2: no second re-seal, no new object in the recovery bucket, and the
// identity step's file reference still names the identity's first bundle.
func TestStorageCredentials_IdentityRerunAfterReSealDoesNotReSealAgain_50AC18_S8b1R3Boot19(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	args := i.args("--confirm-storage", clusterName)
	stdin := twice(goodPass) + goodPass + "\n"
	code, out := i.run(stdin, stopAfter("plan.storage-credentials"), args)
	requireExit(t, code, 0, out)
	j := i.journal()
	if g := j.RecoveryBundle.Generation; g != 2 {
		t.Fatalf("fixture: the bundle is generation %d after plan.storage-credentials, want 2", g)
	}
	sha := j.RecoveryBundle.Sha256
	objs := recoveryObjectsS8b1R3(i)
	if !strings.Contains(identityFileRefS8b1R3(t, j), "-v1.tar.age") {
		t.Fatalf("fixture: the identity's file reference is %s, want bundle v1", identityFileRefS8b1R3(t, j))
	}

	root := i.git("rev-list", "--max-parents=0", "HEAD")
	i.git("reset", "-q", "--hard", root)

	code, out = i.run(stdin, stopAfter("plan.storage-credentials"), args)
	requireExit(t, code, 0, out)
	j = i.journal()
	if g := j.RecoveryBundle.Generation; g != 2 {
		t.Errorf("the bundle is generation %d after plan.install-identity ran again, want 2: the credentials were sealed a second time", g)
	}
	if j.RecoveryBundle.Sha256 != sha {
		t.Errorf("the recovery bundle changed (%s, was %s)", j.RecoveryBundle.Sha256, sha)
	}
	after := recoveryObjectsS8b1R3(i)
	for k := range after {
		if _, ok := objs[k]; !ok {
			t.Errorf("a new object %s was uploaded to the recovery bucket", k)
		}
	}
	if ref := identityFileRefS8b1R3(t, j); !strings.Contains(ref, "-v1.tar.age") {
		t.Errorf("the identity step's file reference is %s after the rerun, want the first bundle (v1)", ref)
	}
}

// The state-migrate Run short path (the remote state exists, no local state is left, and the check path just deleted
// it) records the same reference as the normal migration: the object with its ?versionId=.
func TestStateMigrate_ShortPathReferenceCarriesTheVersionId_50AC18_S8b1R3Ref(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	inner := newFlexTofu(i.tofu)
	tf := &changesFailTofu{flexTofu: inner, failChanges: 1}
	e := &established{inst: i, tf: inner, with: func(d *install.Deps) { d.Tofu = tf }, st: bucketName("tofu-state"), key: tofustate.StateKey(clusterName, "storage")}
	code, out := i.runWith(e.with, twice(goodPass)+goodPass+"\n", stopAfter("plan.state-migrate"), i.args("--confirm-storage", clusterName))
	if code == 0 {
		t.Fatalf("fixture: the run whose remote plan failed exited 0:\n%s", out)
	}
	code, out = e.resume(goodPass + "\n")
	requireExit(t, code, 0, out)
	st, ok := i.journal().step("plan.state-migrate")
	if !ok || st.Status != "succeeded" {
		t.Fatalf("fixture: plan.state-migrate is %+v after the rerun", st)
	}
	re := regexp.MustCompile(`^s3://` + regexp.QuoteMeta(e.st+"/"+e.key) + `\?versionId=.+$`)
	found := false
	for _, r := range st.OutputRefs {
		if r.Kind != "tofu-state" {
			continue
		}
		found = true
		if !re.MatchString(r.Ref) {
			t.Errorf("the tofu-state reference is %q, want s3://%s/%s?versionId=<id> like the normal migration", r.Ref, e.st, e.key)
		}
	}
	if !found {
		t.Error("plan.state-migrate has no tofu-state output reference")
	}
}

// SetInstallUID sits just before SetRecoveryBundle: a local I/O failure after the passphrase and the seal (the
// bundles directory cannot be created) leaves no real install_uid without a recovery bundle, and the rerun after the
// fault is removed completes the identity step.
func TestInstallIdentity_LocalWriteFailureLeavesNoUidWithoutBundleAndTheRerunProceeds_50AC18_S8b1R3Uid(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	cdir := filepath.Join(i.blackstart, clusterName)
	if err := os.MkdirAll(cdir, 0o700); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(cdir, "bundles")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := i.args("--confirm-storage", clusterName)
	code, out := i.run(twice(goodPass), stopAfter("plan.install-identity"), args)
	if code == 0 {
		t.Fatalf("fixture: the identity step succeeded although the bundles directory cannot be created:\n%s", out)
	}
	if j, ok := i.journalIfAny(); ok && j.InstallUID != "" && j.InstallUID != uidPlaceholderS8b1R3 && j.RecoveryBundle.LocalPath == "" {
		t.Errorf("the journal carries install_uid %s without a recovery bundle after the local write failed", j.InstallUID)
	}

	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	code, out = i.run(twice(goodPass), stopAfter("plan.install-identity"), args)
	if code != 0 {
		t.Fatalf("the rerun after the fault was removed: exit %d, want 0:\n%s", code, out)
	}
	j := i.journal()
	if j.InstallUID == "" || j.InstallUID == uidPlaceholderS8b1R3 || j.RecoveryBundle.LocalPath == "" {
		t.Errorf("after the rerun: install_uid %q, bundle %q, want both set", j.InstallUID, j.RecoveryBundle.LocalPath)
	}
}
