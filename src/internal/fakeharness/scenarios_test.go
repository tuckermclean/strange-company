package fakeharness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/fakeharness"
)

// Slice S4 of F07: the fake scenarios failure, timeout and ignore-cancel (plan 28 section 14.4, section 8.2).

const helperEnv = "SC_TEST_FAKEHARNESS_HELPER" // test-only

func TestHelperProcess_Run(t *testing.T) {
	if os.Getenv(helperEnv) == "" {
		t.Skip("helper process only")
	}
	var args []string
	for i, a := range os.Args {
		if a == "--" {
			args = os.Args[i+1:]
		}
	}
	os.Exit(fakeharness.Run(args, os.Getenv, os.Stdout, os.Stderr))
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type fx struct {
	repo, ctxDir, out string
	gw                *httptest.Server
	hits              atomic.Int64
}

func newFx(t *testing.T) *fx {
	t.Helper()
	f := &fx{repo: t.TempDir(), ctxDir: t.TempDir(), out: t.TempDir()}
	f.gw = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		f.hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"fake","choices":[]}`)
	}))
	t.Cleanup(f.gw.Close)
	return f
}

func (f *fx) args(scenario string) []string {
	return []string{"--scenario", scenario, "--role", "tester", "--attempt", "1", "--task", "sc-1", "--execution", "sc-1-x1",
		"--repo", f.repo, "--context", f.ctxDir, "--out", f.out}
}

func (f *fx) getenv(k string) string {
	switch k {
	case "SC_FAKE_GATEWAY_URL":
		return f.gw.URL
	case "SC_FAKE_GATEWAY_TOKEN":
		return "gw-token-for-tests"
	}
	return ""
}

// plan 28 section 14.4, row failure: prints 3 steps, terminal is_error true, exit 1, no claim (after the model call).
func TestRun_FailureScenario_ThreeStepsTerminalErrorExit1NoClaim(t *testing.T) {
	f := newFx(t)
	var stdout, stderr bytes.Buffer
	code := fakeharness.Run(f.args("failure"), f.getenv, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit code = %d, want 1; stderr: %s", code, stderr.String())
	}
	steps, terminals := 0, 0
	isErr := false
	for _, l := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var ev struct {
			Type    string `json:"type"`
			IsError bool   `json:"is_error"`
		}
		if json.Unmarshal([]byte(l), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "sc_fake_step":
			steps++
		case "sc_fake_result":
			terminals++
			isErr = ev.IsError
		}
	}
	if steps < 3 {
		t.Errorf("%d sc_fake_step lines, want 3", steps)
	}
	if terminals != 1 || !isErr {
		t.Errorf("terminal events = %d (is_error %v), want exactly one with is_error true", terminals, isErr)
	}
	if _, err := os.Stat(filepath.Join(f.out, "claim.json")); err == nil {
		t.Errorf("failure scenario wrote a claim")
	}
	if f.hits.Load() < 1 {
		t.Errorf("no request reached the gateway (28-AC-7)")
	}
}

type child struct {
	cmd    *exec.Cmd
	stdout lockedBuf
	done   chan struct{}
	code   int
}

func startChild(t *testing.T, f *fx, scenario string) *child {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := &child{done: make(chan struct{})}
	c.cmd = exec.Command(exe, append([]string{"-test.run=^TestHelperProcess_Run$", "--"}, f.args(scenario)...)...)
	c.cmd.Env = []string{helperEnv + "=1", "SC_FAKE_GATEWAY_URL=" + f.gw.URL, "SC_FAKE_GATEWAY_TOKEN=gw-token-for-tests", "PATH=" + os.Getenv("PATH")}
	c.cmd.Stdout, c.cmd.Stderr = &c.stdout, &c.stdout
	if err := c.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := c.cmd.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			c.code = ee.ExitCode()
		}
		close(c.done)
	}()
	t.Cleanup(func() { _ = c.cmd.Process.Kill() })
	return c
}

func (c *child) exitedWithin(d time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	select {
	case <-c.done:
		return true
	case <-ctx.Done():
		return false
	}
}

// waitModelCall waits until the child has called the gateway and had time to arm its signal handling.
func waitModelCall(t *testing.T, f *fx, c *child) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for f.hits.Load() < 1 {
		select {
		case <-c.done:
			t.Fatalf("the harness exited (code %d) before it called the gateway; output:\n%s", c.code, c.stdout.String())
		default:
		}
		if err := (clock.Real{}).Sleep(ctx, 20*time.Millisecond); err != nil {
			t.Fatalf("the harness did not call the gateway within 15 s; output:\n%s", c.stdout.String())
		}
	}
	_ = (clock.Real{}).Sleep(context.Background(), 500*time.Millisecond)
	select {
	case <-c.done:
		t.Fatalf("the harness exited (code %d) right after the model call instead of sleeping; output:\n%s", c.code, c.stdout.String())
	default:
	}
}

// plan 28 section 14.4, row timeout: after the model call the fake sleeps 24 h and honors SIGTERM.
func TestRun_TimeoutScenario_SleepsAndHonorsSIGTERM(t *testing.T) {
	f := newFx(t)
	c := startChild(t, f, "timeout")
	waitModelCall(t, f, c)
	if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if !c.exitedWithin(10 * time.Second) {
		t.Errorf("the harness did not exit within 10 s of SIGTERM")
	}
}

// plan 28 section 14.4, row ignore-cancel: after the model call the fake traps SIGTERM and sleeps on.
func TestRun_IgnoreCancelScenario_TrapsSIGTERM(t *testing.T) {
	f := newFx(t)
	c := startChild(t, f, "ignore-cancel")
	waitModelCall(t, f, c)
	if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if c.exitedWithin(2 * time.Second) {
		t.Errorf("the harness exited on SIGTERM (code %d), want it to ignore it", c.code)
	}
	_ = c.cmd.Process.Kill()
}
