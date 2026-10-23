package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/worker"
)

// Slice S4 of F07, review round 1: pod deletion (B1), the early-exit shim (N3) and `done` with running jobs (N1).

// B1, plan 27 section 14.14 and 27-AC-3 (SIGTERM path): Kubernetes sends SIGTERM to both containers at once and
// the kubelet kills the pod 30 s later (terminationGracePeriodSeconds 30). With term_grace_seconds 25 and a
// harness that ignores SIGTERM, the harness group must be dead within 22 s of the signal, sc-worker must write
// exactly one END, post exactly one failed result and exit 143 within 30 s of the signal.
func TestShim_PodDeletionSIGTERMToBothContainers_27AC3_B1(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"shim-first", "worker-first"} {
		t.Run(order, func(t *testing.T) {
			t.Parallel()
			gr := s4Run(t, s4Opts{grace: 25, mode: "ops", ops: killDoubleOps()})
			waitUntil(t, 45*time.Second, "the harness to be ready", func() bool { return gr.outFile("ready.txt") != "" })
			pgrp := gr.harnessGroup(t)
			procs := []*os.Process{gr.shimProc.cmd.Process, gr.proc.cmd.Process}
			if order == "worker-first" {
				procs[0], procs[1] = procs[1], procs[0]
			}
			t0 := clock.Real{}.Now()
			for _, p := range procs {
				if err := p.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
			defer cancel()
			for len(survivors(pgrp)) > 0 {
				if err := (clock.Real{}).Sleep(ctx, 50*time.Millisecond); err != nil {
					t.Errorf("the harness group %d is still alive 22 s after SIGTERM (term_grace_seconds 25): %+v", pgrp, survivors(pgrp))
					break
				}
			}
			left := 30*time.Second - clock.Real{}.Now().Sub(t0)
			if left < time.Second {
				left = time.Second
			}
			if code := gr.waitExit(t, left, "SIGTERM (kubelet kills the pod after 30 s)"); code != 143 {
				t.Errorf("exit code = %d, want 143; stderr:\n%s", code, gr.proc.stderr.String())
			}
			gr.endShim()
			d := clock.Real{}.Now().Sub(t0)
			if d > 31*time.Second {
				t.Errorf("sc-worker took %s after SIGTERM, want at most 30 s", d)
			}
			posts := gr.stub.calls("POST", "/v1/worker/result")
			if len(posts) != 1 {
				t.Fatalf("%d result POSTs, want 1", len(posts))
			}
			res := decodeResult(t, posts[0].Body)
			if reason, detail := failureReason(res); res["state"] != "failed" || (reason != "cancelled" && reason != "harness_timeout") {
				t.Errorf("result state %v reason %q (%s), want failed with cancelled or harness_timeout", res["state"], reason, detail)
			}
			parseFraming(t, gr.proc.stdout.String(), true).checkHeaders(t)
			expectNoSurvivors(t, pgrp)
		})
	}
}

// N3, plan 27 section 7.4: when sc-worker exits before it creates .sc-shim, the shim does not wait for the pod
// deadline; it leaves once 120 s have passed since its start (fake clock, no alive file ever appears).
func TestShim_NoShimDirectory_LeavesAfter120s_P27S7_4_N3(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fc := clock.NewFake(start)
	done := make(chan int, 1)
	go func() {
		done <- worker.Shim(worker.ShimConfig{WorkspaceDir: ws, HarnessSecretsDir: filepath.Join(ws, "hs"),
			Clock: fc, Stdout: io.Discard, Stderr: io.Discard})
	}()
	t.Cleanup(func() {
		// Whatever happened, let a still running shim go.
		dir := filepath.Join(ws, ".sc-shim")
		_ = os.MkdirAll(dir, 0o750)
		_ = os.WriteFile(filepath.Join(dir, "done"), []byte("{}"), 0o640)
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		select {
		case <-done:
		case <-cctx.Done():
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		select {
		case <-done:
			if el := fc.Now().Sub(start); el < 120*time.Second {
				t.Errorf("the shim left after only %s on its clock, want 120 s", el)
			}
			return
		case <-ctx.Done():
			t.Fatalf("the shim without .sc-shim and without alive is still running after %s on its clock", fc.Now().Sub(start))
		default:
			_ = (clock.Real{}).Sleep(context.Background(), 10*time.Millisecond)
		}
	}
}

// N1, plan 27 section 7.4: `done` means sc-worker is finished with the shim; every process group that still
// runs is SIGKILLed at once (no grace period) and the shim exits 0.
func TestShim_DoneKillsRemainingGroupsAtOnce_P27S7_4_N1(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dirs := map[string]string{}
	for _, d := range []string{"workspace", "harness-secrets", "home", "tmp"} {
		dirs[d] = filepath.Join(root, d)
		if err := os.MkdirAll(dirs[d], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gr := &gitRun{ws: dirs["workspace"], dirs: dirs}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	gr.startRealShim(t, exe, dirs, "http://127.0.0.1:1", "ops", killDoubleOps(), nil)
	out := filepath.Join(dirs["workspace"], "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(gr.shimDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	req, _ := json.Marshal(map[string]any{"id": "n1", "kind": "harness",
		"argv": []string{"sc-fake-harness", "--repo", dirs["workspace"], "--out", out, "--role", "implementer"},
		"env":  map[string]string{}, "cwd": dirs["workspace"], "timeout_seconds": 600, "term_grace_seconds": 30})
	tmp := filepath.Join(gr.shimDir(), "req-1.json.tmp")
	if err := os.WriteFile(tmp, req, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(gr.shimDir(), "req-1.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gr.shimDir(), "alive"), []byte("{}"), 0o640); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 45*time.Second, "the harness to be ready", func() bool { return gr.outFile("ready.txt") != "" })
	pgrp := gr.harnessGroup(t)
	if len(survivors(pgrp)) < 2 {
		t.Fatalf("the harness group %d has %d processes, want the harness and its child", pgrp, len(survivors(pgrp)))
	}
	t0 := clock.Real{}.Now()
	if err := os.WriteFile(filepath.Join(gr.shimDir(), "done"), []byte("{}"), 0o640); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for len(survivors(pgrp)) > 0 {
		if err := (clock.Real{}).Sleep(ctx, 20*time.Millisecond); err != nil {
			t.Fatalf("%d process(es) of group %d still live 2 s after done (term_grace_seconds 30): %+v", len(survivors(pgrp)), pgrp, survivors(pgrp))
		}
	}
	rest := 5*time.Second - clock.Real{}.Now().Sub(t0)
	ctx2, cancel2 := context.WithTimeout(context.Background(), rest)
	defer cancel2()
	select {
	case <-gr.shimDone:
		if gr.shimProc.code != 0 {
			t.Errorf("the shim exited %d after done, want 0; stderr:\n%s", gr.shimProc.code, gr.shimProc.stderr.String())
		}
	case <-ctx2.Done():
		t.Errorf("the shim did not exit %s after done", fmt.Sprint(5*time.Second))
	}
}
