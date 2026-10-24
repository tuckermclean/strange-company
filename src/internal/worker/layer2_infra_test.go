package worker_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
)

// Layer-2 infrastructure (plan 27 section 22, plan 28 section 22, brief D3). These two "tests" are NOT tests: they are
// the entry points that test/e2e/worker/run runs from the compiled test binary (`go test -c`) inside its infra
// container, so that the container-level suite reuses the Layer-1 fixture builder, the packet builder and the
// schemas instead of duplicating them. Without SC_L2_STATE they skip, so `make test` is unaffected.
//
//	TestLayer2Prepare  builds the bare fixture sc-e2e/fake-target.git (D7) and the packets below $SC_L2_STATE.
//	TestLayer2Serve    serves the stub Runner (:8082) and the stub Model Gateway (:8080) until it is killed.
//
// The stub Runner serves the packet of the case chosen by POST /_l2/reset?case=<name> (which also clears all records),
// accepts the worker endpoints only with `Authorization: Bearer $SC_L2_TASK_TOKEN`, and keeps what it saw for
// GET /_l2/state. The deadline_at of the packet is set to "now + 4 h" when the packet is served.

// l2IdentityProbe prints what identifies the container a process runs in; test/e2e/worker/run runs the same command
// as sc-worker's container entry for comparison.
const l2IdentityProbe = `printf 'cgroup=%s\nhost=%s\nmntns=%s\nrunsc=%s\n' "$(tr '\n' ' ' </proc/self/cgroup)" "$(cat /etc/hostname)" "$(readlink /proc/self/ns/mnt)" "$(ls /var/run/sc | tr '\n' ',')"`

const (
	l2RunnerAddr  = ":8082"
	l2GatewayAddr = ":8080"
)

func l2State(t *testing.T) string {
	t.Helper()
	d := os.Getenv("SC_L2_STATE")
	if d == "" {
		t.Skip("SC_L2_STATE is not set: Layer-2 entry point (test/e2e/worker/run)")
	}
	return d
}

func TestLayer2Prepare(t *testing.T) {
	state := l2State(t)
	fx := buildFixture(t, false)
	dst := filepath.Join(state, "fixture", "sc-e2e", "fake-target.git")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cp", "-a", fx.bare, dst).CombinedOutput(); err != nil {
		t.Fatalf("cp -a: %v: %s", err, out)
	}
	writeTestFile(t, filepath.Join(state, "base-commit.txt"), fx.base+"\n", 0o644)

	// The validation command of the success case: written by the shim's process in the harness container; its cgroup
	// is compared with sc-worker's (27-AC-16).
	probe := map[string]any{"name": "cgroup", "timeout_seconds": 30,
		"argv": []any{"sh", "-c", l2IdentityProbe + " > /workspace/out/validation-id.txt"}}
	cases := map[string]struct {
		scenario string
		grace    int
		policy   map[string]any
	}{
		"success":       {"success", 30, map[string]any{"validation_commands": []any{probe}}},
		"ignore-cancel": {"ignore-cancel", 5, nil},
	}
	for name, c := range cases {
		p := gitPacket(t, fx, pktOpts{policy: c.policy})
		p["harness"].(map[string]any)["fake_scenario"] = c.scenario
		p["limits"].(map[string]any)["term_grace_seconds"] = c.grace
		validate(t, "worker-input", p)
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(state, "packets", name+".json"), string(b), 0o644)
	}
}

type l2Call struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	AuthOK bool   `json:"auth_ok"`
}

type l2Stub struct {
	state, token string
	clk          clock.Clock

	mu         sync.Mutex
	current    string
	calls      []l2Call
	result     json.RawMessage
	resultPost int
	gateway    int
	badAuth    int
}

func (s *l2Stub) reset(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current, s.calls, s.result, s.resultPost, s.gateway, s.badAuth = name, nil, nil, 0, 0, 0
}

func (s *l2Stub) runner(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	if strings.HasPrefix(r.URL.Path, "/_l2/") {
		s.control(w, r)
		return
	}
	ok := r.Header.Get("Authorization") == "Bearer "+s.token
	s.mu.Lock()
	s.calls = append(s.calls, l2Call{r.Method, r.URL.Path, ok})
	if !ok {
		s.badAuth++
	}
	cur := s.current
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthorized"}`))
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/worker/packet":
		raw, err := os.ReadFile(filepath.Join(s.state, "packets", cur+".json"))
		if err != nil {
			http.Error(w, `{"code":"no_such_case"}`, http.StatusNotFound)
			return
		}
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		p["execution"].(map[string]any)["deadline_at"] = s.clk.Now().UTC().Add(4 * time.Hour).Format(time.RFC3339)
		out, _ := json.Marshal(p)
		_, _ = w.Write(out)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/worker/heartbeat":
		dl := s.clk.Now().UTC().Add(4 * time.Hour).Format(time.RFC3339)
		_, _ = w.Write([]byte(`{"cancel_requested":false,"deadline_at":"` + dl + `"}`))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/worker/result":
		s.mu.Lock()
		s.result, s.resultPost = append(json.RawMessage(nil), body...), s.resultPost+1
		s.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_found"}`))
	}
}

func (s *l2Stub) control(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/_l2/reset":
		s.reset(r.URL.Query().Get("case"))
		_, _ = w.Write([]byte(`{}`))
	case "/_l2/state":
		s.mu.Lock()
		defer s.mu.Unlock()
		res := s.result
		if res == nil {
			res = json.RawMessage("null")
		}
		heartbeats, packets := 0, 0
		for _, c := range s.calls {
			switch c.Path {
			case "/v1/worker/heartbeat":
				heartbeats++
			case "/v1/worker/packet":
				packets++
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"case": s.current, "calls": s.calls, "result": res, "result_posts": s.resultPost,
			"gateway_requests": s.gateway, "bad_auth": s.badAuth, "heartbeats": heartbeats, "packet_gets": packets,
		})
	default:
		http.NotFound(w, r)
	}
}

func (s *l2Stub) gatewayHandler(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	s.mu.Lock()
	s.gateway++
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{}`))
}

func TestLayer2Serve(t *testing.T) {
	state := l2State(t)
	s := &l2Stub{state: state, token: os.Getenv("SC_L2_TASK_TOKEN"), clk: clock.Real{}}
	if s.token == "" {
		t.Fatal("SC_L2_TASK_TOKEN is not set")
	}
	s.reset("success")
	for addr, h := range map[string]http.Handler{l2RunnerAddr: http.HandlerFunc(s.runner), l2GatewayAddr: http.HandlerFunc(s.gatewayHandler)} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: h}
		go func() { _ = srv.Serve(ln) }()
	}
	writeTestFile(t, filepath.Join(state, "ready"), "ready\n", 0o644)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	<-ctx.Done()
}
