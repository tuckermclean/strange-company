// Package s3emu runs the SeaweedFS S3 emulator (ADR-0205) as a subprocess on free local ports, and provides the
// If-None-Match-stripping proxy fixture (plan 06 section 22, 06-AC-2).
package s3emu

import (
	"context"
	"net/http"
)

// Options configure Start.
// SC-STUB(F08 06-AC-1)
type Options struct {
	Binary  string
	DataDir string
	Env     []string
}

// Emulator is a running weed subprocess.
// SC-STUB(F08 06-AC-1)
type Emulator struct{}

// StrippingProxy forwards to an upstream S3 endpoint and removes If-None-Match from every request.
// SC-STUB(F08 06-AC-2)
type StrippingProxy struct{}

// Start launches the emulator and returns once its S3 endpoint answers.
// SC-STUB(F08 06-AC-1)
func Start(ctx context.Context, o Options) (*Emulator, error) { return nil, nil }

// Endpoint is http://127.0.0.1:<s3 port>.
// SC-STUB(F08 06-AC-1)
func (e *Emulator) Endpoint() string { return "" }

// Args is the argument vector the subprocess was started with (without the binary).
// SC-STUB(F08 06-AC-1)
func (e *Emulator) Args() []string { return nil }

// Stop kills the subprocess and waits for it; calling it again is a no-op.
// SC-STUB(F08 06-AC-1)
func (e *Emulator) Stop() error { return nil }

// FreePorts returns n distinct free TCP ports between 1024 and 55000 (weed derives gRPC ports as port+10000).
// SC-STUB(F08 06-AC-1)
func FreePorts(n int) ([]int, error) { return nil, nil }

// NewStrippingProxy starts a proxy in front of upstream.
// SC-STUB(F08 06-AC-2)
func NewStrippingProxy(upstream string) (*StrippingProxy, error) { return nil, nil }

// URL is the proxy's base URL.
// SC-STUB(F08 06-AC-2)
func (p *StrippingProxy) URL() string { return "" }

// Handler is the proxy as an http.Handler.
// SC-STUB(F08 06-AC-2)
func (p *StrippingProxy) Handler() http.Handler { return nil }

// Close stops the proxy.
// SC-STUB(F08 06-AC-2)
func (p *StrippingProxy) Close() {}
