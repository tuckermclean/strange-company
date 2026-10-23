package worker_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"syscall"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/worker"
)

// The component harness runs sc-worker's real library entry (worker.Run) as a separate process (helper-process
// re-exec of this test binary) against a recording stub Runner. Path roots, the clock and the panic seam are
// injected through worker.Config; the plan 27 section 3 variables travel as real environment variables.

const (
	helperEnv   = "SC_TEST_WORKER_HELPER_CONFIG" // test-only; never read by production code
	taskToken   = "tt-CANARY-0001-task-token-value"
	clockOrigin = "2026-10-13T12:00:10Z"
)

type helperConfig struct {
	PrivateDir, HarnessSecretsDir, HomeDir, TmpDir string
	ClockStart                                     string
	PanicAt                                        string
}

// fakeClock is a deterministic clock: Now is the injected start plus the virtual time slept so far, so no
// outcome depends on wall time. Sleep advances the virtual time at once; it only yields 1 ms of real time so a
// hot retry or heartbeat loop cannot starve the process.
type fakeClock struct {
	*clock.Fake
}

func (c fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := c.Fake.Sleep(ctx, d); err != nil {
		return err
	}
	return clock.Real{}.Sleep(ctx, time.Millisecond)
}

// TestHelperProcess_ScWorkerRun is the child side of the component harness; it does nothing in a normal run.
func TestHelperProcess_ScWorkerRun(t *testing.T) {
	raw := os.Getenv(helperEnv)
	if raw == "" {
		t.Skip("helper process only")
	}
	var hc helperConfig
	if err := json.Unmarshal([]byte(raw), &hc); err != nil {
		t.Fatal(err)
	}
	start, err := time.Parse(time.RFC3339, hc.ClockStart)
	if err != nil {
		t.Fatal(err)
	}
	cfg := worker.DefaultConfig()
	cfg.PrivateDir, cfg.HarnessSecretsDir, cfg.HomeDir, cfg.TmpDir = hc.PrivateDir, hc.HarnessSecretsDir, hc.HomeDir, hc.TmpDir
	cfg.Clock = fakeClock{clock.NewFake(start)}
	cfg.Stdout, cfg.Stderr = os.Stdout, os.Stderr
	cfg.PanicAt = hc.PanicAt
	os.Exit(worker.Run(cfg))
}

type recorded struct {
	Method, Path, Auth, UA string
	Body                   []byte
}

type stubRunner struct {
	srv          *httptest.Server
	mu           sync.Mutex
	reqs         []recorded
	packetStatus int
	packetBody   []byte
	packetHold   chan struct{} // when non-nil, GET /v1/worker/packet blocks until it is closed or the client leaves
	packetSeen   chan struct{}
	resultStatus int
	// resultScript, when non-nil, decides the answer to the nth (1-based) result POST: a status, or hold to block
	// until the client leaves (the request is recorded before it blocks and resultHeld is signalled).
	resultScript func(n int) (status int, hold bool)
	resultHeld   chan struct{}
	nResult      int
}

func newStubRunner(t *testing.T, packet map[string]any) *stubRunner {
	t.Helper()
	s := &stubRunner{packetStatus: 200, resultStatus: 202, packetSeen: make(chan struct{}, 1), resultHeld: make(chan struct{}, 1)}
	if packet != nil {
		b, err := json.Marshal(packet)
		if err != nil {
			t.Fatal(err)
		}
		s.packetBody = b
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, recorded{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("User-Agent"), body})
		hold, pstatus, pbody, rstatus := s.packetHold, s.packetStatus, s.packetBody, s.resultStatus
		rhold := false
		if r.Method == http.MethodPost && r.URL.Path == "/v1/worker/result" {
			s.nResult++
			if s.resultScript != nil {
				rstatus, rhold = s.resultScript(s.nResult)
			}
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/worker/packet":
			select {
			case s.packetSeen <- struct{}{}:
			default:
			}
			if hold != nil {
				select {
				case <-hold:
				case <-r.Context().Done():
					return
				}
			}
			w.WriteHeader(pstatus)
			_, _ = w.Write(pbody)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/worker/heartbeat":
			_, _ = io.WriteString(w, `{"cancel_requested":false,"deadline_at":"2026-10-13T12:20:00Z"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/worker/result":
			if rhold {
				select {
				case s.resultHeld <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				return
			}
			w.WriteHeader(rstatus)
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(func() {
		s.mu.Lock()
		if s.packetHold != nil {
			close(s.packetHold)
			s.packetHold = nil
		}
		s.mu.Unlock()
		s.srv.Close()
	})
	return s
}

func (s *stubRunner) calls(method, path string) []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []recorded
	for _, r := range s.reqs {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func (s *stubRunner) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

type runSpec struct {
	stub    *stubRunner
	env     map[string]string // overrides; an empty-string key value is kept, delete the key to unset
	secrets map[string]string
	panicAt string
}

func (rs *runSpec) defaults(t *testing.T) {
	rs.env = map[string]string{
		"SC_RUNNER_URL":   rs.stub.srv.URL,
		"SC_TASK_ID":      "sc-184",
		"SC_EXECUTION_ID": "sc-184-x1",
	}
	rs.secrets = map[string]string{
		"task-token":    taskToken,
		"gateway-token": "gw-CANARY-0002-gateway-token-value",
		"known_hosts":   "forgejo-ssh.forgejo.svc.cluster.local ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFAKEHOSTKEYFORTESTSONLY\n",
	}
}

type process struct {
	cmd    *exec.Cmd
	stdout bytes.Buffer
	stderr bytes.Buffer
	done   chan struct{}
	code   int
}

func start(t *testing.T, rs *runSpec) *process {
	t.Helper()
	root := t.TempDir()
	dirs := map[string]string{}
	for _, d := range []string{"secrets", "workspace", "harness-secrets", "private", "home", "tmp"} {
		dirs[d] = filepath.Join(root, d)
		if err := os.MkdirAll(dirs[d], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range rs.secrets {
		if err := os.WriteFile(filepath.Join(dirs["secrets"], name), []byte(content), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	hc, _ := json.Marshal(helperConfig{
		PrivateDir: dirs["private"], HarnessSecretsDir: dirs["harness-secrets"], HomeDir: dirs["home"], TmpDir: dirs["tmp"],
		ClockStart: clockOrigin, PanicAt: rs.panicAt,
	})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &process{done: make(chan struct{})}
	p.cmd = exec.Command(exe, "-test.run=^TestHelperProcess_ScWorkerRun$")
	env := []string{
		helperEnv + "=" + string(hc),
		"SC_SECRETS_DIR=" + dirs["secrets"],
		"SC_WORKSPACE=" + dirs["workspace"],
		"HOME=" + dirs["home"],
		"TMPDIR=" + dirs["tmp"],
		"PATH=" + os.Getenv("PATH"),
	}
	for k, v := range rs.env {
		env = append(env, k+"="+v)
	}
	p.cmd.Env = env
	p.cmd.Stdout, p.cmd.Stderr = &p.stdout, &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := p.cmd.Wait()
		p.code = 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			p.code = ee.ExitCode()
		} else if err != nil {
			p.code = -2
		}
		close(p.done)
	}()
	t.Cleanup(func() { _ = p.cmd.Process.Kill() })
	return p
}

func (p *process) wait(t *testing.T) int {
	t.Helper()
	<-p.done
	return p.code
}

// resultBlock extracts the framed result of plan 27 section 14.12 step 5 from captured stdout.
type blockHeader struct {
	ExecutionID string `json:"execution_id"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
}

func countLines(stdout, prefix string) int {
	n := 0
	for _, l := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// checkFramedResult asserts 27-AC-17 for stdout and the body the stub Runner received.
func checkFramedResult(t *testing.T, stdout string, body []byte) {
	t.Helper()
	if b, e := countLines(stdout, "::SC-RESULT-BEGIN::"), countLines(stdout, "::SC-RESULT-END::"); b != 1 || e != 1 {
		t.Fatalf("stdout has %d BEGIN and %d END result markers, want exactly 1 each:\n%s", b, e, stdout)
	}
	lines := strings.Split(stdout, "\n")
	bi := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "::SC-RESULT-BEGIN::") {
			bi = i
		}
	}
	if bi+2 >= len(lines) || !strings.HasPrefix(lines[bi+2], "::SC-RESULT-END::") {
		t.Fatalf("BEGIN is not followed by exactly one JSON line and END:\n%s", stdout)
	}
	var hdr blockHeader
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(lines[bi], "::SC-RESULT-BEGIN::"))), &hdr); err != nil {
		t.Fatalf("BEGIN header is not JSON: %v: %q", err, lines[bi])
	}
	sum := sha256.Sum256(body)
	if hdr.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("block sha256 = %s, posted body sha256 = %x", hdr.SHA256, sum)
	}
	if hdr.Bytes != len(body) {
		t.Errorf("block bytes = %d, posted body has %d", hdr.Bytes, len(body))
	}
	if hdr.ExecutionID != "sc-184-x1" {
		t.Errorf("block execution_id = %q, want sc-184-x1", hdr.ExecutionID)
	}
	if lines[bi+1] != string(body) {
		t.Errorf("framed JSON line differs from the posted body")
	}
}

func decodeResult(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("posted body is not JSON: %v", err)
	}
	validate(t, "worker-result", v)
	return v.(map[string]any)
}

func failureReason(r map[string]any) (string, string) {
	f, _ := r["failure"].(map[string]any)
	reason, _ := f["reason"].(string)
	detail, _ := f["detail"].(string)
	return reason, detail
}

// 27-AC-2 and 27-AC-17: each documented exit path of plan 27 section 14.14 is forced and its exit code observed;
// every case that posts a result also frames it on stdout.
func TestWorkerRun_ExitCodes_27AC2(t *testing.T) {
	type tc struct {
		name      string
		want      int
		setup     func(rs *runSpec)
		wantPosts func(n int) bool // number of result POSTs
		wantBlock bool             // 27-AC-17: a framed block is expected on stdout
		check     func(t *testing.T, rs *runSpec, out *process)
	}
	postsOne := func(n int) bool { return n == 1 }
	postsNone := func(n int) bool { return n == 0 }
	noCalls := func(t *testing.T, rs *runSpec, _ *process) {
		t.Helper()
		if n := rs.stub.total(); n != 0 {
			t.Errorf("%d network calls reached the Runner, want none", n)
		}
	}
	oneGetNoResult := func(t *testing.T, rs *runSpec, _ *process) {
		t.Helper()
		if n := len(rs.stub.calls("GET", "/v1/worker/packet")); n != 1 {
			t.Errorf("GET /v1/worker/packet called %d times, want 1 (no retry)", n)
		}
	}
	packetStatus := func(code int) func(rs *runSpec) {
		return func(rs *runSpec) { rs.stub.packetStatus = code; rs.stub.packetBody = []byte(`{"code":"X"}`) }
	}
	cases := []tc{
		{name: "exit0_result_accepted_credentials_missing", want: 0, wantPosts: postsOne, wantBlock: true,
			setup: func(rs *runSpec) {},
			check: func(t *testing.T, rs *runSpec, _ *process) {
				posts := rs.stub.calls("POST", "/v1/worker/result")
				res := decodeResult(t, posts[0].Body)
				if res["state"] != "failed" {
					t.Errorf("state = %v, want failed", res["state"])
				}
				if reason, detail := failureReason(res); reason != "credentials_missing" || !strings.Contains(detail, "deploy-key") {
					t.Errorf("failure = %q/%q, want credentials_missing naming deploy-key", reason, detail)
				}
				for _, r := range rs.stub.reqs {
					if r.Auth != "Bearer "+taskToken {
						t.Errorf("%s %s Authorization = %q, want Bearer task-token", r.Method, r.Path, r.Auth)
					}
					if !strings.HasPrefix(r.UA, "sc-worker/") {
						t.Errorf("%s %s User-Agent = %q, want sc-worker/<version>", r.Method, r.Path, r.UA)
					}
				}
			}},
		{name: "exit0_result_replay_200", want: 0, wantPosts: postsOne, wantBlock: true,
			setup: func(rs *runSpec) { rs.stub.resultStatus = 200 }},
		{name: "exit2_env_missing_execution_id", want: 2, wantPosts: postsNone, check: noCalls,
			setup: func(rs *runSpec) { delete(rs.env, "SC_EXECUTION_ID") }},
		{name: "exit2_env_missing_runner_url", want: 2, wantPosts: postsNone, check: noCalls,
			setup: func(rs *runSpec) { delete(rs.env, "SC_RUNNER_URL") }},
		{name: "exit2_packet_401", want: 2, wantPosts: postsNone, check: oneGetNoResult, setup: packetStatus(401)},
		{name: "exit2_packet_403", want: 2, wantPosts: postsNone, check: oneGetNoResult, setup: packetStatus(403)},
		{name: "exit2_packet_404", want: 2, wantPosts: postsNone, check: oneGetNoResult, setup: packetStatus(404)},
		{name: "exit2_packet_409", want: 2, wantPosts: postsNone, check: oneGetNoResult, setup: packetStatus(409)},
		{name: "exit2_packet_5xx_retries_exhausted", want: 2, wantPosts: postsNone,
			setup: packetStatus(503),
			check: func(t *testing.T, rs *runSpec, _ *process) {
				if n := len(rs.stub.calls("GET", "/v1/worker/packet")); n < 5 || n > 10 {
					t.Errorf("GET /v1/worker/packet called %d times, want 5..10 (backoff 1,2,4,8,16,30,30,30 s)", n)
				}
			}},
		{name: "exit2_packet_unusable", want: 2, wantPosts: postsNone, check: oneGetNoResult,
			setup: func(rs *runSpec) { rs.stub.packetBody = []byte(`{}`) }},
		{name: "exit3_result_post_fails_whole_window", want: 3, wantBlock: true,
			wantPosts: func(n int) bool { return n >= 4 && n <= 60 },
			setup:     func(rs *runSpec) { rs.stub.resultStatus = 503 },
			check: func(t *testing.T, rs *runSpec, _ *process) {
				posts := rs.stub.calls("POST", "/v1/worker/result")
				for _, p := range posts[1:] {
					if !bytes.Equal(p.Body, posts[0].Body) {
						t.Errorf("a retried result body differs from the first attempt")
					}
				}
			}},
		{name: "exit4_result_409", want: 4, wantPosts: postsOne, wantBlock: true,
			setup: func(rs *runSpec) { rs.stub.resultStatus = 409 }},
		{name: "exit4_result_422", want: 4, wantPosts: postsOne, wantBlock: true,
			setup: func(rs *runSpec) { rs.stub.resultStatus = 422 }},
		{name: "exit5_task_token_missing", want: 5, wantPosts: postsNone, check: noCalls,
			setup: func(rs *runSpec) { delete(rs.secrets, "task-token") }},
		{name: "exit5_task_token_empty", want: 5, wantPosts: postsNone, check: noCalls,
			setup: func(rs *runSpec) { rs.secrets["task-token"] = "" }},
		{name: "exit70_injected_panic_posts_failed_internal", want: 70, wantPosts: postsOne, wantBlock: true,
			setup: func(rs *runSpec) { rs.panicAt = "after_packet" },
			check: func(t *testing.T, rs *runSpec, _ *process) {
				res := decodeResult(t, rs.stub.calls("POST", "/v1/worker/result")[0].Body)
				if res["state"] != "failed" {
					t.Errorf("state = %v, want failed", res["state"])
				}
				if reason, _ := failureReason(res); reason != "internal" {
					t.Errorf("failure.reason = %q, want internal", reason)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := &runSpec{stub: newStubRunner(t, examplePacket(t))}
			rs.defaults(t)
			c.setup(rs)
			out := start(t, rs)
			if got := out.wait(t); got != c.want {
				t.Errorf("exit code = %d, want %d; stderr:\n%s", got, c.want, out.stderr.String())
			}
			posts := rs.stub.calls("POST", "/v1/worker/result")
			postsOK := c.wantPosts(len(posts))
			if !postsOK {
				t.Errorf("%d result POSTs reached the Runner", len(posts))
			}
			if c.check != nil && postsOK {
				c.check(t, rs, out)
			}
			if c.wantBlock {
				if len(posts) == 0 {
					t.Fatal("no posted body to compare the framed result with")
				}
				checkFramedResult(t, out.stdout.String(), posts[len(posts)-1].Body)
			} else if b, e := countLines(out.stdout.String(), "::SC-RESULT-BEGIN::"), countLines(out.stdout.String(), "::SC-RESULT-END::"); b != 0 || e != 0 {
				t.Errorf("a case that posts nothing framed a result (BEGIN %d, END %d)", b, e)
			}
		})
	}
}

// 27-AC-2: SIGTERM while sc-worker is blocked in a slow Runner call ends with exit 143, and any best-effort post
// is a schema-valid failed result.
func TestWorkerRun_Exit143_OnSIGTERM_27AC2(t *testing.T) {
	rs := &runSpec{stub: newStubRunner(t, examplePacket(t))}
	rs.defaults(t)
	rs.stub.packetHold = make(chan struct{})
	out := start(t, rs)
	select {
	case <-rs.stub.packetSeen:
	case <-out.done:
		t.Fatalf("sc-worker exited with %d before the packet call; stderr:\n%s", out.code, out.stderr.String())
	}
	if err := out.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if got := out.wait(t); got != 143 {
		t.Errorf("exit code = %d, want 143; stderr:\n%s", got, out.stderr.String())
	}
	posts := rs.stub.calls("POST", "/v1/worker/result")
	if len(posts) > 1 {
		t.Fatalf("%d result POSTs, want at most one best-effort post", len(posts))
	}
	for _, p := range posts {
		res := decodeResult(t, p.Body)
		if res["state"] != "failed" {
			t.Errorf("best-effort result state = %v, want failed", res["state"])
		}
		checkFramedResult(t, out.stdout.String(), p.Body)
	}
}

// safetyCtx bounds a test wait at 30 s without a wall-clock timer channel (D8).
func safetyCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func waitDone(t *testing.T, p *process, what string) int {
	t.Helper()
	ctx, cancel := safetyCtx()
	defer cancel()
	select {
	case <-p.done:
		return p.code
	case <-ctx.Done():
		t.Fatalf("sc-worker did not exit within 30 s of %s; stderr:\n%s", what, p.stderr.String())
		return -1
	}
}

// 27-AC-2, N5 (plan 27 section 14.14): a SIGTERM during the normal result-post retry window ends the retries,
// but sc-worker still makes one more attempt with the identical body (single 5 s attempt) before exit 143.
func TestWorkerRun_SIGTERMInResultRetryWindowPostsOnce_27AC2_N5(t *testing.T) {
	rs := &runSpec{stub: newStubRunner(t, examplePacket(t))}
	rs.defaults(t)
	rs.stub.resultScript = func(n int) (int, bool) {
		switch n {
		case 1:
			return 503, false
		case 2:
			return 0, true // in flight when the signal arrives
		}
		return 202, false
	}
	out := start(t, rs)
	ctx, cancel := safetyCtx()
	defer cancel()
	select {
	case <-rs.stub.resultHeld:
	case <-out.done:
		t.Fatalf("sc-worker exited with %d before the second result POST; stderr:\n%s", out.code, out.stderr.String())
	case <-ctx.Done():
		t.Fatal("the second result POST never arrived")
	}
	if err := out.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if got := waitDone(t, out, "SIGTERM"); got != 143 {
		t.Errorf("exit code = %d, want 143; stderr:\n%s", got, out.stderr.String())
	}
	posts := rs.stub.calls("POST", "/v1/worker/result")
	if len(posts) != 3 {
		t.Fatalf("%d result POSTs, want 3 (503, the one in flight at SIGTERM, one more after it)", len(posts))
	}
	for i, p := range posts[1:] {
		if !bytes.Equal(p.Body, posts[0].Body) {
			t.Errorf("result POST %d body differs from the first attempt", i+2)
		}
	}
	checkFramedResult(t, out.stdout.String(), posts[len(posts)-1].Body)
}

// 27-R-3, N3: nothing sc-worker writes to stdout or stderr contains a credential value, including the
// "packet unusable" line that echoes a schema error and may carry packet values.
func TestWorkerRun_StderrIsScrubbed_N3(t *testing.T) {
	const gwToken = "gw-CANARY-0002-gateway-token-value"
	sk := "sk-" + "ant-" + strings.Repeat("Ab1_-", 6)
	for name, canary := range map[string]string{"gateway-token": gwToken, "sk-ant": sk, "task-token": taskToken} {
		t.Run(name, func(t *testing.T) {
			pkt := examplePacket(t)
			// An unusable packet with no identity: the schema error is what reaches stderr.
			delete(pkt["task"].(map[string]any), "task_id")
			// The validator names unknown properties in its error, so a property named like a credential reaches it.
			pkt[canary] = "x"
			pkt["harness"].(map[string]any)[canary] = "x"
			rs := &runSpec{stub: newStubRunner(t, pkt)}
			rs.defaults(t)
			out := start(t, rs)
			if got := waitDone(t, out, "start"); got != 2 {
				t.Errorf("exit code = %d, want 2 (packet unusable); stderr:\n%s", got, out.stderr.String())
			}
			if !strings.Contains(out.stderr.String(), "packet unusable") {
				t.Fatalf("stderr has no packet-unusable line (the canary may not reach it): %q", out.stderr.String())
			}
			for stream, text := range map[string]string{"stdout": out.stdout.String(), "stderr": out.stderr.String()} {
				if strings.Contains(text, canary) {
					t.Errorf("%s contains the canary %q:\n%s", stream, canary, text)
				}
			}
		})
	}
}
