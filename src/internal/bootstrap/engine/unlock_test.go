package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/engine"
)

// Plan 50 section 17.4 "Breaking" and procedures/bootstrap-resume.md#lock: `./bootstrap unlock` shows the lock content
// and requires typing `unlock <cluster> <run_id>`; it deletes the lock (the local lock file and the S3 object) and
// records the confirmation unlock-run-lock; it prints "lock released".
//
// Where the plan and the procedure are silent, these tests fix (every one is an invention to be confirmed):
//   - the entry point is (*Engine).Unlock(ctx, UnlockOptions{RunID, Confirm}); the Engine's Config carries Dir, Store,
//     Cluster, InstallUID, Host and Clock as for a run; Unlock does not take the run lock;
//   - liveness: a local OFD lock on .bootstrap/run.lock held by a live process is a live holder on this machine:
//     exit 6 RUN_LOCK_HELD, nothing deleted, whatever was typed. A holder on another host cannot be probed; the typed
//     confirmation is the operator's statement that it is dead, and an unexpired lease does not block it;
//   - install_uid: when this machine knows its install_uid and the lock names another one, the lock belongs to
//     another installation: exit 2 UNLOCK_INSTALL_MISMATCH, nothing deleted. A new machine (empty InstallUID) skips it;
//   - --run-id ID must equal the lock's run_id, else exit 2 UNLOCK_RUN_ID_MISMATCH, nothing deleted;
//   - without the exact typed text: exit 5, the message shows the lock (run id, command, user@host, started, lease)
//     and the text to type, nothing deleted;
//   - no lock object and no local lock content: exit 0 and the message says no run lock is held;
//   - success: exit 0, message contains "lock released", the object is deleted (If-Match its ETag where supported),
//     .bootstrap/run.lock is removed, and the journal (local and remote, when one exists) gains one confirmation
//     kind unlock-run-lock, method typed, subject_digest sha256 of the lock object bytes, run_id the unlocking run.

const deadRun = "20261026T110000Z-0a0b"

// plantLock writes a lock object the way a dead holder left it.
func plantLock(t *testing.T, v *tenv, host, uid string) []byte {
	t.Helper()
	b, _ := json.Marshal(map[string]string{
		"run_id": deadRun, "command": "run", "user": "bob", "host": host, "install_uid": uid,
		"started_at": "2026-10-26T11:00:00Z", "lease_expires_at": "2026-10-26T23:00:00Z",
	})
	if _, err := v.st.PutIfAbsent(context.Background(), lockKey, b); err != nil {
		t.Fatal(err)
	}
	return b
}

func typed(run string) string { return "unlock " + cluster + " " + run }

func TestUnlock_RequiresTheTypedConfirmation_50S17_4(t *testing.T) {
	t.Parallel()
	for name, confirm := range map[string]string{"nothing typed": "", "wrong run": typed("20261026T110000Z-ffff"), "yes": "yes", "no cluster": "unlock " + deadRun} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			v := newEnv(t)
			plantLock(t, v, "host-b", installUID)
			res := v.eng().Unlock(context.Background(), engine.UnlockOptions{Confirm: confirm})
			if res.Exit != 5 {
				t.Fatalf("exit %d (%s %s), want 5 (confirmation missing or mismatched)", res.Exit, res.Code, res.Message)
			}
			for _, want := range []string{deadRun, "host-b", "bob", "unlock " + cluster + " " + deadRun} {
				if !strings.Contains(res.Message, want) {
					t.Errorf("message lacks %q (the lock content and the text to type):\n%s", want, res.Message)
				}
			}
			if v.st.raw(lockKey) == nil {
				t.Error("the lock object was deleted without the typed confirmation")
			}
		})
	}
}

func TestUnlock_BreaksADeadLockAndRecordsIt_50S17_4(t *testing.T) {
	t.Parallel()
	for _, ifMatch := range []bool{false, true} {
		v := newEnv(t)
		v.st.ifMatch = ifMatch
		// a finished run leaves a journal; then the lock of a run on another host is left behind
		first := newUnit("plan")
		if res := v.exec(pipe(first), engine.Options{}); res.Exit != 0 {
			t.Fatalf("fixture run: exit %d %s", res.Exit, res.Message)
		}
		seq0 := seqOf(v.remote())
		body := plantLock(t, v, "host-b", installUID)
		local := filepath.Join(v.dir, "run.lock")
		if err := os.WriteFile(local, body, 0o600); err != nil {
			t.Fatal(err)
		}
		u := v.eng()
		res := u.Unlock(context.Background(), engine.UnlockOptions{Confirm: typed(deadRun)})
		if res.Exit != 0 || !strings.Contains(res.Message, "lock released") {
			t.Fatalf("ifMatch=%v: exit %d (%s %s), want 0 and \"lock released\"", ifMatch, res.Exit, res.Code, res.Message)
		}
		if v.st.raw(lockKey) != nil {
			t.Errorf("ifMatch=%v: the lock object still exists", ifMatch)
		}
		if _, err := os.Stat(local); err == nil {
			t.Errorf("ifMatch=%v: .bootstrap/run.lock still exists", ifMatch)
		}
		var del op
		for _, o := range v.st.opsFor(lockKey) {
			if o.Op == "delete" {
				del = o
			}
		}
		want := ""
		if ifMatch {
			want = etagOf(body)
		}
		if del.Op != "delete" || del.IfMatch != want {
			t.Errorf("ifMatch=%v: delete op %+v, want a delete with If-Match %q", ifMatch, del, want)
		}
		for what, j := range map[string]map[string]any{"local": v.local(), "remote": v.remote()} {
			cs := confirmations(j, "unlock-run-lock")
			if len(cs) != 1 {
				t.Errorf("ifMatch=%v: the %s journal has %d unlock-run-lock confirmations, want 1", ifMatch, what, len(cs))
				continue
			}
			c := cs[0]
			if str(c["method"]) != "interactive" || str(c["subject_digest"]) != shaOf(body) || str(c["run_id"]) != u.RunID() {
				t.Errorf("ifMatch=%v: %s confirmation %v, want method interactive, subject_digest %s, run_id %s", ifMatch, what, c, shaOf(body), u.RunID())
			}
			journalSchemaValidate(t, j)
		}
		if got := seqOf(v.remote()); got <= seq0 {
			t.Errorf("ifMatch=%v: journal_seq %d after the unlock, want more than %d", ifMatch, got, seq0)
		}
		if res := v.eng().Acquire(context.Background()); res.Exit != 0 {
			t.Errorf("ifMatch=%v: Acquire after unlock exit %d (%s), want 0", ifMatch, res.Exit, res.Message)
		}
	}
}

// The holder is alive on this machine: its OFD lock is held. The typed text does not matter.
func TestUnlock_RefusesALiveLocalHolder_50S17_4(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	holder := v.eng()
	if res := holder.Acquire(context.Background()); res.Exit != 0 {
		t.Fatalf("fixture Acquire exit %d", res.Exit)
	}
	res := v.eng().Unlock(context.Background(), engine.UnlockOptions{Confirm: typed(holder.RunID())})
	if res.Exit != 6 || res.Code != "RUN_LOCK_HELD" {
		t.Fatalf("exit %d code %q (%s), want 6 RUN_LOCK_HELD: the holder is alive", res.Exit, res.Code, res.Message)
	}
	if str(lockJSON(t, v)["run_id"]) != holder.RunID() {
		t.Error("the live holder's lock object was changed")
	}
	if _, err := os.Stat(filepath.Join(v.dir, "run.lock")); err != nil {
		t.Errorf("the live holder's local lock file is gone: %v", err)
	}
	holder.Release(context.Background())
	if res := v.eng().Unlock(context.Background(), engine.UnlockOptions{}); res.Exit != 0 || !strings.Contains(res.Message, "no run lock") {
		t.Errorf("after the holder released: exit %d (%s), want 0 and a message that no run lock is held", res.Exit, res.Message)
	}
}

func TestUnlock_NothingHeld_50S17_4(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	res := v.eng().Unlock(context.Background(), engine.UnlockOptions{Confirm: typed(deadRun)})
	if res.Exit != 0 || !strings.Contains(res.Message, "no run lock") {
		t.Errorf("exit %d (%s %s), want 0 and a message that no run lock is held", res.Exit, res.Code, res.Message)
	}
}

func TestUnlock_RefusesAnotherInstallationsLock_50S17_4(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	plantLock(t, v, "host-b", "11111111-2222-4333-8444-555555555555")
	res := v.eng().Unlock(context.Background(), engine.UnlockOptions{Confirm: typed(deadRun)})
	if res.Exit != 2 || res.Code != "UNLOCK_INSTALL_MISMATCH" {
		t.Errorf("exit %d code %q (%s), want 2 UNLOCK_INSTALL_MISMATCH", res.Exit, res.Code, res.Message)
	}
	if v.st.raw(lockKey) == nil {
		t.Error("another installation's lock was deleted")
	}
}

func TestUnlock_RunIDFlagMustMatchTheLock_50S17_4(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	plantLock(t, v, "host-b", installUID)
	res := v.eng().Unlock(context.Background(), engine.UnlockOptions{RunID: "20261026T110000Z-ffff", Confirm: typed(deadRun)})
	if res.Exit != 2 || res.Code != "UNLOCK_RUN_ID_MISMATCH" {
		t.Errorf("exit %d code %q (%s), want 2 UNLOCK_RUN_ID_MISMATCH", res.Exit, res.Code, res.Message)
	}
	if v.st.raw(lockKey) == nil {
		t.Error("the lock was deleted although --run-id named another run")
	}
	if res := v.eng().Unlock(context.Background(), engine.UnlockOptions{RunID: deadRun, Confirm: typed(deadRun)}); res.Exit != 0 {
		t.Errorf("matching --run-id: exit %d (%s), want 0", res.Exit, res.Message)
	}
}

// S8b2 review-1 N2: a new machine has no local journal and so no install_uid and another host name, so a dead
// holder's lock is RUN_LOCK_HELD and the automatic same-host takeover cannot apply. unlock clears it and the run
// resumes from the remote journal. (The crash package's new-machine test deletes the lock object by hand.)
func TestUnlock_NewMachineClearsADeadHoldersLockAndTheRunResumes_S8b2N2(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	a, b := newUnit("plan"), newUnit("provision")
	b.runFn = fail("PROVIDER_DELIVERY_TIMEOUT", false)
	if res := v.exec(pipe(a, b), engine.Options{}); res.Exit != 1 {
		t.Fatalf("fixture run on the first machine: exit %d (%s), want 1", res.Exit, res.Message)
	}
	plantLock(t, v, "host-a", installUID) // the first machine died holding the lock
	newMachine := func(c *engine.Config) { c.Dir, c.Host, c.InstallUID = v.dir+"-b", "host-b", "" }

	res := v.eng(newMachine).Acquire(context.Background())
	if res.Exit != 6 || res.Code != "RUN_LOCK_HELD" || !strings.Contains(res.Message, "./bootstrap unlock") {
		t.Fatalf("new machine, dead holder: exit %d code %q (%s), want 6 RUN_LOCK_HELD naming ./bootstrap unlock", res.Exit, res.Code, res.Message)
	}
	if res := v.eng(newMachine).Unlock(context.Background(), engine.UnlockOptions{Confirm: typed(deadRun)}); res.Exit != 0 || !strings.Contains(res.Message, "lock released") {
		t.Fatalf("unlock on the new machine: exit %d code %q (%s), want 0 \"lock released\"", res.Exit, res.Code, res.Message)
	}
	if v.st.raw(lockKey) != nil {
		t.Fatal("the lock object survived the unlock")
	}
	if cs := confirmations(v.remote(), "unlock-run-lock"); len(cs) != 1 {
		t.Errorf("the remote journal has %d unlock-run-lock confirmations, want 1", len(cs))
	}
	b.runFn = nil
	if res := v.exec(pipe(a, b), engine.Options{}, newMachine); res.Exit != 0 {
		t.Fatalf("the run on the new machine after unlock: exit %d code %q (%s), want 0", res.Exit, res.Code, res.Message)
	}
	if got := runsOf(a, b); !eqInts(got, []int{1, 2}) {
		t.Errorf("Run counts %v, want [1 2]: the finished phase is not repeated, the failed one runs again", got)
	}
}
