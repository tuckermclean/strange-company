package s3emu_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"strange.company/sc/internal/s3emu"
)

// TestMain doubles as a fake `weed`: the launcher tests start this very test binary as the "weed" executable, and
// it answers HTTP on the S3 port it is given. No real weed binary and no network are needed at layer 1 (plan 52 D2).
func TestMain(m *testing.M) {
	switch os.Getenv("SC_S3EMU_FAKE_WEED") {
	case "serve":
		os.Exit(fakeWeed())
	case "exit":
		fmt.Fprintln(os.Stderr, "fake weed: refusing to start")
		os.Exit(3)
	}
	os.Exit(m.Run())
}

func fakeWeed() int {
	port := ""
	for _, a := range os.Args[1:] {
		if v, ok := strings.CutPrefix(a, "-s3.port="); ok {
			port = v
		}
	}
	if port == "" {
		fmt.Fprintln(os.Stderr, "fake weed: no -s3.port")
		return 2
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake weed:", err)
		return 2
	}
	_ = http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	return 0
}

func argInt(t *testing.T, args []string, name string) int {
	t.Helper()
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				t.Fatalf("%s=%q is not a number", name, v)
			}
			return n
		}
	}
	t.Errorf("argument %s=<n> missing from %q", name, args)
	return 0
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func startFake(t *testing.T) *s3emu.Emulator {
	t.Helper()
	t.Setenv("SC_S3EMU_FAKE_WEED", "serve")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	e, err := s3emu.Start(context.Background(), s3emu.Options{Binary: exe, DataDir: t.TempDir()})
	if err != nil || e == nil {
		t.Fatalf("Start: emulator=%v err=%v", e, err)
	}
	t.Cleanup(func() { _ = e.Stop() })
	return e
}

// 06-AC-1: the launcher starts the subprocess on a free port and returns once the endpoint answers.
func TestStart_LaunchesSubprocessOnFreePorts_06AC1(t *testing.T) {
	e := startFake(t)
	ep := e.Endpoint()
	if !strings.HasPrefix(ep, "http://127.0.0.1:") {
		t.Fatalf("Endpoint = %q, want http://127.0.0.1:<port>", ep)
	}
	resp, err := (&http.Client{}).Get(ep + "/")
	if err != nil {
		t.Fatalf("the endpoint does not answer: %v", err)
	}
	resp.Body.Close()
	args := e.Args()
	if len(args) == 0 || args[0] != "server" {
		t.Fatalf("Args = %q, want weed's `server` subcommand first", args)
	}
	if !hasArg(args, "-s3") {
		t.Errorf("Args lack -s3: %q", args)
	}
	s3port := argInt(t, args, "-s3.port")
	if ep != fmt.Sprintf("http://127.0.0.1:%d", s3port) {
		t.Errorf("Endpoint %q does not use -s3.port=%d", ep, s3port)
	}
	seen := map[int]string{}
	for _, name := range []string{"-s3.port", "-master.port", "-volume.port", "-filer.port", "-iam.port"} {
		p := argInt(t, args, name)
		if p < 1024 || p > 55000 {
			t.Errorf("%s=%d is outside 1024..55000 (weed derives gRPC ports as port+10000)", name, p)
		}
		if other, dup := seen[p]; dup {
			t.Errorf("%s and %s share port %d", name, other, p)
		}
		seen[p] = name
	}
	// ports that weed would otherwise take from the host defaults
	for _, a := range []string{"-s3.port.iceberg=0", "-s3.port.lance=0", "-ip=127.0.0.1"} {
		if !hasArg(args, a) {
			t.Errorf("Args lack %s: %q", a, args)
		}
	}
	dirOK := false
	for _, a := range args {
		if strings.HasPrefix(a, "-dir=") && len(a) > len("-dir=") {
			dirOK = true
		}
	}
	if !dirOK {
		t.Errorf("Args lack -dir=<data dir>: %q", args)
	}
}

func TestStart_TwoEmulatorsDoNotCollide_06AC1(t *testing.T) {
	a, b := startFake(t), startFake(t)
	if a.Endpoint() == b.Endpoint() {
		t.Fatalf("both emulators use %s", a.Endpoint())
	}
	pa, pb := map[int]bool{}, map[int]bool{}
	for _, n := range []string{"-s3.port", "-master.port", "-volume.port", "-filer.port", "-iam.port"} {
		pa[argInt(t, a.Args(), n)] = true
		pb[argInt(t, b.Args(), n)] = true
	}
	for p := range pa {
		if pb[p] {
			t.Errorf("port %d is used by both emulators", p)
		}
	}
}

// Stop kills the subprocess, frees the port, and is idempotent.
func TestStop_KillsTheSubprocess_06AC1(t *testing.T) {
	e := startFake(t)
	addr := strings.TrimPrefix(e.Endpoint(), "http://")
	if err := e.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if c, err := net.Dial("tcp", addr); err == nil {
		c.Close()
		t.Error("the endpoint still accepts connections after Stop")
	}
	if err := e.Stop(); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestStart_FailsClearlyWhenWeedIsMissingOrExits_06AC1(t *testing.T) {
	if e, err := s3emu.Start(context.Background(), s3emu.Options{Binary: "/nonexistent/weed", DataDir: t.TempDir()}); err == nil {
		_ = e.Stop()
		t.Error("Start with a missing binary returned no error")
	}
	t.Setenv("SC_S3EMU_FAKE_WEED", "exit")
	exe, _ := os.Executable()
	e, err := s3emu.Start(context.Background(), s3emu.Options{Binary: exe, DataDir: t.TempDir()})
	if err == nil {
		_ = e.Stop()
		t.Fatal("Start returned no error although the subprocess exited at once")
	}
	if !strings.Contains(err.Error(), "refusing to start") {
		t.Errorf("error %q does not carry the subprocess output", err)
	}
}

func TestFreePorts_06AC1(t *testing.T) {
	ps, err := s3emu.FreePorts(6)
	if err != nil || len(ps) != 6 {
		t.Fatalf("FreePorts(6) = %v, %v", ps, err)
	}
	seen := map[int]bool{}
	for _, p := range ps {
		if p < 1024 || p > 55000 || seen[p] {
			t.Errorf("port %d out of range or repeated in %v", p, ps)
		}
		seen[p] = true
	}
}

type upstreamRec struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
}

func newUpstream(t *testing.T) (*httptest.Server, *upstreamRec) {
	t.Helper()
	rec := &upstreamRec{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.reqs = append(rec.reqs, r.Clone(context.Background()))
		rec.body = append(rec.body, string(b))
		rec.mu.Unlock()
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("x-amz-version-id", "ver-1")
		w.WriteHeader(201)
		io.WriteString(w, "upstream says hi")
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// 06-AC-2: the proxy removes If-None-Match and nothing else.
func TestStrippingProxy_RemovesIfNoneMatchOnly_06AC2(t *testing.T) {
	up, rec := newUpstream(t)
	p, err := s3emu.NewStrippingProxy(up.URL)
	if err != nil || p == nil {
		t.Fatalf("NewStrippingProxy: %v, %v", p, err)
	}
	t.Cleanup(p.Close)
	req, _ := http.NewRequest("PUT", p.URL()+"/bkt/key%20one?versionId=7&x=y", strings.NewReader("payload"))
	req.Header.Set("If-None-Match", "*")
	req.Header.Set("If-Match", `"etag"`)
	req.Header.Set("x-amz-meta-k", "v")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatalf("request through the proxy: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 201 || string(body) != "upstream says hi" || resp.Header.Get("ETag") != `"abc"` || resp.Header.Get("x-amz-version-id") != "ver-1" {
		t.Errorf("response through the proxy: %d %q etag=%q version=%q", resp.StatusCode, body, resp.Header.Get("ETag"), resp.Header.Get("x-amz-version-id"))
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.reqs) != 1 {
		t.Fatalf("upstream saw %d requests, want 1", len(rec.reqs))
	}
	u := rec.reqs[0]
	if v := u.Header.Get("If-None-Match"); v != "" {
		t.Errorf("upstream received If-None-Match: %q", v)
	}
	if u.Header.Get("If-Match") != `"etag"` || u.Header.Get("x-amz-meta-k") != "v" {
		t.Errorf("other headers were not forwarded: %v", u.Header)
	}
	if u.Method != "PUT" || u.URL.Path != "/bkt/key one" || u.URL.Query().Get("versionId") != "7" || u.URL.Query().Get("x") != "y" {
		t.Errorf("method/path/query changed: %s %s", u.Method, u.URL.String())
	}
	if rec.body[0] != "payload" {
		t.Errorf("body = %q", rec.body[0])
	}
}

func TestStrippingProxy_HandlerAndGet_06AC2(t *testing.T) {
	up, rec := newUpstream(t)
	p, err := s3emu.NewStrippingProxy(up.URL)
	if err != nil || p == nil {
		t.Fatalf("NewStrippingProxy: %v, %v", p, err)
	}
	t.Cleanup(p.Close)
	h := p.Handler()
	if h == nil {
		t.Fatal("Handler() is nil")
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/bkt?list-type=2", nil)
	req.Header.Set("If-None-Match", "*")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.reqs) != 1 || rec.reqs[0].Header.Get("If-None-Match") != "" || rec.reqs[0].URL.Query().Get("list-type") != "2" {
		t.Errorf("upstream request: %+v", rec.reqs)
	}
}

func TestNewStrippingProxy_RejectsBadUpstream_06AC2(t *testing.T) {
	for _, u := range []string{"", "not a url", "ftp://x"} {
		if p, err := s3emu.NewStrippingProxy(u); err == nil {
			p.Close()
			t.Errorf("NewStrippingProxy(%q) returned no error", u)
		}
	}
}
