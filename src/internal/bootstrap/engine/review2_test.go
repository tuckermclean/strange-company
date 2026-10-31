package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/bootstrap/redact"
	"strange.company/sc/internal/bootstrap/store"
)

func localSeq(t *testing.T, v *tenv) int {
	t.Helper()
	return seqOf(v.local())
}

// 50-AC-6 and plan 50 sections 15 and 17.3: after a failed conditional PUT (another writer) the run exits 7 and
// writes nothing more: the local journal keeps the seq of the failing save, so a rerun sees equal seqs with different
// content and exits 7 instead of silently overwriting the other writer's copy.
func TestJournal_ConflictOnPutDoesNotResaveLocal_50AC6(t *testing.T) {
	v := newEnv(t)
	v.st.ifMatch = true
	u := newUnit("preflight")
	var seenSeqs []int
	var seenBytes [][]byte
	v.st.failPut = func(key string) error {
		if key == journalKey {
			b, _ := os.ReadFile(v.localPath())
			seenSeqs = append(seenSeqs, seqOf(readJSON(t, b, "local journal")))
			seenBytes = append(seenBytes, b)
		}
		return nil
	}
	var other []byte
	before := 0
	u.runFn = func(*unit) ([]engine.OutputRef, error) {
		j := v.remote()
		j["journal_seq"] = float64(seqOf(j) + 1)
		j["facts"] = map[string]any{"other_writer": "wins"}
		other, _ = json.MarshalIndent(j, "", "  ")
		if _, err := v.st.Put(context.Background(), journalKey, other, ""); err != nil {
			t.Fatal(err)
		}
		before = len(seenSeqs)
		return nil, nil
	}
	res := v.exec(pipe(u), engine.Options{})
	if res.Exit != 7 || res.Code != "JOURNAL_CONFLICT" {
		t.Fatalf("exit %d code %q (%s), want 7 JOURNAL_CONFLICT", res.Exit, res.Code, res.Message)
	}
	if len(seenSeqs) == 0 {
		t.Fatal("no journal PUT was attempted")
	}
	if n := len(seenSeqs) - before; n != 1 {
		t.Errorf("%d journal PUT attempts after the other writer won, want exactly 1 (no re-save of a failed save)", n)
	}
	last := seenSeqs[len(seenSeqs)-1]
	if got := localSeq(t, v); got != last {
		t.Errorf("local journal_seq %d after the failed PUT of seq %d: the failed save must not be followed by another local write", got, last)
	}
	if lb, _ := os.ReadFile(v.localPath()); !bytes.Equal(lb, seenBytes[len(seenBytes)-1]) {
		t.Errorf("the local journal was rewritten after the failing PUT")
	}
	if !bytes.Equal(v.st.raw(journalKey), other) {
		t.Errorf("the other writer's S3 journal was modified")
	}

	// rerun on the same machine
	u2 := newUnit("preflight")
	res2 := v.exec(pipe(u2), engine.Options{})
	if res2.Exit != 7 || res2.Code != "JOURNAL_CONFLICT" {
		t.Errorf("rerun: exit %d code %q (%s), want 7 JOURNAL_CONFLICT", res2.Exit, res2.Code, res2.Message)
	}
	if _, r := u2.counts(); r != 0 {
		t.Errorf("rerun: the phase ran on an ambiguous journal")
	}
	if !bytes.Equal(v.st.raw(journalKey), other) {
		t.Errorf("rerun: the other writer's S3 journal was modified")
	}
}

// Plan 50 section 15: a journal write that fails five times gives up with exit 7 and mutates nothing more, locally
// or remotely: exactly 5 PUT attempts, one local journal write.
func TestJournal_RemoteOutageExactlyFivePutsNoFurtherLocalWrite_50AC6(t *testing.T) {
	v := newEnv(t)
	u := newUnit("preflight")
	down := false
	var seqs []int
	var last []byte
	v.st.failPut = func(key string) error {
		if key != journalKey || !down {
			return nil
		}
		b, _ := os.ReadFile(v.localPath())
		seqs = append(seqs, seqOf(readJSON(t, b, "local journal")))
		last = b
		return &store.Error{Kind: store.Unavailable, Msg: "503 SlowDown"}
	}
	putsBefore := 0
	u.runFn = func(*unit) ([]engine.OutputRef, error) {
		for _, o := range v.st.opsFor(journalKey) {
			if o.Op == "put" {
				putsBefore++
			}
		}
		down = true
		return nil, nil
	}
	res := v.exec(pipe(u), engine.Options{})
	if res.Exit != 7 || res.Code != "JOURNAL_REMOTE_WRITE_FAILED" {
		t.Fatalf("exit %d code %q (%s), want 7 JOURNAL_REMOTE_WRITE_FAILED", res.Exit, res.Code, res.Message)
	}
	puts := 0
	for _, o := range v.st.opsFor(journalKey) {
		if o.Op == "put" {
			puts++
		}
	}
	if got := puts - putsBefore; got != 5 {
		t.Errorf("%d journal PUT attempts during the outage, want exactly 5", got)
	}
	for _, s := range seqs {
		if len(seqs) > 0 && s != seqs[0] {
			t.Errorf("local journal_seq changed between PUT attempts: %v", seqs)
			break
		}
	}
	if lb, _ := os.ReadFile(v.localPath()); !bytes.Equal(lb, last) {
		t.Errorf("the local journal was rewritten after the failing writes (seq %d, was %v)", localSeq(t, v), seqs)
	}
}

// Plan 50 section 16: the ceiling counts consecutive failed attempts with the same error code and input digest.
func TestEngine_RetryCeilingNeedsTheSameErrorCode_Plan50S16(t *testing.T) {
	v := newEnv(t)
	u := newUnit("provision")
	for i := 1; i <= 5; i++ {
		code := "CODE_A"
		if i == 5 {
			code = "CODE_B"
		}
		u.runFn = fail(code, false)
		if res := v.exec(pipe(u), engine.Options{}); res.Exit != 1 || res.Code != code {
			t.Fatalf("run %d: exit %d code %q, want 1 %s", i, res.Exit, res.Code, code)
		}
	}
	res := v.exec(pipe(u), engine.Options{})
	if res.Code == "RETRY_CEILING" {
		t.Errorf("four CODE_A failures then one CODE_B hit RETRY_CEILING; only consecutive failures with the same code count")
	}
	if _, r := u.counts(); r != 6 {
		t.Errorf("Run called %d times, want 6", r)
	}
}

// Plan 50 section 21: attempt, error_code and duration_ms are top-level fields of the JSON log lines.
func TestLog_AttemptErrorCodeDurationAreTopLevel_Plan50S21(t *testing.T) {
	v := newEnv(t)
	u := newUnit("provision")
	u.runFn = func(*unit) ([]engine.OutputRef, error) {
		v.clk.Advance(2_000_000_000)
		return nil, &engine.Error{Code: "PROVIDER_REJECTED", Message: "no", Retryable: false}
	}
	e := v.eng()
	if res := e.Execute(context.Background(), pipe(u), engine.Options{}); res.Exit != 1 {
		t.Fatalf("exit %d", res.Exit)
	}
	b, err := os.ReadFile(filepath.Join(v.dir, "runs", e.RunID(), "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var attempt, code, dur bool
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			t.Fatalf("log line is not JSON: %s", line)
		}
		if a, ok := rec["attempt"].(float64); ok && a == 1 {
			attempt = true
		}
		if rec["error_code"] == "PROVIDER_REJECTED" {
			code = true
		}
		if _, ok := rec["duration_ms"].(float64); ok {
			dur = true
		}
		if f, ok := rec["fields"].(map[string]any); ok {
			for _, k := range []string{"attempt", "error_code", "duration_ms"} {
				if _, nested := f[k]; nested {
					t.Errorf("%s is nested under fields in %s; plan 50 section 21 makes it a top-level field", k, line)
				}
			}
		}
	}
	for name, ok := range map[string]bool{"attempt": attempt, "error_code": code, "duration_ms": dur} {
		if !ok {
			t.Errorf("no log record carries top-level %s", name)
		}
	}
}

// getFailStore fails reads of the journal with an error text that holds a secret.
type getFailStore struct {
	*dirStore
	msg string
}

func (s getFailStore) Get(ctx context.Context, key string) (store.Object, error) {
	if key == journalKey {
		return store.Object{}, &store.Error{Kind: store.Unavailable, Msg: s.msg}
	}
	return s.dirStore.Get(ctx, key)
}

// Plan 50 section 10.2: Result.Message is redacted on every return path, including the early ones.
func TestExecute_ResultMessageRedactedOnEarlyReturns_Plan50S10_2(t *testing.T) {
	const secret = "s3cr3t-Value-Zq81xT"
	marker := "«redacted:SC-OBJECT-STORAGE»"
	newRed := func() *redact.Redactor {
		r := redact.New()
		r.Register("SC-OBJECT-STORAGE", secret)
		return r
	}
	check := func(t *testing.T, res engine.Result, wantExit int) {
		t.Helper()
		if res.Exit != wantExit {
			t.Fatalf("exit %d code %q (%s), want %d", res.Exit, res.Code, res.Message, wantExit)
		}
		if strings.Contains(res.Message, secret) {
			t.Errorf("Result.Message leaks a registered secret: %s", res.Message)
		}
		if !strings.Contains(res.Message, marker) {
			t.Errorf("Result.Message lacks %s: %s", marker, res.Message)
		}
	}
	t.Run("journal load failure", func(t *testing.T) {
		v := newEnv(t)
		gs := getFailStore{v.st, "denied for key " + secret}
		res := v.exec(pipe(newUnit("preflight")), engine.Options{}, func(c *engine.Config) { c.Store = gs; c.Redactor = newRed() })
		check(t, res, 7)
	})
	t.Run("lock held by another run", func(t *testing.T) {
		v := newEnv(t)
		body, _ := json.Marshal(map[string]string{
			"run_id": "20261026T110000Z-abcd", "command": "run", "user": secret, "host": "host-b", "install_uid": installUID,
			"started_at": "2026-10-26T11:00:00Z", "lease_expires_at": "2026-10-26T23:00:00Z",
		})
		if _, err := v.st.PutIfAbsent(context.Background(), lockKey, body); err != nil {
			t.Fatal(err)
		}
		res := v.exec(pipe(newUnit("preflight")), engine.Options{}, func(c *engine.Config) { c.Redactor = newRed() })
		check(t, res, 6)
	})
}
