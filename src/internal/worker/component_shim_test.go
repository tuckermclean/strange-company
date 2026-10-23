package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/worker"
)

// Slice S4 of F07: the production shim (worker.Shim, plan 27 section 7.4) runs as a separate process, the real
// sc-worker pipeline talks to it through /workspace/.sc-shim, and real elapsed time is the subject (D5 allows
// clock.Real here). Tests cover framing (27-AC-3), timeout (27-AC-11), stdin (27-AC-15), the shim protocol
// (27-AC-16), cancellation through the heartbeat (plan 27 section 14.7 step 4) and the shim directory (S3-F8).
//
// The shim must keep polling until `done` appears: it is started before sc-worker has created .sc-shim.
// The test-double harness is selected only through the shim's Rewrite seam, never through packet content.

const shimHelperEnv = "SC_TEST_SHIM_CONFIG" // test-only; never read by production code

type shimHelperConfig struct {
	Workspace, HarnessSecrets, GatewayURL, Mode, Ops string
	Extra                                            map[string]string
}

// TestHelperProcess_Shim is the child side of the S4 harness: worker.Shim as a process of its own.
func TestHelperProcess_Shim(t *testing.T) {
	raw := os.Getenv(shimHelperEnv)
	if raw == "" {
		t.Skip("helper process only")
	}
	var hc shimHelperConfig
	if err := json.Unmarshal([]byte(raw), &hc); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := worker.DefaultShimConfig()
	cfg.WorkspaceDir, cfg.HarnessSecretsDir = hc.Workspace, hc.HarnessSecrets
	cfg.PathMap = map[string]string{"/var/run/sc/harness-secrets": hc.HarnessSecrets, "/workspace": hc.Workspace}
	cfg.EnvOverride = map[string]string{"SC_FAKE_GATEWAY_URL": hc.GatewayURL}
	cfg.Rewrite = func(argv, env []string) ([]string, []string) {
		switch argv[0] {
		case "sc-fake-harness":
			env = append(env, helperKindEnv+"=fake-harness", "SC_TEST_HARNESS_MODE="+hc.Mode, "SC_TEST_HARNESS_OPS="+hc.Ops)
			for k, v := range hc.Extra {
				env = append(env, k+"="+v)
			}
			return append([]string{exe, "-test.run=^TestHelperProcess_FakeHarness$", "--"}, argv[1:]...), env
		case "sc-probe":
			env = append(env, helperKindEnv+"=probe")
			return append([]string{exe, "-test.run=^TestHelperProcess_Probe$", "--"}, argv[1:]...), env
		}
		return argv, env
	}
	cfg.Stdout, cfg.Stderr = os.Stdout, os.Stderr
	os.Exit(worker.Shim(cfg))
}

// TestHelperProcess_Probe is the validation command: it reports its parent process and exits with --code.
func TestHelperProcess_Probe(t *testing.T) {
	if os.Getenv(helperKindEnv) != "probe" {
		t.Skip("helper process only")
	}
	code := 0
	args := helperArgs()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--code" {
			code, _ = strconv.Atoi(args[i+1])
		}
	}
	fmt.Printf("probe ppid=%d\n", os.Getppid())
	os.Exit(code)
}

// startRealShim starts worker.Shim as a process (called by runGit when gitSpec.realShim is set).
func (gr *gitRun) startRealShim(t *testing.T, exe string, dirs map[string]string, gwURL, mode string, ops []harnessOp, extra map[string]string) {
	t.Helper()
	opsJSON, _ := json.Marshal(ops)
	hc, _ := json.Marshal(shimHelperConfig{Workspace: dirs["workspace"], HarnessSecrets: dirs["harness-secrets"],
		GatewayURL: gwURL, Mode: mode, Ops: string(opsJSON), Extra: extra})
	p := &process{done: make(chan struct{})}
	p.cmd = exec.Command(exe, "-test.run=^TestHelperProcess_Shim$")
	p.cmd.Env = []string{shimHelperEnv + "=" + string(hc), "PATH=" + os.Getenv("PATH"), "HOME=" + dirs["home"], "TMPDIR=" + dirs["tmp"]}
	if v := extra["SC_TEST_SHIM_GOMAXPROCS"]; v != "" { // S5b: a single-P shim makes scheduling races deterministic
		p.cmd.Env = append(p.cmd.Env, "GOMAXPROCS="+v)
	}
	p.cmd.Stdout, p.cmd.Stderr = &p.stdout, &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := p.cmd.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			p.code = ee.ExitCode()
		} else if err != nil {
			p.code = -2
		}
		close(p.done)
	}()
	t.Cleanup(func() { _ = p.cmd.Process.Kill() })
	gr.shimProc, gr.shimDone = p, p.done
	// finish calls cancelShim after sc-worker exited: the shim has been told `done`, give it time to leave.
	gr.cancelShim = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		select {
		case <-p.done:
		case <-ctx.Done():
			_ = p.cmd.Process.Kill()
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// harness of the S4 tests

type s4Opts struct {
	scenario      string // packet.harness.fake_scenario (default success)
	mode          string // harness mode: real (default) or ops
	ops           []harnessOp
	timeout       int // limits.harness_timeout_seconds (default 600)
	grace         int // limits.term_grace_seconds (default 5)
	maxLog        int // limits.max_log_bytes (default 10 MiB)
	validation    []any
	cancelAfterHB int
	env           map[string]string   // extra environment of the harness double (S5b)
	scWorkspace   func(string) string // SC_WORKSPACE as sc-worker sees it (S5c review 1, N3)
}

// s4Run starts the production shim and sc-worker (real clock) for a tester run against the bare fixture.
func s4Run(t *testing.T, o s4Opts) *gitRun {
	t.Helper()
	if o.scenario == "" {
		o.scenario = "success"
	}
	if o.timeout == 0 {
		o.timeout = 600
	}
	if o.grace == 0 {
		o.grace = 5
	}
	fx := buildFixture(t, false)
	po := pktOpts{}
	if o.validation != nil {
		po.policy = map[string]any{"validation_commands": o.validation}
	}
	pkt := gitPacket(t, fx, po)
	pkt["harness"].(map[string]any)["fake_scenario"] = o.scenario
	limits := pkt["limits"].(map[string]any)
	limits["harness_timeout_seconds"], limits["term_grace_seconds"] = o.timeout, o.grace
	if o.maxLog > 0 {
		limits["max_log_bytes"] = o.maxLog
	}
	pkt["execution"].(map[string]any)["deadline_at"] = clock.Real{}.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	validate(t, "worker-input", pkt)
	gr := runGit(t, gitSpec{fx: fx, pkt: pkt, mode: o.mode, ops: o.ops, shimEnv: o.env, realShim: true, realClock: true, scWorkspace: o.scWorkspace})
	if o.cancelAfterHB > 0 {
		gr.stub.setCancelAfter(o.cancelAfterHB)
	}
	return gr
}

func (s *stubRunner) setCancelAfter(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelAfterHB = n
}

// waitUntil polls cond (real time, 50 ms) for at most d and fails the test when it does not become true.
func waitUntil(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	for !cond() {
		if err := (clock.Real{}).Sleep(ctx, 50*time.Millisecond); err != nil {
			if cond() {
				return
			}
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
	}
}

func (gr *gitRun) shimDir() string { return filepath.Join(gr.ws, ".sc-shim") }

func (gr *gitRun) shimPID() int { return gr.shimProc.cmd.Process.Pid }

func (gr *gitRun) streamText(n int) string {
	b, _ := os.ReadFile(filepath.Join(gr.shimDir(), fmt.Sprintf("stream-%d.log", n)))
	return string(b)
}

func (gr *gitRun) resultPosted() bool { return len(gr.stub.calls("POST", "/v1/worker/result")) > 0 }

func (gr *gitRun) outFile(name string) string {
	b, _ := os.ReadFile(filepath.Join(gr.ws, "out", name))
	return strings.TrimSpace(string(b))
}

func (gr *gitRun) shimJSON(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(gr.shimDir(), name))
	if err != nil {
		t.Fatalf("shim file %s: %v", name, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("shim file %s is not JSON: %v", name, err)
	}
	return m
}

// waitExit waits for sc-worker to exit within d and returns its exit code.
func (gr *gitRun) waitExit(t *testing.T, d time.Duration, what string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	select {
	case <-gr.proc.done:
		return gr.proc.code
	case <-ctx.Done():
		t.Fatalf("sc-worker did not exit within %s of %s", d, what)
		return -1
	}
}

// finishWithin waits for sc-worker (at most d) and then applies the common checks of gitRun.finish.
func (gr *gitRun) finishWithin(t *testing.T, d time.Duration, what string) map[string]any {
	t.Helper()
	gr.waitExit(t, d, what)
	return gr.finish(t)
}

// endShim lets the shim go once sc-worker has exited (used by tests that do not call finish).
func (gr *gitRun) endShim() {
	gr.cancelShim()
	<-gr.shimDone
}

type procEntry struct {
	pid, ppid, pgrp int
	state           byte
}

func scanProcs() []procEntry {
	var out []procEntry
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:]) // state ppid pgrp
		if len(f) < 3 {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		pgrp, _ := strconv.Atoi(f[2])
		out = append(out, procEntry{pid, ppid, pgrp, f[0][0]})
	}
	return out
}

// survivors lists the non-zombie processes of process group pgrp.
func survivors(pgrp int) []procEntry {
	var out []procEntry
	for _, p := range scanProcs() {
		if p.pgrp == pgrp && p.state != 'Z' {
			out = append(out, p)
		}
	}
	return out
}

var killMu sync.Mutex

// harnessGroup waits for the process the shim started (a child of the shim in a group of its own) and returns the
// group id; the group is killed at the end of the test whatever happens.
func (gr *gitRun) harnessGroup(t *testing.T) int {
	t.Helper()
	shim := gr.shimPID()
	pgrp := 0
	waitUntil(t, 45*time.Second, "the shim to start the harness in a process group of its own", func() bool {
		for _, p := range scanProcs() {
			if p.ppid == shim && p.pgrp == p.pid && p.state != 'Z' {
				pgrp = p.pgrp
				return true
			}
		}
		return false
	})
	t.Cleanup(func() {
		killMu.Lock()
		defer killMu.Unlock()
		_ = syscall.Kill(-pgrp, syscall.SIGKILL)
	})
	return pgrp
}

// expectNoSurvivors fails when any non-zombie process is left in group pgrp (a /proc scan).
func expectNoSurvivors(t *testing.T, pgrp int) {
	t.Helper()
	if left := survivors(pgrp); len(left) > 0 {
		t.Errorf("%d process(es) survive in the harness process group %d: %+v", len(left), pgrp, left)
	}
}

// framing is the parsed harness block of sc-worker's stdout (plan 27 section 14.8).
type framing struct {
	begin, end map[string]any
	inner      []string // lines strictly between BEGIN and END (or after BEGIN when END is absent)
	lines      []string
	bi, ei     int
}

const (
	beginPrefix = "::STRANGE-COMPANY-STREAM-BEGIN::"
	endPrefix   = "::STRANGE-COMPANY-STREAM-END::"
)

// parseFraming asserts the 27-AC-3 count (exactly one BEGIN; one END unless wantEnd is false, then none) and
// decodes the two header lines.
func parseFraming(t *testing.T, stdout string, wantEnd bool) framing {
	t.Helper()
	f := framing{bi: -1, ei: -1, lines: strings.Split(stdout, "\n")}
	if b := countLines(stdout, beginPrefix); b != 1 {
		t.Errorf("stdout has %d harness BEGIN lines, want exactly 1", b)
	}
	e := countLines(stdout, endPrefix)
	if wantEnd && e != 1 {
		t.Errorf("stdout has %d harness END lines, want exactly 1", e)
	}
	if !wantEnd && e != 0 {
		t.Errorf("stdout has %d harness END lines, want none", e)
	}
	decode := func(l, prefix string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(l, prefix))), &m); err != nil {
			t.Errorf("%s header is not JSON: %q", prefix, l)
		}
		return m
	}
	for i, l := range f.lines {
		switch {
		case strings.HasPrefix(l, beginPrefix) && f.bi < 0:
			f.bi, f.begin = i, decode(l, beginPrefix)
		case strings.HasPrefix(l, endPrefix) && f.ei < 0:
			f.ei, f.end = i, decode(l, endPrefix)
		}
	}
	if f.bi >= 0 {
		to := len(f.lines)
		if f.ei >= 0 {
			to = f.ei
			if f.ei < f.bi {
				t.Errorf("END precedes BEGIN")
				return f
			}
		}
		f.inner = f.lines[f.bi+1 : to]
	}
	return f
}

// checkHeaders asserts the BEGIN and END fields of plan 27 section 14.8.
func (f framing) checkHeaders(t *testing.T) {
	t.Helper()
	if f.begin != nil {
		if f.begin["execution_id"] != "sc-1-x1" || f.begin["adapter"] != "fake" {
			t.Errorf("BEGIN = %v, want execution_id sc-1-x1 and adapter fake", f.begin)
		}
		if s, _ := f.begin["started_at"].(string); s == "" {
			t.Errorf("BEGIN has no started_at: %v", f.begin)
		} else if _, err := time.Parse(time.RFC3339, s); err != nil {
			t.Errorf("BEGIN started_at %q is not RFC 3339", s)
		}
	}
	if f.end != nil {
		for _, k := range []string{"exit_code", "signal", "lines", "bytes", "truncated_lines", "log_truncated"} {
			if _, ok := f.end[k]; !ok {
				t.Errorf("END lacks %q: %v", k, f.end)
			}
		}
		if n, _ := f.end["lines"].(float64); n < 1 {
			t.Errorf("END lines = %v, want >= 1", f.end["lines"])
		}
		if n, _ := f.end["bytes"].(float64); n < 1 {
			t.Errorf("END bytes = %v, want >= 1", f.end["bytes"])
		}
	}
}

func readNanos(t *testing.T, s string) time.Time {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("not a timestamp: %q", s)
	}
	return time.Unix(0, n)
}

// checkKilledAfterGrace asserts that the harness that ignored SIGTERM (out/term.txt) was killed by the shim no
// earlier than the grace period and within grace + 2 s (27-AC-16); the shim's timestamps have 1 s resolution.
func (gr *gitRun) checkKilledAfterGrace(t *testing.T, grace int) {
	t.Helper()
	termTxt := gr.outFile("term.txt")
	if termTxt == "" {
		t.Fatalf("the harness never received SIGTERM (out/term.txt is absent)")
	}
	term := readNanos(t, termTxt)
	res := gr.shimJSON(t, "res-1.json")
	if res["signal"] == nil {
		t.Errorf("res-1.json signal = null, want the kill signal: %v", res)
	}
	fin, err := time.Parse(time.RFC3339, fmt.Sprint(res["finished_at"]))
	if err != nil {
		t.Fatalf("res-1.json finished_at %v: %v", res["finished_at"], err)
	}
	elapsed := fin.Sub(term).Seconds()
	if elapsed <= float64(grace)-1 || elapsed > float64(grace)+2 {
		t.Errorf("harness was killed %.1f s after SIGTERM, want within [%d, %d] s (term_grace_seconds %d + 2)", elapsed, grace, grace+2, grace)
	}
}

// killDoubleOps is a harness that records itself, leaves a SIGTERM-immune child, ignores SIGTERM and hangs.
func killDoubleOps() []harnessOp {
	return []harnessOp{{Op: "record-proc"}, {Op: "spawn-child"}, {Op: "ignore-sigterm"}, {Op: "hang"}}
}

func pidAlive(pid int) bool {
	for _, p := range scanProcs() {
		if p.pid == pid {
			return p.state != 'Z'
		}
	}
	return false
}

// ---------------------------------------------------------------------------------------------------------------
// 27-AC-3 framing

// 27-AC-3: success scenario, exactly one BEGIN and one END with the plan 27 section 14.8 fields.
func TestShim_Success_ExactlyOneBeginAndEnd_27AC3(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{})
	res := gr.finishWithin(t, 60*time.Second, "start")
	if res["state"] != "completed" || res["pushed"] != true {
		t.Errorf("state = %v, pushed = %v, want completed and pushed; failure %v", res["state"], res["pushed"], res["failure"])
	}
	f := parseFraming(t, gr.proc.stdout.String(), true)
	f.checkHeaders(t)
	if f.end != nil && (f.end["exit_code"] != float64(0) || f.end["signal"] != nil || f.end["truncated_lines"] != float64(0) || f.end["log_truncated"] != false) {
		t.Errorf("END = %v, want exit_code 0, signal null, truncated_lines 0, log_truncated false", f.end)
	}
	found := false
	for _, l := range f.inner {
		if strings.Contains(l, `"sc_fake_result"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("the harness terminal event is not between BEGIN and END")
	}
}

// 27-AC-3: scenario failure (plan 28 section 14.4: 3 steps, terminal is_error true, exit 1, no claim).
func TestShim_FailureScenario_ExactlyOneBeginAndEnd_27AC3(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{scenario: "failure"})
	res := gr.finishWithin(t, 60*time.Second, "start")
	wantFailedResult(t, res, "harness_error")
	f := parseFraming(t, gr.proc.stdout.String(), true)
	f.checkHeaders(t)
	if f.end != nil && f.end["exit_code"] != float64(1) {
		t.Errorf("END exit_code = %v, want 1", f.end["exit_code"])
	}
}

// 27-AC-11 and the timeout row of 27-AC-3: harness_timeout_seconds 60, scenario timeout; the shim enforces the
// timeout, the failed/harness_timeout result is posted within 60 + term_grace_seconds + 15 s of harness start and
// the process group has no survivor. This is the only 60 s test.
func TestShim_TimeoutScenario_27AC11_27AC3(t *testing.T) {
	t.Parallel()
	const timeout, grace = 60, 5
	gr := s4Run(t, s4Opts{scenario: "timeout", timeout: timeout, grace: grace})
	var t0 time.Time
	waitUntil(t, 45*time.Second, "the shim to start the harness (stream-1.log)", func() bool {
		if _, err := os.Stat(filepath.Join(gr.shimDir(), "stream-1.log")); err == nil {
			t0 = clock.Real{}.Now()
			return true
		}
		return false
	})
	pgrp := gr.harnessGroup(t)
	budget := time.Duration(timeout+grace+15) * time.Second
	waitUntil(t, budget-clock.Real{}.Now().Sub(t0), "the result within 60 + term_grace_seconds + 15 s of harness start", gr.resultPosted)
	res := gr.finishWithin(t, 30*time.Second, "the result post")

	wantFailedResult(t, res, "harness_timeout")
	expectNoSurvivors(t, pgrp)
	sr := gr.shimJSON(t, "res-1.json")
	if sr["timed_out"] != true {
		t.Errorf("res-1.json timed_out = %v, want true (the shim enforces the timeout)", sr["timed_out"])
	}
	st, e1 := time.Parse(time.RFC3339, fmt.Sprint(sr["started_at"]))
	fi, e2 := time.Parse(time.RFC3339, fmt.Sprint(sr["finished_at"]))
	if e1 != nil || e2 != nil {
		t.Fatalf("res-1.json timestamps: %v %v", sr["started_at"], sr["finished_at"])
	}
	if d := fi.Sub(st).Seconds(); d < timeout-1 || d > timeout+grace+2 {
		t.Errorf("the shim ended the harness after %.0f s, want %d s up to %d s", d, timeout, timeout+grace+2)
	}
	f := parseFraming(t, gr.proc.stdout.String(), true)
	f.checkHeaders(t)
	if f.end != nil && f.end["signal"] == nil && f.end["exit_code"] == float64(0) {
		t.Errorf("END = %v, want a signal or a non-zero exit code", f.end)
	}
}

// 27-AC-3, ignore-cancel row (SIGTERM path of plan 27 section 14.14): SIGTERM to sc-worker forwards to the
// harness group, the shim kills it after the grace period, END is written once, a failed/cancelled result is
// posted once and the exit code is 143.
func TestShim_IgnoreCancelScenario_SIGTERMPath_27AC3(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{scenario: "ignore-cancel", grace: 5})
	waitUntil(t, 45*time.Second, "the harness to call the gateway", func() bool { return strings.Contains(gr.streamText(1), "gateway_request") })
	pgrp := gr.harnessGroup(t)
	if err := gr.proc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := gr.waitExit(t, 40*time.Second, "SIGTERM"); code != 143 {
		t.Errorf("exit code = %d, want 143; stderr:\n%s", code, gr.proc.stderr.String())
	}
	gr.endShim()
	posts := gr.stub.calls("POST", "/v1/worker/result")
	if len(posts) != 1 {
		t.Fatalf("%d result POSTs, want 1", len(posts))
	}
	checkFramed(t, gr.proc.stdout.String(), posts[0].Body)
	res := decodeResult(t, posts[0].Body)
	wantFailedResult(t, res, "cancelled")
	f := parseFraming(t, gr.proc.stdout.String(), true)
	f.checkHeaders(t)
	expectNoSurvivors(t, pgrp)
}

// 27-AC-3, last clause: a test that SIGKILLs sc-worker mid-harness sees BEGIN and no END.
func TestShim_SIGKILLWorkerMidHarness_BeginWithoutEnd_27AC3(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{scenario: "timeout"})
	waitUntil(t, 45*time.Second, "the harness to call the gateway", func() bool { return strings.Contains(gr.streamText(1), "gateway_request") })
	gr.harnessGroup(t)
	if err := gr.proc.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	gr.waitExit(t, 10*time.Second, "SIGKILL")
	// Nobody tells the shim now: end the harness through the protocol so that nothing is left behind.
	for _, name := range []string{"cancel-1", "done"} {
		if err := os.WriteFile(filepath.Join(gr.shimDir(), name), []byte("{}"), 0o660); err != nil {
			t.Fatal(err)
		}
	}
	gr.endShim()
	f := parseFraming(t, gr.proc.stdout.String(), false)
	if f.bi < 0 {
		t.Errorf("no BEGIN line in the captured stdout")
	}
	if countLines(gr.proc.stdout.String(), "::SC-RESULT-BEGIN::") != 0 {
		t.Errorf("a killed sc-worker printed a result block")
	}
}

// ---------------------------------------------------------------------------------------------------------------
// 27-AC-3 framer details (plan 27 section 14.8)

// Each harness line is truncated to 64 KiB with the suffix " …[truncated N bytes]"; END counts it.
func TestFramer_LongLineTruncatedAt64KiB_27AC3(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{mode: "ops", ops: []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "bigline", N: 100000}}})
	gr.finishWithin(t, 60*time.Second, "start")
	f := parseFraming(t, gr.proc.stdout.String(), true)
	want := strings.Repeat("A", 65536) + " …[truncated 34464 bytes]"
	n := 0
	for _, l := range f.inner {
		if strings.HasPrefix(l, "AAAA") {
			n++
			if l != want {
				t.Errorf("long line = %d bytes ending %q, want 65536 bytes plus ' …[truncated 34464 bytes]'", len(l), l[max(0, len(l)-40):])
			}
		}
	}
	if n != 1 {
		t.Errorf("%d long lines between BEGIN and END, want 1", n)
	}
	if f.end != nil && f.end["truncated_lines"] != float64(1) {
		t.Errorf("END truncated_lines = %v, want 1", f.end["truncated_lines"])
	}
}

// After max_log_bytes of harness output further lines are parsed but not written; one log_budget_exhausted line
// is emitted and END carries log_truncated: true. The terminal event after the cut is still parsed.
func TestFramer_LogBudget_27AC3(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("B", 99)
	gr := s4Run(t, s4Opts{maxLog: 65536, mode: "ops",
		ops: []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "print", Content: body, N: 2000}}})
	res := gr.finishWithin(t, 60*time.Second, "start")
	if res["state"] != "completed" {
		t.Errorf("state = %v, want completed (the terminal event after the budget is still parsed)", res["state"])
	}
	f := parseFraming(t, gr.proc.stdout.String(), true)
	got, exhausted := 0, 0
	for _, l := range f.inner {
		if l == body {
			got++
		}
		if strings.Contains(l, "log_budget_exhausted") {
			exhausted++
			if !strings.Contains(l, `"src":"sc-worker"`) {
				t.Errorf("budget line %q lacks src sc-worker", l)
			}
		}
	}
	if got < 500 || got*100 > 65536 {
		t.Errorf("%d harness body lines written, want between 500 and 655 (max_log_bytes 65536, 100 bytes per line)", got)
	}
	if exhausted != 1 {
		t.Errorf("%d log_budget_exhausted lines between BEGIN and END, want 1", exhausted)
	}
	if f.end == nil || f.end["log_truncated"] != true {
		t.Errorf("END = %v, want log_truncated true", f.end)
	}
	if f.end != nil {
		if n, _ := f.end["lines"].(float64); n < 2000 {
			t.Errorf("END lines = %v, want >= 2000 (lines past the budget are still counted)", f.end["lines"])
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// 27-AC-15 and 27-AC-16

// 27-AC-15: a harness that blocks reading stdin gets EOF at once (stdin is /dev/null) and the scenario
// completes in less than 5 s.
func TestShim_StdinIsClosed_27AC15(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{mode: "ops", ops: []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "read-stdin"}}})
	res := gr.finishWithin(t, 60*time.Second, "start")
	if res["state"] != "completed" {
		t.Errorf("state = %v, want completed", res["state"])
	}
	f := strings.Fields(gr.outFile("stdin.txt"))
	if len(f) != 2 {
		t.Fatalf("out/stdin.txt = %q: the harness did not get EOF on stdin", gr.outFile("stdin.txt"))
	}
	if ms, _ := strconv.Atoi(f[0]); ms >= 5000 {
		t.Errorf("reading stdin took %d ms, want EOF at once (< 5 s)", ms)
	}
	if f[1] != "/dev/null" {
		t.Errorf("fd 0 of the harness is %q, want /dev/null (plan 27 section 7.2)", f[1])
	}
}

// 27-AC-16, first clause: a harness that ignores SIGTERM is killed by the shim within term_grace_seconds + 2 s
// (here after SIGTERM to sc-worker), together with every process of its group.
func TestShim_IgnoresSIGTERM_KilledWithinGrace_27AC16(t *testing.T) {
	t.Parallel()
	const grace = 5
	gr := s4Run(t, s4Opts{grace: grace, mode: "ops", ops: killDoubleOps()})
	waitUntil(t, 45*time.Second, "the harness to be ready", func() bool { return gr.outFile("ready.txt") != "" })
	pgrp := gr.harnessGroup(t)
	var child int
	child, _ = strconv.Atoi(gr.outFile("child.txt"))
	if child == 0 || !pidAlive(child) {
		t.Fatalf("the harness's child %q is not running", gr.outFile("child.txt"))
	}
	if err := gr.proc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := gr.waitExit(t, 40*time.Second, "SIGTERM"); code != 143 {
		t.Errorf("exit code = %d, want 143", code)
	}
	gr.endShim()
	gr.checkKilledAfterGrace(t, grace)
	expectNoSurvivors(t, pgrp)
	if pidAlive(child) {
		t.Errorf("the harness's child %d survived the group kill", child)
	}
	if posts := gr.stub.calls("POST", "/v1/worker/result"); len(posts) != 1 {
		t.Errorf("%d result POSTs, want 1", len(posts))
	}
	parseFraming(t, gr.proc.stdout.String(), true).checkHeaders(t)
}

// Plan 27 section 14.7 step 4: a heartbeat response with cancel_requested true makes sc-worker write cancel-<n>;
// the shim sends SIGTERM to the process group and SIGKILL after term_grace_seconds; END is written, the posted
// result is failed/cancelled and sc-worker exits 0.
func TestCancel_HeartbeatRequest_TerminatesGroup_P27S14_7(t *testing.T) {
	t.Parallel()
	const grace = 5
	gr := s4Run(t, s4Opts{grace: grace, cancelAfterHB: 1, mode: "ops", ops: killDoubleOps()})
	waitUntil(t, 45*time.Second, "the harness to be ready", func() bool { return gr.outFile("ready.txt") != "" })
	pgrp := gr.harnessGroup(t)
	res := gr.finishWithin(t, 90*time.Second, "the cancel request")
	wantFailedResult(t, res, "cancelled")
	if _, err := os.Stat(filepath.Join(gr.shimDir(), "cancel-1")); err != nil {
		t.Errorf("sc-worker did not write cancel-1: %v", err)
	}
	if v := gr.event("harness_exit"); v == nil || v["verdict"] != "cancelled" {
		t.Errorf("harness_exit event = %v, want verdict cancelled", v)
	}
	gr.checkKilledAfterGrace(t, grace)
	expectNoSurvivors(t, pgrp)
	f := parseFraming(t, gr.proc.stdout.String(), true)
	f.checkHeaders(t)
}

// Plan 27 section 7.4: the shim forwards a container SIGTERM to the running process group.
func TestShim_ContainerSIGTERMForwardedToGroup_P27S7_4(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{mode: "ops", ops: []harnessOp{{Op: "record-proc"}, {Op: "hang"}}})
	waitUntil(t, 45*time.Second, "the harness to be ready", func() bool { return gr.outFile("ready.txt") != "" })
	pgrp := gr.harnessGroup(t)
	if err := gr.shimProc.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 20*time.Second, "res-1.json after SIGTERM to the shim", func() bool {
		_, err := os.Stat(filepath.Join(gr.shimDir(), "res-1.json"))
		return err == nil
	})
	if gr.shimJSON(t, "res-1.json")["signal"] == nil {
		t.Errorf("res-1.json signal = null, want the signal that ended the harness")
	}
	expectNoSurvivors(t, pgrp)
	res := gr.finishWithin(t, 60*time.Second, "the harness end")
	if res["state"] != "failed" {
		t.Errorf("state = %v, want failed", res["state"])
	}
}

// 27-AC-16, second clause (Layer-1 equivalent): a validation command is executed by the shim process, not by
// sc-worker, without a shell, and its output tail lands in validation[i].output_tail.
func TestShim_ValidationCommandRunsInShim_27AC16(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{mode: "ops", ops: []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc)},
		validation: []any{map[string]any{"name": "probe", "argv": []any{"sc-probe", "--code", "3"}, "timeout_seconds": 30}}})
	res := gr.finishWithin(t, 60*time.Second, "start")
	if res["state"] != "completed" {
		t.Fatalf("state = %v, want completed", res["state"])
	}
	vs, _ := res["validation"].([]any)
	if len(vs) != 1 {
		t.Fatalf("validation = %v, want one entry", res["validation"])
	}
	v, _ := vs[0].(map[string]any)
	if v["name"] != "probe" || v["exit_code"] != float64(3) || v["timed_out"] != false {
		t.Errorf("validation[0] = %v, want name probe, exit_code 3, timed_out false", v)
	}
	tail, _ := v["output_tail"].(string)
	if want := fmt.Sprintf("ppid=%d", gr.shimPID()); !strings.Contains(tail, want) {
		t.Errorf("output_tail = %q, want the probe's parent to be the shim (%s)", tail, want)
	}
	if strings.Contains(tail, fmt.Sprintf("ppid=%d\n", gr.proc.cmd.Process.Pid)) {
		t.Errorf("the validation command ran as a child of sc-worker")
	}
	req := gr.shimJSON(t, "req-2.json")
	argv, _ := req["argv"].([]any)
	if req["kind"] != "validation" || len(argv) == 0 || argv[0] != "sc-probe" {
		t.Errorf("req-2.json = %v, want kind validation with argv sc-probe (no shell)", req)
	}
}

// 27-AC-16, third clause: harness output reaches sc-worker's stdout only through stream-<n>.log and the framer.
// The harness writes to its stdout and stderr; neither the shim's own streams nor sc-worker's stderr carry it,
// and on sc-worker's stdout it appears only between BEGIN and END, stderr lines prefixed "[stderr] ".
func TestShim_HarnessOutputOnlyThroughFramer_27AC16(t *testing.T) {
	t.Parallel()
	const outMark, errMark = "HARNESS-BYPASS-STDOUT-7f3a91", "HARNESS-BYPASS-STDERR-7f3a91"
	gr := s4Run(t, s4Opts{mode: "ops", ops: []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "print", Content: outMark, Msg: errMark}}})
	gr.finishWithin(t, 60*time.Second, "start")
	for _, b := range []struct{ name, text string }{
		{"shim stdout", gr.shimProc.stdout.String()}, {"shim stderr", gr.shimProc.stderr.String()}, {"sc-worker stderr", gr.proc.stderr.String()},
	} {
		if strings.Contains(b.text, outMark) || strings.Contains(b.text, errMark) {
			t.Errorf("%s carries harness output:\n%s", b.name, b.text)
		}
	}
	stream := gr.streamText(1)
	if !strings.Contains(stream, "\n"+outMark+"\n") && !strings.HasPrefix(stream, outMark+"\n") {
		t.Errorf("stream-1.log lacks the harness stdout line")
	}
	if !strings.Contains(stream, "[stderr] "+errMark+"\n") {
		t.Errorf("stream-1.log lacks the harness stderr line prefixed [stderr] ")
	}
	f := parseFraming(t, gr.proc.stdout.String(), true)
	inner := map[string]int{}
	for _, l := range f.inner {
		inner[l]++
	}
	for _, want := range []string{outMark, "[stderr] " + errMark} {
		if inner[want] != 1 {
			t.Errorf("framed stream has %d copies of %q, want 1", inner[want], want)
		}
	}
	for i, l := range f.lines {
		if strings.Contains(l, outMark) || strings.Contains(l, errMark) {
			if i <= f.bi || (f.ei >= 0 && i >= f.ei) {
				t.Errorf("harness line outside BEGIN..END: %q", l)
			}
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// S3-F8: the shim directory

// Plan 27 section 7.4: /workspace/.sc-shim is created mode 0770; request files are written atomically
// (*.tmp, then rename) and are not world-readable (the shim reads them as a group member).
func TestShim_DirectoryAndRequestFileModes_S3F8(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var torn []string
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			ents, _ := os.ReadDir(gr.shimDir())
			for _, e := range ents {
				n := e.Name()
				if strings.HasPrefix(n, "req-") && strings.HasSuffix(n, ".json") {
					if b, err := os.ReadFile(filepath.Join(gr.shimDir(), n)); err == nil {
						var m map[string]any
						if json.Unmarshal(b, &m) != nil {
							mu.Lock()
							torn = append(torn, fmt.Sprintf("%s: %q", n, b))
							mu.Unlock()
						}
					}
				}
			}
			select {
			case <-stop:
				return
			default:
			}
			_ = clock.Real{}.Sleep(context.Background(), 2*time.Millisecond)
		}
	}()
	gr.finishWithin(t, 60*time.Second, "start")
	close(stop)
	wg.Wait()

	st, err := os.Lstat(gr.shimDir())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o770 {
		t.Errorf(".sc-shim mode = %o, want 770", st.Mode().Perm())
	}
	if len(torn) > 0 {
		t.Errorf("request files were observed incomplete (not written atomically): %v", torn)
	}
	reqs, _ := filepath.Glob(filepath.Join(gr.shimDir(), "req-*.json"))
	if len(reqs) == 0 {
		t.Fatalf("no request file left in %s", gr.shimDir())
	}
	for _, r := range reqs {
		fi, err := os.Lstat(r)
		if err != nil {
			t.Fatal(err)
		}
		if p := fi.Mode().Perm(); p&0o007 != 0 || p&0o040 == 0 {
			t.Errorf("%s mode = %o, want group-readable and not world-accessible", filepath.Base(r), p)
		}
	}
	if tmps, _ := filepath.Glob(filepath.Join(gr.shimDir(), "*.tmp")); len(tmps) > 0 {
		t.Errorf("leftover temporary files: %v", tmps)
	}
}
