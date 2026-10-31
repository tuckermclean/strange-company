package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"strange.company/sc/internal/bootstrap/engine"
)

func lockJSON(t testing.TB, v *tenv) map[string]any {
	t.Helper()
	return readJSON(t, v.st.raw(lockKey), "lock object")
}

// 50-AC-5, plan 50 section 17.4: the remote lock object is created with a conditional create and names the run.
func TestLock_AcquireCreatesObjectWithRunDetails_50AC5(t *testing.T) {
	v := newEnv(t)
	e := v.eng()
	if !runIDRe.MatchString(e.RunID()) {
		t.Fatalf("run id %q, want <UTC yyyymmddThhmmssZ>-<4 hex>", e.RunID())
	}
	if !strings.HasPrefix(e.RunID(), "20261026T120000Z-") {
		t.Errorf("run id %q does not carry the clock's time 20261026T120000Z", e.RunID())
	}
	if res := e.Acquire(context.Background()); res.Exit != 0 {
		t.Fatalf("Acquire exit %d (%s %s), want 0", res.Exit, res.Code, res.Message)
	}
	l := lockJSON(t, v)
	for k, want := range map[string]string{"run_id": e.RunID(), "command": "run", "user": "alice", "host": "host-a", "install_uid": installUID} {
		if str(l[k]) != want {
			t.Errorf("lock %s = %q, want %q", k, str(l[k]), want)
		}
	}
	started, err1 := time.Parse(time.RFC3339, str(l["started_at"]))
	lease, err2 := time.Parse(time.RFC3339, str(l["lease_expires_at"]))
	if err1 != nil || err2 != nil {
		t.Fatalf("started_at %q / lease_expires_at %q are not RFC 3339", l["started_at"], l["lease_expires_at"])
	}
	if !started.Equal(v.clk.Now()) || lease.Sub(started) != 12*time.Hour {
		t.Errorf("started_at %v lease_expires_at %v, want the clock's now and a 12 h lease", started, lease)
	}
	ops := v.st.opsFor(lockKey)
	if len(ops) == 0 || ops[0].Op != "put-if-absent" {
		t.Errorf("the lock object must be created with a conditional create (If-None-Match), ops: %+v", ops)
	}
}

// 50-AC-5: a second run on the same host while the first is alive exits 6 and names the holder.
func TestLock_SecondRunRefusedAndNamesHolder_50AC5(t *testing.T) {
	v := newEnv(t)
	e1, e2 := v.eng(), v.eng()
	if res := e1.Acquire(context.Background()); res.Exit != 0 {
		t.Fatalf("first Acquire exit %d", res.Exit)
	}
	res := e2.Acquire(context.Background())
	if res.Exit != 6 {
		t.Fatalf("second Acquire exit %d (%s), want 6", res.Exit, res.Message)
	}
	if !strings.Contains(res.Message, e1.RunID()) {
		t.Errorf("message %q does not name the holder %s", res.Message, e1.RunID())
	}
	if got := str(lockJSON(t, v)["run_id"]); got != e1.RunID() {
		t.Errorf("lock object names %q after the refusal, want the holder %s", got, e1.RunID())
	}
}

// 50-AC-5: two concurrent runs, exactly one exits 6, the lock object names the winner.
func TestLock_ConcurrentRunsExactlyOneWins_50AC5(t *testing.T) {
	for _, tc := range []struct {
		name         string
		sameDir      bool
		hostA, hostB string
	}{
		{"same host, same directory", true, "host-a", "host-a"},
		{"two machines, one bucket", false, "host-a", "host-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for round := 0; round < 5; round++ {
				v := newEnv(t)
				dirB := v.dir
				if !tc.sameDir {
					dirB = v.dir + "-b"
				}
				e1 := v.eng(func(c *engine.Config) { c.Host = tc.hostA })
				e2 := v.eng(func(c *engine.Config) { c.Host = tc.hostB; c.Dir = dirB })
				start := make(chan struct{})
				var wg sync.WaitGroup
				var r1, r2 int
				var m2 string
				wg.Add(2)
				go func() { defer wg.Done(); <-start; r1 = e1.Acquire(context.Background()).Exit }()
				go func() {
					defer wg.Done()
					<-start
					res := e2.Acquire(context.Background())
					r2, m2 = res.Exit, res.Message
				}()
				close(start)
				wg.Wait()
				if !((r1 == 0 && r2 == 6) || (r1 == 6 && r2 == 0)) {
					t.Fatalf("round %d: exits %d and %d, want exactly one 0 and one 6", round, r1, r2)
				}
				winner, loserMsg := e1.RunID(), m2
				if r2 == 0 {
					winner = e2.RunID()
					loserMsg = ""
				}
				if got := str(lockJSON(t, v)["run_id"]); got != winner {
					t.Fatalf("round %d: lock names %q, want the winner %s", round, got, winner)
				}
				if loserMsg != "" && !strings.Contains(loserMsg, winner) {
					t.Errorf("round %d: refusal %q does not name the winner %s", round, loserMsg, winner)
				}
			}
		})
	}
}

// Release deletes the lock object, conditional on its ETag where the store supports it, and frees the local lock.
func TestLock_ReleaseDeletesObjectAndFreesLocalLock_50AC5(t *testing.T) {
	for _, ifMatch := range []bool{false, true} {
		v := newEnv(t)
		v.st.ifMatch = ifMatch
		e1 := v.eng()
		if res := e1.Acquire(context.Background()); res.Exit != 0 {
			t.Fatalf("ifMatch=%v: Acquire exit %d", ifMatch, res.Exit)
		}
		etag := etagOf(v.st.raw(lockKey))
		if res := e1.Release(context.Background()); res.Exit != 0 {
			t.Fatalf("ifMatch=%v: Release exit %d (%s)", ifMatch, res.Exit, res.Message)
		}
		if v.st.raw(lockKey) != nil {
			t.Errorf("ifMatch=%v: the lock object still exists after Release", ifMatch)
		}
		var del op
		for _, o := range v.st.opsFor(lockKey) {
			if o.Op == "delete" {
				del = o
			}
		}
		want := ""
		if ifMatch {
			want = etag
		}
		if del.Op != "delete" || del.IfMatch != want {
			t.Errorf("ifMatch=%v: delete op %+v, want a delete with If-Match %q", ifMatch, del, want)
		}
		if res := v.eng().Acquire(context.Background()); res.Exit != 0 {
			t.Errorf("ifMatch=%v: Acquire after Release exit %d (%s), want 0", ifMatch, res.Exit, res.Message)
		}
	}
}

// Renew every 10 minutes with PUT If-Match when the store supports it (plan 50 section 17.4).
func TestLock_RenewExtendsLeaseOnlyWithIfMatch_50AC5(t *testing.T) {
	t.Run("if-match supported", func(t *testing.T) {
		v := newEnv(t)
		v.st.ifMatch = true
		e := v.eng()
		e.Acquire(context.Background())
		old := v.st.raw(lockKey)
		v.clk.Advance(10 * time.Minute)
		if res := e.Renew(context.Background()); res.Exit != 0 {
			t.Fatalf("Renew exit %d (%s)", res.Exit, res.Message)
		}
		l := lockJSON(t, v)
		lease, _ := time.Parse(time.RFC3339, str(l["lease_expires_at"]))
		if want := v.clk.Now().Add(12 * time.Hour); !lease.Equal(want) {
			t.Errorf("lease_expires_at %v, want %v (now + 12 h)", lease, want)
		}
		if str(l["run_id"]) != e.RunID() {
			t.Errorf("renewal changed the holder to %q", l["run_id"])
		}
		found := false
		for _, o := range v.st.opsFor(lockKey) {
			if o.Op == "put" && o.IfMatch == etagOf(old) {
				found = true
			}
		}
		if !found {
			t.Errorf("no PUT with If-Match %s, ops: %+v", etagOf(old), v.st.opsFor(lockKey))
		}
	})
	t.Run("no if-match: the run lock alone protects", func(t *testing.T) {
		v := newEnv(t)
		e := v.eng()
		e.Acquire(context.Background())
		old := v.st.raw(lockKey)
		v.clk.Advance(10 * time.Minute)
		if res := e.Renew(context.Background()); res.Exit != 0 {
			t.Fatalf("Renew exit %d (%s)", res.Exit, res.Message)
		}
		if string(v.st.raw(lockKey)) != string(old) {
			t.Errorf("Renew rewrote the lock object although the store has no If-Match support")
		}
	})
}

func staleLockBody(host, uid string) []byte {
	b, _ := json.Marshal(map[string]any{
		"run_id": "20261026T110000Z-dead", "command": "run", "user": "alice", "host": host, "install_uid": uid,
		"started_at": "2026-10-26T11:00:00Z", "lease_expires_at": "2026-10-26T23:00:00Z",
	})
	return b
}

// 50-AC-17: a same-host holder whose local lock is free is provably dead. The lock is taken over and the journal
// records a stale-lock-takeover confirmation.
func TestLock_SameHostStaleHolderTakenOver_50AC17(t *testing.T) {
	v := newEnv(t)
	old := staleLockBody("host-a", installUID)
	if _, err := v.st.PutIfAbsent(context.Background(), lockKey, old); err != nil {
		t.Fatal(err)
	}
	u := newUnit("preflight")
	e := v.eng()
	res := e.Execute(context.Background(), pipe(u), engine.Options{})
	if res.Exit != 0 {
		t.Fatalf("Execute exit %d (%s %s), want 0 (automatic takeover)", res.Exit, res.Code, res.Message)
	}
	if _, runs := u.counts(); runs != 1 {
		t.Errorf("phase ran %d times, want 1", runs)
	}
	for name, j := range map[string]map[string]any{"local": v.local(), "remote": v.remote()} {
		cs := confirmations(j, "stale-lock-takeover")
		if len(cs) != 1 {
			t.Fatalf("%s journal: %d stale-lock-takeover confirmations, want 1: %v", name, len(cs), j["confirmations"])
		}
		c := cs[0]
		if str(c["run_id"]) != e.RunID() || str(c["method"]) != "flag" || str(c["subject_digest"]) != shaOf(old) {
			t.Errorf("%s journal: takeover confirmation %v, want run_id %s, method flag, subject_digest %s (the old lock content)", name, c, e.RunID(), shaOf(old))
		}
	}
}

// 50-AC-17: a holder on a different host, or for a different install, is not provably dead: exit 6.
func TestLock_ForeignHolderRefused_50AC17(t *testing.T) {
	for name, body := range map[string][]byte{
		"different host":        staleLockBody("host-b", installUID),
		"different install uid": staleLockBody("host-a", "11111111-2222-4333-8444-555555555555"),
	} {
		v := newEnv(t)
		if _, err := v.st.PutIfAbsent(context.Background(), lockKey, body); err != nil {
			t.Fatal(err)
		}
		u := newUnit("preflight")
		res := v.exec(pipe(u), engine.Options{})
		if res.Exit != 6 {
			t.Errorf("%s: Execute exit %d (%s), want 6", name, res.Exit, res.Message)
		}
		if _, runs := u.counts(); runs != 0 {
			t.Errorf("%s: the phase ran under a lock held by another run", name)
		}
		if string(v.st.raw(lockKey)) != string(body) {
			t.Errorf("%s: the foreign lock object was modified", name)
		}
	}
}
