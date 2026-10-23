package worker_test

import (
	"context"
	"fmt"
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

// Slice S6a of F07 (continued): 27-AC-5 Layer 1, review follow-ups S4-N4 (no harness descendant survives a job),
// S4-NB-d (a lingering ssh child cannot block git) and S5c-r3 F2 (a tampered workspace ends the push retry loop).

// sleepRecorder is a clock that appends the duration of every Sleep to a file before it delegates.
type sleepRecorder struct {
	clock.Clock
	path string
}

func (r sleepRecorder) Sleep(ctx context.Context, d time.Duration) error {
	if f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintf(f, "%d\n", d.Nanoseconds())
		_ = f.Close()
	}
	return r.Clock.Sleep(ctx, d)
}

func (gr *gitRun) sleeps(t *testing.T) []time.Duration {
	t.Helper()
	b, err := os.ReadFile(gr.sleepPath)
	if err != nil {
		return nil
	}
	var out []time.Duration
	for _, l := range strings.Fields(string(b)) {
		n, err := strconv.ParseInt(l, 10, 64)
		if err != nil {
			t.Fatalf("sleep log line %q: %v", l, err)
		}
		out = append(out, time.Duration(n))
	}
	return out
}

// ---------------------------------------------------------------------------------------------------------------
// 27-AC-5 (Layer 1)

func parseEnvDump(b []byte) map[string]string {
	env := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && k != "" {
			env[k] = v
		}
	}
	return env
}

// 27-AC-5: the environment the harness process receives holds the gateway token variable named by the registry
// (SC_FAKE_GATEWAY_TOKEN for the fake) and HTTPS_PROXY carrying the egress token, and none of SC_RUNNER_URL, SC_TASK_ID,
// the Runner task-token value, the deploy-key content or KUBERNETES_SERVICE_HOST, although the shim's own environment
// has them. The fake harness dumps its environment and the file list to /workspace/out (env.txt, files.txt). The real
// /var/run and /var/sc file list is the Layer-2 check of S6b.
func TestEnvIsolation_HarnessEnvironmentDump_27AC5(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{env: map[string]string{
		"SC_TEST_SHIM_ENV_KUBERNETES_SERVICE_HOST": "10.43.0.1",
		"SC_TEST_SHIM_ENV_SC_RUNNER_URL":           "http://agent-runner.invalid:8082",
		"SC_TEST_SHIM_ENV_SC_TASK_ID":              "sc-1",
	}})
	res := gr.finishWithin(t, 90*time.Second, "start")
	if res["state"] != "completed" {
		t.Fatalf("state = %v, want completed (failure %v)", res["state"], res["failure"])
	}
	raw, err := os.ReadFile(filepath.Join(gr.ws, "out", "env.txt"))
	if err != nil {
		t.Fatalf("the fake harness wrote no /workspace/out/env.txt: %v", err)
	}
	env := parseEnvDump(raw)
	if got := env["SC_FAKE_GATEWAY_TOKEN"]; got != "gw-CANARY-0002-gateway-token-value" {
		t.Errorf("SC_FAKE_GATEWAY_TOKEN = %q, want the gateway token (the variable named by the registry)", got)
	}
	wantProxy := "http://sc:eg-CANARY-0003-egress-token-value@egress-proxy.strange-company.svc.cluster.local:3128"
	if got := env["HTTPS_PROXY"]; got != wantProxy {
		t.Errorf("HTTPS_PROXY = %q, want %q (the egress token, resolved by the shim)", got, wantProxy)
	}
	for _, name := range []string{"SC_RUNNER_URL", "SC_TASK_ID", "KUBERNETES_SERVICE_HOST"} {
		if v, ok := env[name]; ok {
			t.Errorf("the harness environment has %s=%q", name, v)
		}
	}
	files, err := os.ReadFile(filepath.Join(gr.ws, "out", "files.txt"))
	if err != nil {
		t.Fatalf("the fake harness wrote no /workspace/out/files.txt: %v", err)
	}
	keyLine := strings.Split(deployKeyBody, "\n")[1]
	for name, text := range map[string]string{"env.txt": string(raw), "files.txt": string(files)} {
		for _, secret := range []string{taskToken, keyLine} {
			if strings.Contains(text, secret) {
				t.Errorf("%s contains a secret that the harness container must not see: %q", name, secret)
			}
		}
	}
	for _, l := range strings.Split(string(files), "\n") {
		if strings.Contains(l, "task-token") || strings.Contains(l, "deploy-key") || strings.Contains(l, "/var/sc/private") {
			t.Errorf("files.txt lists %q", l)
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// S4-N4: no harness descendant survives a job

// S4-N4 (plan 27 section 7.4, review S4 N4): the shim is a child subreaper and kills every process that is still alive
// below it after a job, including a double-forked, setsid'd one, and it does so before it publishes res-<n>.json, so that
// sc-worker never runs post-harness git next to a live harness descendant.
func TestShim_KillsDoubleForkedDescendantAfterJob_S4N4(t *testing.T) {
	t.Parallel()
	gr := s4Run(t, s4Opts{mode: "ops", ops: []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "double-fork"}}})
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
	pid, _ = strconv.Atoi(gr.outFile("linger.pid"))
	if pid == 0 {
		t.Fatalf("the harness did not record the pid of its background process; stream:\n%s", gr.streamText(1))
	}
	if pidAlive(pid) {
		t.Errorf("the double-forked background process %d is alive although the shim published res-1.json", pid)
	}
	gr.finishWithin(t, 90*time.Second, "the result")
	if pidAlive(pid) {
		t.Errorf("the double-forked background process %d is still alive after the run", pid)
	}
}

// S4-N4: a foreign (forged) res-<n>.json for a request that is still running makes the shim kill the harness and every
// descendant; nothing is left that could keep rewriting the workspace.
func TestShim_ForgedResultKillsHarnessAndDescendants_S4N4(t *testing.T) {
	t.Parallel()
	stop := filepath.Join(t.TempDir(), "stop-writer")
	ops := []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc), {Op: "record-proc"}, {Op: "double-fork"}, {Op: "linger", Path: stop}}
	gr := s4Run(t, s4Opts{mode: "ops", ops: ops})
	var lingerPID, harnessPID int
	t.Cleanup(func() {
		_ = os.WriteFile(stop, nil, 0o644)
		for _, p := range []int{lingerPID, harnessPID} {
			if p > 0 {
				_ = syscall.Kill(p, syscall.SIGKILL)
			}
		}
	})
	gr.waitExit(t, 90*time.Second, "the forged result")
	lingerPID, _ = strconv.Atoi(gr.outFile("linger.pid"))
	if f := strings.Fields(gr.outFile("proc.txt")); len(f) > 0 {
		harnessPID, _ = strconv.Atoi(f[0])
	}
	if lingerPID == 0 || harnessPID == 0 {
		t.Fatalf("the harness did not record its pids (linger %d, harness %d)", lingerPID, harnessPID)
	}
	waitUntil(t, 30*time.Second, "the shim to kill the harness and its background process", func() bool {
		return !pidAlive(lingerPID) && !pidAlive(harnessPID)
	})
	gr.endShim()
}

// S4-N4: as PID 1 (the shim is the init process of the harness container) the sweep kills every process of the
// container with kill(-1, SIGKILL); anywhere else it walks the descendants. A Layer-1 test must never signal -1, so
// this pins only the pure decision; the descendant walk is exercised by the two tests above.
func TestShim_SweepMode_PID1KillsEverything_S4N4(t *testing.T) {
	if got := worker.ShimSweepMode(1); got != "all" {
		t.Errorf("ShimSweepMode(1) = %q, want all (kill(-1, SIGKILL))", got)
	}
	for _, pid := range []int{2, 47, 4242} {
		if got := worker.ShimSweepMode(pid); got != "descendants" {
			t.Errorf("ShimSweepMode(%d) = %q, want descendants", pid, got)
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------
// S4-NB-d: a lingering ssh child cannot block git

func lingeringSSH(passThrough bool) func(map[string]string, string) string {
	return func(dirs map[string]string, helper string) string {
		pids := filepath.Join(filepath.Dir(dirs["bin"]), "ssh-linger.pids")
		s := "#!/bin/sh\nsleep 1000 &\necho $! >> '" + pids + "'\n"
		if passThrough {
			return s + "exec " + helper + " \"$@\"\n"
		}
		return s + "exit 255\n"
	}
}

func killLingerers(t *testing.T, gr *gitRun) {
	t.Helper()
	t.Cleanup(func() {
		b, _ := os.ReadFile(filepath.Join(filepath.Dir(gr.dirs["bin"]), "ssh-linger.pids"))
		for _, f := range strings.Fields(string(b)) {
			if pid, err := strconv.Atoi(f); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
}

// S4-NB-d: ssh fails after leaving a background child that holds git's stdout and stderr; git exits, but a Wait on the
// captured pipes would block until that child dies. sc-worker must not wait for it (cmd.WaitDelay or a process-group
// kill) and posts failed/clone_failed.
func TestGit_LingeringSSHChildDoesNotBlockFailedClone_S4NBd(t *testing.T) {
	t.Parallel()
	fx := buildFixture(t, false)
	gr := runGit(t, gitSpec{fx: fx, pkt: gitPacket(t, fx, pktOpts{}), sshScript: lingeringSSH(false)})
	killLingerers(t, gr)
	gr.waitExit(t, 45*time.Second, "a clone whose ssh left a background child")
	gr.cancelShim()
	<-gr.shimDone
	posts := gr.stub.calls("POST", "/v1/worker/result")
	if len(posts) != 1 {
		t.Fatalf("%d result POSTs, want 1; stderr:\n%s", len(posts), gr.proc.stderr.String())
	}
	wantFailedResult(t, decodeResult(t, posts[0].Body), "clone_failed")
}

// S4-NB-d: every ssh call (fetch, ls-remote, push, ls-remote) leaves a background child holding the pipes; the run still
// completes, pushed, within a bounded time.
func TestGit_LingeringSSHChildDoesNotBlockSuccessfulRun_S4NBd(t *testing.T) {
	t.Parallel()
	fx := buildFixture(t, false)
	gr := runGit(t, gitSpec{fx: fx, pkt: gitPacket(t, fx, pktOpts{}), mode: "ops",
		ops: []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc)}, sshScript: lingeringSSH(true)})
	killLingerers(t, gr)
	gr.waitExit(t, 100*time.Second, "a run whose ssh calls left background children")
	res := gr.finish(t)
	if res["state"] != "completed" || res["pushed"] != true {
		t.Errorf("state = %v, pushed = %v, want completed and pushed; failure %v", res["state"], res["pushed"], res["failure"])
	}
	if head := fx.head("agent/sc-1"); head == "" || head != res["commit"] {
		t.Errorf("remote head %q, result commit %v", head, res["commit"])
	}
}

// ---------------------------------------------------------------------------------------------------------------
// S5c-r3 F2: the push retry loop

// S5c-r3 F2 (plan 27 section 14.11 and 13.3): the repository directory is swapped while `git push` runs (the fake ssh
// does it on receive-pack and then fails the transport). The failure carries workspace_tampered, so the push stage
// ends at once as failed/internal with detail workspace_tampered, no ref changes, and the clock shows none of the 2, 4
// and 8 s retry sleeps.
func TestPush_TamperedWorkspaceEndsImmediately_NoBackoff_S5cR3F2(t *testing.T) {
	t.Parallel()
	fx := buildFixture(t, false)
	before := fx.refsDigest()
	script := func(dirs map[string]string, helper string) string {
		ws := dirs["workspace"]
		return "#!/bin/sh\ncase \"$*\" in\n*receive-pack*)\n  mv '" + ws + "/repo' '" + ws + "/repo.real' && ln -s '" + dirs["private"] + "' '" + ws + "/repo'\n  exit 255 ;;\nesac\n" +
			"exec " + helper + " \"$@\"\n"
	}
	gr := runGit(t, gitSpec{fx: fx, pkt: gitPacket(t, fx, pktOpts{}), mode: "ops",
		ops: []harnessOp{opWrite("calc/add_ac_test.go", addACTestSrc)}, sshScript: script, sleepLog: true})
	res := gr.finish(t)
	if detail := wantFailedResult(t, res, "internal"); detail != "workspace_tampered" {
		t.Errorf("detail = %q, want workspace_tampered", detail)
	}
	if got := fx.refsDigest(); got != before {
		t.Errorf("remote refs changed:\n%s", fx.refList())
	}
	for _, d := range gr.sleeps(t) {
		if d == 2*time.Second || d == 4*time.Second || d == 8*time.Second {
			t.Errorf("sc-worker slept %s: a tampered workspace must not enter the 2+4+8 s push retry ladder", d)
		}
	}
}
