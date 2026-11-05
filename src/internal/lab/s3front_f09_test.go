package lab_test

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"strange.company/sc/internal/lab"
)

// F09 05-AC-6 (lab run 4): SeaweedFS answers PutPublicAccessBlock with 501, so sc-lab puts a small front in front of
// weed that keeps the bucket ?publicAccessBlock sub-resource in memory and forwards every other request unchanged.

type recorded struct {
	Method, Path, RawQuery, Host, Auth, Body string
}

type recorder struct {
	mu   sync.Mutex
	reqs []recorded
}

func (r *recorder) all() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded(nil), r.reqs...)
}

func newFront(t *testing.T) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.reqs = append(rec.reqs, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Host, r.Header.Get("Authorization"), string(b)})
		rec.mu.Unlock()
		w.Header().Set("X-Upstream", "weed")
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "upstream-body")
	}))
	t.Cleanup(up.Close)
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	h := lab.NewS3Front(u)
	if h == nil {
		t.Fatal("NewS3Front returned a nil handler")
	}
	front := httptest.NewServer(h)
	t.Cleanup(front.Close)
	return front, rec
}

func do(t *testing.T, method, url, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

type pabXML struct {
	XMLName               xml.Name `xml:"PublicAccessBlockConfiguration"`
	BlockPublicAcls       bool     `xml:"BlockPublicAcls"`
	IgnorePublicAcls      bool     `xml:"IgnorePublicAcls"`
	BlockPublicPolicy     bool     `xml:"BlockPublicPolicy"`
	RestrictPublicBuckets bool     `xml:"RestrictPublicBuckets"`
}

func pabBody(a, b, c, d bool) string {
	x, _ := xml.Marshal(pabXML{BlockPublicAcls: a, IgnorePublicAcls: b, BlockPublicPolicy: c, RestrictPublicBuckets: d})
	return `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + string(x)
}

func TestS3Front_PutPublicAccessBlock_Answers200NotForwarded_05AC6(t *testing.T) {
	t.Parallel()
	front, rec := newFront(t)
	resp, body := do(t, http.MethodPut, front.URL+"/b1?publicAccessBlock", pabBody(true, true, true, true), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT ?publicAccessBlock = %d, want 200:\n%s", resp.StatusCode, body)
	}
	if n := len(rec.all()); n != 0 {
		t.Errorf("the public-access-block PUT was forwarded upstream (%d requests): %+v", n, rec.all())
	}
}

func TestS3Front_GetPublicAccessBlock_RoundTrips_05AC6(t *testing.T) {
	t.Parallel()
	front, rec := newFront(t)
	if resp, body := do(t, http.MethodPut, front.URL+"/b1?publicAccessBlock", pabBody(true, false, true, false), nil); resp.StatusCode != 200 {
		t.Fatalf("PUT = %d:\n%s", resp.StatusCode, body)
	}
	resp, body := do(t, http.MethodGet, front.URL+"/b1?publicAccessBlock", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET ?publicAccessBlock = %d, want 200:\n%s", resp.StatusCode, body)
	}
	var got pabXML
	if err := xml.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("GET body is not PublicAccessBlockConfiguration XML: %v\n%s", err, body)
	}
	if !got.BlockPublicAcls || got.IgnorePublicAcls || !got.BlockPublicPolicy || got.RestrictPublicBuckets {
		t.Errorf("round trip lost the booleans: %+v\n%s", got, body)
	}
	if n := len(rec.all()); n != 0 {
		t.Errorf("GET ?publicAccessBlock was forwarded upstream: %+v", rec.all())
	}
}

func TestS3Front_GetPublicAccessBlock_NoneStored404_05AC6(t *testing.T) {
	t.Parallel()
	front, _ := newFront(t)
	resp, body := do(t, http.MethodGet, front.URL+"/nobucket?publicAccessBlock", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET with nothing stored = %d, want 404:\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "<Code>NoSuchPublicAccessBlockConfiguration</Code>") {
		t.Errorf("the 404 body lacks the S3 error code NoSuchPublicAccessBlockConfiguration:\n%s", body)
	}
}

func TestS3Front_DeletePublicAccessBlock_Removes_05AC6(t *testing.T) {
	t.Parallel()
	front, rec := newFront(t)
	do(t, http.MethodPut, front.URL+"/b1?publicAccessBlock", pabBody(true, true, true, true), nil)
	resp, body := do(t, http.MethodDelete, front.URL+"/b1?publicAccessBlock", "", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE ?publicAccessBlock = %d, want 204:\n%s", resp.StatusCode, body)
	}
	resp, body = do(t, http.MethodGet, front.URL+"/b1?publicAccessBlock", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET after DELETE = %d, want 404:\n%s", resp.StatusCode, body)
	}
	if n := len(rec.all()); n != 0 {
		t.Errorf("DELETE ?publicAccessBlock was forwarded upstream: %+v", rec.all())
	}
}

func TestS3Front_PublicAccessBlock_IsPerBucket_05AC6(t *testing.T) {
	t.Parallel()
	front, _ := newFront(t)
	do(t, http.MethodPut, front.URL+"/a?publicAccessBlock", pabBody(true, true, true, true), nil)
	if resp, body := do(t, http.MethodGet, front.URL+"/b?publicAccessBlock", "", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("bucket b sees bucket a's configuration: %d\n%s", resp.StatusCode, body)
	}
	do(t, http.MethodPut, front.URL+"/b?publicAccessBlock", pabBody(false, false, false, false), nil)
	_, body := do(t, http.MethodGet, front.URL+"/a?publicAccessBlock", "", nil)
	var got pabXML
	if err := xml.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("GET a: %v\n%s", err, body)
	}
	if !got.BlockPublicAcls || !got.IgnorePublicAcls || !got.BlockPublicPolicy || !got.RestrictPublicBuckets {
		t.Errorf("bucket a's configuration was overwritten by bucket b's PUT: %+v", got)
	}
}

func TestS3Front_OtherRequestsForwardedUnchanged_05AC6(t *testing.T) {
	t.Parallel()
	front, rec := newFront(t)
	hdr := map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=k/20261104/us-east-1/s3/aws4_request, Signature=abc"}
	cases := []struct{ method, path, query, body string }{
		{http.MethodGet, "/b1", "versioning=", ""},
		{http.MethodPut, "/b1/some/key.txt", "", "payload-bytes"},
		{http.MethodGet, "/b1", "list-type=2&prefix=a%2Fb", ""},
	}
	for _, c := range cases {
		u := front.URL + c.path
		if c.query != "" {
			u += "?" + c.query
		}
		resp, body := do(t, c.method, u, c.body, hdr)
		if resp.StatusCode != http.StatusTeapot || body != "upstream-body" ||
			resp.Header.Get("X-Upstream") != "weed" || resp.Header.Get("ETag") != `"abc"` {
			t.Errorf("%s %s: upstream response not returned unchanged: %d %q %v", c.method, u, resp.StatusCode, body, resp.Header)
		}
	}
	got := rec.all()
	if len(got) != len(cases) {
		t.Fatalf("upstream saw %d requests, want %d: %+v", len(got), len(cases), got)
	}
	frontHost := strings.TrimPrefix(front.URL, "http://")
	for i, c := range cases {
		g := got[i]
		if g.Method != c.method || g.Path != c.path || g.RawQuery != c.query || g.Body != c.body {
			t.Errorf("request %d forwarded as %+v, want %s %s?%s body %q", i, g, c.method, c.path, c.query, c.body)
		}
		if g.Host != frontHost {
			t.Errorf("request %d Host = %q, want the client's Host %q (the SigV4 signature covers it)", i, g.Host, frontHost)
		}
		if g.Auth != hdr["Authorization"] {
			t.Errorf("request %d Authorization = %q, want it unchanged", i, g.Auth)
		}
	}
}
