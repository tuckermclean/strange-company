package engine_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"strange.company/sc/internal/bootstrap/engine"
	"strange.company/sc/internal/clock"
)

// --- a real SIGKILL (subprocess) and the same-host takeover that follows (50-AC-17)

// markerPipeline is a pipeline whose world is a directory of marker files: Run appends a line, Check is the file.
func markerPipeline(dir string) (engine.Pipeline, func() map[string]int) {
	mk := func(name string) *unit {
		u := newUnit(name)
		path := dir + "/marks/" + name
		u.checkFn = func(*unit) engine.Check {
			if _, err := os.Stat(path); err == nil {
				return engine.Check{State: engine.Done}
			}
			return engine.Check{State: engine.NotDone}
		}
		u.runFn = func(*unit) ([]engine.OutputRef, error) {
			_ = os.MkdirAll(dir+"/marks", 0o755)
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			_, err = f.WriteString("run\n")
			return nil, err
		}
		return u
	}
	steps := []string{"plan.install-identity", "plan.storage-apply", "plan.confirm"}
	var su []engine.Unit
	for _, s := range steps {
		su = append(su, mk(s))
	}
	plan := newUnit("plan", su...)
	prov := mk("provision")
	counts := func() map[string]int {
		m := map[string]int{}
		for _, n := range append(append([]string{}, steps...), "provision") {
			b, _ := os.ReadFile(dir + "/marks/" + n)
			m[n] = strings.Count(string(b), "run\n")
		}
		return m
	}
	return pipe(plan, prov), counts
}

// rootEnv is a scratch installation rooted at root, shared with a child process: root/.bootstrap, root/s3, root/marks.
func rootEnv(t *testing.T, root string) *tenv {
	return &tenv{
		t: t, dir: filepath.Join(root, ".bootstrap"), st: newDirStore(filepath.Join(root, "s3"), false),
		clk: clock.NewFake(time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)), rnd: &ctrRand{},
	}
}

// TestCrashHelperProcess is the child of the SIGKILL tests. It does nothing unless SC_ENGINE_HELPER=1. With the
// crash point set it must die by SIGKILL inside Execute; if the engine returns, the child exits with Execute's code
// so the parent sees a plain exit instead of a signal.
func TestCrashHelperProcess(t *testing.T) {
	if os.Getenv("SC_ENGINE_HELPER") != "1" {
		t.Skip("child process of the SIGKILL tests")
	}
	root := os.Getenv("HELPER_DIR")
	v := rootEnv(t, root)
	v.rnd.n = 0xe0
	pl, _ := markerPipeline(root)
	e := v.eng(func(c *engine.Config) {
		c.Env = map[string]string{"SC_TEST_MODE": os.Getenv("SC_TEST_MODE"), "SC_TEST_CRASH_AT": os.Getenv("SC_TEST_CRASH_AT")}
		// c.Kill stays nil: the engine's default is a real SIGKILL of this process
	})
	res := e.Execute(context.Background(), pl, engine.Options{})
	os.Exit(res.Exit)
}

// crashChild runs the helper and reports how it ended.
func crashChild(t *testing.T, root, spec string) (signal syscall.Signal, exitCode int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelperProcess$")
	cmd.Env = append(os.Environ(), "SC_ENGINE_HELPER=1", "HELPER_DIR="+root, "SC_TEST_MODE=1", "SC_TEST_CRASH_AT="+spec)
	err := cmd.Run()
	if err == nil {
		return 0, 0
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("child: %v", err)
	}
	ws := ee.Sys().(syscall.WaitStatus)
	if ws.Signaled() {
		return ws.Signal(), -1
	}
	return 0, ws.ExitStatus()
}

// 50-AC-17 and plan 50 section 22 (BOOT-5): SC_TEST_CRASH_AT delivers SIGKILL at the boundary; the rerun on the same
// host takes the stale lock over (journal confirmation stale-lock-takeover), converges, and no step ran twice.
func TestCrash_SigkillThenSameHostRerunTakesOver_50AC17(t *testing.T) {
	for _, spec := range []string{"plan.storage-apply:before", "plan.storage-apply:after"} {
		t.Run(spec, func(t *testing.T) {
			root := t.TempDir()
			sig, code := crashChild(t, root, spec)
			if sig != syscall.SIGKILL {
				t.Fatalf("child ended with signal %v exit %d, want SIGKILL", sig, code)
			}
			v := rootEnv(t, root)
			_, counts := markerPipeline(root)
			lock := readJSON(t, v.st.raw(lockKey), "lock object after the crash (a killed run does not release it)")
			if !runIDRe.MatchString(str(lock["run_id"])) {
				t.Errorf("lock object after the crash: %v", lock)
			}
			want := 0
			if strings.HasSuffix(spec, ":after") {
				want = 1
			}
			if got := counts()["plan.storage-apply"]; got != want {
				t.Fatalf("after the crash plan.storage-apply ran %d times, want %d", got, want)
			}
			pl, _ := markerPipeline(root)
			e := v.eng()
			res := e.Execute(context.Background(), pl, engine.Options{})
			if res.Exit != 0 {
				t.Fatalf("rerun exit %d (%s %s), want 0 (automatic same-host takeover)", res.Exit, res.Code, res.Message)
			}
			for step, n := range counts() {
				if n != 1 {
					t.Errorf("step %s ran %d times in total, want exactly 1", step, n)
				}
			}
			j := v.remote()
			cs := confirmations(j, "stale-lock-takeover")
			if len(cs) != 1 || str(cs[0]["run_id"]) != e.RunID() {
				t.Errorf("confirmations %v, want one stale-lock-takeover by run %s", j["confirmations"], e.RunID())
			}
			if got := statusOf(t, j, "plan"); got != "succeeded" {
				t.Errorf("plan status %q after the rerun, want succeeded", got)
			}
			journalSchemaValidate(t, j)
			if v.st.raw(lockKey) != nil {
				t.Errorf("the lock object remains after a completed run")
			}
		})
	}
}

// 50-AC-17: the same crash, but the lock object names a different host: the rerun exits 6 and does nothing.
func TestCrash_RerunWithDifferentLockHostExits6_50AC17(t *testing.T) {
	root := t.TempDir()
	if sig, code := crashChild(t, root, "plan.storage-apply:before"); sig != syscall.SIGKILL {
		t.Fatalf("child ended with signal %v exit %d, want SIGKILL", sig, code)
	}
	v := rootEnv(t, root)
	lock := readJSON(t, v.st.raw(lockKey), "lock object")
	lock["host"] = "some-other-host"
	b, _ := jsonMarshal(lock)
	if _, err := v.st.Put(context.Background(), lockKey, b, ""); err != nil {
		t.Fatal(err)
	}
	pl, counts := markerPipeline(root)
	before := counts()
	res := v.exec(pl, engine.Options{})
	if res.Exit != 6 {
		t.Fatalf("rerun exit %d (%s %s), want 6", res.Exit, res.Code, res.Message)
	}
	after := counts()
	for k, n := range before {
		if after[k] != n {
			t.Errorf("step %s ran under a foreign lock", k)
		}
	}
	if string(v.st.raw(lockKey)) != string(b) {
		t.Errorf("the foreign lock object was modified")
	}
}
