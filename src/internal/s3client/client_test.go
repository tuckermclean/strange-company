package s3client_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/s3client"
)

var _ store.S3 = (*s3client.Bucket)(nil)

const awsDocSecretTail = "/bPxRfiCYEXAMPLEKEY"

// The published AWS credentials of the S3 SigV4 documentation examples, assembled at runtime so that no
// credential-shaped literal sits in the repository.
func awsDocKeys() (access, secret string) {
	return "AKIA" + "IOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG" + awsDocSecretTail
}

var authRe = regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=([^,]+),\s*SignedHeaders=([^,]+),\s*Signature=([0-9a-f]{64})$`)

func parseAuth(t *testing.T, h string) (cred, signed, sig string) {
	t.Helper()
	m := authRe.FindStringSubmatch(h)
	if m == nil {
		t.Fatalf("Authorization header %q is not AWS4-HMAC-SHA256 Credential=..,SignedHeaders=..,Signature=<64 hex>", h)
	}
	return m[1], m[2], m[3]
}

func sha(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }

// 06-AC-1 (library): the signer reproduces the three published AWS S3 SigV4 examples byte for byte.
func TestSigner_PublishedAWSVectors_06AC1(t *testing.T) {
	access, secret := awsDocKeys()
	now := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	empty := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	s := s3client.Signer{AccessKey: access, SecretKey: secret, Region: "us-east-1", Service: "s3"}
	cases := []struct {
		name, method, url, payload string
		headers                    map[string]string
		signed, sig                string
	}{
		{"get-object-range", "GET", "https://examplebucket.s3.amazonaws.com/test.txt", empty,
			map[string]string{"Range": "bytes=0-9"},
			"host;range;x-amz-content-sha256;x-amz-date",
			"f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"},
		{"put-object", "PUT", "https://examplebucket.s3.amazonaws.com/test%24file.text", sha("Welcome to Amazon S3."),
			map[string]string{"Date": "Fri, 24 May 2013 00:00:00 GMT", "x-amz-storage-class": "REDUCED_REDUNDANCY"},
			"date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class",
			"98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd"},
		{"list-objects-query", "GET", "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J", empty, nil,
			"host;x-amz-content-sha256;x-amz-date",
			"34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := http.NewRequest(c.method, c.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			if err := s.Sign(req, c.payload, now); err != nil {
				t.Fatalf("Sign: %v", err)
			}
			auth := req.Header.Get("Authorization")
			if auth == "" {
				t.Fatal("Sign set no Authorization header")
			}
			cred, signed, sig := parseAuth(t, auth)
			if want := access + "/20130524/us-east-1/s3/aws4_request"; cred != want {
				t.Errorf("Credential = %q, want %q", cred, want)
			}
			if signed != c.signed {
				t.Errorf("SignedHeaders = %q, want %q", signed, c.signed)
			}
			if sig != c.sig {
				t.Errorf("Signature = %s, want the published %s", sig, c.sig)
			}
			if got := req.Header.Get("x-amz-date"); got != "20130524T000000Z" {
				t.Errorf("x-amz-date = %q", got)
			}
			if got := req.Header.Get("x-amz-content-sha256"); got != c.payload {
				t.Errorf("x-amz-content-sha256 = %q, want the payload hash", got)
			}
			if strings.Contains(auth, secret) {
				t.Error("the Authorization header contains the secret key")
			}
		})
	}
}

// 06-AC-1 (library): a request for a different instant or region gets a different signature, and the secret
// never leaves the process.
func TestSigner_ScopeChangesSignature_06AC1(t *testing.T) {
	access, secret := awsDocKeys()
	sign := func(region string, now time.Time) string {
		req, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/test.txt", nil)
		s := s3client.Signer{AccessKey: access, SecretKey: secret, Region: region, Service: "s3"}
		if err := s.Sign(req, sha(""), now); err != nil {
			t.Fatalf("Sign: %v", err)
		}
		return req.Header.Get("Authorization")
	}
	base := sign("us-east-1", time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	if base == "" {
		t.Fatal("no Authorization header")
	}
	if base == sign("eu-west-1", time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)) {
		t.Error("the region is not part of the signature")
	}
	if base == sign("us-east-1", time.Date(2013, 5, 25, 0, 0, 0, 0, time.UTC)) {
		t.Error("the date is not part of the signature")
	}
}

func newClient(t *testing.T, f *fakeS3) (*s3client.Client, *s3client.Bucket) {
	t.Helper()
	c, err := s3client.New(s3client.Config{Endpoint: f.srv.URL, Region: f.region, AccessKey: f.access, SecretKey: f.secret, PathStyle: true, Clock: f.clk})
	if err != nil || c == nil {
		t.Fatalf("New: client=%v err=%v", c, err)
	}
	ctx := context.Background()
	if err := c.CreateBucket(ctx, "state-bkt"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	b := c.Bucket("state-bkt")
	if b == nil {
		t.Fatal("Bucket returned nil")
	}
	return c, b
}

func noAuthFailures(t *testing.T, f *fakeS3) {
	t.Helper()
	for _, m := range f.authFailures() {
		t.Errorf("the fake S3 rejected a request signature: %s", m)
	}
}

// 06-AC-1 (library): the first request the client sends is verifiably SigV4-signed (an independent verifier in
// the fake recomputes the signature from the wire request).
func TestClient_SignaturesVerify_06AC1(t *testing.T) {
	f := newFakeS3(t)
	c, b := newClient(t, f)
	ctx := context.Background()
	if _, err := b.PutIfAbsent(ctx, "a/b.txt", []byte("hello")); err != nil {
		t.Fatalf("PutIfAbsent: %v", err)
	}
	o, err := b.Get(ctx, "a/b.txt")
	if err != nil || string(o.Data) != "hello" {
		t.Fatalf("Get = %q, %v", o.Data, err)
	}
	if err := c.HeadBucket(ctx, "state-bkt"); err != nil {
		t.Errorf("HeadBucket: %v", err)
	}
	noAuthFailures(t, f)
	if len(f.requests()) < 3 {
		t.Errorf("the fake saw %d requests, want at least 3", len(f.requests()))
	}
}

// 06-AC-1: wrong credentials are refused and the error does not leak the secret or the signature.
func TestClient_BadCredentialsDoNotLeak_06AC1(t *testing.T) {
	f := newFakeS3(t)
	c, err := s3client.New(s3client.Config{Endpoint: f.srv.URL, Region: f.region, AccessKey: f.access, SecretKey: "wrong-" + f.secret, PathStyle: true, Clock: f.clk})
	if err != nil || c == nil {
		t.Fatalf("New: %v", err)
	}
	err = c.CreateBucket(context.Background(), "state-bkt")
	if err == nil {
		t.Fatal("CreateBucket succeeded with a wrong secret key")
	}
	var se *s3client.Error
	if !errors.As(err, &se) || se.Status != 403 {
		t.Errorf("error = %#v, want *s3client.Error with Status 403", err)
	}
	for _, leak := range []string{"wrong-" + f.secret, f.secret} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("error text leaks the secret key: %v", err)
		}
	}
}

func TestNew_Validation_06AC1(t *testing.T) {
	for _, ep := range []string{"", "ftp://example.invalid", "not a url", "example.invalid:9000"} {
		if c, err := s3client.New(s3client.Config{Endpoint: ep, Region: "us-east-1", AccessKey: "a", SecretKey: "b", PathStyle: true}); err == nil {
			t.Errorf("New(Endpoint=%q) = %v, nil error; want an error", ep, c)
		}
	}
}

// 06-AC-1 (C1, C2): PutIfAbsent sends If-None-Match: * and maps both 412 and 409 to store.Exists, keeping the status.
func TestPutIfAbsent_ConditionalCreate_06AC1(t *testing.T) {
	for _, tc := range []struct {
		name   string
		s409   bool
		status int
	}{{"412", false, 412}, {"409", true, 409}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeS3(t)
			f.setStatus409(tc.s409)
			_, b := newClient(t, f)
			ctx := context.Background()
			etag, err := b.PutIfAbsent(ctx, "k1", []byte("one"))
			if err != nil {
				t.Fatalf("first PutIfAbsent: %v", err)
			}
			if etag != md5hex([]byte("one")) {
				t.Errorf("etag = %q, want the unquoted MD5 %q", etag, md5hex([]byte("one")))
			}
			_, err = b.PutIfAbsent(ctx, "k1", []byte("two"))
			if !store.Is(err, store.Exists) {
				t.Fatalf("second PutIfAbsent error = %v, want store.Exists", err)
			}
			var se *s3client.Error
			if !errors.As(err, &se) || se.Status != tc.status {
				t.Errorf("error = %#v, want *s3client.Error with Status %d", err, tc.status)
			}
			noAuthFailures(t, f)
			saw := false
			for _, r := range f.requests() {
				if r.Method == "PUT" && r.Header.Get("If-None-Match") == "*" {
					saw = true
				}
			}
			if !saw {
				t.Error("no PUT carried If-None-Match: *")
			}
			o, err := b.Get(ctx, "k1")
			if err != nil || string(o.Data) != "one" {
				t.Errorf("object after the refused create = %q, %v; want the first content", o.Data, err)
			}
		})
	}
}

// 06-AC-1 (C5): If-Match is sent as a quoted ETag; a wrong ETag is store.PreconditionFailed.
func TestPut_IfMatch_06AC1(t *testing.T) {
	f := newFakeS3(t)
	_, b := newClient(t, f)
	ctx := context.Background()
	etag, err := b.PutIfAbsent(ctx, "k4", []byte("v1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Put(ctx, "k4", []byte("v2"), etag); err != nil {
		t.Fatalf("Put with the right ETag: %v", err)
	}
	quoted := `"` + md5hex([]byte("v2")) + `"`
	if _, err := b.Put(ctx, "k4", []byte("v3"), quoted); err != nil {
		t.Fatalf("Put with an already quoted ETag: %v", err)
	}
	_, err = b.Put(ctx, "k4", []byte("v4"), "deadbeef")
	if !store.Is(err, store.PreconditionFailed) {
		t.Fatalf("Put with a wrong ETag error = %v, want store.PreconditionFailed", err)
	}
	var headers []string
	for _, r := range f.requests() {
		if v := r.Header.Get("If-Match"); r.Method == "PUT" && v != "" {
			headers = append(headers, v)
		}
	}
	want := []string{`"` + etag + `"`, quoted, `"deadbeef"`}
	if strings.Join(headers, "|") != strings.Join(want, "|") {
		t.Errorf("If-Match headers = %q, want %q", headers, want)
	}
	if err := b.Delete(ctx, "k4", "wrong"); !store.Is(err, store.PreconditionFailed) {
		t.Errorf("Delete with a wrong ETag error = %v, want store.PreconditionFailed", err)
	}
	noAuthFailures(t, f)
}

func TestGet_NotFoundAndPlainPut_06AC1(t *testing.T) {
	f := newFakeS3(t)
	_, b := newClient(t, f)
	ctx := context.Background()
	if _, err := b.Get(ctx, "nope"); !store.Is(err, store.NotFound) {
		t.Errorf("Get(missing) error = %v, want store.NotFound", err)
	}
	if _, err := b.Put(ctx, "k", []byte("x"), ""); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := b.Put(ctx, "k", []byte("y"), ""); err != nil {
		t.Fatalf("unconditional overwrite: %v", err)
	}
	o, err := b.Get(ctx, "k")
	if err != nil || string(o.Data) != "y" || o.ETag != md5hex([]byte("y")) {
		t.Errorf("Get = %q etag %q, %v", o.Data, o.ETag, err)
	}
	if err := b.Delete(ctx, "k", ""); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := b.Get(ctx, "k"); !store.Is(err, store.NotFound) {
		t.Errorf("Get after Delete error = %v, want store.NotFound", err)
	}
}

// Keys with spaces, plus signs and non-ASCII characters survive the round trip and the canonical URI.
func TestKeys_EscapingRoundTrip_06AC1(t *testing.T) {
	f := newFakeS3(t)
	_, b := newClient(t, f)
	ctx := context.Background()
	keys := []string{"lab one/state file.tfstate", "a+b=c&d/e%f", "café/über.txt", "x/~tilde/.dot"}
	for i, k := range keys {
		if _, err := b.Put(ctx, k, []byte{byte('a' + i)}, ""); err != nil {
			t.Fatalf("Put(%q): %v", k, err)
		}
	}
	for i, k := range keys {
		o, err := b.Get(ctx, k)
		if err != nil || !bytes.Equal(o.Data, []byte{byte('a' + i)}) {
			t.Errorf("Get(%q) = %q, %v", k, o.Data, err)
		}
	}
	got, err := b.List(ctx, "")
	if err != nil || len(got) != len(keys) {
		t.Fatalf("List = %q, %v; want %d keys", got, err, len(keys))
	}
	noAuthFailures(t, f)
}

// List follows continuation tokens through every page, applies the prefix, and returns keys in order.
func TestList_FollowsPages_06AC1(t *testing.T) {
	f := newFakeS3(t)
	f.setPageSize(2)
	_, b := newClient(t, f)
	ctx := context.Background()
	for _, k := range []string{"p/e", "p/a", "p/c", "q/z", "p/b", "p/d"} {
		if _, err := b.Put(ctx, k, []byte(k), ""); err != nil {
			t.Fatal(err)
		}
	}
	got, err := b.List(ctx, "p/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := "p/a p/b p/c p/d p/e"; strings.Join(got, " ") != want {
		t.Errorf("List(p/) = %q, want %q", got, want)
	}
	pages := 0
	for _, r := range f.requests() {
		if r.Method == "GET" && r.Query.Get("list-type") == "2" {
			pages++
		}
	}
	if pages < 3 {
		t.Errorf("List made %d list requests for 5 keys at 2 per page, want at least 3", pages)
	}
	noAuthFailures(t, f)
}

// 06-AC-1 (C6): PutOptions.Checksum adds the SDK-style x-amz-checksum-sha256 header; without it the header is absent.
func TestPutObject_ChecksumOption_06AC1(t *testing.T) {
	f := newFakeS3(t)
	_, b := newClient(t, f)
	ctx := context.Background()
	body := []byte("checksummed")
	if _, err := b.PutObject(ctx, "ck", body, s3client.PutOptions{Checksum: true}); err != nil {
		t.Fatalf("PutObject with checksum: %v", err)
	}
	if _, err := b.PutObject(ctx, "plain", body, s3client.PutOptions{}); err != nil {
		t.Fatalf("PutObject without checksum: %v", err)
	}
	sum := sha256.Sum256(body)
	want := base64.StdEncoding.EncodeToString(sum[:])
	for _, r := range f.requests() {
		if r.Method != "PUT" {
			continue
		}
		switch {
		case strings.HasSuffix(r.Path, "/ck"):
			if got := r.Header.Get("x-amz-checksum-sha256"); got != want {
				t.Errorf("x-amz-checksum-sha256 on /ck = %q, want %q", got, want)
			}
		case strings.HasSuffix(r.Path, "/plain"):
			if got := r.Header.Get("x-amz-checksum-sha256"); got != "" {
				t.Errorf("x-amz-checksum-sha256 on /plain = %q, want none", got)
			}
		}
	}
	noAuthFailures(t, f)
}

// 06-AC-1 (C4) and 06-AC-8: versions are listed newest first, paged, readable by id, copyable onto the same key.
func TestVersions_ListGetCopyDelete_06AC8(t *testing.T) {
	f := newFakeS3(t)
	f.setPageSize(2)
	c, b := newClient(t, f)
	ctx := context.Background()
	if err := c.SetVersioning(ctx, "state-bkt", true); err != nil {
		t.Fatalf("SetVersioning: %v", err)
	}
	var ids []string
	for _, content := range []string{"first", "second", "third"} {
		r, err := b.PutObject(ctx, "m/terraform.tfstate", []byte(content), s3client.PutOptions{})
		if err != nil || r.VersionID == "" {
			t.Fatalf("PutObject(%s) = %+v, %v; want a version id", content, r, err)
		}
		ids = append(ids, r.VersionID)
	}
	if _, err := b.PutObject(ctx, "m/terraform.tfstate.tflock", []byte("lock"), s3client.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	vs, err := b.ListVersions(ctx, "m/terraform.tfstate")
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(vs) != 4 {
		t.Fatalf("ListVersions(prefix) returned %d versions, want 4 (three state versions and the lock object): %+v", len(vs), vs)
	}
	if vs[0].Key != "m/terraform.tfstate" || vs[0].VersionID != ids[2] || !vs[0].IsLatest || vs[1].VersionID != ids[1] || vs[1].IsLatest || vs[2].VersionID != ids[0] {
		t.Errorf("versions are not newest first with IsLatest only on the newest: %+v (ids %v)", vs, ids)
	}
	if vs[0].Size != int64(len("third")) || vs[0].ETag != md5hex([]byte("third")) || vs[0].LastModified.IsZero() {
		t.Errorf("version metadata = %+v", vs[0])
	}
	o, err := b.GetVersion(ctx, "m/terraform.tfstate", ids[0])
	if err != nil || string(o.Data) != "first" {
		t.Fatalf("GetVersion(first) = %q, %v", o.Data, err)
	}
	h, err := b.Head(ctx, "m/terraform.tfstate")
	if err != nil || h.VersionID != ids[2] || h.Size != 5 || h.ETag != md5hex([]byte("third")) {
		t.Errorf("Head = %+v, %v; want the third version", h, err)
	}
	cp, err := b.CopyVersion(ctx, "m/terraform.tfstate", ids[0])
	if err != nil || cp.VersionID == "" {
		t.Fatalf("CopyVersion = %+v, %v", cp, err)
	}
	for _, id := range ids {
		if cp.VersionID == id {
			t.Errorf("CopyVersion reused version id %s; a copy creates a new version", id)
		}
	}
	cur, err := b.Get(ctx, "m/terraform.tfstate")
	if err != nil || string(cur.Data) != "first" {
		t.Errorf("current content after CopyVersion = %q, %v; want first", cur.Data, err)
	}
	if n := f.versionCount("state-bkt", "m/terraform.tfstate"); n != 4 {
		t.Errorf("version count after CopyVersion = %d, want 4 (history preserved)", n)
	}
	if err := b.DeleteVersion(ctx, "m/terraform.tfstate", ids[1]); err != nil {
		t.Fatalf("DeleteVersion: %v", err)
	}
	if n := f.versionCount("state-bkt", "m/terraform.tfstate"); n != 3 {
		t.Errorf("version count after DeleteVersion = %d, want 3", n)
	}
	if _, err := b.GetVersion(ctx, "m/terraform.tfstate", ids[1]); !store.Is(err, store.NotFound) {
		t.Errorf("GetVersion(deleted) error = %v, want store.NotFound", err)
	}
	noAuthFailures(t, f)
}

func TestVersions_DeleteMarkerListed_06AC8(t *testing.T) {
	f := newFakeS3(t)
	c, b := newClient(t, f)
	ctx := context.Background()
	if err := c.SetVersioning(ctx, "state-bkt", true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.PutObject(ctx, "k", []byte("x"), s3client.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ctx, "k", ""); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := b.Get(ctx, "k"); !store.Is(err, store.NotFound) {
		t.Errorf("Get behind a delete marker error = %v, want store.NotFound", err)
	}
	vs, err := b.ListVersions(ctx, "k")
	if err != nil || len(vs) != 2 {
		t.Fatalf("ListVersions = %+v, %v; want the version and the delete marker", vs, err)
	}
	if !vs[0].IsDeleteMarker || !vs[0].IsLatest || vs[1].IsDeleteMarker {
		t.Errorf("versions = %+v; want the newest to be the delete marker", vs)
	}
}

// 06-AC-1 (C8): ServerTime is the server's Date header.
func TestServerTime_UsesDateHeader_06AC1(t *testing.T) {
	f := newFakeS3(t)
	_, b := newClient(t, f)
	f.setSkew(90 * time.Second)
	got, err := b.ServerTime(context.Background())
	if err != nil {
		t.Fatalf("ServerTime: %v", err)
	}
	if want := f.clk.Now().Add(90 * time.Second); got.Unix() != want.Unix() {
		t.Errorf("ServerTime = %v, want %v", got, want)
	}
}

func TestBucketOps_06AC1(t *testing.T) {
	f := newFakeS3(t)
	c, _ := newClient(t, f)
	ctx := context.Background()
	if err := c.HeadBucket(ctx, "no-such-bucket"); !store.Is(err, store.NotFound) {
		t.Errorf("HeadBucket(missing) error = %v, want store.NotFound", err)
	}
	if err := c.CreateBucket(ctx, "other-bkt"); err != nil {
		t.Fatal(err)
	}
	names, err := c.ListBuckets(ctx)
	if err != nil || strings.Join(names, " ") != "other-bkt state-bkt" {
		t.Errorf("ListBuckets = %q, %v", names, err)
	}
}

// Status classification: 5xx is Unavailable, 501 on a conditional write is Unsupported, 404 is NotFound.
func TestErrors_ClassifyStatuses_06AC1(t *testing.T) {
	f := newFakeS3(t)
	_, b := newClient(t, f)
	ctx := context.Background()
	f.failWith(503, "SlowDown")
	if _, err := b.Get(ctx, "k"); !store.Is(err, store.Unavailable) {
		t.Errorf("503 error = %v, want store.Unavailable", err)
	}
	f.failWith(501, "NotImplemented")
	_, err := b.PutIfAbsent(ctx, "k", []byte("x"))
	if !store.Is(err, store.Unsupported) {
		t.Errorf("501 on a conditional create error = %v, want store.Unsupported", err)
	}
	var se *s3client.Error
	if !errors.As(err, &se) || se.Status != 501 || se.Code != "NotImplemented" {
		t.Errorf("error = %#v, want Status 501 Code NotImplemented", err)
	}
	f.failWith(0, "")
}

func TestContextCancelStopsRequests_06AC1(t *testing.T) {
	f := newFakeS3(t)
	_, b := newClient(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Get(ctx, "k"); err == nil {
		t.Error("Get with a cancelled context succeeded")
	}
}
