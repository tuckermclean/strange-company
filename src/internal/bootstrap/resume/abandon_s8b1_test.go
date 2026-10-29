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
	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

// S8b1 of F08 (plan 50 sections 7.1, 7.2, 14.3 and 16, BOOT-6, 50-AC-18; plan 06 `state import --module storage`):
// abandon completeness. Findings of S8a review 1 (M1, N2) and review 4 (R4-1, R4-2).

// importTofu is the fake Tofu seam extended by the import the plan asks for. Its contract, which the implementation
// must offer (as an optional interface of install.Tofu or as a method of it), is
//
//	ImportStorage(ctx context.Context, s install.StorageSpec, remote install.RemoteState) error
//
// "Re-import the existing buckets of s.Buckets into fresh storage state at remote, encrypted under s.Passphrase
// (generation s.Generation)". The fake tags every state it writes with the passphrase that encrypted it, and a
// remote StateList is readable only with that passphrase.
type importTofu struct {
	*fakeTofu
	mu        sync.Mutex
	localTag  string
	remoteTag map[string]string
	imports   []importCall
	applied   []string
}

type importCall struct {
	Spec   install.StorageSpec
	Remote install.RemoteState
}

func newImportTofu(f *fakeTofu) *importTofu {
	return &importTofu{fakeTofu: f, remoteTag: map[string]string{}}
}

func (f *importTofu) ApplyStorage(ctx context.Context, s install.StorageSpec) error {
	f.mu.Lock()
	f.localTag = s.Passphrase
	f.applied = nil
	for _, b := range s.Buckets {
		f.applied = append(f.applied, b.Name)
	}
	f.mu.Unlock()
	return f.fakeTofu.ApplyStorage(ctx, s)
}

func (f *importTofu) Migrate(ctx context.Context, s install.StorageSpec, r install.RemoteState) error {
	if err := f.fakeTofu.Migrate(ctx, s, r); err != nil {
		return err
	}
	f.mu.Lock()
	f.remoteTag[r.Bucket+"/"+r.Key] = f.localTag
	f.mu.Unlock()
	return nil
}

func (f *importTofu) StateList(ctx context.Context, s install.StorageSpec, r *install.RemoteState) ([]string, error) {
	if r != nil {
		f.mu.Lock()
		tag := f.remoteTag[r.Bucket+"/"+r.Key]
		f.mu.Unlock()
		if tag != "" && s.Passphrase != tag && s.Previous != tag {
			return nil, errors.New("fake tofu: the state cannot be decrypted with the passphrase given")
		}
	}
	return f.fakeTofu.StateList(ctx, s, r)
}

func (f *importTofu) ImportStorage(_ context.Context, s install.StorageSpec, r install.RemoteState) error {
	var addrs []string
	for _, b := range s.Buckets {
		addrs = append(addrs, addrOf(b.Name)...)
	}
	sort.Strings(addrs)
	body, _ := json.Marshal(map[string]any{"version": 4, "resources": addrs})
	if err := f.w.write(r.S3.AccessKey, r.Bucket, r.Key, body); err != nil {
		return err
	}
	f.mu.Lock()
	f.remoteTag[r.Bucket+"/"+r.Key] = s.Passphrase
	f.imports = append(f.imports, importCall{s, r})
	f.mu.Unlock()
	return nil
}

// M1 (plan 50 section 7.1, plan 06 state import --module storage): the identity is abandoned after the storage state
// was migrated to the tofu-state bucket under the abandoned identity's passphrase. The rerun with a new passphrase
// re-imports the existing buckets into fresh storage state encrypted under the new identity's passphrase, so the
// storage state is readable with the new passphrase and not only with the abandoned one.
func TestAbandonIdentity_RerunReimportsBucketsIntoStorageStateOfTheNewIdentity_50AC18_S8b1M1(t *testing.T) {
	t.Parallel()
	i := newInst(t)
	tf := newImportTofu(i.tofu)
	with := func(d *install.Deps) { d.Tofu = tf }
	code, out := i.runWith(with, twice(goodPass)+goodPass+"\n", stopAfter("plan.state-migrate"), i.args("--confirm-storage", clusterName))
	requireExit(t, code, 0, out)
	st := bucketName("tofu-state")
	key := tofustate.StateKey(clusterName, "storage")
	oldTag := tf.remoteTag[st+"/"+key]
	if oldTag == "" {
		t.Fatal("fixture: the storage state was not migrated to the tofu-state bucket")
	}
	s := &stale{inst: i, uid: i.journal().InstallUID, cdir: filepath.Join(i.blackstart, clusterName), journalKey: clusterName + "/journal/journal.json"}
	code, out = s.runWith(with, s.typed(), nil, s.args("abandon-identity"))
	requireExit(t, code, 0, out)

	const newPass = "second passphrase for the lab cluster"
	_, d8 := planDigest(t)
	code, out = s.runWith(with, twice(newPass)+newPass+"\n", merge(stopAfter("plan"), map[string]string{"SC_CONFIRM_PLAN": d8}), s.args("--confirm-storage", clusterName))
	requireExit(t, code, 0, out)

	tf.mu.Lock()
	imports := append([]importCall(nil), tf.imports...)
	tag := tf.remoteTag[st+"/"+key]
	applied := append([]string(nil), tf.applied...)
	tf.mu.Unlock()
	if len(imports) != 1 {
		t.Fatalf("the rerun after abandon-identity imported the buckets %d times, want once (the storage state stays encrypted under the abandoned passphrase)", len(imports))
	}
	imp := imports[0]
	if imp.Spec.Passphrase == "" || imp.Spec.Passphrase == oldTag {
		t.Errorf("the import used the abandoned identity's passphrase (or none)")
	}
	if imp.Remote.Bucket != st || imp.Remote.Key != key {
		t.Errorf("the import targets %s/%s, want %s/%s", imp.Remote.Bucket, imp.Remote.Key, st, key)
	}
	var got []string
	for _, b := range imp.Spec.Buckets {
		got = append(got, b.Name)
	}
	sort.Strings(got)
	sort.Strings(applied)
	if strings.Join(got, ",") != strings.Join(applied, ",") || len(got) != len(bucketPurposes) {
		t.Errorf("the import covers buckets %v, want all of %v", got, applied)
	}
	if tag != imp.Spec.Passphrase {
		t.Errorf("the remote storage state is encrypted under the abandoned passphrase, not the new identity's")
	}
	if _, err := tf.StateList(context.Background(), imp.Spec, &imp.Remote); err != nil {
		t.Errorf("the storage state is not readable with the new passphrase: %v", err)
	}
	old := imp.Spec
	old.Passphrase = oldTag
	if _, err := tf.StateList(context.Background(), old, &imp.Remote); err == nil {
		t.Errorf("the storage state is still readable with the abandoned passphrase")
	}
}

// N2 (plan 50 sections 7.1 and 14.3): a new machine after abandon-identity has no <blackstart>/<cluster>/abandoned/
// folder, but the recovery bucket holds the abandoned identity's bundle v1 (Object Lock, immutable). The new
// identity's first generation comes from the recovery bucket listing, so the run reaches planned instead of exiting 8
// BUNDLE_VERSION_EXISTS.
func TestAbandonIdentity_NewMachineTakesFirstGenerationFromRecoveryBucket_50AC18_S8b1N2(t *testing.T) {
	t.Parallel()
	s := newStale(t)
	code, out := s.abandon(nil, s.typed())
	requireExit(t, code, 0, out)
	if err := os.RemoveAll(filepath.Join(s.cdir, "abandoned")); err != nil {
		t.Fatal(err)
	}
	v1 := clusterName + "/bundles/sc-recovery-" + clusterName + "-v1.tar.age"
	if got, ok := s.world.read(bucketName("recovery"), v1); !ok || string(got) != string(s.bundle) {
		t.Fatal("fixture: the recovery bucket does not hold the abandoned bundle v1")
	}

	s.requireNewIdentityPlanned(t)
	if g := s.journal().RecoveryBundle.Generation; g < 2 {
		t.Errorf("the new identity's bundle is generation %d, want at least 2 (v1 is the abandoned identity's)", g)
	}
	if got, _ := s.world.read(bucketName("recovery"), v1); string(got) != string(s.bundle) {
		t.Errorf("the abandoned identity's bundle v1 in the recovery bucket changed")
	}
}

// orphanedWith is a Deps.NewS3 whose account answers the tofu-state bucket's HeadBucket with head and every
// body-bearing request on it with body (nil: the real answer of the world).
func orphanedWith(head, body error) func(*install.Deps) {
	return func(d *install.Deps) {
		open := d.NewS3
		d.NewS3 = func(c s3client.Config) (install.Account, error) {
			a, err := open(c)
			if err != nil {
				return nil, err
			}
			return headAccount{Account: a, head: head, body: body}, nil
		}
	}
}

type headAccount struct {
	install.Account
	head, body error
}

func (a headAccount) HeadBucket(ctx context.Context, name string) error {
	if name == bucketName("tofu-state") && a.head != nil {
		return a.head
	}
	return a.Account.HeadBucket(ctx, name)
}

func (a headAccount) Versioning(ctx context.Context, name string) (bool, error) {
	if name == bucketName("tofu-state") && a.body != nil {
		return false, a.body
	}
	return a.Account.Versioning(ctx, name)
}

func (a headAccount) ListBuckets(ctx context.Context) ([]string, error) {
	if a.body != nil {
		return nil, a.body
	}
	return a.Account.ListBuckets(ctx)
}

func (a headAccount) Bucket(name string) tofustate.Store {
	st := a.Account.Bucket(name)
	if name == bucketName("tofu-state") && a.body != nil {
		return bodyStore{Store: st, err: a.body}
	}
	return st
}

type bodyStore struct {
	tofustate.Store
	err error
}

func (s bodyStore) Get(context.Context, string) (store.Object, error) { return store.Object{}, s.err }
func (s bodyStore) List(context.Context, string) ([]string, error)    { return nil, s.err }
func (s bodyStore) Head(context.Context, string) (s3client.ObjectInfo, error) {
	return s3client.ObjectInfo{}, s.err
}
func (s bodyStore) ListVersions(context.Context, string) ([]s3client.ObjectInfo, error) {
	return nil, s.err
}

// leftovers is an installation stopped after plan.install-identity whose journal was lost: no local journal, the
// bundle and recovery-identity.txt left behind, and (before plan.storage-apply) no tofu-state bucket in the store.
func leftovers(t *testing.T) *stale {
	t.Helper()
	i := newInst(t)
	code, out := i.run(twice(goodPass), stopAfter("plan.install-identity"), i.args("--confirm-storage", clusterName))
	requireExit(t, code, 0, out)
	s := &stale{inst: i, uid: i.journal().InstallUID, cdir: filepath.Join(i.blackstart, clusterName), journalKey: clusterName + "/journal/journal.json"}
	if err := os.Remove(filepath.Join(i.repo, ".bootstrap", "journal.json")); err != nil {
		t.Fatal(err)
	}
	if len(s.localBundles()) == 0 {
		t.Fatal("fixture: no leftover bundle files")
	}
	return s
}

func (s *stale) requireNothingMoved(t *testing.T, before string) {
	t.Helper()
	if after := s.localFileSet(); after != before {
		t.Errorf("the refused abandon-identity changed the local files:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if m, _ := filepath.Glob(filepath.Join(s.cdir, "abandoned", "*")); len(m) != 0 {
		t.Errorf("files were moved to %v", m)
	}
}

// R4-1: the store can be opened but HeadBucket on the tofu-state bucket fails with something other than "no such
// bucket" (unavailable, 403). The remote journal cannot be ruled out: exit 7, nothing moved.
func TestAbandonIdentity_HeadBucketUnavailableOrDeniedRefusesAndMovesNothing_50AC18_S8b1R41(t *testing.T) {
	t.Parallel()
	cases := map[string]error{
		"unavailable": &store.Error{Kind: store.Unavailable, Msg: "the store is unavailable"},
		"403":         &s3client.Error{Status: 403, Code: "AccessDenied"},
	}
	for name, herr := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := leftovers(t)
			before := s.localFileSet()
			code, out := s.abandon(orphanedWith(herr, nil), "")
			requireUnreachableRefusal(t, code, out)
			s.requireNothingMoved(t, before)
		})
	}
}

// R4-2a: a HEAD 404 alone does not rule a remote journal out (a wrong endpoint, a proxy 404, a wildcard virtual
// host). Only a body-bearing request that answers 404 with the code NoSuchBucket confirms an absent bucket; any other
// 404 body keeps the answer "unknown": exit 7, nothing moved.
func TestAbandonIdentity_HeadBucket404WithoutNoSuchBucketBodyRefusesAndMovesNothing_50AC18_S8b1R42a(t *testing.T) {
	t.Parallel()
	head := &s3client.Error{Status: 404, Kind: store.NotFound}
	for _, code := range []string{"", "ProxyNotFound", "NoSuchKey"} {
		t.Run("code "+code, func(t *testing.T) {
			t.Parallel()
			s := leftovers(t)
			before := s.localFileSet()
			body := &s3client.Error{Status: 404, Code: code, Kind: store.NotFound}
			c, out := s.abandon(orphanedWith(head, body), "")
			requireUnreachableRefusal(t, c, out)
			s.requireNothingMoved(t, before)
		})
	}
}

// R4-2b: an S3 endpoint with a URL path is dropped by the S3 client (it keeps scheme and host), so requests would go
// to another place than the operator configured. Config validation rejects it with CONFIG_INVALID, exit 2 (the code
// and exit of every other malformed variable).
func TestConfig_S3EndpointWithAPathIsRejected_50AC18_S8b1R42b(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"S3_ENDPOINT", "S3_STATE_ENDPOINT"} {
		t.Run(v, func(t *testing.T) {
			t.Parallel()
			i := newInst(t)
			i.vars[v] += "/storage/v1/s3"
			i.writeEnv()
			code, out := i.run(twice(goodPass), stopAfter("plan.install-identity"), i.args("--confirm-storage", clusterName))
			if code != 2 || !strings.Contains(out, "CONFIG_INVALID") || !strings.Contains(out, v) {
				t.Errorf("exit %d, want 2 with CONFIG_INVALID naming %s:\n%s", code, v, out)
			}
		})
	}
}

// R4-2c: the success message of the orphan move names the store it consulted: the endpoint of the state account and
// the tofu-state bucket confirmed absent there, so an operator who pointed at the wrong store sees it.
func TestAbandonIdentity_OrphanMoveMessageNamesEndpointAndBucket_50AC18_S8b1R42c(t *testing.T) {
	t.Parallel()
	s := leftovers(t)
	code, out := s.abandon(nil, "")
	requireExit(t, code, 0, out)
	if !strings.Contains(out, bucketName("tofu-state")) {
		t.Errorf("the message does not name the bucket %s:\n%s", bucketName("tofu-state"), out)
	}
	if !strings.Contains(out, "https://s3.state.example.net") {
		t.Errorf("the message does not name the state endpoint https://s3.state.example.net:\n%s", out)
	}
}
