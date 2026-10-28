package engine_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"strange.company/sc/internal/bootstrap/engine"
)

// S8a of F08: the engine carry-overs of the S3 reviews (progress.md, "Review-2 non-blockers" and N3), pinned before the
// resume semantics of the install are built on them.

// --- review-2 (1): the attempt counter starts again after a success

// Plan 50 section 16: attempt >= 5 with the same error code and input digest refuses further automatic attempts. The
// count is of consecutive failures: a success ends the series. A unit that failed once, then succeeded, and then (its
// inputs changed) fails with the same code again gets five attempts of the new series, not three.
func TestEngine_AttemptCounterStartsAgainAfterSuccess_Plan50S16_S3R2(t *testing.T) {
	v := newEnv(t)
	u := newUnit("provision")
	u.runFn = fail("PROVIDER_DELIVERY_TIMEOUT", false)
	if res := v.exec(pipe(u), engine.Options{}); res.Exit != 1 || res.Code != "PROVIDER_DELIVERY_TIMEOUT" {
		t.Fatalf("run 1: exit %d code %q, want 1 PROVIDER_DELIVERY_TIMEOUT", res.Exit, res.Code)
	}
	u.runFn = nil
	if res := v.exec(pipe(u), engine.Options{}); res.Exit != 0 {
		t.Fatalf("run 2: exit %d code %q (%s), want the success that ends the first series", res.Exit, res.Code, res.Message)
	}

	u.setInputs(map[string]any{"phase": "provision", "v": 2})
	u.setDone(false)
	u.runFn = fail("PROVIDER_DELIVERY_TIMEOUT", false)
	for k := 1; k <= 5; k++ {
		res := v.exec(pipe(u), engine.Options{})
		if res.Exit != 1 || res.Code != "PROVIDER_DELIVERY_TIMEOUT" {
			t.Fatalf("failure %d of the new series: exit %d code %q, want 1 PROVIDER_DELIVERY_TIMEOUT (the ceiling is five consecutive failures after the last success)", k, res.Exit, res.Code)
		}
	}
	res := v.exec(pipe(u), engine.Options{})
	if res.Exit != 1 || res.Code != "RETRY_CEILING" {
		t.Errorf("sixth run of the new series: exit %d code %q, want 1 RETRY_CEILING", res.Exit, res.Code)
	}
	if _, runs := u.counts(); runs != 7 {
		t.Errorf("Run called %d times, want 7 (one failure, one success, five failures; the last run is refused)", runs)
	}
}

// --- review-2 (2): a unit whose Check or Inputs fails logs its own duration and attempt

// probeUnit is a stepless phase whose Inputs or Check fails.
type probeUnit struct {
	name                string
	inputsErr, checkErr error
	ran                 bool
}

func (u *probeUnit) Name() string { return u.name }
func (u *probeUnit) Inputs(context.Context) (any, error) {
	if u.inputsErr != nil {
		return nil, u.inputsErr
	}
	return map[string]any{"phase": u.name}, nil
}
func (u *probeUnit) Check(context.Context) (engine.Check, error) {
	return engine.Check{State: engine.NotDone}, u.checkErr
}
func (u *probeUnit) Run(context.Context) ([]engine.OutputRef, error) {
	u.ran = true
	return nil, nil
}
func (u *probeUnit) Timeout() time.Duration { return time.Minute }
func (u *probeUnit) Retryable(error) bool   { return false }
func (u *probeUnit) Steps() []engine.Unit   { return nil }

func logRecords(t testing.TB, dir, runID string) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "runs", runID, "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("log line is not JSON: %v: %s", err, sc.Text())
		}
		out = append(out, rec)
	}
	return out
}

// Plan 50 section 17.3 (structured log: duration_ms and attempt belong to the unit the record is about): the previous
// unit ran seven seconds of injected time; the unit after it fails before it starts, so its unit-failed record carries
// duration 0 and the attempt that failed, not the previous unit's duration and attempt 0.
func TestEngine_CheckAndInputsFailureLogTheUnitsOwnDurationAndAttempt_S3R2(t *testing.T) {
	for name, bad := range map[string]*probeUnit{
		"check fails": {name: "b", checkErr: errors.New("the world cannot be read")},
		"inputs fail": {name: "b", inputsErr: errors.New("the inputs cannot be computed")},
	} {
		t.Run(name, func(t *testing.T) {
			v := newEnv(t)
			a := newUnit("a")
			a.runFn = func(*unit) ([]engine.OutputRef, error) {
				v.clk.Advance(7 * time.Second)
				return nil, nil
			}
			e := v.eng()
			res := e.Execute(context.Background(), engine.Pipeline{Kind: "install", Phases: []engine.Phase{a, bad}}, engine.Options{})
			if res.Exit != 1 {
				t.Fatalf("exit %d code %q (%s), want 1", res.Exit, res.Code, res.Message)
			}
			if bad.ran {
				t.Fatal("the unit ran although its probe failed")
			}
			var rec map[string]any
			for _, r := range logRecords(t, v.dir, e.RunID()) {
				if r["event"] == "unit-failed" && r["phase"] == "b" {
					rec = r
				}
			}
			if rec == nil {
				t.Fatal("no unit-failed record for phase b in the run log")
			}
			if d, ok := rec["duration_ms"].(float64); !ok || d != 0 {
				t.Errorf("unit-failed duration_ms = %v, want 0: the unit never started (the previous unit took 7000)", rec["duration_ms"])
			}
			if n, ok := rec["attempt"].(float64); !ok || n < 1 {
				t.Errorf("unit-failed attempt = %v, want 1 or more: the failed attempt of this unit, not 0", rec["attempt"])
			}
		})
	}
}

// --- review-2 (3): the first journal conflict found while the run ends

// Plan 50 sections 15 and 17.3: a journal conflict is exit 7 wherever it is found. Here nothing saves between the
// other writer's PUT (during the unit's Check) and the end of the run: the unit is refused by the retry ceiling, and
// the save that ends the run is the first to meet the conflict.
func TestEngine_FirstConflictAtTheEndOfAFailedRunExits7_50AC6_S3R2(t *testing.T) {
	v := newEnv(t)
	v.st.ifMatch = true
	u := newUnit("provision")
	failFive(t, v, u)

	var other []byte
	u.checkFn = func(*unit) engine.Check {
		j := v.remote()
		j["journal_seq"] = float64(seqOf(j) + 1)
		j["facts"] = map[string]any{"other_writer": "wins"}
		other, _ = json.MarshalIndent(j, "", "  ")
		if _, err := v.st.Put(context.Background(), journalKey, other, ""); err != nil {
			t.Fatal(err)
		}
		return engine.Check{State: engine.NotDone}
	}
	res := v.exec(pipe(u), engine.Options{})
	if res.Exit != 7 || res.Code != "JOURNAL_CONFLICT" {
		t.Errorf("sixth run with another writer: exit %d code %q (%s), want 7 JOURNAL_CONFLICT", res.Exit, res.Code, res.Message)
	}
	if _, runs := u.counts(); runs != 5 {
		t.Errorf("Run called %d times, want 5 (the ceiling still refuses the unit)", runs)
	}
	if other == nil || !bytes.Equal(v.st.raw(journalKey), other) {
		t.Errorf("the other writer's S3 journal was modified")
	}
}

// --- N3: the phase timeout runs on the injected clock

// timedUnit is a stepless phase whose Run is the given function.
type timedUnit struct {
	name    string
	timeout time.Duration
	run     func(ctx context.Context) error
}

func (u *timedUnit) Name() string                        { return u.name }
func (u *timedUnit) Inputs(context.Context) (any, error) { return map[string]any{"phase": u.name}, nil }
func (u *timedUnit) Check(context.Context) (engine.Check, error) {
	return engine.Check{State: engine.NotDone}, nil
}
func (u *timedUnit) Run(ctx context.Context) ([]engine.OutputRef, error) { return nil, u.run(ctx) }
func (u *timedUnit) Timeout() time.Duration                              { return u.timeout }
func (u *timedUnit) Retryable(error) bool                                { return false }
func (u *timedUnit) Steps() []engine.Unit                                { return nil }

// Plan 50 section 16 (PHASE_TIMEOUT) and plan 52 D8 (no wall clock in worker code): the timeout of a unit is measured
// on the injected clock, not on the wall clock.
func TestEngine_PhaseTimeoutRunsOnTheInjectedClock_Plan50S16_S3N3(t *testing.T) {
	t.Run("a wall-clock instant is not a timeout", func(t *testing.T) {
		// One nanosecond of wall time has passed before Run looks at its context; the injected clock has not moved.
		v := newEnv(t)
		u := &timedUnit{name: "provision", timeout: time.Nanosecond, run: func(ctx context.Context) error { return ctx.Err() }}
		res := v.exec(engine.Pipeline{Kind: "install", Phases: []engine.Phase{u}}, engine.Options{})
		if res.Exit != 0 {
			t.Errorf("exit %d code %q (%s), want 0: the injected clock did not advance, so the unit did not time out", res.Exit, res.Code, res.Message)
		}
	})
	t.Run("time the injected clock passed is", func(t *testing.T) {
		v := newEnv(t)
		u := &timedUnit{name: "provision", timeout: 10 * time.Minute, run: func(context.Context) error {
			v.clk.Advance(11 * time.Minute)
			return errors.New("the operation was abandoned")
		}}
		res := v.exec(engine.Pipeline{Kind: "install", Phases: []engine.Phase{u}}, engine.Options{})
		if res.Exit != 1 || res.Code != "PHASE_TIMEOUT" {
			t.Errorf("exit %d code %q (%s), want 1 PHASE_TIMEOUT: the injected clock passed 11 minutes of a 10 minute timeout", res.Exit, res.Code, res.Message)
		}
	})
	t.Run("a failure inside the timeout stays itself", func(t *testing.T) {
		v := newEnv(t)
		u := &timedUnit{name: "provision", timeout: 10 * time.Minute, run: func(context.Context) error {
			v.clk.Advance(9 * time.Minute)
			return errors.New("the operation failed")
		}}
		res := v.exec(engine.Pipeline{Kind: "install", Phases: []engine.Phase{u}}, engine.Options{})
		if res.Exit != 1 || res.Code != "PHASE_FAILED" {
			t.Errorf("exit %d code %q (%s), want 1 PHASE_FAILED", res.Exit, res.Code, res.Message)
		}
	})
}
