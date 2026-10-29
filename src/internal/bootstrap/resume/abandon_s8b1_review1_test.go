package resume_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"strange.company/sc/internal/bootstrap/install"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

// S8b1 review 1 of F08 (plan 50 sections 7.1 and 14.3, plan 06 section 20.4 `state import --module storage`: the plan
// after the import shows imports only, then no-op): findings B1, B2, N-2, N-3 and N-4.

// flexTofu is importTofu with switches for the review-1 scenarios; with every switch off it behaves as importTofu.
//
//   - listErr makes every StateList against the remote fail with it (a transient or a 5xx style failure that has
//     nothing to do with the passphrase).
//   - stage makes ImportStorage build the imported state in the LOCAL state file of the spec (the way the plan asks
//     for it: build locally, check the plan, then migrate); only Migrate (the existing local-to-remote copy) puts it in
//     the remote. The remote is not touched by ImportStorage in this mode.
//   - failImports makes the first failImports calls of ImportStorage (stage mode) write a PARTIAL local state and fail.
//   - changesAfterImport is what Changes reports once a staged import completed.
type flexTofu struct {
	*importTofu
	fmu                sync.Mutex
	listErr            error
	stage              bool
	failImports        int
	changesAfterImport int
	attempts           int
	imported           bool
}

func newFlexTofu(f *fakeTofu) *flexTofu { return &flexTofu{importTofu: newImportTofu(f)} }

func (f *flexTofu) configure(fn func(*flexTofu)) {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	fn(f)
}

func (f *flexTofu) StateList(ctx context.Context, s install.StorageSpec, r *install.RemoteState) ([]string, error) {
	f.fmu.Lock()
	err := f.listErr
	f.fmu.Unlock()
	if r != nil && err != nil {
		return nil, err
	}
	return f.importTofu.StateList(ctx, s, r)
}

func (f *flexTofu) Changes(ctx context.Context, s install.StorageSpec, r *install.RemoteState) (int, error) {
	f.fmu.Lock()
	n, done := f.changesAfterImport, f.imported
	f.fmu.Unlock()
	if done && n > 0 {
		return n, nil
	}
	return f.importTofu.Changes(ctx, s, r)
}

func (f *flexTofu) ImportStorage(ctx context.Context, s install.StorageSpec, r install.RemoteState) error {
	f.fmu.Lock()
	f.attempts++
	n, stage, fail := f.attempts, f.stage, f.failImports
	f.fmu.Unlock()
	if !stage {
		return f.importTofu.ImportStorage(ctx, s, r)
	}
	var addrs []string
	for _, b := range s.Buckets {
		addrs = append(addrs, addrOf(b.Name)...)
	}
	sort.Strings(addrs)
	partial := n <= fail
	if partial {
		addrs = addrs[:1]
	}
	body, _ := json.Marshal(map[string]any{"version": 4, "resources": addrs})
	if err := os.MkdirAll(filepath.Dir(s.LocalState), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(s.LocalState, body, 0o600); err != nil {
		return err
	}
	if partial {
		return errors.New("fake tofu: import interrupted after the first resource")
	}
	f.mu.Lock()
	f.localTag = s.Passphrase
	f.imports = append(f.imports, importCall{s, r})
	f.mu.Unlock()
	f.fmu.Lock()
	f.imported = true
	f.fmu.Unlock()
	return nil
}

func (f *flexTofu) importCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.imports)
}

func (f *flexTofu) attemptCount() int {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	return f.attempts
}

func (f *flexTofu) migrateCount() int {
	_, _, m := f.fakeTofu.counts()
	return m
}

const newPassS8b1R1 = "second passphrase for the lab cluster"

// established is an installation stopped after plan.state-migrate succeeded (storage state under the identity's
// passphrase in the tofu-state bucket).
type established struct {
	*inst
	tf   *flexTofu
	with func(*install.Deps)
	key  string
	st   string
}

func newEstablished(t *testing.T) *established {
	t.Helper()
	i := newInst(t)
	tf := newFlexTofu(i.tofu)
	e := &established{inst: i, tf: tf, with: func(d *install.Deps) { d.Tofu = tf }, st: bucketName("tofu-state"), key: tofustate.StateKey(clusterName, "storage")}
	code, out := i.runWith(e.with, twice(goodPass)+goodPass+"\n", stopAfter("plan.state-migrate"), i.args("--confirm-storage", clusterName))
	requireExit(t, code, 0, out)
	if got := i.journal().stepStatus("plan.state-migrate"); got != "succeeded" {
		t.Fatalf("fixture: plan.state-migrate is %s", got)
	}
	return e
}

func (e *established) resume(stdin string) (int, string) {
	e.t.Helper()
	return e.runWith(e.with, stdin, stopAfter("plan.state-migrate"), e.args("--confirm-storage", clusterName))
}

func (e *established) requireNotReimported(t *testing.T, code int, out string, before []byte) {
	t.Helper()
	if code == 0 || !strings.Contains(out, "CHECK_FAILED") || !strings.Contains(out, "503 Service Unavailable") {
		t.Errorf("exit %d, want non-zero CHECK_FAILED naming the StateList error (503 Service Unavailable):\n%s", code, out)
	}
	if strings.Contains(out, "STATE_IMPORT_FAILED") {
		t.Errorf("a transient StateList failure was treated as a stale passphrase (STATE_IMPORT_FAILED):\n%s", out)
	}
	if n := e.tf.attemptCount(); n != 0 {
		t.Errorf("ImportStorage was called %d times on an established install whose StateList failed for another reason", n)
	}
	if now, _ := e.world.read(e.st, e.key); string(now) != string(before) {
		t.Errorf("the remote storage state changed")
	}
}

// B1a (plan 50 section 7.1, plan 06 section 20.4): plan.state-migrate already succeeded for this identity and was not
// abandoned. A remote StateList failure that is not about the passphrase (a 5xx) must not trigger a re-import that
// replaces the valid remote state: the check fails with CHECK_FAILED carrying the error.
func TestStateMigrate_EstablishedInstallWithFailingStateListIsNotReimported_50AC18_S8b1R1B1a(t *testing.T) {
	t.Parallel()
	e := newEstablished(t)
	before, _ := e.world.read(e.st, e.key)
	e.tf.configure(func(f *flexTofu) { f.listErr = errors.New("fake tofu: 503 Service Unavailable") })
	code, out := e.resume(goodPass + "\n")
	e.requireNotReimported(t, code, out, before)
}

// B1b: "ever succeeded": after that failure the step's latest status is failed, not succeeded. Resuming again with the
// same failing store still does not import, and once the store answers again the run completes without an import.
func TestStateMigrate_FailedLatestStatusAfterEarlierSuccessStillNotReimported_50AC18_S8b1R1B1b(t *testing.T) {
	t.Parallel()
	e := newEstablished(t)
	before, _ := e.world.read(e.st, e.key)
	e.tf.configure(func(f *flexTofu) { f.listErr = errors.New("fake tofu: 503 Service Unavailable") })
	if code, out := e.resume(goodPass + "\n"); code == 0 {
		t.Fatalf("fixture: the first failing resume exited 0:\n%s", out)
	}
	if got := e.journal().stepStatus("plan.state-migrate"); got == "succeeded" {
		t.Fatalf("fixture: plan.state-migrate is still succeeded after a failed check")
	}
	code, out := e.resume(goodPass + "\n")
	e.requireNotReimported(t, code, out, before)

	e.tf.configure(func(f *flexTofu) { f.listErr = nil })
	code, out = e.resume(goodPass + "\n")
	requireExit(t, code, 0, out)
	if n := e.tf.attemptCount(); n != 0 {
		t.Errorf("ImportStorage was called %d times after the store recovered", n)
	}
	if now, _ := e.world.read(e.st, e.key); string(now) != string(before) {
		t.Errorf("the remote storage state changed")
	}
}

// abandoned is an installation abandoned after plan.state-migrate, ready for the rerun with a new passphrase.
func newAbandoned(t *testing.T) (*established, *stale) {
	t.Helper()
	e := newEstablished(t)
	s := &stale{inst: e.inst, uid: e.journal().InstallUID, cdir: filepath.Join(e.blackstart, clusterName), journalKey: clusterName + "/journal/journal.json"}
	code, out := s.runWith(e.with, s.typed(), nil, s.args("abandon-identity"))
	requireExit(t, code, 0, out)
	return e, s
}

func (e *established) rerunNewIdentity(s *stale, stdin string) (int, string) {
	e.t.Helper()
	_, d8 := planDigest(e.t)
	return s.runWith(e.with, stdin, merge(stopAfter("plan"), map[string]string{"SC_CONFIRM_PLAN": d8}), s.args("--confirm-storage", clusterName))
}

// B2a (plan 06 section 20.4 step 3): after the import the plan against the storage module must show no changes. An
// incomplete import (Changes non-zero) fails with STATE_IMPORT_MISMATCH, does not replace the remote storage state and
// does not mark plan.state-migrate succeeded.
func TestStateMigrate_IncompleteImportFailsWithMismatchAndKeepsRemote_50AC18_S8b1R1B2a(t *testing.T) {
	t.Parallel()
	e, s := newAbandoned(t)
	before, _ := e.world.read(e.st, e.key)
	e.tf.configure(func(f *flexTofu) { f.stage, f.changesAfterImport = true, 3 })
	code, out := e.rerunNewIdentity(s, twice(newPassS8b1R1)+newPassS8b1R1+"\n")
	if code == 0 || !strings.Contains(out, "STATE_IMPORT_MISMATCH") {
		t.Errorf("exit %d, want non-zero STATE_IMPORT_MISMATCH for an import whose plan shows 3 changes:\n%s", code, out)
	}
	if now, _ := e.world.read(e.st, e.key); string(now) != string(before) {
		t.Errorf("the remote storage state was replaced by the incomplete import")
	}
	if got := e.journal().stepStatus("plan.state-migrate"); got == "succeeded" {
		t.Errorf("plan.state-migrate is marked succeeded after an incomplete import")
	}
}

// B2b: an ImportStorage that fails part-way leaves a partial local state. The next rerun must not migrate that partial
// state and mark plan.state-migrate done: it re-runs the import, and only the complete state reaches the remote.
func TestStateMigrate_PartialImportIsNeverMigratedAndTheRerunReimports_50AC18_S8b1R1B2b(t *testing.T) {
	t.Parallel()
	e, s := newAbandoned(t)
	baseline := e.tf.migrateCount()
	e.tf.configure(func(f *flexTofu) { f.stage, f.failImports = true, 1 })
	code, out := e.rerunNewIdentity(s, twice(newPassS8b1R1)+newPassS8b1R1+"\n")
	if code == 0 {
		t.Fatalf("fixture: the run with a failing import exited 0:\n%s", out)
	}
	if m := e.tf.migrateCount(); m != baseline {
		t.Errorf("a partial import was migrated (%d Migrate calls after the failing import, want %d)", m-baseline, 0)
	}
	code, out = e.rerunNewIdentity(s, newPassS8b1R1+"\n")
	if code != 0 {
		t.Errorf("the rerun after the interrupted import: exit %d, want 0:\n%s", code, out)
	}
	if n := e.tf.attemptCount(); n != 2 {
		t.Errorf("ImportStorage was attempted %d times, want 2 (the rerun repeats the import instead of migrating the partial state)", n)
	}
	e.tf.mu.Lock()
	imports := append([]importCall(nil), e.tf.imports...)
	e.tf.mu.Unlock()
	if len(imports) == 1 {
		got, err := e.tf.StateList(context.Background(), imports[0].Spec, &imports[0].Remote)
		if err != nil || len(got) != 2*len(bucketPurposes) {
			t.Errorf("the remote storage state lists %d addresses (err %v), want the complete %d", len(got), err, 2*len(bucketPurposes))
		}
	}
}

// N-2 (plan 50 sections 7.1 and 14.3): the identity's first generation is derived from the journal, not from host-local
// files. After abandon-identity the new identity's bundle is v2; it is created and uploaded on one machine and
// resumed on another that has the bundle files but neither <cdir>/first-generation nor abandoned/. There the
// comparison "has this identity rotated" must still know that v2 is the first bundle, so plan.storage-credentials
// re-seals the bundle with the credentials (generation 3) instead of declaring itself done.
func TestStorageCredentials_NewMachineKnowsTheIdentitysFirstGenerationFromTheJournal_50AC18_S8b1R1N2(t *testing.T) {
	t.Parallel()
	s := newStale(t)
	code, out := s.abandon(nil, s.typed())
	requireExit(t, code, 0, out)
	code, out = s.run(twice(newPassS8b1R1)+newPassS8b1R1+"\n", stopAfter("plan.bundle-upload"), s.args("--confirm-storage", clusterName))
	requireExit(t, code, 0, out)
	if g := s.journal().RecoveryBundle.Generation; g != 2 {
		t.Fatalf("fixture: the new identity's bundle is generation %d, want 2", g)
	}
	// the other machine: it holds the current bundle files but neither the first-generation marker of this host nor
	// the abandoned/ folder
	for _, p := range []string{filepath.Join(s.cdir, "first-generation"), filepath.Join(s.cdir, "abandoned")} {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
	}
	code, out = s.run(newPassS8b1R1+"\n", stopAfter("plan.storage-credentials"), s.args("--confirm-storage", clusterName))
	requireExit(t, code, 0, out)
	j := s.journal()
	if g := j.RecoveryBundle.Generation; g < 3 {
		t.Errorf("after plan.storage-credentials the bundle is still generation %d: the step took v2 for a rotated bundle and skipped the re-seal", g)
	}
}

// listFailAccount is an Account whose recovery bucket listing fails with err.
type listFailAccount struct {
	install.Account
	err error
}

type listFailStore struct {
	tofustate.Store
	err error
}

func (s listFailStore) List(context.Context, string) ([]string, error) { return nil, s.err }

func (a listFailAccount) Bucket(name string) tofustate.Store {
	st := a.Account.Bucket(name)
	if name == bucketName("recovery") {
		return listFailStore{Store: st, err: a.err}
	}
	return st
}

// N-3 (plan 50 sections 7.1 and 14.3): a listing of the recovery bucket that fails with something other than
// NoSuchBucket (403, 5xx) means the new identity cannot know which versions exist (they are immutable): the identity
// step fails with exit 7 (the code of an unreachable object store, as in the abandon refusals) instead of
// silently starting at generation 1. Nothing is added to the recovery bucket.
func TestInstallIdentity_RecoveryListingFailureAfterAbandonFailsWithExit7_50AC18_S8b1R1N3(t *testing.T) {
	t.Parallel()
	errs := map[string]error{
		"403": &s3client.Error{Status: 403, Code: "AccessDenied"},
		"503": &s3client.Error{Status: 503, Code: "ServiceUnavailable"},
	}
	for name, lerr := range errs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newStale(t)
			code, out := s.abandon(nil, s.typed())
			requireExit(t, code, 0, out)
			before := s.world.objects(bucketName("recovery"), "")
			mod := func(d *install.Deps) {
				open := d.NewS3
				d.NewS3 = func(c s3client.Config) (install.Account, error) {
					a, err := open(c)
					if err != nil {
						return nil, err
					}
					return listFailAccount{Account: a, err: lerr}, nil
				}
			}
			_, d8 := planDigest(t)
			code, out = s.runWith(mod, twice(newPassS8b1R1)+newPassS8b1R1+"\n", merge(stopAfter("plan"), map[string]string{"SC_CONFIRM_PLAN": d8}), s.args("--confirm-storage", clusterName))
			if code != 7 {
				t.Errorf("exit %d, want 7 (the recovery bucket cannot be listed):\n%s", code, out)
			}
			if after := s.world.objects(bucketName("recovery"), ""); strings.Join(after, ",") != strings.Join(before, ",") {
				t.Errorf("the recovery bucket changed: before %v, after %v", before, after)
			}
		})
	}
}

// N-4: the orphan-move message names the tofu-state bucket and the state endpoint also when the store is attached (the
// bucket exists and holds no journal).
func TestAbandonIdentity_OrphanMoveMessageNamesEndpointAndBucketWhenStoreIsAttached_50AC18_S8b1R1N4(t *testing.T) {
	t.Parallel()
	s := leftovers(t)
	if err := s.world.createBucket(stateKeyID, bucketName("tofu-state")); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	code, out := s.abandon(nil, "")
	requireExit(t, code, 0, out)
	if !strings.Contains(out, bucketName("tofu-state")) {
		t.Errorf("the message does not name the bucket %s:\n%s", bucketName("tofu-state"), out)
	}
	if !strings.Contains(out, "https://s3.state.example.net") {
		t.Errorf("the message does not name the state endpoint:\n%s", out)
	}
}
