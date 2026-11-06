package lab

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// pabMaxBody caps the body of a PutPublicAccessBlock request; the real document is a few hundred bytes.
const pabMaxBody = 64 << 10

const pabNoneXML = `<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>NoSuchPublicAccessBlockConfiguration</Code><Message>The public access block configuration was not found</Message></Error>`

// NewS3Front returns the lab S3 TLS front's handler. SeaweedFS answers PutPublicAccessBlock with 501, so the front keeps
// the bucket ?publicAccessBlock sub-resource in memory (PUT 200, GET the stored XML or 404, DELETE 204, per bucket) and
// proxies every other request to upstream unchanged, keeping the client's Host and Authorization (SigV4 covers them).
func NewS3Front(upstream *url.URL) http.Handler {
	proxy := &httputil.ReverseProxy{
		// Director leaves outreq.Host (the client's Host) alone, so the signature still verifies upstream.
		Director: func(r *http.Request) {
			r.URL.Scheme = upstream.Scheme
			r.URL.Host = upstream.Host
		},
		FlushInterval: -1,
	}
	var mu sync.Mutex
	store := map[string][]byte{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bucket, ok := pabBucket(r)
		if !ok {
			proxy.ServeHTTP(w, r)
			return
		}
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, pabMaxBody))
			if err != nil {
				http.Error(w, "request body too large or unreadable", http.StatusRequestEntityTooLarge)
				return
			}
			mu.Lock()
			store[bucket] = body
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			mu.Lock()
			body, have := store[bucket]
			mu.Unlock()
			w.Header().Set("Content-Type", "application/xml")
			if !have {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, pabNoneXML)
				return
			}
			_, _ = w.Write(body)
		case http.MethodDelete:
			mu.Lock()
			delete(store, bucket)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			proxy.ServeHTTP(w, r)
		}
	})
}

// pabBucket reports the bucket of a /<bucket>?publicAccessBlock request.
func pabBucket(r *http.Request) (string, bool) {
	if _, has := r.URL.Query()["publicAccessBlock"]; !has {
		return "", false
	}
	b := strings.Trim(r.URL.Path, "/")
	if b == "" || strings.Contains(b, "/") {
		return "", false
	}
	return b, true
}

// RunS3Front serves NewS3Front on listen over TLS (certFile, keyFile) until ctx ends; it is the body of the hidden
// `sc-lab s3-front` subcommand.
func RunS3Front(ctx context.Context, listen, upstream, certFile, keyFile string) error {
	u, err := url.Parse(upstream)
	if err != nil || u.Host == "" {
		return fmt.Errorf("bad --upstream %q", upstream)
	}
	if err := requireLoopback(listen); err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           NewS3Front(u),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// requireLoopback refuses a listen address whose host is not a literal loopback IP (an empty host, a name or a wildcard).
func requireLoopback(listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("bad --listen %q: %w", listen, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--listen %q is not a loopback address", listen)
	}
	return nil
}
