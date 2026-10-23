package worker_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
)

// lateSwapRun runs a lingering harness that swaps (private-late) or toggles (toggle-late) <workspace>/repo only after
// sc-worker has written its commit message, with the real worker clock so that a retry ladder shows in elapsed time.
func lateSwapRun(t *testing.T, mode string) (*gitRun, *fixture, string, map[string]any, time.Duration) {
	t.Helper()
	fx := buildFixture(t, false)
	before := fx.refsDigest()
	stop := filepath.Join(t.TempDir(), "stop-writer")
	pkt := gitPacket(t, fx, pktOpts{})
	pkt["execution"].(map[string]any)["deadline_at"] = clock.Real{}.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	ops := symlinkOps(harnessOp{Op: "snapshot-private"}, harnessOp{Op: "linger", Path: stop, Content: mode})
	gr := runGit(t, gitSpec{fx: fx, mode: "ops", ops: ops, pkt: pkt, realClock: true})
	go func() {
		<-gr.proc.done
		_ = os.WriteFile(stop, nil, 0o644)
	}()
	t.Cleanup(func() { _ = os.WriteFile(stop, nil, 0o644) })
	waitUntil(t, 120*time.Second, "the harness to mark the swap", func() bool { return fileExists(filepath.Join(gr.ws, "out", "swapped.txt")) })
	t0 := clock.Real{}.Now()
	gr.waitExit(t, 60*time.Second, "the swap")
	elapsed := clock.Real{}.Now().Sub(t0)
	res := gr.finish(t)
	return gr, fx, before, res, elapsed
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// S5c review 2 (N-a): a repository swap noticed only after the commit message was written (inside a git call of the
// commit or push stage) is workspace_tampered, never commit_failed or push_rejected, and the run does not sit through
// the 2+4+8 s push retry ladder.
func TestSymlinkSafety_LateRepoSwapIsWorkspaceTampered_NoRetryDelay_S5cRev2Na(t *testing.T) {
	gr, fx, before, res, elapsed := lateSwapRun(t, "private-late")
	if detail := wantFailedResult(t, res, "internal"); detail != "workspace_tampered" {
		t.Errorf("detail = %q, want workspace_tampered", detail)
	}
	if elapsed > 8*time.Second {
		t.Errorf("sc-worker took %s after the swap; a tampered workspace must not be retried (14 s ladder)", elapsed)
	}
	if got := fx.refsDigest(); got != before {
		t.Errorf("remote refs changed:\n%s", fx.refList())
	}
	wantPrivateUntouched(t, gr)
}

// S5c review 2 (TOCTOU): the harness renames repo away, links repo to the private directory, unlinks it and renames the
// real directory back, between sc-worker's post-harness git calls. Inode and type are the same at every later check,
// but the workspace directory changed: workspace_tampered, nothing pushed.
func TestSymlinkSafety_RepoToggledBetweenGitCalls_WorkspaceTampered_S5cRev2(t *testing.T) {
	gr, fx, before, res, _ := lateSwapRun(t, "toggle-late")
	if detail := wantFailedResult(t, res, "internal"); detail != "workspace_tampered" {
		t.Errorf("detail = %q, want workspace_tampered", detail)
	}
	if got := fx.refsDigest(); got != before {
		t.Errorf("remote refs changed:\n%s", fx.refList())
	}
	wantPrivateUntouched(t, gr)
}

// S5c review 2 (B2): the worker packages build for linux/arm64 (a supported local-build path, plans 02 and 23).
func TestCrossBuild_LinuxArm64_S5cRev2B2(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go binary")
	}
	for _, pkg := range []string{"strange.company/sc/internal/worker", "strange.company/sc/cmd/sc-worker", "strange.company/sc/cmd/sc-fake-harness"} {
		t.Run(filepath.Base(pkg), func(t *testing.T) {
			cmd := exec.Command(goBin, "build", "-o", os.DevNull, pkg)
			cmd.Dir = ".." + string(filepath.Separator) + ".."
			cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("go build %s for linux/arm64: %v\n%s", pkg, err, strings.TrimSpace(string(out)))
			}
		})
	}
}
