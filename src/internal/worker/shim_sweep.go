package worker

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The sweep of the shim (plan 27 section 7.4, review S4 N4): after each job, and when a job is killed because of a
// forged result file, no process of the job may be left alive before res-<n>.json is published, whatever it did
// (setsid, double fork, leaving the process group).

const (
	sweepRounds = 300
	sweepPause  = 10 * time.Millisecond
)

// sweep kills every process left over by job j. As PID 1 of the container it kills everything that is not the shim
// itself; otherwise it walks and kills the descendants of the shim, except those of other running jobs.
func (s *shimState) sweep(j *shimJob) {
	if ShimSweepMode(os.Getpid()) == "all" && s.noOtherJob(j) {
		s.sweepAll()
		return
	}
	s.sweepDescendants(j)
}

func (s *shimState) noOtherJob(j *shimJob) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.running {
		if o != j {
			return false
		}
	}
	return true
}

// otherLeaders are the pids of the leaders of the jobs that run besides j.
func (s *shimState) otherLeaders(j *shimJob) map[int]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[int]bool{}
	for _, o := range s.running {
		if o != j && o.pgid > 0 {
			out[o.pgid] = true
		}
	}
	return out
}

// sweepAll is kill(-1, SIGKILL) as the init process of the container, followed by the reaping of every child. It
// signals -1 only when this process really is PID 1.
func (s *shimState) sweepAll() {
	if os.Getpid() != 1 {
		return
	}
	_ = syscall.Kill(-1, syscall.SIGKILL)
	for i := 0; i < sweepRounds; i++ {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if err == syscall.ECHILD {
			return
		}
		if pid <= 0 {
			if s.clk.Sleep(context.Background(), sweepPause) != nil {
				return
			}
		}
	}
}

type procInfo struct {
	ppid   int
	zombie bool
}

// procTable reads pid -> (ppid, state) of every process from /proc.
func procTable() map[int]procInfo {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	out := map[int]procInfo{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
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
	return out
}

// sweepDescendants kills (SIGKILL) and reaps every descendant of the shim until none is left alive, repeating
// because a dying process may have forked once more.
func (s *shimState) sweepDescendants(j *shimJob) {
	self := os.Getpid()
	skip := s.otherLeaders(j)
	for i := 0; i < sweepRounds; i++ {
		tab := procTable()
		children := map[int][]int{}
		for pid, p := range tab {
			children[p.ppid] = append(children[p.ppid], pid)
		}
		alive := 0
		stack := append([]int(nil), children[self]...)
		for len(stack) > 0 {
			pid := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if skip[pid] {
				continue // another running job: not ours to kill
			}
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
			return
		}
		if s.clk.Sleep(context.Background(), sweepPause) != nil {
			return
		}
	}
}
