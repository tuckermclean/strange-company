package engine_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"strange.company/sc/internal/bootstrap/engine"
)

// Plan 50 section 17.2: input_digest is the sha256 over the canonical JSON of the phase's declared inputs.
func TestEngine_InputDigestIsSha256OfCanonicalInputs_Plan50S17_2(t *testing.T) {
	v := newEnv(t)
	plan := newUnit("plan")
	plan.inputs = map[string]any{"profile": "self-hosting-minimum", "catalogue": "2026.10", "tofu_tree": "4b825dc642cb6eb9a060e54bf8d69288fbee4904"}
	prov := newUnit("provision")
	prov.inputs = map[string]any{"plan_digest": "sha256:" + strings.Repeat("0", 64)}
	stepped := newUnit("configure", newUnit("configure.k3s"))
	stepped.steps[0].(*unit).inputs = map[string]any{"nodes": []any{"10.0.0.1", "10.0.0.2"}}
	if res := v.exec(pipe(plan, prov, stepped), engine.Options{}); res.Exit != 0 {
		t.Fatalf("exit %d (%s %s)", res.Exit, res.Code, res.Message)
	}
	j := v.remote()
	for _, u := range []*unit{plan, prov, stepped} {
		if got, want := str(phaseJ(t, j, u.name)["input_digest"]), digestOf(u.inputs); got != want {
			t.Errorf("phase %s input_digest %q, want %q", u.name, got, want)
		}
	}
	step := stepJ(t, phaseJ(t, j, "configure"), "configure.k3s")
	if got, want := str(step["input_digest"]), digestOf(stepped.steps[0].(*unit).inputs); got != want {
		t.Errorf("step input_digest %q, want %q", got, want)
	}
}

// Plan 50 section 8.1: Check first; Done with no prior record is recorded as succeeded without calling Run.
func TestEngine_CheckDoneRecordedSucceededWithoutRun_Plan50S8_1(t *testing.T) {
	v := newEnv(t)
	u := newUnit("provision")
	u.done = true
	if res := v.exec(pipe(u), engine.Options{}); res.Exit != 0 {
		t.Fatalf("exit %d (%s %s)", res.Exit, res.Code, res.Message)
	}
	if c, r := u.counts(); r != 0 || c == 0 {
		t.Errorf("checks %d runs %d, want Check consulted and Run never called", c, r)
	}
	if got := statusOf(t, v.remote(), "provision"); got != "succeeded" {
		t.Errorf("status %q, want succeeded", got)
	}
}

// Plan 50 section 17.1 rule 3: a succeeded phase with an unchanged digest is a no-op recorded as skipped.
func TestEngine_RerunWithUnchangedDigestIsSkipped_Plan50S17_1(t *testing.T) {
	v := newEnv(t)
	us := []*unit{newUnit("preflight"), newUnit("plan"), newUnit("provision")}
	// preflight is always re-run (plan 50 section 14.2): its world is never done
	us[0].checkFn = func(*unit) engine.Check { return engine.Check{State: engine.NotDone} }
	if res := v.exec(pipe(us...), engine.Options{}); res.Exit != 0 {
		t.Fatalf("first run exit %d", res.Exit)
	}
	if res := v.exec(pipe(us...), engine.Options{}); res.Exit != 0 {
		t.Fatalf("rerun exit %d (%s %s)", res.Exit, res.Code, res.Message)
	}
	if got := runsOf(us...); !eqInts(got, []int{2, 1, 1}) {
		t.Errorf("Run counts %v, want [2 1 1] (only the always-run preflight runs again)", got)
	}
	j := v.remote()
	for _, n := range []string{"plan", "provision"} {
		if got := statusOf(t, j, n); got != "skipped" {
			t.Errorf("phase %s status %q on the rerun, want skipped", n, got)
		}
	}
}

// Plan 50 section 17.1 rule 4: a changed digest on a succeeded phase sets it to pending while installation.state
// has not advanced past it, and it runs again.
func TestEngine_DigestChangeBeforeAdvanceRerunsPhase_Plan50S17_1(t *testing.T) {
	v := newEnv(t)
	plan := newUnit("plan")
	if res := v.exec(pipe(plan), engine.Options{}); res.Exit != 0 {
		t.Fatalf("first run exit %d", res.Exit)
	}
	plan.setInputs(map[string]any{"phase": "plan", "v": 2})
	plan.setDone(false)
	if res := v.exec(pipe(plan), engine.Options{}); res.Exit != 0 {
		t.Fatalf("rerun exit %d (%s %s)", res.Exit, res.Code, res.Message)
	}
	if _, r := plan.counts(); r != 2 {
		t.Errorf("plan ran %d times, want 2", r)
	}
	p := phaseJ(t, v.remote(), "plan")
	if str(p["input_digest"]) != digestOf(plan.inputs) || p["attempt"] != float64(2) || str(p["status"]) != "succeeded" {
		t.Errorf("plan journal entry %v, want the new digest, attempt 2, succeeded", p)
	}
}

// Plan 50 section 17.1 rule 4 (BOOT-2): once a later phase consumed the output, Check()=Done wins over a digest
// change: the drift is reported, not re-run.
func TestEngine_DigestChangeAfterAdvanceCheckDoneWins_Plan50S17_1(t *testing.T) {
	v := newEnv(t)
	plan, prov := newUnit("plan"), newUnit("provision")
	if res := v.exec(pipe(plan, prov), engine.Options{}); res.Exit != 0 {
		t.Fatalf("first run exit %d", res.Exit)
	}
	plan.setInputs(map[string]any{"phase": "plan", "v": 2}) // the world (plan outputs) is still there: Check says Done
	res := v.exec(pipe(plan, prov), engine.Options{})
	if res.Exit != 0 {
		t.Fatalf("rerun exit %d (%s %s)", res.Exit, res.Code, res.Message)
	}
	if got := runsOf(plan, prov); !eqInts(got, []int{1, 1}) {
		t.Errorf("Run counts %v, want [1 1]", got)
	}
	found := false
	for _, d := range res.Drift {
		found = found || d == "plan"
	}
	if !found {
		t.Errorf("Result.Drift %v does not report the drifted phase plan", res.Drift)
	}
	if got := statusOf(t, v.remote(), "plan"); got != "succeeded" && got != "skipped" {
		t.Errorf("plan status %q, want succeeded or skipped (not pending)", got)
	}
}

// Plan 50 section 8.1: Drifted causes a re-run for idempotent phases.
func TestEngine_DriftedPhaseIsRerun_Plan50S8_1(t *testing.T) {
	v := newEnv(t)
	cfgu := newUnit("configure")
	cfgu.checkFn = func(u *unit) engine.Check {
		if u.runs == 0 {
			return engine.Check{State: engine.Drifted, Reason: "node-02 missing from the inventory"}
		}
		return engine.Check{State: engine.Done}
	}
	if res := v.exec(pipe(cfgu), engine.Options{}); res.Exit != 0 {
		t.Fatalf("exit %d (%s %s)", res.Exit, res.Code, res.Message)
	}
	if _, r := cfgu.counts(); r != 1 {
		t.Errorf("Run called %d times for a drifted phase, want 1", r)
	}
}

// A permanent failure stops the pipeline: exit 1, the journal carries the error code and the later phases stay pending.
func TestEngine_PermanentFailureStopsPipeline_Plan50S7_2(t *testing.T) {
	v := newEnv(t)
	plan, prov, cfgu := newUnit("plan"), newUnit("provision"), newUnit("configure")
	prov.runFn = fail("PROVIDER_DELIVERY_TIMEOUT", false)
	e := v.eng()
	res := e.Execute(context.Background(), pipe(plan, prov, cfgu), engine.Options{})
	if res.Exit != 1 || res.Code != "PROVIDER_DELIVERY_TIMEOUT" {
		t.Fatalf("exit %d code %q, want 1 PROVIDER_DELIVERY_TIMEOUT", res.Exit, res.Code)
	}
	if got := runsOf(plan, prov, cfgu); !eqInts(got, []int{1, 1, 0}) {
		t.Errorf("Run counts %v, want [1 1 0]", got)
	}
	j := v.remote()
	p := phaseJ(t, j, "provision")
	if str(p["status"]) != "failed" || p["attempt"] != float64(1) || str(dig(p, "error", "code")) != "PROVIDER_DELIVERY_TIMEOUT" || dig(p, "error", "retryable") != false {
		t.Errorf("provision journal entry %v, want failed, attempt 1, error PROVIDER_DELIVERY_TIMEOUT retryable false", p)
	}
	if got := statusOf(t, j, "configure"); got != "pending" {
		t.Errorf("configure status %q, want pending", got)
	}
	if got := str(dig(j, "installation", "state")); got != "planned" {
		t.Errorf("installation.state %q, want planned (it advances only when the phase succeeds)", got)
	}
	r := j["runs"].([]any)[0].(map[string]any)
	if str(r["outcome"]) != "failed" || r["exit_code"] != float64(1) {
		t.Errorf("run entry %v, want outcome failed exit_code 1", r)
	}
}

// Plan 50 section 16: transient errors are retried in-run with exponential backoff 5 s, 10 s, 20 s, 40 s, at most 5
// attempts per step per run.
func TestEngine_TransientRetryBackoff_Plan50S16(t *testing.T) {
	t.Run("succeeds on the third attempt", func(t *testing.T) {
		v := newEnv(t)
		u := newUnit("provision")
		u.runFn = func(u *unit) ([]engine.OutputRef, error) {
			if u.runs < 3 {
				return nil, &engine.Error{Code: "S3_UNAVAILABLE", Message: "503 SlowDown", Retryable: true}
			}
			return nil, nil
		}
		start := v.clk.Now()
		if res := v.exec(pipe(u), engine.Options{}); res.Exit != 0 {
			t.Fatalf("exit %d (%s %s)", res.Exit, res.Code, res.Message)
		}
		if _, r := u.counts(); r != 3 {
			t.Errorf("Run called %d times, want 3", r)
		}
		if d := v.clk.Now().Sub(start); d != 15*time.Second {
			t.Errorf("clock advanced %v, want 5s + 10s of backoff", d)
		}
		if p := phaseJ(t, v.remote(), "provision"); p["attempt"] != float64(1) {
			t.Errorf("attempt %v, want 1 (in-run retries do not increment the cross-run attempt)", p["attempt"])
		}
	})
	t.Run("gives up after five attempts", func(t *testing.T) {
		v := newEnv(t)
		u := newUnit("provision")
		u.runFn = fail("S3_UNAVAILABLE", true)
		start := v.clk.Now()
		res := v.exec(pipe(u), engine.Options{})
		if res.Exit != 1 || res.Code != "S3_UNAVAILABLE" {
			t.Fatalf("exit %d code %q, want 1 S3_UNAVAILABLE", res.Exit, res.Code)
		}
		if _, r := u.counts(); r != 5 {
			t.Errorf("Run called %d times, want 5", r)
		}
		if d := v.clk.Now().Sub(start); d != 75*time.Second {
			t.Errorf("clock advanced %v, want 5+10+20+40 = 75s of backoff", d)
		}
		if got := dig(phaseJ(t, v.remote(), "provision"), "error", "retryable"); got != true {
			t.Errorf("journal error.retryable %v, want true", got)
		}
	})
	t.Run("permanent errors are not retried", func(t *testing.T) {
		v := newEnv(t)
		u := newUnit("provision")
		u.runFn = fail("GUARD_REFUSED", false)
		start := v.clk.Now()
		if res := v.exec(pipe(u), engine.Options{}); res.Exit != 1 {
			t.Fatalf("exit %d", res.Exit)
		}
		if _, r := u.counts(); r != 1 || v.clk.Now() != start {
			t.Errorf("Run called %d times, clock moved %v; a permanent error must run once and not wait", r, v.clk.Now().Sub(start))
		}
	})
}

// failFive runs a permanently failing phase in five separate invocations.
func failFive(t *testing.T, v *tenv, u *unit) {
	t.Helper()
	u.runFn = fail("PROVIDER_DELIVERY_TIMEOUT", false)
	for i := 1; i <= 5; i++ {
		res := v.exec(pipe(u), engine.Options{})
		if res.Exit != 1 || res.Code != "PROVIDER_DELIVERY_TIMEOUT" {
			t.Fatalf("run %d: exit %d code %q, want 1 PROVIDER_DELIVERY_TIMEOUT", i, res.Exit, res.Code)
		}
	}
	if p := phaseJ(t, v.remote(), "provision"); p["attempt"] != float64(5) {
		t.Fatalf("attempt %v after five failing runs, want 5", p["attempt"])
	}
}

// Plan 50 section 16: attempt >= 5 with the same error code and input digest refuses further automatic attempts
// with RETRY_CEILING (exit 1) until the digest changes or the operator passes --retry-phase.
func TestEngine_RetryCeiling_Plan50S16(t *testing.T) {
	t.Run("refused after five attempts", func(t *testing.T) {
		v := newEnv(t)
		u := newUnit("provision")
		failFive(t, v, u)
		res := v.exec(pipe(u), engine.Options{})
		if res.Exit != 1 || res.Code != "RETRY_CEILING" {
			t.Errorf("sixth run: exit %d code %q, want 1 RETRY_CEILING", res.Exit, res.Code)
		}
		if _, r := u.counts(); r != 5 {
			t.Errorf("Run called %d times, want 5 (the sixth is refused)", r)
		}
	})
	t.Run("--retry-phase resets the ceiling only", func(t *testing.T) {
		v := newEnv(t)
		u := newUnit("provision")
		failFive(t, v, u)
		res := v.exec(pipe(u), engine.Options{RetryPhase: "provision"})
		if res.Code == "RETRY_CEILING" {
			t.Fatalf("--retry-phase provision still hit the ceiling")
		}
		if _, r := u.counts(); r != 6 {
			t.Errorf("Run called %d times, want 6 (retry runs Check and Run again)", r)
		}
		if res.Exit != 1 || res.Code != "PROVIDER_DELIVERY_TIMEOUT" {
			t.Errorf("exit %d code %q, want the phase's own failure (the flag never marks anything done)", res.Exit, res.Code)
		}
	})
	t.Run("a changed digest lifts it", func(t *testing.T) {
		v := newEnv(t)
		u := newUnit("provision")
		failFive(t, v, u)
		u.setInputs(map[string]any{"phase": "provision", "v": 2})
		res := v.exec(pipe(u), engine.Options{})
		if res.Code == "RETRY_CEILING" {
			t.Errorf("a changed input digest must lift the ceiling")
		}
		if _, r := u.counts(); r != 6 {
			t.Errorf("Run called %d times, want 6", r)
		}
	})
	t.Run("unknown phase for --retry-phase is a usage error", func(t *testing.T) {
		v := newEnv(t)
		if res := v.exec(pipe(newUnit("provision")), engine.Options{RetryPhase: "nope"}); res.Exit != 2 {
			t.Errorf("exit %d, want 2", res.Exit)
		}
	})
}

// Plan 50 section 7.1: --from-phase re-runs the named phase and every later one: their Check probes run again and
// their status is reset to pending. It never marks anything done (50-R-2).
func TestEngine_FromPhase_Plan50S7_1(t *testing.T) {
	setup := func(t *testing.T) (*tenv, []*unit) {
		v := newEnv(t)
		us := []*unit{newUnit("plan"), newUnit("provision"), newUnit("configure")}
		if res := v.exec(pipe(us...), engine.Options{}); res.Exit != 0 {
			t.Fatalf("setup exit %d", res.Exit)
		}
		return v, us
	}
	t.Run("world intact: probes run again, nothing re-runs", func(t *testing.T) {
		v, us := setup(t)
		c0 := make([]int, len(us))
		for i, u := range us {
			c0[i], _ = u.counts()
		}
		if res := v.exec(pipe(us...), engine.Options{FromPhase: "provision"}); res.Exit != 0 {
			t.Fatalf("exit %d (%s %s)", res.Exit, res.Code, res.Message)
		}
		for i, u := range us[1:] {
			if c, _ := u.counts(); c <= c0[i+1] {
				t.Errorf("%s: Check not consulted again (%d -> %d)", u.name, c0[i+1], c)
			}
		}
		if got := runsOf(us...); !eqInts(got, []int{1, 1, 1}) {
			t.Errorf("Run counts %v, want [1 1 1]: --from-phase never marks anything done nor forces a run", got)
		}
		j := v.remote()
		if got := statusOf(t, j, "plan"); got != "skipped" {
			t.Errorf("plan (before the named phase) status %q, want skipped", got)
		}
		for _, n := range []string{"provision", "configure"} {
			if got := statusOf(t, j, n); got != "succeeded" {
				t.Errorf("%s status %q after Check=Done, want succeeded", n, got)
			}
		}
	})
	t.Run("world lost: the named phase runs again", func(t *testing.T) {
		v, us := setup(t)
		us[1].setDone(false)
		if res := v.exec(pipe(us...), engine.Options{FromPhase: "provision"}); res.Exit != 0 {
			t.Fatalf("exit %d", res.Exit)
		}
		if got := runsOf(us...); !eqInts(got, []int{1, 2, 1}) {
			t.Errorf("Run counts %v, want [1 2 1]", got)
		}
	})
	t.Run("unknown phase", func(t *testing.T) {
		v, us := setup(t)
		if res := v.exec(pipe(us...), engine.Options{FromPhase: "nope"}); res.Exit != 2 {
			t.Errorf("exit %d, want 2", res.Exit)
		}
	})
}

// freezePipe is the install pipeline up to control-transfer with stubbed post-transfer phases (50-AC-16).
type freezePipe struct {
	us       map[string]*unit
	order    []string
	switched atomic.Bool
	verifyN  atomic.Int32
}

func newFreezePipe() *freezePipe {
	f := &freezePipe{us: map[string]*unit{}}
	f.order = []string{"preflight", "plan", "provision", "configure", "seed", "forge-init", "flux-bootstrap", "verify", "control-transfer"}
	for _, n := range f.order {
		f.us[n] = newUnit(n)
	}
	always := func(*unit) engine.Check { return engine.Check{State: engine.NotDone} }
	f.us["preflight"].checkFn = always
	f.us["verify"].checkFn = always
	f.us["flux-bootstrap"].runFn = func(*unit) ([]engine.OutputRef, error) { f.switched.Store(true); return nil, nil }
	f.us["verify"].runFn = func(*unit) ([]engine.OutputRef, error) {
		if f.verifyN.Add(1) == 1 {
			return nil, &engine.Error{Code: "VERIFY_REQUIRED_CHECK_FAILED", Message: "V-05 failed", Retryable: false}
		}
		return nil, nil
	}
	return f
}

func (f *freezePipe) pipeline() engine.Pipeline {
	var us []*unit
	for _, n := range f.order {
		us = append(us, f.us[n])
	}
	return pipe(us...)
}

func (f *freezePipe) cfg(c *engine.Config) {
	c.SourceSwitched = func(context.Context) (bool, error) { return f.switched.Load(), nil }
}

func (f *freezePipe) allCounts() map[string][2]int {
	m := map[string][2]int{}
	for n, u := range f.us {
		c, r := u.counts()
		m[n] = [2]int{c, r}
	}
	return m
}

// 50-AC-16 (unit level, plan 50 section 17.1 rule 5): after the switch seed, forge-init and flux-bootstrap are
// skipped on the rerun where verify fails once, and --from-phase refuses them with PHASE_FROZEN.
func TestFreeze_SkipsSwitchedPhasesAndRefusesFromPhase_50AC16(t *testing.T) {
	v := newEnv(t)
	f := newFreezePipe()
	res := v.exec(f.pipeline(), engine.Options{}, f.cfg)
	if res.Exit != 1 || res.Code != "VERIFY_REQUIRED_CHECK_FAILED" {
		t.Fatalf("first run: exit %d code %q, want 1 VERIFY_REQUIRED_CHECK_FAILED", res.Exit, res.Code)
	}
	if got := str(dig(v.remote(), "installation", "state")); got != "flux-sourced" {
		t.Errorf("installation.state %q after the failed verify, want flux-sourced", got)
	}
	res = v.exec(f.pipeline(), engine.Options{}, f.cfg)
	if res.Exit != 0 {
		t.Fatalf("rerun: exit %d (%s %s), want 0", res.Exit, res.Code, res.Message)
	}
	j := v.remote()
	for _, n := range []string{"seed", "forge-init", "flux-bootstrap"} {
		if got := statusOf(t, j, n); got != "skipped" {
			t.Errorf("rerun: phase %s status %q, want skipped", n, got)
		}
		if _, r := f.us[n].counts(); r != 1 {
			t.Errorf("phase %s ran %d times, want 1", n, r)
		}
	}
	for _, n := range []string{"verify", "control-transfer"} {
		if got := statusOf(t, j, n); got != "succeeded" {
			t.Errorf("rerun: phase %s status %q, want succeeded", n, got)
		}
	}
	if got := str(dig(j, "installation", "state")); got != "transferred" {
		t.Errorf("installation.state %q, want transferred", got)
	}
	// --from-phase seed | forge-init | flux-bootstrap is refused before anything runs
	for _, n := range []string{"seed", "forge-init", "flux-bootstrap"} {
		before := f.allCounts()
		res = v.exec(f.pipeline(), engine.Options{FromPhase: n}, f.cfg)
		if res.Exit != 2 || res.Code != "PHASE_FROZEN" {
			t.Errorf("--from-phase %s: exit %d code %q, want 2 PHASE_FROZEN", n, res.Exit, res.Code)
		}
		after := f.allCounts()
		for k, b := range before {
			if after[k] != b {
				t.Errorf("--from-phase %s: %s was probed or run (%v -> %v) although the request is refused", n, k, b, after[k])
			}
		}
	}
	// a phase after the frozen ones may be re-run
	if res = v.exec(f.pipeline(), engine.Options{FromPhase: "verify"}, f.cfg); res.Exit != 0 {
		t.Errorf("--from-phase verify: exit %d (%s), want 0", res.Exit, res.Code)
	}
}

// Plan 50 section 17.1 rule 5: from the switch onward the engine never re-runs seed, forge-init or flux-bootstrap,
// even when their Check says NotDone and their inputs changed. The switch is read from reality (the root's sourceRef)
// or from the journal (switching/switched).
func TestFreeze_NeverReRunsFrozenPhases_50AC16(t *testing.T) {
	for _, via := range []string{"reality", "journal"} {
		t.Run(via, func(t *testing.T) {
			v := newEnv(t)
			f := newFreezePipe()
			f.verifyN.Store(1) // verify passes
			if res := v.exec(f.pipeline(), engine.Options{}, f.cfg); res.Exit != 0 {
				t.Fatalf("setup exit %d (%s %s)", res.Exit, res.Code, res.Message)
			}
			cfgMod := f.cfg
			if via == "journal" {
				f.switched.Store(false)
				j := v.remote()
				j["journal_seq"] = seqOf(j) + 1
				j["facts"] = map[string]any{"transfer": map[string]any{"switching_at": "2026-10-26T12:00:00Z"}}
				v.writeBoth(j)
			}
			for _, n := range []string{"seed", "forge-init", "flux-bootstrap"} {
				f.us[n].setDone(false)
				f.us[n].setInputs(map[string]any{"changed": n})
			}
			if res := v.exec(f.pipeline(), engine.Options{}, cfgMod); res.Exit != 0 {
				t.Fatalf("rerun exit %d (%s %s), want 0", res.Exit, res.Code, res.Message)
			}
			for _, n := range []string{"seed", "forge-init", "flux-bootstrap"} {
				if _, r := f.us[n].counts(); r != 1 {
					t.Errorf("%s ran %d times, want 1: it is frozen", n, r)
				}
				if got := statusOf(t, v.remote(), n); got != "skipped" {
					t.Errorf("%s status %q, want skipped", n, got)
				}
			}
			if res := v.exec(f.pipeline(), engine.Options{FromPhase: "seed"}, cfgMod); res.Exit != 2 || res.Code != "PHASE_FROZEN" {
				t.Errorf("--from-phase seed: exit %d code %q, want 2 PHASE_FROZEN", res.Exit, res.Code)
			}
		})
	}
}

// Before the switch, --from-phase seed is fine.
func TestFreeze_FromPhaseSeedAllowedBeforeSwitch_50AC16(t *testing.T) {
	v := newEnv(t)
	f := newFreezePipe()
	f.verifyN.Store(1)
	ps := f.pipeline()
	ps.Phases = ps.Phases[:5] // up to and including seed
	if res := v.exec(ps, engine.Options{}, f.cfg); res.Exit != 0 {
		t.Fatalf("setup exit %d", res.Exit)
	}
	f.us["seed"].setDone(false)
	if res := v.exec(ps, engine.Options{FromPhase: "seed"}, f.cfg); res.Exit != 0 {
		t.Errorf("--from-phase seed before the switch: exit %d (%s %s), want 0", res.Exit, res.Code, res.Message)
	}
	if _, r := f.us["seed"].counts(); r != 2 {
		t.Errorf("seed ran %d times, want 2", r)
	}
}

// Plan 50 section 14.7: on a transferred install the default run does preflight and verify only; a drifted earlier
// phase is not auto-repaired.
func TestTransferred_OnlyPreflightAndVerifyRun_Plan50S14_7(t *testing.T) {
	v := newEnv(t)
	f := newFreezePipe()
	f.verifyN.Store(1)
	if res := v.exec(f.pipeline(), engine.Options{}, f.cfg); res.Exit != 0 {
		t.Fatalf("setup exit %d (%s %s)", res.Exit, res.Code, res.Message)
	}
	if got := str(dig(v.remote(), "installation", "state")); got != "transferred" {
		t.Fatalf("setup: state %q, want transferred", got)
	}
	before := f.allCounts()
	for _, n := range []string{"plan", "provision", "configure", "control-transfer"} {
		n := n
		f.us[n].checkFn = func(*unit) engine.Check { return engine.Check{State: engine.Drifted, Reason: "node-02 missing"} }
	}
	if res := v.exec(f.pipeline(), engine.Options{}, f.cfg); res.Exit != 0 {
		t.Fatalf("rerun exit %d (%s %s), want 0", res.Exit, res.Code, res.Message)
	}
	after := f.allCounts()
	for _, n := range f.order {
		r0, r1 := before[n][1], after[n][1]
		want := r0
		if n == "preflight" || n == "verify" {
			want = r0 + 1
		}
		if r1 != want {
			t.Errorf("phase %s ran %d times, want %d", n, r1, want)
		}
	}
}

// --- the crash hook (plan 50 section 22)

type killed struct{ spec string }

// crashPipeline is preflight, plan (steps plan.install-identity and plan.storage-apply), provision and configure.
func crashPipeline() (engine.Pipeline, map[string]*unit) {
	a1, a2 := newUnit("plan.install-identity"), newUnit("plan.storage-apply")
	us := []*unit{newUnit("preflight"), newUnit("plan", a1, a2), newUnit("provision"), newUnit("configure")}
	us[0].checkFn = func(*unit) engine.Check { return engine.Check{State: engine.NotDone} }
	by := map[string]*unit{}
	for _, u := range append(append([]*unit{}, us...), a1, a2) {
		by[u.name] = u
	}
	return pipe(us...), by
}

// seam: SC_TEST_CRASH_AT=<step>:<before|after> with SC_TEST_MODE=1 calls the kill function exactly once at that
// boundary; "before" means the step has not run, "after" means it has and the next one has not.
func TestCrashHook_FiresAtEveryStepBoundary_Plan50S22(t *testing.T) {
	order := []string{"preflight", "plan.install-identity", "plan.storage-apply", "provision", "configure"}
	for _, step := range order {
		for _, when := range []string{"before", "after"} {
			spec := step + ":" + when
			t.Run(spec, func(t *testing.T) {
				v := newEnv(t)
				pl, byName := crashPipeline()
				var fired int
				var snap map[string]int
				kill := func() {
					fired++
					snap = map[string]int{}
					for n, u := range byName {
						_, r := u.counts()
						snap[n] = r
					}
					panic(killed{spec})
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							if _, ok := r.(killed); !ok {
								panic(r)
							}
						}
					}()
					v.exec(pl, engine.Options{}, func(c *engine.Config) {
						c.Env = map[string]string{"SC_TEST_MODE": "1", "SC_TEST_CRASH_AT": spec}
						c.Kill = kill
					})
				}()
				if fired != 1 {
					t.Fatalf("kill fired %d times, want exactly 1", fired)
				}
				idx := -1
				for i, n := range order {
					if n == step {
						idx = i
					}
				}
				for i, n := range order {
					want := 0
					if i < idx || (i == idx && when == "after") {
						want = 1
					}
					if snap[n] != want {
						t.Errorf("at the %s boundary %s had run %d times, want %d", spec, n, snap[n], want)
					}
				}
			})
		}
	}
}

// The hook is inert without SC_TEST_MODE=1 and for a step the pipeline does not have.
func TestCrashHook_InertOutsideTestMode_Plan50S22(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"no test mode":   {"SC_TEST_CRASH_AT": "provision:before"},
		"test mode 0":    {"SC_TEST_MODE": "0", "SC_TEST_CRASH_AT": "provision:before"},
		"unknown step":   {"SC_TEST_MODE": "1", "SC_TEST_CRASH_AT": "nonexistent:before"},
		"no crash point": {"SC_TEST_MODE": "1"},
	} {
		v := newEnv(t)
		u := newUnit("provision")
		fired := 0
		res := v.exec(pipe(u), engine.Options{}, func(c *engine.Config) {
			c.Env = env
			c.Kill = func() { fired++ }
		})
		if fired != 0 || res.Exit != 0 {
			t.Errorf("%s: kill fired %d times, exit %d; want 0 and 0", name, fired, res.Exit)
		}
		if _, r := u.counts(); r != 1 {
			t.Errorf("%s: phase ran %d times, want 1", name, r)
		}
	}
}
