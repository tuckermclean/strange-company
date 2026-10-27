package s3emu_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/s3emu"
)

const (
	rsAccess = "fixture-access-id-0006"
	rsSecret = "fixture-secret-key-0006"
)

// sigV4Upstream is an authentication-enabled S3 stand-in: it recomputes the SigV4 signature of every request from
// what it received and answers 403 on a mismatch, 200 otherwise. It records whether If-None-Match arrived.
type sigV4Upstream struct {
	mu      sync.Mutex
	bad     int
	good    int
	sawINM  bool
	server  *httptest.Server
	signKey s3client.Signer
}

func newSigV4Upstream(t *testing.T) *sigV4Upstream {
	t.Helper()
	u := &sigV4Upstream{signKey: s3client.Signer{AccessKey: rsAccess, SecretKey: rsSecret, Region: "us-east-1", Service: "s3"}}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		ok := u.verify(r)
		u.mu.Lock()
		if r.Header.Get("If-None-Match") != "" {
			u.sawINM = true
		}
		if ok {
			u.good++
		} else {
			u.bad++
		}
		u.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `<Error><Code>SignatureDoesNotMatch</Code><Message>bad signature</Message></Error>`)
			return
		}
		w.Header().Set("ETag", `"0123456789abcdef0123456789abcdef"`)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(u.server.Close)
	return u
}

func (u *sigV4Upstream) verify(r *http.Request) bool {
	at, err := time.Parse("20060102T150405Z", r.Header.Get("x-amz-date"))
	if err != nil {
		return false
	}
	clone, err := http.NewRequest(r.Method, "http://"+r.Host+r.URL.RequestURI(), nil)
	if err != nil {
		return false
	}
	clone.Host = r.Host
	for k, vs := range r.Header {
		if k != "Authorization" {
			clone.Header[k] = append([]string(nil), vs...)
		}
	}
	if err := u.signKey.Sign(clone, r.Header.Get("x-amz-content-sha256"), at); err != nil {
		return false
	}
	return clone.Header.Get("Authorization") == r.Header.Get("Authorization")
}

func (u *sigV4Upstream) counts() (good, bad int, sawINM bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.good, u.bad, u.sawINM
}

func putTwiceThrough(t *testing.T, endpoint string) (first, second error) {
	t.Helper()
	c, err := s3client.New(s3client.Config{Endpoint: endpoint, Region: "us-east-1", AccessKey: rsAccess, SecretKey: rsSecret, PathStyle: true,
		Clock: clock.NewFake(time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))})
	if err != nil {
		t.Fatal(err)
	}
	b := c.Bucket("sc-fixture-tofu-state")
	_, first = b.PutIfAbsent(context.Background(), "lab-one/conformance/k1", []byte("one"))
	_, second = b.PutIfAbsent(context.Background(), "lab-one/conformance/k1", []byte("two"))
	return first, second
}

// 06-AC-2 with an authentication-enabled emulator (S4 follow-up N3): the stripping proxy removes If-None-Match,
// which is a signed header, so it must sign the request again; the upstream then accepts both conditional
// creates (the second one is unconditional by then, which is exactly what the proxy fixture simulates).
func TestResigningProxy_UpstreamVerifiesSignatures_06AC2(t *testing.T) {
	up := newSigV4Upstream(t)

	// Control: the plain stripping proxy breaks the signature, so the verifier above is a real check.
	plain, err := s3emu.NewStrippingProxy(up.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if first, _ := putTwiceThrough(t, plain.URL()); first == nil {
		t.Fatalf("the plain stripping proxy passed a signed If-None-Match request to an authenticating upstream; the verifier is not strict")
	}

	p, err := s3emu.NewResigningProxy(up.server.URL, s3emu.Credentials{AccessKey: rsAccess, SecretKey: rsSecret, Region: "us-east-1"})
	if err != nil || p == nil {
		t.Fatalf("NewResigningProxy = %v, %v", p, err)
	}
	defer p.Close()
	goodBefore, _, _ := up.counts()
	first, second := putTwiceThrough(t, p.URL())
	if first != nil || second != nil {
		t.Fatalf("conditional creates through the re-signing proxy failed: %v / %v", first, second)
	}
	good, _, sawINM := up.counts()
	if good-goodBefore != 2 {
		t.Errorf("the upstream accepted %d requests through the proxy, want 2", good-goodBefore)
	}
	if sawINM {
		t.Errorf("If-None-Match reached the upstream through the re-signing proxy")
	}
}
