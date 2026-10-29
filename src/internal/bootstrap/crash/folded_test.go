package crash_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"strange.company/sc/internal/s3client"
)

// The items the S8a and S8b1 reviews folded into this slice: the stale-lock scenario of 50-AC-17 with a real SIGKILL
// (S8a N6), the BOOT-19 residual window and the credentials commit after a Git reset (S8b1 R4b1-1 and R4b1-3), and the
// adopt path's recovery-identity.txt.

var runIDRe = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{4}$`)

func sha256Of(b []byte) string { s := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(s[:]) }

func (s *scenario) cdir() string { return filepath.Join(s.blackstart, clusterName) }

func (s *scenario) rootCommit() string { return s.git("rev-list", "--max-parents=0", "HEAD") }

func (s *scenario) writeJournal(j map[string]any) {
	s.t.Helper()
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(s.journalPath(), append(b, '\n'), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func stopAfter(step string) map[string]string { return map[string]string{"SC_TEST_STOP_AFTER": step} }

// stepOf finds a step of the install pipeline in a decoded journal.
func stepOf(j map[string]any, name string) map[string]any {
	pipes, _ := j["pipelines"].([]any)
	for _, p := range pipes {
		phases, _ := p.(map[string]any)["phases"].([]any)
		for _, ph := range phases {
			steps, _ := ph.(map[string]any)["steps"].([]any)
			for _, st := range steps {
				if m, _ := st.(map[string]any); m["name"] == name {
					return m
				}
			}
		}
	}
	return nil
}

func generationOf(j map[string]any) int {
	rb, _ := j["recovery_bundle"].(map[string]any)
	g, _ := rb["generation"].(float64)
	return int(g)
}

func bundleShaOf(j map[string]any) string {
	rb, _ := j["recovery_bundle"].(map[string]any)
	s, _ := rb["sha256"].(string)
	return s
}

// 50-AC-17 with a real SIGKILL (S8a N6): the engine dies by SIGKILL right after plan.storage-apply (the run lock object
// is in the tofu-state bucket, no release follows). A rerun with another host in the lock object exits 6 with
// RUN_LOCK_HELD and changes nothing; with the lock object's own host the rerun exits 0, takes the lock over, and the
// journal shows exactly one stale-lock-takeover for the dead run's lock.
func TestStaleLock_SIGKILLAfterStorageApply_SameHostTakesOver_OtherHostExits6_50AC17_N6(t *testing.T) {
	t.Parallel()
	s := newScenario(t)
	env := s.unattendedEnv()
	args := s.runArgs("--non-interactive")

	crashed := s.run("", merge(env, map[string]string{"SC_TEST_CRASH_AT": "plan.storage-apply:after"}), args)
	if !crashed.killedByKill() {
		t.Fatalf("the engine ended with exit %d (signal %v), want death by SIGKILL:\n%s", crashed.exit, crashed.signal, tail(crashed.out, 2000))
	}
	orig, ok := s.lockObject()
	if !ok {
		t.Fatal("a killed run leaves its lock object behind; there is none")
	}
	var holder struct {
		RunID string `json:"run_id"`
		Host  string `json:"host"`
	}
	if err := json.Unmarshal(orig, &holder); err != nil || holder.Host == "" || !runIDRe.MatchString(holder.RunID) {
		t.Fatalf("lock object %s: %v", orig, err)
	}
	tofuState := s.client(true).Bucket(s.bucket("tofu-state"))
	putLock := func(b []byte) {
		t.Helper()
		if _, err := tofuState.PutObject(context.Background(), s.lockKey(), b, s3client.PutOptions{}); err != nil {
			t.Fatalf("rewriting the lock object: %v", err)
		}
	}

	// Another host: exit 6, nothing changes.
	foreign := bytes.Replace(orig, []byte(`"host":"`+holder.Host+`"`), []byte(`"host":"some-other-host"`), 1)
	if bytes.Equal(foreign, orig) {
		t.Fatalf("fixture: cannot rewrite the host in %s", orig)
	}
	putLock(foreign)
	journalBefore, _ := s.journalBytes()
	res := s.run("", env, args)
	if res.exit != 6 || res.signal != 0 {
		t.Fatalf("50-AC-17: rerun with a lock object of another host: exit %d (signal %v), want 6:\n%s", res.exit, res.signal, tail(res.out, 2000))
	}
	if !strings.Contains(res.out, "RUN_LOCK_HELD") {
		t.Errorf("50-AC-17: the refused rerun does not report RUN_LOCK_HELD:\n%s", tail(res.out, 2000))
	}
	if now, _ := s.lockObject(); !bytes.Equal(now, foreign) {
		t.Errorf("50-AC-17: the foreign lock object was modified by the refused rerun")
	}
	if after, _ := s.journalBytes(); !bytes.Equal(journalBefore, after) {
		t.Errorf("50-AC-17: the refused rerun wrote the journal")
	}

	// The lock object's own host: automatic takeover.
	putLock(orig)
	res = s.run("", env, args)
	s.requireExit(res, 0, "50-AC-17: same-host rerun (automatic takeover of the stale lock)")
	j := s.journal()
	var found []map[string]any
	cs, _ := j["confirmations"].([]any)
	for _, c := range cs {
		if cm, _ := c.(map[string]any); cm["kind"] == "stale-lock-takeover" {
			found = append(found, cm)
		}
	}
	if len(found) != 1 {
		t.Fatalf("50-AC-17: confirmations %v, want exactly one stale-lock-takeover", cs)
	}
	if found[0]["subject_digest"] != sha256Of(orig) {
		t.Errorf("50-AC-17: stale-lock-takeover subject_digest = %v, want the digest of the dead run's lock object %s", found[0]["subject_digest"], sha256Of(orig))
	}
	if id, _ := found[0]["run_id"].(string); id == holder.RunID || !runIDRe.MatchString(id) {
		t.Errorf("50-AC-17: stale-lock-takeover run_id = %q, want the id of the taking-over run (not the dead run %s)", id, holder.RunID)
	}
	if _, ok := s.lockObject(); ok {
		t.Errorf("50-AC-17: the lock object remains after the rerun finished")
	}
	s.assertConverged("after the takeover")
}

// BOOT-19 (plan 50 section 14.3, "re-sealed at most once during an install run"), S8b1 R4b1-1. The window is the
// SIGKILL of the identity step between its commit and its journal save: the rerun records the step through its check
// path, which keeps no output reference. The crash points of the engine are the step boundaries, so the state of that
// window is made from a real run: the install stops after plan.install-identity, and the step's output_refs are emptied
// in the journal (what the check path writes). plan.storage-credentials then re-seals the bundle (generation 2); after
// a Git reset that loses the installation commit the identity step runs again. The bundle stays generation 2: no
// second re-seal, no new object in the recovery bucket.
func TestStorageCredentials_IdentityStepRecordedWithoutReferenceDoesNotReSealTwice_BOOT19_R4b1_1(t *testing.T) {
	t.Parallel()
	s := newScenario(t)
	env := s.unattendedEnv()
	args := s.runArgs("--non-interactive")

	res := s.run("", merge(env, stopAfter("plan.install-identity")), args)
	s.requireExit(res, 0, "run to plan.install-identity")
	j := s.journal()
	st := stepOf(j, "plan.install-identity")
	if st == nil || st["status"] != "succeeded" {
		t.Fatalf("fixture: plan.install-identity is %v after the first run", st)
	}
	st["output_refs"] = []any{}
	s.writeJournal(j)

	res = s.run("", merge(env, stopAfter("plan.storage-credentials")), args)
	s.requireExit(res, 0, "run to plan.storage-credentials")
	j = s.journal()
	if g := generationOf(j); g != 2 {
		t.Fatalf("fixture: the bundle is generation %d after plan.storage-credentials, want 2", g)
	}
	sha := bundleShaOf(j)
	objs := s.listKeys("recovery", "")
	if len(objs) == 0 {
		t.Fatal("fixture: the recovery bucket holds no object after plan.bundle-upload")
	}

	s.git("reset", "-q", "--hard", s.rootCommit())

	res = s.run("", env, args)
	s.requireExit(res, 0, "run after the Git reset")
	j = s.journal()
	if g := generationOf(j); g != 2 {
		t.Errorf("BOOT-19: the bundle is generation %d after plan.install-identity ran again, want 2: the credentials were sealed a second time", g)
	}
	if got := bundleShaOf(j); got != sha {
		t.Errorf("BOOT-19: the recovery bundle changed (%s, was %s)", got, sha)
	}
	have := map[string]bool{}
	for _, k := range objs {
		have[k] = true
	}
	for _, k := range s.listKeys("recovery", "") {
		if !have[k] {
			t.Errorf("BOOT-19: a new object %s was uploaded to the recovery bucket", k)
		}
	}
}

// S8b1 R4b1-3: plan.storage-credentials is done only while its commit exists. After a Git reset that loses the
// "object-storage credentials" commit the rerun creates it again, once, and the installation config commit as well.
func TestStorageCredentials_CommitIsCreatedAgainAfterGitReset_R4b1_3(t *testing.T) {
	t.Parallel()
	s := newScenario(t)
	env := s.unattendedEnv()
	args := s.runArgs("--non-interactive")
	credSubject := "bootstrap(" + clusterName + "): object-storage credentials"

	res := s.run("", merge(env, stopAfter("plan.storage-credentials")), args)
	s.requireExit(res, 0, "run to plan.storage-credentials")
	if n := countSubject(commitSubjects(t, s.repo), credSubject); n != 1 {
		t.Fatalf("fixture: %d %q commits after plan.storage-credentials, want 1", n, credSubject)
	}

	s.git("reset", "-q", "--hard", s.rootCommit())
	if n := countSubject(commitSubjects(t, s.repo), credSubject); n != 0 {
		t.Fatalf("fixture: the reset left %d credentials commits", n)
	}

	res = s.run("", env, args)
	s.requireExit(res, 0, "run after the Git reset")
	subjects := commitSubjects(t, s.repo)
	if n := countSubject(subjects, credSubject); n != 1 {
		t.Errorf("git log has %d %q commits after the rerun, want exactly one:\n%s", n, credSubject, strings.Join(subjects, "\n"))
	}
	if n := countSubject(subjects, s.installSubject()); n != 1 {
		t.Errorf("git log has %d %q commits after the rerun, want exactly one:\n%s", n, s.installSubject(), strings.Join(subjects, "\n"))
	}
}

// The adopt path (plan 50 section 14.3): the journal is lost on both sides (here: no S3 journal exists yet, the local
// one is deleted), the bundle files of an earlier run are still in the blackstart directory, and recovery-identity.txt
// is not (a local write failed after the bundle files were written). The run opens the newest bundle with the
// passphrase, and writes recovery-identity.txt again: the SC-AGE-RECOVERY identity of the bundle, the file the
// operator is told to take offline.
func TestAdoptPath_WritesRecoveryIdentityFile_AfterJournalLostOnBothSides(t *testing.T) {
	t.Parallel()
	s := newScenario(t)
	env := s.unattendedEnv()
	args := s.runArgs("--non-interactive")

	res := s.run("", merge(env, stopAfter("plan.install-identity")), args)
	s.requireExit(res, 0, "run to plan.install-identity")
	rid := filepath.Join(s.cdir(), "recovery-identity.txt")
	want, err := os.ReadFile(rid)
	if err != nil || len(bytes.TrimSpace(want)) == 0 {
		t.Fatalf("fixture: recovery-identity.txt after plan.install-identity: %v", err)
	}
	if err := os.Remove(rid); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.journalPath()); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.lockObject(); ok {
		t.Fatal("fixture: the store already holds a run lock")
	}

	res = s.run("", merge(env, stopAfter("plan.install-identity")), args)
	s.requireExit(res, 0, "run that adopts the bundle")
	got, err := os.ReadFile(rid)
	if err != nil {
		t.Fatalf("the adopt path did not write recovery-identity.txt: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("recovery-identity.txt written by the adopt path differs from the bundle's recovery identity")
	}
}
