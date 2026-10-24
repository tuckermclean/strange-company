package worker_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"syscall"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/worker"
)

// Review round 2 of slice S6a of F07 (cd64f69).

// forgedPidWatch kills the forged job (if it ever started) when the test ends.
func forgedPidWatch(t *testing.T, gr *gitRun) {
	t.Helper()
	t.Cleanup(func() {
		if pid, _ := strconv.Atoi(gr.outFile("forged.pid")); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
}

// B1' (S4-N4): the harness plants req-99.json (a lingering writer) and exits at once, without waiting. The shim's next
// scan must not start it after the sweep: by the time res-1.json exists, and 400 ms later, the forged command either
// never ran or is dead, and the run ends failed/internal with detail workspace_tampered, nothing pushed.
func TestShim_ReqPlantedByExitingHarnessIsNeverRun_S4N4B1b(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{mode: "ops", ops: []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "plant-req"}}})
	forgedPidWatch(t, gr)
	waitUntil(t, 60*time.Second, "res-1.json", func() bool {
		_, err := os.Stat(filepath.Join(gr.shimDir(), "res-1.json"))
		return err == nil
	})
	if err := (clock.Real{}).Sleep(t.Context(), 400*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if pid, _ := strconv.Atoi(gr.outFile("forged.pid")); pid > 0 && pidAlive(pid) {
		t.Errorf("the forged job %d is alive 400 ms after the shim published res-1.json", pid)
	}
	res := gr.finishWithin(t, 90*time.Second, "the result")
	if detail := wantFailedResult(t, res, "internal"); detail != "workspace_tampered" {
		t.Errorf("detail = %q, want workspace_tampered (a request the harness planted)", detail)
	}
	if pid, _ := strconv.Atoi(gr.outFile("forged.pid")); pid > 0 && pidAlive(pid) {
		t.Errorf("the forged job %d is alive after the run", pid)
	}
}

// B1' shim level: a request that appears while job 1 runs is foreign (sc-worker is serial): it is never executed (no
// pid file), and res-1.json carries the flag foreign_request (or sweep_incomplete).
func TestShim_ReqPlantedWhileJobRunsIsNotExecutedAndFlagged_S4N4B1b(t *testing.T) {
	t.Parallel()
	ops := []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "plant-req"}, {Op: "pause", N: 800}}
	gr := s4Run(t, s4Opts{mode: "ops", ops: ops})
	forgedPidWatch(t, gr)
	waitUntil(t, 60*time.Second, "res-1.json", func() bool {
		_, err := os.Stat(filepath.Join(gr.shimDir(), "res-1.json"))
		return err == nil
	})
	res1 := gr.shimJSON(t, "res-1.json")
	if res1["foreign_request"] != true && res1["sweep_incomplete"] != true {
		t.Errorf("res-1.json = %v, want foreign_request or sweep_incomplete true", res1)
	}
	if pid := gr.outFile("forged.pid"); pid != "" {
		t.Errorf("the forged request ran (pid %s) although job 1 was running", pid)
	}
	res := gr.finishWithin(t, 90*time.Second, "the result")
	if detail := wantFailedResult(t, res, "internal"); detail != "workspace_tampered" {
		t.Errorf("detail = %q, want workspace_tampered", detail)
	}
}

// N2': the harness pre-plants res-2.json, stream-2.log or cancel-2 before sc-worker writes req-2.json (the first
// validation command). Whatever of the three exists, sc-worker refuses: failed/internal workspace_tampered, no push.
func TestRun_PreplantedShimFilesForNextRequestAreTampering_S4N2b(t *testing.T) {
	probe := []any{map[string]any{"name": "probe", "argv": []any{"sc-probe", "--code", "0"}, "timeout_seconds": 30}}
	forgedRes := `{"id":"sc-1-x1-2","exit_code":0,"signal":null,"started_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:00:01Z","timed_out":false}`
	for name, plant := range map[string]harnessOp{
		"res":    {Op: "write", Path: "@shim@/res-2.json", Content: forgedRes},
		"stream": {Op: "write", Path: "@shim@/stream-2.log", Content: "forged output\n"},
		"cancel": {Op: "write", Path: "@shim@/cancel-2", Content: "{}"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			gr := s4Run(t, s4Opts{mode: "ops", ops: []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), plant}, validation: probe})
			res := gr.finishWithin(t, 90*time.Second, "the result")
			if detail := wantFailedResult(t, res, "internal"); detail != "workspace_tampered" {
				t.Errorf("detail = %q, want workspace_tampered", detail)
			}
		})
	}
}

// N4': only the validation job's result (res-2.json) carries sweep_incomplete: the validation path of sc-worker
// honours the marker too (failed/internal workspace_tampered, no push). Guard test: the behaviour may already exist.
func TestRun_IncompleteSweepOfValidationJobIsWorkspaceTampered_S4N4N4b(t *testing.T) {
	t.Parallel()
	fx := buildFixture(t, false)
	before := fx.refsDigest()
	validation := []any{map[string]any{"name": "true", "argv": []any{"/bin/true"}, "timeout_seconds": 30}}
	gr := runGit(t, gitSpec{fx: fx, pkt: gitPacket(t, fx, pktOpts{policy: map[string]any{"validation_commands": validation}}), mode: "ops",
		ops:             []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc)},
		shimResExtraByN: map[string]map[string]any{"2": {"sweep_incomplete": true}}})
	res := gr.finish(t)
	if detail := wantFailedResult(t, res, "internal"); detail != "workspace_tampered" {
		t.Errorf("detail = %q, want workspace_tampered", detail)
	}
	if got := fx.refsDigest(); got != before {
		t.Errorf("remote refs changed:\n%s", fx.refList())
	}
}

// N5' (hardening, parse level only: no signal is ever sent by this test): a /proc-shaped root with the entries -1, 0
// and 1 must not yield those pids, so that no sweep can reach kill(-1), kill(0) or init.
func TestProcPids_ExcludesPidsAtOrBelowOne_S4N4N5b(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"-1", "0", "1", "5", "300", "abc", "+7", "self"} {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "stat"), []byte(name+" (x) S 1 1 1 0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := worker.ProcPids(root)
	sort.Ints(got)
	if want := []int{5, 300}; !reflect.DeepEqual(got, want) {
		t.Errorf("ProcPids = %v, want %v (nothing <= 1, no non-numeric names)", got, want)
	}
}

// CI regression (run 40051183071, S5a 27-AC-10): a live harness that rewrites .git/config without any pause recreates
// it between the removal and the exclusive create of sc-worker's restore ("restore config: file exists"). The
// private GIT_DIR makes the restore defence in depth: the run must still complete and push exactly the change.
func TestPrivateGitDir_TightConfigRewriterDoesNotBreakRestore_27AC10(t *testing.T) {
	for i := 0; i < 16; i++ {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			fx := buildFixture(t, false)
			stop := filepath.Join(t.TempDir(), "stop-writer")
			ops := []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "linger", Path: stop, Content: "tight"}}
			gr := runGit(t, gitSpec{fx: fx, mode: "ops", ops: ops, pkt: gitPacket(t, fx, pktOpts{})})
			go func() {
				<-gr.proc.done
				_ = os.WriteFile(stop, nil, 0o644)
			}()
			t.Cleanup(func() { _ = os.WriteFile(stop, nil, 0o644) })
			res := gr.finish(t)
			wantNoCanary(t, gr)
			wantPushedOnly(t, fx, res, "calc/add_ac_test.go", addACTestSrc)
		})
	}
}
