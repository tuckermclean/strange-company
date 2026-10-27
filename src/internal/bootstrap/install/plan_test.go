package install_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/identity"
	"strange.company/sc/internal/bootstrap/install"
	"strange.company/sc/internal/s3client"
)

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// stopAfter ends the pipeline successfully after a step (SC_TEST_STOP_AFTER, plan 50 section 9.2).
func stopAfter(step string) map[string]string { return map[string]string{"SC_TEST_STOP_AFTER": step} }

func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func requireExit(t *testing.T, got, want int, out string) {
	t.Helper()
	if got != want {
		t.Fatalf("exit %d, want %d:\n%s", got, want, out)
	}
}

// 50-AC-11: `plan` on a fresh fake install creates no S3 bucket, no object and no Git commit, and previews the
// storage footprint through a storage plan (never an apply).
func TestPlan_CreatesNothing_50AC11(t *testing.T) {
	i := newInst(t)
	head := i.git("rev-parse", "HEAD")

	code, out := i.run("", nil, i.args("plan"))
	requireExit(t, code, 0, out)

	for _, key := range []string{mainKeyID, stateKeyID} {
		if got := i.world.bucketsOf(key); len(got) != 0 {
			t.Errorf("plan created buckets %v in the account of %s; it must create none", got, key)
		}
	}
	if got := i.world.allBuckets(""); len(got) != 0 {
		t.Errorf("emulator bucket list after plan = %v, want empty", got)
	}
	if got := i.git("rev-parse", "HEAD"); got != head {
		t.Errorf("git HEAD moved from %s to %s: plan must not commit", head, got)
	}
	if st := i.git("status", "--porcelain"); st != "" {
		t.Errorf("plan changed the work tree (git status --porcelain):\n%s", st)
	}
	plans, applies, migrates := i.tofu.counts()
	if applies != 0 || migrates != 0 {
		t.Errorf("plan ran tofu apply %d times and migrate %d times, want 0 and 0", applies, migrates)
	}
	if plans != 1 {
		t.Errorf("plan ran the storage plan %d times, want 1 (throwaway local backend, plan 50 section 7.1)", plans)
	}
	if bundles, _ := filepath.Glob(filepath.Join(i.blackstart, "*", "bundles", "*")); len(bundles) != 0 {
		t.Errorf("plan wrote recovery bundles %v", bundles)
	}
	for _, n := range allBucketNames() {
		if !strings.Contains(out, n) {
			t.Errorf("the plan preview does not list the bucket %s:\n%s", n, out)
		}
	}
	if n := i.createCount(); n != 0 {
		t.Errorf("fake provider create counter = %d, want 0", n)
	}
}

// 50-AC-30: --non-interactive without --confirm-storage exits 5, prints the footprint, and no bucket exists.
func TestStorageConfirm_NonInteractiveWithoutFlagExits5_50AC30(t *testing.T) {
	i := newInst(t)
	code, out := i.run("", i.passphraseFile(), i.args("--non-interactive"))
	requireExit(t, code, 5, out)

	if got := i.world.allBuckets(""); len(got) != 0 {
		t.Errorf("buckets exist after the declined confirmation: %v", got)
	}
	if _, applies, _ := i.tofu.counts(); applies != 0 {
		t.Errorf("the storage module was applied %d times without a confirmation", applies)
	}
	if !strings.Contains(out, "usage-billed") {
		t.Errorf("the storage footprint statement (usage-billed; cost bounded by retention; ...) was not printed:\n%s", out)
	}
	for _, n := range allBucketNames() {
		if !strings.Contains(out, n) {
			t.Errorf("the storage footprint does not list %s:\n%s", n, out)
		}
	}
	if j, ok := i.journalIfAny(); ok {
		if _, has := j.confirmation("storage-footprint"); has {
			t.Errorf("the journal records a storage-footprint confirmation that was never given")
		}
	}
}

// 50-AC-30: a confirmation for another cluster does not confirm the footprint.
func TestStorageConfirm_MismatchedValueExits5_50AC30(t *testing.T) {
	i := newInst(t)
	code, out := i.run("", i.passphraseFile(), i.args("--non-interactive", "--confirm-storage", "other-cluster"))
	requireExit(t, code, 5, out)
	if got := i.world.allBuckets(""); len(got) != 0 {
		t.Errorf("buckets exist after a mismatched confirmation: %v", got)
	}
}

// 50-AC-30: with the confirmation (flag or SC_CONFIRM_STORAGE) the buckets are created, after a storage-footprint
// confirmation whose journal timestamp precedes the first CreateBucket (the injected clock ticks on every read).
func TestStorageConfirm_BucketsCreatedAfterConfirmation_50AC30(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"flag", []string{"--confirm-storage", clusterName}, nil},
		{"env", nil, map[string]string{"SC_CONFIRM_STORAGE": clusterName}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			i := newInst(t)
			args := i.args(append([]string{"--non-interactive"}, c.args...)...)
			code, out := i.run("", merge(i.passphraseFile(), stopAfter("plan.storage-apply"), c.env), args)
			requireExit(t, code, 0, out)

			var got []string
			got = append(got, i.world.bucketsOf(mainKeyID)...)
			got = append(got, i.world.bucketsOf(stateKeyID)...)
			if strings.Join(sorted(got), ",") != strings.Join(allBucketNames(), ",") {
				t.Errorf("buckets after the confirmed run = %v, want the ten of plan 14 section 9.1: %v", sorted(got), allBucketNames())
			}
			if _, applies, _ := i.tofu.counts(); applies != 1 {
				t.Errorf("the storage module was applied %d times, want 1", applies)
			}
			j := i.journal()
			conf, ok := j.confirmation("storage-footprint")
			if !ok {
				t.Fatalf("the journal has no storage-footprint confirmation: %+v", j.Confirmations)
			}
			if conf.Method != "flag" {
				t.Errorf("storage-footprint method %q, want flag", conf.Method)
			}
			if !digestRe.MatchString(conf.SubjectDigest) {
				t.Errorf("storage-footprint subject_digest %q is not a sha256 digest", conf.SubjectDigest)
			}
			first, ok := i.world.firstCreate()
			if !ok {
				t.Fatal("no CreateBucket was recorded")
			}
			if at := parseTime(t, conf.ConfirmedAt); !at.Before(first) {
				t.Errorf("storage-footprint confirmed_at %s does not precede the first CreateBucket at %s", at, first)
			}
			if s, ok := j.step("plan.storage-apply"); !ok || s.Status != "succeeded" {
				t.Errorf("step plan.storage-apply = %+v (found %v), want succeeded", s, ok)
			}
			// The done check of plan.storage-apply: every bucket exists and versioning is Enabled.
			for _, key := range []string{mainKeyID, stateKeyID} {
				acct, _ := i.world.open(s3client.Config{AccessKey: key})
				for _, n := range i.world.bucketsOf(key) {
					if on, err := acct.Versioning(context.Background(), n); err != nil || !on {
						t.Errorf("bucket %s versioning = %v, %v, want Enabled", n, on, err)
					}
				}
			}
		})
	}
}

// 50-AC-30: the typed confirmation `storage <cluster>` (interactive) also confirms the footprint.
func TestStorageConfirm_TypedConfirmation_50AC30(t *testing.T) {
	i := newInst(t)
	stdin := twice(goodPass) + "storage " + clusterName + "\n"
	code, out := i.run(stdin, stopAfter("plan.storage-apply"), i.args())
	requireExit(t, code, 0, out)
	conf, ok := i.journal().confirmation("storage-footprint")
	if !ok || conf.Method != "interactive" {
		t.Fatalf("storage-footprint confirmation = %+v (found %v), want method interactive", conf, ok)
	}
	if got := i.world.allBuckets(bucketPfx + "-"); len(got) != 10 {
		t.Errorf("buckets after the typed confirmation = %v, want 10", got)
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func requireNoStateObjects(t *testing.T, i *inst) {
	t.Helper()
	for _, b := range i.world.allBuckets(bucketPfx + "-") {
		for _, p := range []string{"storage/", "compute/"} {
			if objs := i.world.everyVersion(b, clusterName+"/"+p); len(objs) != 0 {
				t.Errorf("bucket %s holds remote state objects %v", b, objs)
			}
		}
	}
}

// 50-AC-32 / 06-AC-13: shared credential mode without S3_STATE_* fails closed at plan.storage-credentials with
// OBJSTORE_STATE_UNPROTECTED (exit 3), and no remote state object exists.
func TestStateProtection_SharedWithoutStateAccountExits3_50AC32_06AC13(t *testing.T) {
	i := newInst(t)
	i.dropStateAccount()
	code, out := i.run("", merge(i.passphraseFile(), stopAfter("plan.state-migrate")), i.args("--non-interactive", "--confirm-storage", clusterName))
	requireExit(t, code, 3, out)

	if !strings.Contains(out, "OBJSTORE_STATE_UNPROTECTED") {
		t.Errorf("the output does not name OBJSTORE_STATE_UNPROTECTED:\n%s", out)
	}
	requireNoStateObjects(t, i)
	if _, _, migrates := i.tofu.counts(); migrates != 0 {
		t.Errorf("the state was migrated %d times; it must stay local until the state is protected", migrates)
	}
	j := i.journal()
	s, ok := j.step("plan.storage-credentials")
	if !ok || s.Status != "failed" || s.Error == nil || s.Error.Code != "OBJSTORE_STATE_UNPROTECTED" {
		t.Errorf("step plan.storage-credentials = %+v (found %v), want failed with OBJSTORE_STATE_UNPROTECTED", s, ok)
	}
	if _, has := j.confirmation("accept-state-unprotected"); has {
		t.Errorf("accept-state-unprotected was recorded without S3_ALLOW_UNPROTECTED_STATE")
	}
}

// 50-AC-32 / 06-AC-13: with S3_STATE_* the state buckets B1 and B2 exist only in the second account, the
// in-cluster (main) key gets AccessDenied or NoSuchBucket on them, and the state account key is in no repository file.
func TestStateProtection_SecondAccountHoldsStateBuckets_50AC32_06AC13(t *testing.T) {
	i := newInst(t)
	code, out := i.run("", merge(i.passphraseFile(), stopAfter("plan.state-migrate")), i.args("--non-interactive", "--confirm-storage", clusterName))
	requireExit(t, code, 0, out)

	if got := i.world.bucketsOf(stateKeyID); strings.Join(got, ",") != strings.Join(stateBucketNames(), ",") {
		t.Errorf("state account buckets = %v, want exactly B1 and B2 %v", got, stateBucketNames())
	}
	if got := i.world.bucketsOf(mainKeyID); strings.Join(got, ",") != strings.Join(mainBucketNames(), ",") {
		t.Errorf("main account buckets = %v, want the other eight %v", got, mainBucketNames())
	}
	cluster, _ := i.world.open(s3client.Config{AccessKey: mainKeyID})
	for _, n := range stateBucketNames() {
		err := cluster.HeadBucket(context.Background(), n)
		var se *s3client.Error
		if !errors.As(err, &se) || (se.Code != "AccessDenied" && se.Code != "NoSuchBucket") {
			t.Errorf("the in-cluster key on %s: %v, want AccessDenied or NoSuchBucket", n, err)
		}
		if _, err := cluster.Bucket(n).Get(context.Background(), clusterName+"/storage/terraform.tfstate"); err == nil {
			t.Errorf("the in-cluster key read the state object in %s", n)
		}
	}
	state := bucketName("tofu-state")
	if objs := i.world.objects(state, clusterName+"/storage/"); len(objs) == 0 {
		t.Errorf("no state object under %s/storage/ in the state account after plan.state-migrate", state)
	}
	for _, b := range mainBucketNames() {
		if objs := i.world.everyVersion(b, clusterName+"/storage/"); len(objs) != 0 {
			t.Errorf("state objects %v in the main account bucket %s", objs, b)
		}
	}
	// The spec the storage module was applied with places exactly B1 and B2 in the second account.
	i.tofu.mu.Lock()
	specs := append([]install.StorageSpec(nil), i.tofu.specs...)
	i.tofu.mu.Unlock()
	var applied *install.StorageSpec
	for k := range specs {
		if len(specs[k].Buckets) > 0 {
			applied = &specs[k]
		}
	}
	if applied == nil {
		t.Fatal("the fake tofu was never given a storage spec")
	}
	if applied.State == nil || applied.State.AccessKey != stateKeyID || applied.Main.AccessKey != mainKeyID {
		t.Errorf("storage spec credentials main=%q state=%+v, want main %q and the state account key %q", applied.Main.AccessKey, applied.State, mainKeyID, stateKeyID)
	}
	for _, b := range applied.Buckets {
		want := b.Name == bucketName("tofu-state") || b.Name == bucketName("recovery")
		if b.StateAccount != want {
			t.Errorf("bucket %s StateAccount=%v, want %v", b.Name, b.StateAccount, want)
		}
	}
	if len(applied.Passphrase) < 16 {
		t.Errorf("the OpenTofu state encryption passphrase has %d characters, want at least 16 (plan 06 section 7.3)", len(applied.Passphrase))
	}
	// The state account key never reaches a repository file or a log.
	all := treeText(t, i.repo) + out
	for _, secret := range []string{stateKeyID, stateSecret} {
		if strings.Contains(all, secret) {
			t.Errorf("the state account credential %q appears in the repository, the run logs or the output", secret)
		}
	}
}

// 50-AC-32: with S3_ALLOW_UNPROTECTED_STATE=true the run proceeds with every bucket in the one account, the
// journal records accept-state-unprotected, and the run reports the warning.
func TestStateProtection_AllowUnprotectedProceedsAndIsRecorded_50AC32(t *testing.T) {
	i := newInst(t)
	i.dropStateAccount()
	i.setVars(map[string]string{"S3_ALLOW_UNPROTECTED_STATE": "true"})
	code, out := i.run("", merge(i.passphraseFile(), stopAfter("plan.state-migrate")), i.args("--non-interactive", "--confirm-storage", clusterName))
	requireExit(t, code, 0, out)

	if got := i.world.bucketsOf(mainKeyID); strings.Join(got, ",") != strings.Join(allBucketNames(), ",") {
		t.Errorf("main account buckets = %v, want all ten", got)
	}
	conf, ok := i.journal().confirmation("accept-state-unprotected")
	if !ok || conf.Method != "flag" || !digestRe.MatchString(conf.SubjectDigest) {
		t.Errorf("accept-state-unprotected confirmation = %+v (found %v), want method flag and a sha256 subject", conf, ok)
	}
	if !strings.Contains(out, "OBJSTORE_STATE_UNPROTECTED") {
		t.Errorf("the run does not report the unprotected-state warning (OBJSTORE_STATE_UNPROTECTED):\n%s", out)
	}
	i.tofu.mu.Lock()
	for _, s := range i.tofu.specs {
		for _, b := range s.Buckets {
			if b.StateAccount {
				t.Errorf("bucket %s was placed in a state account that was never configured", b.Name)
			}
		}
	}
	i.tofu.mu.Unlock()
	if objs := i.world.objects(bucketName("tofu-state"), clusterName+"/storage/"); len(objs) == 0 {
		t.Errorf("the state was not migrated to %s", bucketName("tofu-state"))
	}
}

// 06-AC-2: behind the If-None-Match-stripping proxy the run exits 3 with PF_S3_LOCK_UNSUPPORTED and the state
// prefix <cluster>/storage/ holds no objects.
func TestStateConformance_StrippedConditionalWritesExit3_06AC2(t *testing.T) {
	i := newInst(t)
	i.world.ignoreIfNoneMatch = true
	code, out := i.run("", merge(i.passphraseFile(), stopAfter("plan.state-migrate")), i.args("--non-interactive", "--confirm-storage", clusterName))
	requireExit(t, code, 3, out)

	if !strings.Contains(out, "PF_S3_LOCK_UNSUPPORTED") {
		t.Errorf("the output does not name PF_S3_LOCK_UNSUPPORTED:\n%s", out)
	}
	state := bucketName("tofu-state")
	if objs := i.world.everyVersion(state, clusterName+"/storage/"); len(objs) != 0 {
		t.Errorf("aws s3api list-objects-v2 --bucket %s --prefix %s/storage/ returns %v, want no objects", state, clusterName, objs)
	}
	requireNoStateObjects(t, i)
	if _, _, migrates := i.tofu.counts(); migrates != 0 {
		t.Errorf("the state was migrated %d times although the store cannot lock", migrates)
	}
	s, ok := i.journal().step("plan.state-conformance")
	if !ok || s.Status != "failed" || s.Error == nil || s.Error.Code != "PF_S3_LOCK_UNSUPPORTED" {
		t.Errorf("step plan.state-conformance = %+v (found %v), want failed with PF_S3_LOCK_UNSUPPORTED", s, ok)
	}
	if left := i.world.everyVersion(state, clusterName+"/conformance/"); len(left) != 0 {
		t.Errorf("conformance objects left behind: %v", left)
	}
}

func expectedStorageAddresses() []string {
	var out []string
	for _, n := range allBucketNames() {
		out = append(out, addrOf(n)...)
	}
	return sorted(out)
}

// 06-AC-5: after plan.state-migrate the local storage state does not exist, the remote state lists the same
// addresses as the pre-migration state, the pre-migration list is in the run log, and the journal records the
// remote object. A rerun changes nothing.
func TestStateMigrate_LocalStateGoneAndAddressesEqual_06AC5(t *testing.T) {
	i := newInst(t)
	args := i.args("--non-interactive", "--confirm-storage", clusterName)
	env := merge(i.passphraseFile(), stopAfter("plan.state-migrate"))
	code, out := i.run("", env, args)
	requireExit(t, code, 0, out)

	work := filepath.Join(i.repo, ".bootstrap", "work")
	if _, err := os.Stat(filepath.Join(work, "storage.local.tfstate")); !os.IsNotExist(err) {
		t.Errorf(".bootstrap/work/storage.local.tfstate still exists after plan.state-migrate (stat: %v)", err)
	}
	if left, _ := filepath.Glob(filepath.Join(work, "storage.local.tfstate*")); len(left) != 0 {
		t.Errorf("local state files left behind: %v", left)
	}
	remote := install.RemoteState{Bucket: bucketName("tofu-state"), Key: clusterName + "/storage/terraform.tfstate"}
	if _, ok := i.world.read(remote.Bucket, remote.Key); !ok {
		t.Fatalf("no remote state object at s3://%s/%s", remote.Bucket, remote.Key)
	}
	after, err := i.tofu.StateList(context.Background(), install.StorageSpec{}, &remote)
	if err != nil {
		t.Fatal(err)
	}
	want := expectedStorageAddresses()
	if strings.Join(sorted(after), "\n") != strings.Join(want, "\n") {
		t.Errorf("remote state list = %v, want the pre-migration list %v", after, want)
	}
	logs := ""
	runs, _ := filepath.Glob(filepath.Join(i.repo, ".bootstrap", "runs", "*", "log.jsonl"))
	for _, p := range runs {
		b, _ := os.ReadFile(p)
		logs += string(b)
	}
	for _, a := range want {
		if !strings.Contains(logs, a) {
			t.Errorf("the pre-migration address %s is not recorded in the run log", a)
		}
	}
	s, ok := i.journal().step("plan.state-migrate")
	if !ok || s.Status != "succeeded" {
		t.Fatalf("step plan.state-migrate = %+v (found %v), want succeeded", s, ok)
	}
	found := false
	for _, r := range s.OutputRefs {
		if r.Kind == "tofu-state" && strings.Contains(r.Ref, "s3://"+remote.Bucket+"/"+remote.Key) {
			found = true
		}
	}
	if !found {
		t.Errorf("plan.state-migrate output_refs %+v lack the tofu-state object s3://%s/%s", s.OutputRefs, remote.Bucket, remote.Key)
	}

	code, out = i.run("", env, args)
	requireExit(t, code, 0, out)
	if _, applies, migrates := i.tofu.counts(); applies != 1 || migrates != 1 {
		t.Errorf("a rerun applied %d and migrated %d times in total, want 1 and 1 (no repeated mutation)", applies, migrates)
	}
}

// 06-AC-5 (data safety): when the remote address set differs from the pre-migration set the step fails and the
// local state is kept.
func TestStateMigrate_AddressMismatchKeepsLocalState_06AC5(t *testing.T) {
	i := newInst(t)
	i.tofu.dropRemoteAddress = true
	code, out := i.run("", merge(i.passphraseFile(), stopAfter("plan.state-migrate")), i.args("--non-interactive", "--confirm-storage", clusterName))
	requireExit(t, code, 1, out)
	if _, err := os.Stat(filepath.Join(i.repo, ".bootstrap", "work", "storage.local.tfstate")); err != nil {
		t.Errorf("the local state was deleted although the remote list differs: %v", err)
	}
	if s, ok := i.journal().step("plan.state-migrate"); !ok || s.Status != "failed" {
		t.Errorf("step plan.state-migrate = %+v (found %v), want failed", s, ok)
	}
}

// 50-AC-10: three wrong re-entered passphrases at plan.bundle-confirm exit 8 with recovery_bundle.status
// uploaded, the bundle really in the recovery bucket, provision never started; the next run asks again.
func TestPlanPipeline_BundleConfirmThreeWrongExit8_50AC10(t *testing.T) {
	i := newInst(t)
	stdin := twice(goodPass) + "wrong one entry\nwrong two entry\nwrong three entry\n"
	code, out := i.run(stdin, nil, i.args("--confirm-storage", clusterName))
	requireExit(t, code, 8, out)

	j := i.journal()
	if j.RecoveryBundle.Status != "uploaded" {
		t.Fatalf("recovery_bundle.status = %q, want uploaded (not confirmed)", j.RecoveryBundle.Status)
	}
	if j.RecoveryBundle.Generation != 2 {
		t.Errorf("recovery_bundle.generation = %d, want 2: the bundle is re-sealed once in plan.storage-credentials (plan 50 section 14.3 step 5)", j.RecoveryBundle.Generation)
	}
	s, ok := j.step("plan.bundle-confirm")
	if !ok || s.Status != "failed" || s.Error == nil || s.Error.Code != "BUNDLE_PASSPHRASE_WRONG" {
		t.Errorf("step plan.bundle-confirm = %+v (found %v), want failed with BUNDLE_PASSPHRASE_WRONG", s, ok)
	}
	if j.Installation.State != "new" {
		t.Errorf("installation.state = %q, want new: plan did not complete", j.Installation.State)
	}
	if n := i.createCount(); n != 0 {
		t.Errorf("fake provider create counter = %d, want 0: provision must never start", n)
	}
	for _, p := range j.Pipelines {
		for _, ph := range p.Phases {
			if ph.Name == "provision" && ph.Status != "pending" {
				t.Errorf("phase provision is %s, want pending", ph.Status)
			}
		}
	}

	// The bundle is in the recovery bucket (the state account), byte-identical to the journal's digest, and opens.
	wantURI := "s3://" + bucketName("recovery") + "/" + clusterName + "/bundles/sc-recovery-" + clusterName + "-v2.tar.age"
	if j.RecoveryBundle.S3URI != wantURI {
		t.Fatalf("recovery_bundle.s3_uri = %q, want %q", j.RecoveryBundle.S3URI, wantURI)
	}
	data, found := i.world.read(bucketName("recovery"), clusterName+"/bundles/sc-recovery-"+clusterName+"-v2.tar.age")
	if !found {
		t.Fatalf("the bundle object is not in %s", bucketName("recovery"))
	}
	if _, found := i.world.read(bucketName("recovery"), clusterName+"/bundles/sc-recovery-"+clusterName+"-v1.tar.age"); !found {
		t.Errorf("generation 1 was not uploaded first (plan.bundle-upload runs before plan.storage-credentials)")
	}
	if got := sha256Hex(data); got != strings.TrimPrefix(j.RecoveryBundle.Sha256, "sha256:") {
		t.Errorf("S3 bundle sha256 %s differs from the journal's %s", got, j.RecoveryBundle.Sha256)
	}
	if j.RecoveryBundle.S3VersionID == "" {
		t.Errorf("recovery_bundle.s3_version_id is empty")
	}
	local, err := os.ReadFile(j.RecoveryBundle.LocalPath)
	wantLocal := filepath.Join(i.blackstart, clusterName, "bundles", "sc-recovery-"+clusterName+"-v2.tar.age")
	if err != nil || j.RecoveryBundle.LocalPath != wantLocal || string(local) != string(data) {
		t.Errorf("local bundle %q (err %v) is not the S3 copy at %s", j.RecoveryBundle.LocalPath, err, wantLocal)
	}
	opened, err := identity.Open(data, goodPass)
	if err != nil {
		t.Fatalf("the uploaded bundle does not open with the passphrase: %v", err)
	}
	holdsStateKey := false
	for _, f := range opened.Files {
		if strings.Contains(string(f.Data), stateSecret) {
			holdsStateKey = true
		}
	}
	if !holdsStateKey {
		t.Errorf("the re-sealed bundle (generation 2) does not hold the state account credential (S3_STATE_*)")
	}

	// The journal was pushed to S3 right after the bundle upload (BOOT-6) and shows the same bundle state.
	rj, found := i.world.read(bucketName("tofu-state"), clusterName+"/journal/journal.json")
	if !found {
		t.Fatalf("no remote journal at %s/journal/journal.json", clusterName)
	}
	if got := parseJournal(t, rj).RecoveryBundle.Status; got != "uploaded" {
		t.Errorf("remote journal recovery_bundle.status = %q, want uploaded", got)
	}

	// status --json shows the same through the command.
	code, sout := i.run("", nil, i.args("status", "--json"))
	requireExit(t, code, 0, sout)
	var st struct {
		RecoveryBundle struct {
			Status string `json:"status"`
		} `json:"recovery_bundle"`
		NextAction string `json:"next_action"`
	}
	// Any preflight rows the command prints come first; the JSON document starts at a line that begins with "{".
	doc := sout
	if loc := regexp.MustCompile(`(?m)^\{`).FindStringIndex(sout); loc != nil {
		doc = sout[loc[0]:]
	}
	if err := json.Unmarshal([]byte(doc), &st); err != nil {
		t.Fatalf("status --json is not JSON: %v\n%s", err, sout)
	}
	if st.RecoveryBundle.Status != "uploaded" || st.NextAction == "" {
		t.Errorf("status --json recovery_bundle.status=%q next_action=%q, want uploaded and a next action", st.RecoveryBundle.Status, st.NextAction)
	}

	// The install commit exists once, with the identity trailer.
	if c := i.installCommits(); len(c) != 1 {
		t.Fatalf("installation config commits = %v, want exactly one", c)
	}
	if body := i.git("log", "-1", "--format=%B", "--grep=installation config"); !strings.Contains(body, "SC-Install-Uid: "+j.InstallUID) {
		t.Errorf("the install commit lacks the trailer SC-Install-Uid: %s:\n%s", j.InstallUID, body)
	}

	// The rerun asks again; the right passphrase confirms; nothing is created twice.
	code, out = i.run(goodPass+"\n", stopAfter("plan.bundle-confirm"), i.args("--confirm-storage", clusterName))
	requireExit(t, code, 0, out)
	j2 := i.journal()
	if j2.RecoveryBundle.Status != "confirmed" {
		t.Errorf("after the right passphrase recovery_bundle.status = %q, want confirmed", j2.RecoveryBundle.Status)
	}
	if j2.InstallUID != j.InstallUID {
		t.Errorf("install_uid changed from %s to %s on rerun", j.InstallUID, j2.InstallUID)
	}
	if c := i.installCommits(); len(c) != 1 {
		t.Errorf("after the rerun installation config commits = %v, want exactly one", c)
	}
	if _, applies, migrates := i.tofu.counts(); applies != 1 || migrates != 1 {
		t.Errorf("the rerun applied %d and migrated %d times in total, want 1 and 1", applies, migrates)
	}
}
