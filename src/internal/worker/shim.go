package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"strange.company/sc/internal/clock"
)

// ShimConfig configures the harness-container side of the shim protocol (plan 27 section 7.4). DefaultShimConfig
// returns the production values; tests inject the roots and the program rewrite through the library entry.
type ShimConfig struct {
	// WorkspaceDir is the directory that holds .sc-shim (production: /workspace, or $SC_WORKSPACE).
	WorkspaceDir string
	// HarnessSecretsDir resolves @file:<name> references in request environments (production:
	// /var/run/sc/harness-secrets).
	HarnessSecretsDir string
	// PathMap maps production path prefixes (for example /workspace) in the argv, cwd and environment values of
	// a request to directories of this process's filesystem. It is a test seam; production leaves it nil.
	PathMap map[string]string
	// EnvOverride replaces environment values of the started process after @file resolution (test seam).
	EnvOverride map[string]string
	// Rewrite is a test seam called with the resolved argv and environment (KEY=VALUE) of every request just before
	// the process starts; it returns the argv and environment to run. Production leaves it nil. It never receives
	// packet content.
	Rewrite func(argv, env []string) ([]string, []string)
	Clock   clock.Clock
	Stdout  io.Writer
	Stderr  io.Writer
}

const (
	defaultHarnessSecrets = "/var/run/sc/harness-secrets"
	shimPollInterval      = 50 * time.Millisecond
	// aliveStale is the age of sc-worker's `alive` file after which the shim leaves on its own (plan 27 section 7.4).
	aliveStale = 120 * time.Second
	// maxShimLine bounds one buffered output line; longer output is split (the framer truncates at 64 KiB anyway).
	maxShimLine   = 1 << 20
	maxShimReq    = 4 << 20
	defaultGrace  = 10
	maxCancelGr   = 600
	shimFileMode  = 0o640
	shimStampForm = "2006-01-02T15:04:05.000Z07:00"
)

// DefaultShimConfig returns the production shim configuration: /workspace (or $SC_WORKSPACE), the
// plan 27 harness-secrets directory, the real clock and the process streams.
func DefaultShimConfig() ShimConfig {
	cfg := ShimConfig{
		WorkspaceDir:      defaultWorkspace,
		HarnessSecretsDir: defaultHarnessSecrets,
		Clock:             clock.Real{},
		Stdout:            os.Stdout,
		Stderr:            os.Stderr,
	}
	if v := os.Getenv("SC_WORKSPACE"); v != "" {
		cfg.WorkspaceDir = v
	}
	return cfg
}

type shimRequest struct {
	ID               string            `json:"id"`
	Kind             string            `json:"kind"`
	Argv             []string          `json:"argv"`
	Env              map[string]string `json:"env"`
	Cwd              string            `json:"cwd"`
	TimeoutSeconds   int               `json:"timeout_seconds"`
	TermGraceSeconds int               `json:"term_grace_seconds"`
}

type shimState struct {
	cfg     ShimConfig
	clk     clock.Clock
	dir     string
	errw    io.Writer
	wg      sync.WaitGroup
	mu      sync.Mutex
	running map[string]*shimJob
}

// shimJob is one request that the shim is executing.
type shimJob struct {
	n      string
	grace  time.Duration
	cancel chan time.Duration // termination request with the grace period (buffered, non-blocking sends)
}

func (j *shimJob) terminate(grace time.Duration) {
	select {
	case j.cancel <- grace:
	default:
	}
}

// Shim is the whole of `sc-worker shim` (plan 27 section 7.4): it executes the run requests that sc-worker writes
// to <WorkspaceDir>/.sc-shim and returns the process exit code.
func Shim(cfg ShimConfig) int {
	s := &shimState{cfg: cfg, clk: cfg.Clock, errw: cfg.Stderr, running: map[string]*shimJob{}}
	if s.clk == nil {
		s.clk = clock.Real{}
	}
	if s.errw == nil {
		s.errw = io.Discard
	}
	s.dir = filepath.Join(cfg.WorkspaceDir, ".sc-shim")

	var sigRecv atomic.Bool
	sigc := make(chan os.Signal, 4)
	signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigc)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go func() {
		select {
		case <-sigc:
			sigRecv.Store(true)
		case <-ctx.Done():
		}
	}()

	handled := map[string]bool{}
	began := s.clk.Now()
	forwarded := false
	for {
		if sigRecv.Load() && !forwarded {
			// A container SIGTERM goes to the running process groups (plan 27 section 7.4).
			forwarded = true
			s.terminateAll(0)
		}
		if forwarded && s.idle() {
			s.wg.Wait()
			return 128 + int(syscall.SIGTERM)
		}
		if st, err := os.Lstat(s.dir); err == nil && st.IsDir() {
			if _, err := os.Lstat(filepath.Join(s.dir, "done")); err == nil {
				s.terminateAll(0)
				s.wg.Wait()
				return 0
			}
			ref := began
			if ast, err := os.Lstat(filepath.Join(s.dir, "alive")); err == nil {
				ref = ast.ModTime()
			}
			if s.clk.Now().Sub(ref) > aliveStale {
				fmt.Fprintln(s.errw, "sc-worker shim: sc-worker's alive file is stale; leaving")
				s.terminateAll(0)
				s.wg.Wait()
				return 1
			}
			if !forwarded {
				s.scan(handled)
			}
			s.checkCancels()
		}
		_ = s.clk.Sleep(ctx, shimPollInterval)
	}
}

func (s *shimState) idle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running) == 0
}

// terminateAll asks every running job to terminate; grace 0 means the job's own term_grace_seconds.
func (s *shimState) terminateAll(grace time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.running {
		g := grace
		if g <= 0 {
			g = j.grace
		}
		j.terminate(g)
	}
}

var shimReqName = regexp.MustCompile(`^req-([0-9]+)\.json$`)

// scan starts every request file that was not seen before, in numeric order.
func (s *shimState) scan(handled map[string]bool) {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	var fresh []string
	for _, e := range ents {
		if m := shimReqName.FindStringSubmatch(e.Name()); m != nil && !handled[m[1]] {
			fresh = append(fresh, m[1])
		}
	}
	sort.Slice(fresh, func(i, k int) bool {
		if len(fresh[i]) != len(fresh[k]) {
			return len(fresh[i]) < len(fresh[k])
		}
		return fresh[i] < fresh[k]
	})
	for _, n := range fresh {
		handled[n] = true
		j := &shimJob{n: n, cancel: make(chan time.Duration, 1)}
		s.mu.Lock()
		s.running[n] = j
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.running, j.n)
				s.mu.Unlock()
			}()
			s.runJob(j)
		}()
	}
}

// checkCancels turns a cancel-<n> file into a termination request for the running job n. The file may carry
// {"term_grace_seconds": N} to shorten the grace period (the SIGTERM path of sc-worker, plan 27 section 14.14).
func (s *shimState) checkCancels() {
	s.mu.Lock()
	jobs := make([]*shimJob, 0, len(s.running))
	for _, j := range s.running {
		jobs = append(jobs, j)
	}
	s.mu.Unlock()
	for _, j := range jobs {
		p := filepath.Join(s.dir, "cancel-"+j.n)
		st, err := os.Lstat(p)
		if err != nil {
			continue
		}
		s.mu.Lock()
		g := j.grace
		s.mu.Unlock()
		if st.Mode().IsRegular() && st.Size() < 4096 {
			var c struct {
				Grace int `json:"term_grace_seconds"`
			}
			if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &c) == nil && c.Grace > 0 && c.Grace <= maxCancelGr {
				g = time.Duration(c.Grace) * time.Second
			}
		}
		j.terminate(g)
	}
}

// shimLines appends complete lines to the stream file; stderr lines carry a prefix.
type shimLines struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix string
	buf    []byte
}

func (l *shimLines) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.emit(l.buf[:i])
		l.buf = l.buf[i+1:]
	}
	for len(l.buf) > maxShimLine {
		l.emit(l.buf[:maxShimLine])
		l.buf = l.buf[maxShimLine:]
	}
	return len(p), nil
}

func (l *shimLines) emit(line []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(append([]byte(l.prefix), line...), '\n'))
}

func (l *shimLines) flush() {
	if len(l.buf) > 0 {
		l.emit(l.buf)
		l.buf = nil
	}
}

var shimFileRef = regexp.MustCompile(`@file:([a-z][a-z0-9-]*)`)

func (s *shimState) mapPath(v string) string {
	best := ""
	for from := range s.cfg.PathMap {
		if (v == from || strings.HasPrefix(v, from+"/")) && len(from) > len(best) {
			best = from
		}
	}
	if best == "" {
		return v
	}
	return s.cfg.PathMap[best] + v[len(best):]
}

// stamp formats a time for the result file.
func stamp(t time.Time) string { return t.UTC().Format(shimStampForm) }

// after returns a channel that is closed after d on the shim's clock, or never when ctx ends first.
func (s *shimState) after(ctx context.Context, d time.Duration) <-chan struct{} {
	c := make(chan struct{})
	go func() {
		if s.clk.Sleep(ctx, d) == nil {
			close(c)
		}
	}()
	return c
}

var signalNames = map[syscall.Signal]string{
	syscall.SIGTERM: "SIGTERM", syscall.SIGKILL: "SIGKILL", syscall.SIGINT: "SIGINT", syscall.SIGHUP: "SIGHUP",
	syscall.SIGSEGV: "SIGSEGV", syscall.SIGABRT: "SIGABRT", syscall.SIGPIPE: "SIGPIPE", syscall.SIGQUIT: "SIGQUIT",
}

func signalName(sig syscall.Signal) string {
	if n, ok := signalNames[sig]; ok {
		return n
	}
	return fmt.Sprintf("SIG%d", int(sig))
}

// lookPath resolves a bare program name along the PATH of the request environment.
func lookPath(prog string, env []string) string {
	if strings.Contains(prog, "/") {
		return prog
	}
	path := os.Getenv("PATH")
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	for _, d := range filepath.SplitList(path) {
		if d == "" {
			continue
		}
		p := filepath.Join(d, prog)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
			return p
		}
	}
	return prog
}

func (s *shimState) writeResult(n string, res shimResult) {
	b, _ := json.Marshal(res)
	tmp := filepath.Join(s.dir, "res-"+n+".json.tmp")
	if err := os.WriteFile(tmp, b, shimFileMode); err != nil {
		fmt.Fprintf(s.errw, "sc-worker shim: result %s: %v\n", n, err)
		return
	}
	if err := os.Rename(tmp, filepath.Join(s.dir, "res-"+n+".json")); err != nil {
		fmt.Fprintf(s.errw, "sc-worker shim: result %s: %v\n", n, err)
	}
}

// runJob executes one request: start the process in a group of its own, copy its output to the stream file,
// enforce the timeout, honor termination requests and write the result file.
func (s *shimState) runJob(j *shimJob) {
	started := s.clk.Now()
	res := shimResult{StartedAt: stamp(started), ExitCode: -1}
	finish := func() {
		res.FinishedAt = stamp(s.clk.Now())
		s.writeResult(j.n, res)
	}

	reqPath := filepath.Join(s.dir, "req-"+j.n+".json")
	var req shimRequest
	st, err := os.Lstat(reqPath)
	if err == nil && st.Mode().IsRegular() && st.Size() <= maxShimReq {
		var b []byte
		if b, err = os.ReadFile(reqPath); err == nil {
			err = json.Unmarshal(b, &req)
		}
	} else if err == nil {
		err = errors.New("not a regular request file")
	}
	res.ID = req.ID
	grace := time.Duration(req.TermGraceSeconds) * time.Second
	if grace <= 0 {
		grace = defaultGrace * time.Second
	}
	s.mu.Lock()
	j.grace = grace
	s.mu.Unlock()

	streamPath := filepath.Join(s.dir, "stream-"+j.n+".log")
	stream, serr := os.OpenFile(streamPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY|syscall.O_NOFOLLOW, shimFileMode)
	if serr != nil {
		fmt.Fprintf(s.errw, "sc-worker shim: stream %s: %v\n", j.n, serr)
		finish()
		return
	}
	defer stream.Close()
	var smu sync.Mutex
	fail := func(msg string, code int) {
		_, _ = fmt.Fprintf(stream, "[stderr] sc-worker shim: %s\n", msg)
		res.ExitCode = code
		finish()
	}
	if err != nil || len(req.Argv) == 0 {
		fail("invalid request", 126)
		return
	}

	argv := make([]string, len(req.Argv))
	for i, a := range req.Argv {
		argv[i] = s.mapPath(a)
	}
	keys := make([]string, 0, len(req.Env))
	for k := range req.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		v := s.mapPath(req.Env[k])
		var missing string
		v = shimFileRef.ReplaceAllStringFunc(v, func(ref string) string {
			b, rerr := os.ReadFile(filepath.Join(s.cfg.HarnessSecretsDir, strings.TrimPrefix(ref, "@file:")))
			if rerr != nil {
				missing = ref
				return ""
			}
			return strings.TrimSpace(string(b))
		})
		if missing != "" {
			fail("secret file "+strings.TrimPrefix(missing, "@file:")+" is not mounted", 126)
			return
		}
		if o, ok := s.cfg.EnvOverride[k]; ok {
			v = o
		}
		env = append(env, k+"="+v)
	}
	if s.cfg.Rewrite != nil {
		argv, env = s.cfg.Rewrite(argv, env)
	}

	cmd := exec.Command(lookPath(argv[0], env), argv[1:]...)
	cmd.Env = env
	cmd.Dir = s.mapPath(req.Cwd)
	cmd.Stdin = nil // /dev/null (plan 27 section 7.2)
	so := &shimLines{mu: &smu, w: stream}
	se := &shimLines{mu: &smu, w: stream, prefix: "[stderr] "}
	cmd.Stdout, cmd.Stderr = so, se
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		fail("start failed: "+err.Error(), 127)
		return
	}
	pgid := cmd.Process.Pid
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var timeoutC <-chan struct{}
	if req.TimeoutSeconds > 0 {
		timeoutC = s.after(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
	}
	var killC <-chan struct{}
	terminating := false
	term := func(g time.Duration) {
		if terminating {
			return
		}
		terminating = true
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
		killC = s.after(ctx, g)
	}
	var waitErr error
loop:
	for {
		select {
		case waitErr = <-exited:
			break loop
		case <-timeoutC:
			timeoutC = nil
			res.TimedOut = true
			term(grace)
		case g := <-j.cancel:
			term(g)
		case <-killC:
			killC = nil
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}
	// Whatever is left in the group dies with the leader.
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	so.flush()
	se.flush()

	if ps := cmd.ProcessState; ps != nil {
		res.ExitCode = ps.ExitCode()
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Signal = signalName(ws.Signal())
			res.ExitCode = -1
		}
	}
	if waitErr != nil && !errors.Is(waitErr, exec.ErrWaitDelay) {
		var ee *exec.ExitError
		if !errors.As(waitErr, &ee) {
			fmt.Fprintf(s.errw, "sc-worker shim: wait %s: %v\n", j.n, waitErr)
		}
	}
	finish()
}
