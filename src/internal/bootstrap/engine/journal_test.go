package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/bootstrap/store"
)

// Plan 50 section 17.3: local atomic write first, then the S3 PUT; journal_seq increments on every write.
func TestJournal_LocalWriteBeforeRemotePutAndSeqIncrements_50AC9(t *testing.T) {
	v := newEnv(t)
	var seqs []int
	v.st.onPut = func(key string, data []byte) {
		if key != journalKey {
			return
		}
		local, _ := os.ReadFile(v.localPath())
		if !bytes.Equal(local, data) {
			t.Errorf("at the S3 PUT of seq %d the local journal differs (seq %d): the local atomic write must come first",
				seqOf(readJSON(t, data, "put body")), seqOf(readJSON(t, local, "local at put")))
		}
		seqs = append(seqs, seqOf(readJSON(t, data, "put body")))
	}
	a, b := newUnit("preflight"), newUnit("plan")
	if res := v.exec(pipe(a, b), engine.Options{}); res.Exit != 0 {
		t.Fatalf("Execute exit %d (%s %s)", res.Exit, res.Code, res.Message)
	}
	if len(seqs) < 3 {
		t.Fatalf("only %d journal writes for a two-phase run (at least: start, each phase), seqs %v", len(seqs), seqs)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("journal_seq of the S3 PUTs %v: each write must increment by exactly 1", seqs)
		}
	}
	if seqs[0] < 1 {
		t.Errorf("first journal_seq %d, want >= 1", seqs[0])
	}
	loc, rem := v.local(), v.remote()
	if seqOf(loc) != seqs[len(seqs)-1] || seqOf(rem) != seqs[len(seqs)-1] {
		t.Errorf("final seq local %d remote %d, want %d", seqOf(loc), seqOf(rem), seqs[len(seqs)-1])
	}
	lb, _ := os.ReadFile(v.localPath())
	if !bytes.Equal(lb, v.st.raw(journalKey)) {
		t.Errorf("local and S3 journal differ after a completed run")
	}
}

// Plan 50 section 17.3: with If-Match support the S3 PUT is conditional on the previous ETag; without it the run
// lock alone protects and no If-Match is sent.
func TestJournal_ConditionalPutChain_50AC9(t *testing.T) {
	for _, ifMatch := range []bool{true, false} {
		v := newEnv(t)
		v.st.ifMatch = ifMatch
		var last []byte
		v.st.onPut = func(key string, data []byte) {
			if key != journalKey {
				return
			}
			ops := v.st.opsFor(journalKey)
			cur := ops[len(ops)-1]
			if ifMatch && last != nil && cur.IfMatch != etagOf(last) {
				t.Errorf("ifMatch=true: PUT carries If-Match %s, want the ETag %s of the previous journal body", cur.IfMatch, etagOf(last))
			}
			last = append([]byte(nil), data...)
		}
		a, b := newUnit("preflight"), newUnit("plan")
		if res := v.exec(pipe(a, b), engine.Options{}); res.Exit != 0 {
			t.Fatalf("ifMatch=%v: exit %d (%s %s)", ifMatch, res.Exit, res.Code, res.Message)
		}
		ops := v.st.opsFor(journalKey)
		if len(ops) < 3 {
			t.Fatalf("ifMatch=%v: %d journal ops", ifMatch, len(ops))
		}
		for i, o := range ops {
			if !ifMatch && o.IfMatch != "" {
				t.Errorf("ifMatch=false: op %d sent If-Match %q", i, o.IfMatch)
			}
			if ifMatch && i > 0 && o.IfMatch == "" {
				t.Errorf("ifMatch=true: op %d (%s) is not conditional on the previous ETag", i, o.Op)
			}
			if ifMatch && i > 0 && o.Op != "put" {
				t.Errorf("ifMatch=true: op %d is %s, want put", i, o.Op)
			}
		}
	}
}

// Before the bucket exists only the local journal and lock exist (plan 50 section 18).
func TestJournal_LocalOnlyWithoutStore_50AC9(t *testing.T) {
	v := newEnv(t)
	u := newUnit("preflight")
	res := v.exec(pipe(u), engine.Options{}, func(c *engine.Config) { c.Store = nil; c.IfMatch = false })
	if res.Exit != 0 {
		t.Fatalf("Execute exit %d (%s %s)", res.Exit, res.Code, res.Message)
	}
	j := v.local()
	if seqOf(j) < 1 || statusOf(t, j, "preflight") != "succeeded" {
		t.Errorf("local journal seq %d preflight %s", seqOf(j), statusOf(t, j, "preflight"))
	}
	if v.st.raw(journalKey) != nil || len(v.st.ops) != 0 {
		t.Errorf("the engine touched S3 although Store is nil")
	}
}

// Plan 50 section 17.3: on load the higher journal_seq wins; when S3 is higher, local is overwritten.
func TestJournal_RemoteHigherSeqWins_50AC6(t *testing.T) {
	v := newEnv(t)
	if res := v.exec(pipe(newUnit("preflight")), engine.Options{}); res.Exit != 0 {
		t.Fatalf("setup run exit %d", res.Exit)
	}
	rem := v.remote()
	base := seqOf(rem)
	rem["journal_seq"] = base + 2
	rem["facts"] = map[string]any{"seed": map[string]any{"commit": strings.Repeat("ab", 20)}}
	b, _ := json.MarshalIndent(rem, "", "  ")
	if _, err := v.st.Put(context.Background(), journalKey, b, ""); err != nil {
		t.Fatal(err)
	}
	if res := v.exec(pipe(newUnit("preflight")), engine.Options{}); res.Exit != 0 {
		t.Fatalf("run exit %d (%s %s)", res.Exit, res.Code, res.Message)
	}
	loc := v.local()
	if got := str(dig(loc, "facts", "seed", "commit")); got != strings.Repeat("ab", 20) {
		t.Errorf("local journal lacks the fact of the higher remote copy (another machine progressed): facts %v", loc["facts"])
	}
	if seqOf(loc) <= base+2 {
		t.Errorf("seq after the run %d, want > %d", seqOf(loc), base+2)
	}
}

// Plan 50 section 17.3: when local is higher (a failed remote write earlier, or the bucket was just created), local
// wins and S3 catches up.
func TestJournal_LocalHigherSeqWins_50AC6(t *testing.T) {
	v := newEnv(t)
	if res := v.exec(pipe(newUnit("preflight")), engine.Options{}); res.Exit != 0 {
		t.Fatalf("setup run exit %d", res.Exit)
	}
	loc := v.local()
	base := seqOf(loc)
	loc["journal_seq"] = base + 3
	loc["facts"] = map[string]any{"forge_init": map[string]any{"pushed_sha": strings.Repeat("cd", 20)}}
	b, _ := json.MarshalIndent(loc, "", "  ")
	if err := os.WriteFile(v.localPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if res := v.exec(pipe(newUnit("preflight")), engine.Options{}); res.Exit != 0 {
		t.Fatalf("run exit %d (%s %s)", res.Exit, res.Code, res.Message)
	}
	rem := v.remote()
	if got := str(dig(rem, "facts", "forge_init", "pushed_sha")); got != strings.Repeat("cd", 20) {
		t.Errorf("S3 journal lacks the fact of the higher local copy: facts %v", rem["facts"])
	}
	if seqOf(rem) <= base+3 {
		t.Errorf("remote seq %d, want > %d", seqOf(rem), base+3)
	}
}

// conflict builds the 50-AC-6 fixture: equal journal_seq, different content. It returns the local and remote bytes.
func conflict(t *testing.T, v *tenv) (local, remote []byte) {
	t.Helper()
	if res := v.exec(pipe(newUnit("preflight")), engine.Options{}); res.Exit != 0 {
		t.Fatalf("setup run exit %d", res.Exit)
	}
	remote, _ = os.ReadFile(v.localPath())
	j := readJSON(t, remote, "journal")
	j["updated_at"] = "2026-10-26T13:37:00Z"
	local, _ = json.MarshalIndent(j, "", "  ")
	if bytes.Equal(local, remote) {
		t.Fatal("fixture: copies are equal")
	}
	if err := os.WriteFile(v.localPath(), local, 0o600); err != nil {
		t.Fatal(err)
	}
	return local, remote
}

func conflictFiles(t *testing.T, dir string) (runID string, contents [][]byte) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "runs", "*", "journal-conflict", "*"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		contents = append(contents, b)
		runID = filepath.Base(filepath.Dir(filepath.Dir(f)))
	}
	return runID, contents
}

func hasContent(cs [][]byte, want []byte) bool {
	for _, c := range cs {
		if bytes.Equal(c, want) {
			return true
		}
	}
	return false
}

// 50-AC-6: `status` on a journal conflict exits 7 and writes both copies under runs/<id>/journal-conflict/.
func TestStatus_JournalConflictExits7AndWritesBothCopies_50AC6(t *testing.T) {
	for _, jsonOut := range []bool{false, true} {
		v := newEnv(t)
		local, remote := conflict(t, v)
		var out, errb bytes.Buffer
		exit := engine.Status(context.Background(), v.cfg(), jsonOut, &out, &errb)
		if exit != 7 {
			t.Fatalf("json=%v: status exit %d, want 7\nstdout: %s\nstderr: %s", jsonOut, exit, out.String(), errb.String())
		}
		if !strings.Contains(out.String()+errb.String(), "JOURNAL_CONFLICT") {
			t.Errorf("json=%v: output does not name JOURNAL_CONFLICT: %s %s", jsonOut, out.String(), errb.String())
		}
		runID, contents := conflictFiles(t, v.dir)
		if !runIDRe.MatchString(runID) {
			t.Errorf("json=%v: conflict copies sit under runs/%q, want runs/<run-id>/journal-conflict/", jsonOut, runID)
		}
		if len(contents) != 2 || !hasContent(contents, local) || !hasContent(contents, remote) {
			t.Errorf("json=%v: journal-conflict holds %d files; want exactly the local and the S3 copy byte for byte", jsonOut, len(contents))
		}
		// neither copy was overwritten
		if lb, _ := os.ReadFile(v.localPath()); !bytes.Equal(lb, local) {
			t.Errorf("json=%v: the local journal was modified", jsonOut)
		}
		if !bytes.Equal(v.st.raw(journalKey), remote) {
			t.Errorf("json=%v: the S3 journal was modified", jsonOut)
		}
	}
}

// 50-AC-6 and plan 50 section 15: a run (not only status) stops with exit 7 on the conflict and runs nothing.
func TestExecute_JournalConflictExits7_50AC6(t *testing.T) {
	v := newEnv(t)
	local, remote := conflict(t, v)
	u := newUnit("preflight")
	e := v.eng()
	res := e.Execute(context.Background(), pipe(u), engine.Options{})
	if res.Exit != 7 || res.Code != "JOURNAL_CONFLICT" {
		t.Fatalf("Execute exit %d code %q (%s), want 7 JOURNAL_CONFLICT", res.Exit, res.Code, res.Message)
	}
	if _, runs := u.counts(); runs != 0 {
		t.Errorf("the phase ran on an ambiguous journal")
	}
	id, contents := conflictFiles(t, v.dir)
	if id != e.RunID() || len(contents) != 2 || !hasContent(contents, local) || !hasContent(contents, remote) {
		t.Errorf("conflict copies under run %q (%d files), want both copies under runs/%s/journal-conflict/", id, len(contents), e.RunID())
	}
}

// Equal seq and equal content is no conflict.
func TestExecute_EqualSeqEqualContentIsNoConflict_50AC6(t *testing.T) {
	v := newEnv(t)
	if res := v.exec(pipe(newUnit("preflight")), engine.Options{}); res.Exit != 0 {
		t.Fatalf("setup exit %d", res.Exit)
	}
	before := seqOf(v.remote())
	u := newUnit("preflight")
	if res := v.exec(pipe(u), engine.Options{}); res.Exit != 0 {
		t.Errorf("rerun on identical copies: exit %d (%s %s), want 0", res.Exit, res.Code, res.Message)
	}
	if after := seqOf(v.remote()); after <= before {
		t.Errorf("journal_seq %d -> %d: the rerun did not continue the journal", before, after)
	}
	if got := len(v.remote()["runs"].([]any)); got != 2 {
		t.Errorf("runs has %d entries after two runs, want 2", got)
	}
}

// Plan 50 section 15: an S3 journal write that keeps failing fails the phase with JOURNAL_REMOTE_WRITE_FAILED
// (exit 7) and nothing further is mutated.
func TestJournal_RemoteWriteFailureStopsTheRun_Plan50S15(t *testing.T) {
	v := newEnv(t)
	a, b := newUnit("preflight"), newUnit("plan")
	var failed atomic.Int32
	v.st.failPut = func(key string) error {
		if key != journalKey {
			return nil
		}
		if _, ra := a.counts(); ra == 1 {
			failed.Add(1)
			return &store.Error{Kind: store.Unavailable, Msg: "503 SlowDown"}
		}
		return nil
	}
	res := v.exec(pipe(a, b), engine.Options{})
	if res.Exit != 7 || res.Code != "JOURNAL_REMOTE_WRITE_FAILED" {
		t.Fatalf("exit %d code %q (%s), want 7 JOURNAL_REMOTE_WRITE_FAILED", res.Exit, res.Code, res.Message)
	}
	if failed.Load() < 2 {
		t.Errorf("only %d failing write attempts: the write must be retried before giving up", failed.Load())
	}
	if _, rb := b.counts(); rb != 0 {
		t.Errorf("the next phase ran after the durable record failed")
	}
}

// The journal and the run entry (plan 50 section 17.3 and the journal contract).
func TestJournal_ContentOfASuccessfulRun_50AC9(t *testing.T) {
	v := newEnv(t)
	names := []string{"preflight", "plan", "provision", "configure"}
	var us []*unit
	for _, n := range names {
		us = append(us, newUnit(n))
	}
	e := v.eng()
	if res := e.Execute(context.Background(), pipe(us...), engine.Options{}); res.Exit != 0 {
		t.Fatalf("Execute exit %d (%s %s)", res.Exit, res.Code, res.Message)
	}
	j := v.remote()
	for k, want := range map[string]string{"schema_version": "v1alpha1", "record_type": "bootstrap_journal", "cluster": cluster, "install_uid": installUID} {
		if str(j[k]) != want {
			t.Errorf("%s = %q, want %q", k, j[k], want)
		}
	}
	if got := str(dig(j, "installation", "state")); got != "configured" {
		t.Errorf("installation.state %q, want configured (advances with plan, provision, configure)", got)
	}
	runs, _ := j["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs: %v, want one entry", j["runs"])
	}
	r := runs[0].(map[string]any)
	if str(r["run_id"]) != e.RunID() || str(r["outcome"]) != "succeeded" || r["exit_code"] != float64(0) || str(r["command"]) != "run" {
		t.Errorf("run entry %v, want run_id %s outcome succeeded exit_code 0 command run", r, e.RunID())
	}
	for _, n := range names {
		p := phaseJ(t, j, n)
		if str(p["status"]) != "succeeded" || p["attempt"] != float64(1) || str(p["last_run_id"]) != e.RunID() {
			t.Errorf("phase %s: %v, want succeeded attempt 1 last_run_id %s", n, p, e.RunID())
		}
	}
	journalSchemaValidate(t, j)
}
