//go:build linux

package verify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

const prSetChildSubreaper = 36 // PR_SET_CHILD_SUBREAPER

// setSubreaper makes this process the adopter of every orphaned descendant, so that a process that detaches itself
// (setsid, setpgid, double fork) is still found and killed (plan 22 section 8).
func setSubreaper() error {
	if _, _, e := syscall.Syscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); e != 0 {
		return fmt.Errorf("prctl(PR_SET_CHILD_SUBREAPER): %v", e)
	}
	return nil
}

const prSetDumpable = 4 // PR_SET_DUMPABLE

// setNotDumpable makes /proc/<pid>/mem and /proc/<pid>/environ of this process unreadable for the suites, which
// run as the same user (blind TDD-B1). It is called after the re-exec, which would reset the flag.
func setNotDumpable() error {
	if _, _, e := syscall.Syscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); e != 0 {
		return fmt.Errorf("prctl(PR_SET_DUMPABLE): %v", e)
	}
	return nil
}

type procInfo struct {
	pid, ppid int
	state     byte
}

// procTable reads /proc/<pid>/stat of every process.
func procTable() []procInfo {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []procInfo
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
		f := strings.Fields(s[i+1:])
		if len(f) < 2 {
			continue
		}
		ppid, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		out = append(out, procInfo{pid: pid, ppid: ppid, state: f[0][0]})
	}
	return out
}

// descendants returns every process whose ancestry reaches self (reparented orphans included).
func descendants(self int) []procInfo {
	tab := procTable()
	children := map[int][]procInfo{}
	for _, p := range tab {
		children[p.ppid] = append(children[p.ppid], p)
	}
	var out []procInfo
	queue := []int{self}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range children[cur] {
			out = append(out, c)
			queue = append(queue, c.pid)
		}
	}
	return out
}

func reapChildren() {
	for {
		var ws syscall.WaitStatus
		p, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil || p <= 0 {
			return
		}
	}
}

// killDescendants sends SIGKILL to every descendant of this process and reaps them, until none is left. Called
// only while no other child process is running.
func killDescendants(clock Clock) error {
	self := os.Getpid()
	for i := 0; i < 3000; i++ {
		ds := descendants(self)
		if len(ds) == 0 {
			return nil
		}
		for _, d := range ds {
			if d.state != 'Z' {
				_ = syscall.Kill(d.pid, syscall.SIGKILL)
			}
		}
		reapChildren()
		if i > 0 {
			clock.Sleep(2)
		}
	}
	return errors.New("descendants of the suite could not be killed")
}

// maxCapture bounds what is kept of a child's stdout or stderr.
const maxCapture = 128 << 20

type capture struct {
	buf      bytes.Buffer
	overflow bool
}

func (c *capture) drain(r io.Reader, wg *sync.WaitGroup) {
	defer wg.Done()
	chunk := make([]byte, 64<<10)
	for {
		n, err := r.Read(chunk)
		if n > 0 && !c.overflow {
			if c.buf.Len()+n > maxCapture {
				c.overflow = true
			} else {
				c.buf.Write(chunk[:n])
			}
		}
		if err != nil {
			return
		}
	}
}

// procResult is what one child process left behind. The descendants are dead and reaped when it is returned.
type procResult struct {
	Exit       int
	TimedOut   bool
	Started    bool
	Stdout     []byte
	Stderr     []byte
	Overflow   bool
	DurationMs int
}

// lookInPath finds name in the colon separated dirs (the child's PATH, not the observer's).
func lookInPath(name, dirs, cwd string) (string, bool) {
	exe := func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
	}
	if strings.Contains(name, "/") {
		p := name
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		return p, exe(p)
	}
	for _, d := range strings.Split(dirs, ":") {
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		if p := filepath.Join(d, name); exe(p) {
			return p, true
		}
	}
	return "", false
}

// execArgv runs argv (no shell) in dir with exactly env, as the leader of its own process group, with stdout and
// stderr captured in memory (nothing the code under test can reach is written to). After the leader exits, or the
// timeout ends it, every remaining descendant is killed and reaped before the result is returned.
func execArgv(ctx context.Context, clock Clock, dir string, argv, env []string, childPath string, timeoutSec int) (procResult, error) {
	var res procResult
	start := clock.Millis()
	path, ok := lookInPath(argv[0], childPath, dir)
	if !ok {
		res.Exit = 127
		return res, nil
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return res, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return res, err
	}
	cmd := exec.Command(path, argv[1:]...)
	cmd.Args = append([]string{argv[0]}, argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdout, cmd.Stderr = outW, errW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var so, se capture
	var wg sync.WaitGroup
	wg.Add(2)
	go so.drain(outR, &wg)
	go se.drain(errR, &wg)
	if err := cmd.Start(); err != nil {
		outW.Close()
		errW.Close()
		wg.Wait()
		outR.Close()
		errR.Close()
		res.Exit = 126
		return res, nil
	}
	outW.Close()
	errW.Close()
	res.Started = true
	pid := cmd.Process.Pid
	tctx, cancel := clock.WithTimeout(ctx, timeoutSec)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var werr error
	select {
	case werr = <-done:
	case <-tctx.Done():
		res.TimedOut = true
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
		werr = <-done
	}
	var ee *exec.ExitError
	switch {
	case werr == nil:
	case errors.As(werr, &ee):
		res.Exit = ee.ExitCode()
	default:
		res.Exit = -1
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	kerr := killDescendants(clock)
	if kerr != nil {
		// A process survived: closing our ends keeps the drain goroutines from waiting for it.
		outR.Close()
		errR.Close()
	}
	wg.Wait()
	outR.Close()
	errR.Close()
	res.Stdout, res.Stderr, res.Overflow = so.buf.Bytes(), se.buf.Bytes(), so.overflow || se.overflow
	res.DurationMs = int(clock.Millis() - start)
	if res.DurationMs < 0 {
		res.DurationMs = 0
	}
	return res, kerr
}
