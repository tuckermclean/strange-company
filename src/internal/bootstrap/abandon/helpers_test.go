package abandon_test

// Helpers copied unchanged from the resume package's resume_test.go, abandon_review1_test.go and
// abandon_review2_test.go (the two packages were split to fit the go test timeout; plan 52).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/install"
	"strange.company/sc/internal/provider"
	"strange.company/sc/internal/provider/fake"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

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

// planDigest is the digest of the fake provider's plan for the fixture installation (plan 03 section 14.7: D8 is its
// first 8 hex characters).
func planDigest(t testing.TB) (full, d8 string) {
	t.Helper()
	p, err := fake.New(map[string]string{"SC_TEST_MODE": "1"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := p.Plan(context.Background(), provider.Desired{Cluster: clusterName, NodeCount: 1})
	if err != nil || plan == nil || !strings.HasPrefix(plan.Digest, "sha256:") {
		t.Fatalf("fake provider plan = %+v, %v", plan, err)
	}
	return plan.Digest, strings.TrimPrefix(plan.Digest, "sha256:")[:8]
}

// unattendedEnv is the runtime-only environment of plan 50 section 9.2: every confirmation and the passphrase file.
func (i *inst) unattendedEnv(extra ...map[string]string) map[string]string {
	i.t.Helper()
	_, d8 := planDigest(i.t)
	env := merge(i.passphraseFile(), map[string]string{
		"SC_CONFIRM_STORAGE": clusterName, "SC_CONFIRM_PLAN": d8, "SC_CONFIRM_BUDGET": budgetMonthly,
		"SC_CONFIRM_BACKSTOPS": i.backstopFile(),
	})
	return merge(append([]map[string]string{env}, extra...)...)
}

// runWith is run with a change to the injected collaborators.
func (i *inst) runWith(mod func(*install.Deps), stdin string, extraEnv map[string]string, args []string) (int, string) {
	i.t.Helper()
	env := merge(i.env, extraEnv)
	var out, errb bytes.Buffer
	d := i.deps(stdin)
	if mod != nil {
		mod(&d)
	}
	code := install.Run(args, env, &out, &errb, d)
	return code, out.String() + errb.String()
}

func (j journal) stepStatus(name string) string {
	s, ok := j.step(name)
	if !ok {
		return "(absent)"
	}
	return s.Status
}

func sha256Of(b []byte) string { s := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(s[:]) }

func uid8(uid string) string { return uid[:8] }

func fileWithContentUnder(t testing.TB, dir string, want []byte) bool {
	t.Helper()
	found := false
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found {
			return nil
		}
		if b, rerr := os.ReadFile(p); rerr == nil && bytes.Equal(b, want) {
			found = true
		}
		return nil
	})
	return found
}

// refuses with the exit codes of section 7.2.

// drop removes the current version of an object the way a delete does (a delete marker), bypassing access control.
func (w *s3World) drop(bucket, key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if b := w.buckets[bucket]; b != nil && b.cur(key) != nil {
		w.ctr++
		b.objs[key] = append(b.objs[key], &wVersion{id: "d" + string(rune('a'+w.ctr%26)), marker: true, mod: w.clk.Now()})
	}
}

// failAccount is an Account whose buckets fail PutIfAbsent for the keys putIfAbsent rejects.
type failStore struct {
	tofustate.Store
	putIfAbsent func(key string) error
}

func (s failStore) PutIfAbsent(ctx context.Context, key string, data []byte) (string, error) {
	if s.putIfAbsent != nil {
		if err := s.putIfAbsent(key); err != nil {
			return "", err
		}
	}
	return s.Store.PutIfAbsent(ctx, key, data)
}

type failAccount struct {
	install.Account
	putIfAbsent func(key string) error
}

func (a failAccount) Bucket(name string) tofustate.Store {
	return failStore{Store: a.Account.Bucket(name), putIfAbsent: a.putIfAbsent}
}

// unreachableS3 is Deps.NewS3 of a host that cannot reach the object store.
func unreachableS3(s3client.Config) (install.Account, error) {
	return nil, errors.New("dial tcp: connect: connection refused")
}

// stale is an installation stopped after plan.bundle-upload (storage created, bundle sealed and uploaded, not
// confirmed): one seal, no opens, so the fixture stays cheap.
type stale struct {
	*inst
	uid        string
	jraw       []byte
	bundle     []byte
	cdir       string
	journalKey string
}

func newStale(t *testing.T) *stale {
	t.Helper()
	i := newInst(t)
	code, out := i.run(twice(goodPass), stopAfter("plan.bundle-upload"), i.args("--confirm-storage", clusterName))
	requireExit(t, code, 0, out)
	s := &stale{inst: i, uid: i.journal().InstallUID, cdir: filepath.Join(i.blackstart, clusterName), journalKey: clusterName + "/journal/journal.json"}
	s.jraw, _ = os.ReadFile(filepath.Join(i.repo, ".bootstrap", "journal.json"))
	s.bundle, _ = os.ReadFile(i.journal().RecoveryBundle.LocalPath)
	if len(s.jraw) == 0 || len(s.bundle) == 0 {
		t.Fatal("fixture: the stale installation has no local journal or bundle")
	}
	return s
}

func (s *stale) abandon(mod func(*install.Deps), stdin string) (int, string) {
	s.t.Helper()
	return s.runWith(mod, stdin, nil, s.args("abandon-identity"))
}

func (s *stale) typed() string { return "abandon " + clusterName + " " + uid8(s.uid) + "\n" }

func (s *stale) localBundles() []string {
	b, _ := filepath.Glob(filepath.Join(s.cdir, "bundles", "*"))
	return b
}

func (s *stale) localFileSet() string {
	var names []string
	for _, root := range []string{s.cdir, filepath.Join(s.repo, ".bootstrap")} {
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && !strings.Contains(p, "/runs/") && !strings.Contains(p, "/abandoned/") {
				b, _ := os.ReadFile(p)
				names = append(names, p+" "+sha256Of(b))
			}
			return nil
		})
	}
	return strings.Join(names, "\n")
}

// requireMovedAside checks the end state of a completed abandon: the bundle files, recovery-identity.txt and the
// journal are gone from their working places and the bundle is kept below <cdir>/abandoned.
func (s *stale) requireMovedAside(t *testing.T) {
	t.Helper()
	if left := s.localBundles(); len(left) != 0 {
		t.Errorf("bundle files remain in %s: %v", filepath.Join(s.cdir, "bundles"), left)
	}
	if _, err := os.Stat(filepath.Join(s.cdir, "recovery-identity.txt")); err == nil {
		t.Errorf("recovery-identity.txt remains in %s", s.cdir)
	}
	if !fileWithContentUnder(t, filepath.Join(s.cdir, "abandoned"), s.bundle) {
		t.Errorf("the abandoned bundle is not kept below %s (move it aside, never delete it)", filepath.Join(s.cdir, "abandoned"))
	}
	if j, ok := s.journalIfAny(); ok && j.InstallUID == s.uid {
		t.Errorf("the local journal still belongs to the abandoned identity")
	}
}

// requireNewIdentityPlanned reruns with a new passphrase and expects a new identity at planned.
func (s *stale) requireNewIdentityPlanned(t *testing.T) {
	t.Helper()
	const newPass = "second passphrase for the lab cluster"
	_, d8 := planDigest(t)
	code, out := s.run(twice(newPass)+newPass+"\n", merge(stopAfter("plan"), map[string]string{"SC_CONFIRM_PLAN": d8}), s.args("--confirm-storage", clusterName))
	if code != 0 {
		t.Fatalf("the run after abandon-identity: exit %d, want 0 (planned with a new identity):\n%s", code, out)
	}
	j := s.journal()
	if j.Installation.State != "planned" || j.InstallUID == "" || j.InstallUID == s.uid {
		t.Errorf("after the rerun: state %q install_uid %q (old %s), want planned and a new uid", j.Installation.State, j.InstallUID, s.uid)
	}
}

func requireUnreachableRefusal(t *testing.T, code int, out string) {
	t.Helper()
	if code != 7 || !strings.Contains(out, "ABANDON_REMOTE_UNREACHABLE") {
		t.Errorf("exit %d, want 7 with ABANDON_REMOTE_UNREACHABLE:\n%s", code, out)
	}
	if strings.Contains(out, "archived as") || strings.Contains(out, "generates a new identity") {
		t.Errorf("the output claims an abandon or a new identity although the remote was unreachable:\n%s", out)
	}
}
