// Package s3emu runs the SeaweedFS S3 emulator (ADR-0205) as a subprocess on free local ports, and provides the
// If-None-Match-stripping proxy fixture (plan 06 section 22, 06-AC-2).
package s3emu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"strange.company/sc/internal/clock"
)

const (
	minPort = 1024
	// maxPort keeps port+10000 (weed's gRPC port) a valid port number.
	maxPort = 55000
	// readyPolls polls of readyEvery each bound the wait for the S3 endpoint (about 90 seconds).
	readyPolls = 900
	readyEvery = 100 * time.Millisecond
)

// Options configure Start.
type Options struct {
	Binary  string
	DataDir string
	Env     []string
}

// Emulator is a running weed subprocess.
type Emulator struct {
	cmd      *exec.Cmd
	args     []string
	endpoint string
	done     chan struct{}
	out      *syncBuffer
	once     sync.Once
}

// StrippingProxy forwards to an upstream S3 endpoint and removes If-None-Match from every request.
type StrippingProxy struct {
	srv *http.Server
	ln  net.Listener
	url string
	h   http.Handler
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// FreePorts returns n distinct free TCP ports between 1024 and 55000 (weed derives gRPC ports as port+10000).
func FreePorts(n int) ([]int, error) {
	if n < 1 {
		return nil, errors.New("s3emu: FreePorts needs n >= 1")
	}
	var held []net.Listener
	defer func() {
		for _, l := range held {
			_ = l.Close()
		}
	}()
	taken := map[int]bool{}
	var ports []int
	for tries := 0; len(ports) < n; tries++ {
		if tries > 2000 {
			return nil, errors.New("s3emu: no free ports in range")
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("s3emu: listening for a free port: %w", err)
		}
		p := l.Addr().(*net.TCPAddr).Port
		held = append(held, l)
		if p < minPort || p > maxPort || taken[p] || taken[p+10000] || taken[p-10000] {
			continue
		}
		g, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p+10000))
		if err != nil {
			continue
		}
		held = append(held, g)
		taken[p], taken[p+10000] = true, true
		ports = append(ports, p)
	}
	return ports, nil
}

// Start launches the emulator and returns once its S3 endpoint answers. The context bounds the start-up only.
func Start(ctx context.Context, o Options) (*Emulator, error) {
	if o.Binary == "" || o.DataDir == "" {
		return nil, errors.New("s3emu: Binary and DataDir are required")
	}
	ports, err := FreePorts(5)
	if err != nil {
		return nil, err
	}
	s3, master, volume, filer, iam := ports[0], ports[1], ports[2], ports[3], ports[4]
	args := []string{
		"server", "-s3",
		"-ip=127.0.0.1",
		"-dir=" + o.DataDir,
		"-s3.port=" + strconv.Itoa(s3),
		"-master.port=" + strconv.Itoa(master),
		"-volume.port=" + strconv.Itoa(volume),
		"-filer.port=" + strconv.Itoa(filer),
		"-iam.port=" + strconv.Itoa(iam),
		"-s3.port.iceberg=0",
		"-s3.port.lance=0",
		"-master.volumeSizeLimitMB=64",
		"-volume.max=16",
	}
	cmd := exec.Command(o.Binary, args...)
	cmd.Env = append(os.Environ(), o.Env...)
	out := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("s3emu: starting %s: %w", o.Binary, err)
	}
	e := &Emulator{cmd: cmd, args: args, endpoint: "http://127.0.0.1:" + strconv.Itoa(s3), done: make(chan struct{}), out: out}
	go func() {
		_ = cmd.Wait()
		close(e.done)
	}()
	if err := e.waitReady(ctx); err != nil {
		_ = e.Stop()
		return nil, err
	}
	return e, nil
}

func (e *Emulator) waitReady(ctx context.Context) error {
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	clk := clock.Real{}
	// The S3 start line proves the listener belongs to this process, not to another weed that took the port.
	port := e.endpoint[strings.LastIndex(e.endpoint, ":")+1:]
	announced := func() bool {
		for _, l := range strings.Split(e.out.String(), "\n") {
			if strings.Contains(l, "Start Seaweed S3 API Server") && strings.Contains(l, "at http port "+port) {
				return true
			}
		}
		return false
	}
	for i := 0; i < readyPolls; i++ {
		select {
		case <-e.done:
			return fmt.Errorf("s3emu: the emulator exited during start-up:\n%s", tail(e.out.String(), 4000))
		default:
		}
		if announced() {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.endpoint+"/", nil)
			if err != nil {
				return err
			}
			if resp, err := client.Do(req); err == nil {
				resp.Body.Close()
				select {
				case <-e.done:
					return fmt.Errorf("s3emu: the emulator exited during start-up:\n%s", tail(e.out.String(), 4000))
				default:
				}
				return nil
			}
		}
		if err := clk.Sleep(ctx, readyEvery); err != nil {
			return err
		}
	}
	return fmt.Errorf("s3emu: the S3 endpoint %s did not answer:\n%s", e.endpoint, tail(e.out.String(), 4000))
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// Endpoint is http://127.0.0.1:<s3 port>.
func (e *Emulator) Endpoint() string { return e.endpoint }

// Args is the argument vector the subprocess was started with (without the binary).
func (e *Emulator) Args() []string { return append([]string(nil), e.args...) }

// Stop kills the subprocess and waits for it; calling it again is a no-op.
func (e *Emulator) Stop() error {
	e.once.Do(func() {
		select {
		case <-e.done:
		default:
			_ = e.cmd.Process.Kill()
		}
		<-e.done
	})
	return nil
}

// NewStrippingProxy starts a proxy in front of upstream.
func NewStrippingProxy(upstream string) (*StrippingProxy, error) {
	u, err := url.Parse(upstream)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("s3emu: upstream %q must be an http or https URL with a host", upstream)
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(u)
			r.Out.Host = r.In.Host
			r.Out.Header.Del("If-None-Match")
		},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("s3emu: proxy listener: %w", err)
	}
	p := &StrippingProxy{ln: ln, h: rp, url: "http://" + ln.Addr().String()}
	p.srv = &http.Server{Handler: rp}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// URL is the proxy's base URL.
func (p *StrippingProxy) URL() string { return p.url }

// Handler is the proxy as an http.Handler.
func (p *StrippingProxy) Handler() http.Handler { return p.h }

// Close stops the proxy.
func (p *StrippingProxy) Close() {
	_ = p.srv.Close()
	_ = p.ln.Close()
}
