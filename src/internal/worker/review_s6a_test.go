package worker_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/worker"
)

// Review round 1 of slice S6a of F07 (99a44a5).

// B1 (S4-N4): sc-worker issues shim jobs strictly one after the other, so any other job that runs when a job ends is
// forged. The harness plants req-99.json whose command is a lingering writer; by the time res-1.json is published the
// forged job and its subtree must be dead (the sweep may not spare "other running jobs").
func TestShim_ForgedConcurrentJobIsKilledBeforeResultPublished_S4N4B1(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{mode: "ops", ops: []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "forge-job"}}})
	pid := 0
	t.Cleanup(func() {
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	waitUntil(t, 60*time.Second, "res-1.json", func() bool {
		_, err := os.Stat(filepath.Join(gr.shimDir(), "res-1.json"))
		return err == nil
	})
	pid, _ = strconv.Atoi(gr.outFile("forged.pid"))
	if pid == 0 {
		t.Fatalf("the harness did not record the pid of its forged job; stream:\n%s", gr.streamText(1))
	}
	if pidAlive(pid) {
		t.Errorf("the forged job %d is alive although the shim published res-1.json", pid)
	}
	size := func() int64 {
		st, err := os.Stat(filepath.Join(gr.ws, "out", "forged.log"))
		if err != nil {
			return -1
		}
		return st.Size()
	}
	atRes := size()
	gr.finishWithin(t, 90*time.Second, "the result")
	if pidAlive(pid) {
		t.Errorf("the forged job %d is alive after the run", pid)
	}
	if after := size(); after != atRes {
		t.Errorf("the forged job kept writing after res-1.json: forged.log grew from %d to %d bytes", atRes, after)
	}
}

// N1: a sweep that could not finish (an unkillable descendant after the bounded rounds, or an unreadable /proc) must be
// reported, not swallowed. The decision level, against a /proc-shaped directory.
func TestSweep_IncompleteIsReported_S4N4N1(t *testing.T) {
	t.Parallel()
	const self = 7
	const ghost = 5000000 // above pid_max: kill(2) answers ESRCH, the fake /proc entry never goes away
	mk := func(t *testing.T, entries map[int]string) string {
		root := t.TempDir()
		for pid, stat := range entries {
			d := filepath.Join(root, strconv.Itoa(pid))
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, "stat"), []byte(stat), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	clk := clock.NewFake(time.Unix(1700000000, 0))
	live := strconv.Itoa(ghost) + " (sleep) S " + strconv.Itoa(self) + " 1 1 0\n"
	if !worker.SweepDescendantsOf(clk, mk(t, map[int]string{ghost: live}), self, 5) {
		t.Errorf("a descendant that stays alive for all rounds must make the sweep incomplete")
	}
	if !worker.SweepDescendantsOf(clk, filepath.Join(t.TempDir(), "no-proc"), self, 5) {
		t.Errorf("an unreadable /proc must make the sweep incomplete")
	}
	zombie := strconv.Itoa(ghost) + " (sleep) Z " + strconv.Itoa(self) + " 1 1 0\n"
	if worker.SweepDescendantsOf(clk, mk(t, map[int]string{ghost: zombie}), self, 5) {
		t.Errorf("a zombie is not alive: the sweep is complete")
	}
	other := strconv.Itoa(ghost) + " (sleep) S 99 1 1 0\n"
	if worker.SweepDescendantsOf(clk, mk(t, map[int]string{ghost: other}), self, 5) {
		t.Errorf("a process that is not below the shim is not the sweep's business")
	}
}

// N1: sc-worker reads the marker. A result file that says the sweep was incomplete means a live descendant may be
// rewriting the workspace: the run ends failed/internal with detail workspace_tampered, nothing is pushed.
func TestRun_IncompleteShimSweepIsWorkspaceTampered_S4N4N1(t *testing.T) {
	t.Parallel()
	fx := buildFixture(t, false)
	before := fx.refsDigest()
	gr := runGit(t, gitSpec{fx: fx, pkt: gitPacket(t, fx, pktOpts{}), mode: "ops",
		ops:          []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc)},
		shimResExtra: map[string]any{"sweep_incomplete": true}})
	res := gr.finish(t)
	if detail := wantFailedResult(t, res, "internal"); detail != "workspace_tampered" {
		t.Errorf("detail = %q, want workspace_tampered", detail)
	}
	if got := fx.refsDigest(); got != before {
		t.Errorf("remote refs changed:\n%s", fx.refList())
	}
}

// N3 (plan 28 section 13.1): files are writable only under /workspace, /home/worker and /tmp; the self-check must
// therefore treat the roots of the image as read-only. /home/worker is writable, /home itself is not.
func TestSelfTest_DefaultEnvReadOnlyDirsCoverTheImage_N3(t *testing.T) {
	t.Parallel()
	env := worker.DefaultSelfTestEnv()
	ro := map[string]bool{}
	for _, d := range env.ReadOnlyDirs {
		ro[d] = true
	}
	for _, want := range []string{"/", "/etc", "/var", "/opt", "/bin", "/home", "/usr"} {
		if !ro[want] {
			t.Errorf("DefaultSelfTestEnv().ReadOnlyDirs lacks %s (have %s)", want, strings.Join(env.ReadOnlyDirs, " "))
		}
	}
	for _, w := range env.WritableDirs {
		if ro[w] {
			t.Errorf("%s is listed as writable and read-only", w)
		}
	}
}
