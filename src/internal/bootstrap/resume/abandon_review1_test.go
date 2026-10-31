package resume_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/install"
	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

// S8a review-1 of F08 (plan 50 sections 7.1, 7.2 and 14.3, BOOT-6, 50-AC-18): abandon-identity is crash-safe and
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

// B2 (BOOT-6, NB1): the process died after the local journal was removed but before the bundles were moved. There is no
// journal any more and the bundles are still in <blackstart>/<cluster>/bundles. abandon-identity moves them aside
// instead of exiting ABANDON_NO_IDENTITY (no identity is named, so no confirmation can be typed: stdin is empty), and the
// next run with a new passphrase reaches planned. This is the state the present file order leaves after
// a crash (journal removed before the bundles); the fixed order removes the journal last, and either way the rerun
// must complete.
func TestAbandonIdentity_JournalLessWithLeftoverBundlesMovesThemAside_50AC18_S8aR1B2(t *testing.T) {
	t.Parallel()
	s := newStale(t)
	st := bucketName("tofu-state")
	s.world.drop(st, s.journalKey)
	for _, p := range []string{filepath.Join(s.repo, ".bootstrap", "journal.json")} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.localBundles()) == 0 {
		t.Fatal("fixture: no leftover bundle files")
	}

	code, out := s.abandon(nil, "")
	if code != 0 {
		t.Errorf("journal-less abandon-identity with leftover bundles: exit %d, want 0 (not ABANDON_NO_IDENTITY):\n%s", code, out)
	}
	s.requireMovedAside(t)
	s.requireNewIdentityPlanned(t)
}

var namedErr = regexp.MustCompile(`\b[A-Z][A-Z0-9]+(_[A-Z0-9]+)+\b`)

// N1, N5a, N5b: refusals of abandon-identity leave everything as it was. One installation, three refusals in order.
func TestAbandonIdentity_RefusalsExitWithTheCodesOfSection7_2_S8aR1(t *testing.T) {
	t.Parallel()
	s := newStale(t)
	st := bucketName("tofu-state")
	before := s.localFileSet()
	same := func(t *testing.T, what string) {
		t.Helper()
		if after := s.localFileSet(); after != before {
			t.Errorf("%s changed the local files:\nbefore:\n%s\nafter:\n%s", what, before, after)
		}
		if _, ok := s.world.read(st, clusterName+"/journal/abandoned-"+s.uid+".json"); ok {
			t.Errorf("%s left an archived journal behind", what)
		}
		if _, ok := s.world.read(st, s.journalKey); !ok {
			t.Errorf("%s removed the remote journal", what)
		}
	}

	t.Run("N1 remote unreachable while the journal shows the remote exists", func(t *testing.T) {
		code, out := s.abandon(func(d *install.Deps) { d.NewS3 = unreachableS3 }, s.typed())
		if code != 7 {
			t.Errorf("exit %d, want 7 (plan 50 section 7.2: remote journal unwritable); the abandon must not claim an archive it could not make:\n%s", code, out)
		}
		if strings.Contains(out, "archived as") {
			t.Errorf("the output claims an archive although the remote was unreachable:\n%s", out)
		}
		same(t, "the refused abandon-identity")
	})

	t.Run("N5a the archive cannot be written", func(t *testing.T) {
		mod := func(d *install.Deps) {
			open := d.NewS3
			d.NewS3 = func(c s3client.Config) (install.Account, error) {
				a, err := open(c)
				if err != nil {
					return nil, err
				}
				return failAccount{Account: a, putIfAbsent: func(key string) error {
					if strings.Contains(key, "journal/abandoned-") {
						return &store.Error{Kind: store.Unavailable, Msg: "the store refuses writes"}
					}
					return nil
				}}, nil
			}
		}
		code, out := s.abandon(mod, s.typed())
		if code != 7 || !strings.Contains(out, "ABANDON_ARCHIVE_FAILED") {
			t.Errorf("exit %d, want 7 with ABANDON_ARCHIVE_FAILED (remote journal unwritable):\n%s", code, out)
		}
		same(t, "the failed archive")
	})

	t.Run("N5b install_uid shorter than 8 characters", func(t *testing.T) {
		bad := bytes.Replace(s.jraw, []byte(`"install_uid": "`+s.uid+`"`), []byte(`"install_uid": "abc"`), 1)
		if bytes.Equal(bad, s.jraw) {
			t.Fatal("fixture: install_uid not found in the journal")
		}
		if err := os.WriteFile(filepath.Join(s.repo, ".bootstrap", "journal.json"), bad, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := s.world.write(stateKeyID, st, s.journalKey, bad); err != nil {
			t.Fatal(err)
		}
		before = s.localFileSet()
		var code int
		var out string
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("abandon-identity panicked on a malformed install_uid: %v", r)
					code = -1
				}
			}()
			code, out = s.abandon(nil, "abandon "+clusterName+" abc\n")
		}()
		if code != 2 || !namedErr.MatchString(out) {
			t.Errorf("exit %d, want 2 (invalid state, plan 50 section 7.2) with a named error code:\n%s", code, out)
		}
		if after := s.localFileSet(); after != before {
			t.Errorf("the refused abandon-identity changed the local files")
		}
	})
}
