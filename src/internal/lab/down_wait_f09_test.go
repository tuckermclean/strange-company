package lab_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/lab"
)

// downRig is a run with one qemu (pid 4242) and one weed (pid 4444) whose liveness the test controls.
type downRig struct {
	*rig
	fake      *clock.Fake
	start     time.Time
	termed    map[int]bool
	hardened  map[int]bool
	polls     map[int]int
	exitAfter int  // polls after SIGTERM before a pid exits (-1: ignores SIGTERM)
	hardWorks bool // SIGKILL ends the process
}

func newDownRig(t *testing.T) *downRig {
	t.Helper()
	d := &downRig{rig: newRig(t, debianSHA256(sha256hex(debianBytes))), termed: map[int]bool{}, hardened: map[int]bool{},
		polls: map[int]int{}, hardWorks: true}
	runDir := filepath.Join(d.env.RunBase, "t1")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, pid := range map[string]string{"node-01": "4242", "s3": "4444"} {
		if err := os.WriteFile(filepath.Join(runDir, name+".pid"), []byte(pid+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d.start = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d.fake = clock.NewFake(d.start)
	d.env.Clock = d.fake
	d.env.ProcName = func(pid int) (string, error) {
		if pid == 4242 {
			return "qemu-system-x86", nil
		}
		return "weed", nil
	}
	d.env.Kill = func(pid int) error { d.termed[pid] = true; return nil }
	d.env.KillHard = func(pid int) error { d.hardened[pid] = true; return nil }
	d.env.Alive = func(pid int) bool {
		if d.hardened[pid] && d.hardWorks {
			return false
		}
		if !d.termed[pid] {
			return true
		}
		if d.exitAfter < 0 {
			return true
		}
		d.polls[pid]++
		return d.polls[pid] <= d.exitAfter
	}
	return d
}

// 05-AC-6: Down returns only after the killed processes have exited (the harness counts qemu-system right after).
func TestDown_WaitsForProcessesToExit_05AC6(t *testing.T) {
	d := newDownRig(t)
	d.exitAfter = 3
	if err := lab.Down(d.env, "t1"); err != nil {
		t.Fatalf("Down: %v", err)
	}
	for _, pid := range []int{4242, 4444} {
		if !d.termed[pid] {
			t.Errorf("pid %d was not sent SIGTERM", pid)
		}
		if d.env.Alive(pid) {
			t.Errorf("pid %d still alive when Down returned", pid)
		}
		if d.hardened[pid] {
			t.Errorf("pid %d was SIGKILLed although it exited after SIGTERM", pid)
		}
	}
}

// 05-AC-6: a process that ignores SIGTERM is SIGKILLed once the (bounded) deadline passes on the injected clock.
func TestDown_EscalatesToSIGKILLAfterDeadline_05AC6(t *testing.T) {
	d := newDownRig(t)
	d.exitAfter = -1
	if err := lab.Down(d.env, "t1"); err != nil {
		t.Fatalf("Down: %v", err)
	}
	for _, pid := range []int{4242, 4444} {
		if !d.hardened[pid] {
			t.Errorf("pid %d ignored SIGTERM but was not SIGKILLed", pid)
		}
		if d.env.Alive(pid) {
			t.Errorf("pid %d still alive when Down returned", pid)
		}
	}
	if el := d.fake.Now().Sub(d.start); el <= 0 || el > 2*time.Minute {
		t.Errorf("Down waited %v on the clock, want a positive bounded wait (<= 2m)", el)
	}
}

// 05-AC-6: a process that survives SIGKILL makes Down fail (a non-zero sc-lab exit) and name the pid.
func TestDown_ProcessRemains_Errors_05AC6(t *testing.T) {
	d := newDownRig(t)
	d.exitAfter = -1
	d.hardWorks = false
	err := lab.Down(d.env, "t1")
	if err == nil {
		t.Fatal("Down returned nil although pids 4242 and 4444 are still alive")
	}
	if !strings.Contains(err.Error(), "4242") {
		t.Errorf("error %q does not name the remaining pid 4242", err)
	}
	if el := d.fake.Now().Sub(d.start); el > 2*time.Minute {
		t.Errorf("Down waited %v on the clock, want a bounded wait (<= 2m)", el)
	}
}

// 05-AC-6 (S9a review): a failing kill of a live process is reported, not silently ignored.
func TestDown_KillFailureIsReported_05AC6(t *testing.T) {
	d := newDownRig(t)
	d.env.Kill = func(pid int) error { return errors.New("operation not permitted") }
	d.env.KillHard = func(pid int) error { return errors.New("operation not permitted") }
	d.env.Alive = func(int) bool { return true }
	err := lab.Down(d.env, "t1")
	if err == nil {
		t.Fatal("Down returned nil although no kill succeeded and the processes remain")
	}
	if !strings.Contains(err.Error(), "operation not permitted") && !strings.Contains(d.stderr.String(), "operation not permitted") {
		t.Errorf("kill failure not reported: err %v, stderr %q", err, d.stderr.String())
	}
}

// 05-AC-6 (S9a review): a pid whose ProcName lookup fails is logged, not skipped silently.
func TestDown_ProcNameFailureIsLogged_05AC6(t *testing.T) {
	d := newDownRig(t)
	runDir := filepath.Join(d.env.RunBase, "t1")
	if err := os.WriteFile(filepath.Join(runDir, "node-02.pid"), []byte("4545\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inner := d.env.ProcName
	d.env.ProcName = func(pid int) (string, error) {
		if pid == 4545 {
			return "", errors.New("no such process")
		}
		return inner(pid)
	}
	d.exitAfter = 1
	if err := lab.Down(d.env, "t1"); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if !strings.Contains(d.stderr.String(), "4545") {
		t.Errorf("stderr %q does not mention pid 4545 whose name lookup failed", d.stderr.String())
	}
}
