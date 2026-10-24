package worker

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"strange.company/sc/internal/clock"
)

// The sweep of the shim (plan 27 section 7.4, review S4 N4): after each job, and when a job is killed because of a
// forged result file, no process of the job may be left alive before res-<n>.json is published, whatever it did
// (setsid, double fork, leaving the process group).

const (
	sweepRounds = 300
	sweepPause  = 10 * time.Millisecond
)

// sweep kills every process left over by job j and reports whether it could not finish (a process still alive after
// the bounded rounds, or an unreadable /proc). sc-worker issues shim jobs strictly one after the other, so any other job
// that runs now was forged by the harness: it is killed with its whole subtree, like the leftovers of j. As PID 1 of
// the container the shim kills everything that is not itself, unconditionally.
func (s *shimState) sweep(j *shimJob) (incomplete bool) {
	s.killOthers(j)
	if ShimSweepMode(os.Getpid()) == "all" {
		incomplete = s.sweepAll()
	}
	if SweepDescendantsOf(s.clk, "/proc", os.Getpid(), sweepRounds) {
		incomplete = true
	}
	return incomplete
}

// killOthers asks every running job besides j to die at once.
func (s *shimState) killOthers(j *shimJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.running {
		if o != j {
			o.terminate(killNow)
		}
	}
}

// sweepAll is kill(-1, SIGKILL) as the init process of the container, followed by the reaping of every child. It
// signals -1 only when this process really is PID 1. It reports true when the children were not all gone in time.
func (s *shimState) sweepAll() (incomplete bool) {
	if os.Getpid() != 1 {
		return false
	}
	_ = syscall.Kill(-1, syscall.SIGKILL)
	for i := 0; i < sweepRounds; i++ {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if err == syscall.ECHILD {
			return false
		}
		if pid <= 0 {
			if s.clk.Sleep(context.Background(), sweepPause) != nil {
				return true
			}
		}
	}
	return true
}

type procInfo struct {
	ppid   int
	zombie bool
}

// procTable reads pid -> (ppid, state) of every process from a /proc-shaped directory; ok is false when the
// directory cannot be read.
func procTable(root string) (out map[int]procInfo, ok bool) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, false
	}
	out = map[int]procInfo{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, e.Name(), "stat"))
		if err != nil {
			continue
		}
		str := string(b)
		i := strings.LastIndexByte(str, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(str[i+1:])
		if len(f) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		out[pid] = procInfo{ppid: ppid, zombie: f[0] == "Z" || f[0] == "X"}
	}
	return out, true
}

// SweepDescendantsOf kills (SIGKILL) and reaps every descendant of self found in the /proc-shaped directory procRoot
// until none is left alive, repeating because a dying process may have forked once more. It reports incomplete when a
// descendant is still alive after rounds rounds or the directory is unreadable.
func SweepDescendantsOf(clk clock.Clock, procRoot string, self, rounds int) (incomplete bool) {
	for i := 0; i < rounds; i++ {
		tab, ok := procTable(procRoot)
		if !ok {
			return true
		}
		children := map[int][]int{}
		for pid, p := range tab {
			children[p.ppid] = append(children[p.ppid], pid)
		}
		alive := 0
		stack := append([]int(nil), children[self]...)
		for len(stack) > 0 {
			pid := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			stack = append(stack, children[pid]...)
			if tab[pid].zombie {
				if tab[pid].ppid == self {
					var ws syscall.WaitStatus
					_, _ = syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
				}
				continue
			}
			alive++
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		if alive == 0 {
			return false
		}
		if clk.Sleep(context.Background(), sweepPause) != nil {
			return true
		}
	}
	return true
}
