package s3emu

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/s3client"
)

// Credentials sign the requests a re-signing proxy forwards to an authentication-enabled emulator.
type Credentials struct {
	AccessKey, SecretKey, Region string
}

// NewResigningProxy is NewStrippingProxy for an upstream that verifies SigV4: after removing
// If-None-Match it signs every request again with c, so the upstream accepts it (S4 follow-up N3).
func NewResigningProxy(upstream string, c Credentials) (*StrippingProxy, error) {
	u, err := url.Parse(upstream)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("s3emu: upstream %q must be an http or https URL with a host", upstream)
	}
	if c.AccessKey == "" || c.SecretKey == "" || c.Region == "" {
		return nil, fmt.Errorf("s3emu: the re-signing proxy needs an access key, a secret key and a region")
	}
	signer := s3client.Signer{AccessKey: c.AccessKey, SecretKey: c.SecretKey, Region: c.Region, Service: "s3"}
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(u)
			r.Out.Host = u.Host
			r.Out.Header.Del("If-None-Match")
			// The client already hashed the body (x-amz-content-sha256) and stamped the request (x-amz-date); both
			// are kept, only the signature changes. The reader is not touched.
			payload := r.In.Header.Get("x-amz-content-sha256")
			at, err := time.Parse("20060102T150405Z", r.In.Header.Get("x-amz-date"))
			if err != nil {
				at = clock.Real{}.Now()
			}
			if len(payload) != 64 {
				return // unsigned or streaming request: nothing to sign again; the upstream decides
			}
			_ = signer.Sign(r.Out, payload, at)
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
