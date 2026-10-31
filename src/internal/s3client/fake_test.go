package s3client_test

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"strange.company/sc/internal/clock"
)

// fakeS3 is an in-process, path-style S3 server: buckets, versioning, conditional writes, listing with pages,
// version listing, server-side copy by version, and an independent SigV4 verifier (it does not share code with the
// client under test). It is the pure-Go layer of plan 06 section 22.
type fakeS3 struct {
	mu      sync.Mutex
	srv     *httptest.Server
	clk     *clock.Fake
	access  string
	secret  string
	region  string
	buckets map[string]*fbucket
	counter int

	pageSize   int  // keys or versions per list page (0: 1000)
	status409  bool // a failed If-None-Match answers 409 ConditionalRequestConflict instead of 412
	dateSkew   time.Duration
	failNext   int    // every request answers this status (0: off)
	failCode   string // with failNext: the S3 error code
	reqs       []frec
	authErrors []string
}

type frec struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
}

type fver struct {
	id     string
	data   []byte
	etag   string
	mod    time.Time
	marker bool
}

type fbucket struct {
	versioning bool
	objs       map[string][]*fver // oldest first
}

func newFakeS3(t *testing.T) *fakeS3 {
	t.Helper()
	f := &fakeS3{
		clk:     clock.NewFake(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		access:  "AKIA" + "FIXTUREKEY0001",
		secret:  "fixture-secret-" + strings.Repeat("k", 24),
		region:  "us-east-1",
		buckets: map[string]*fbucket{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeS3) requests() []frec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]frec(nil), f.reqs...)
}

func (f *fakeS3) versionCount(bucket, key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b := f.buckets[bucket]; b != nil {
		return len(b.objs[key])
	}
	return 0
}

func (f *fakeS3) setPageSize(n int) { f.mu.Lock(); f.pageSize = n; f.mu.Unlock() }

func (f *fakeS3) setStatus409(v bool) { f.mu.Lock(); f.status409 = v; f.mu.Unlock() }

func (f *fakeS3) setSkew(d time.Duration) { f.mu.Lock(); f.dateSkew = d; f.mu.Unlock() }

// failWith makes every request answer status/code until it is called again with 0.
func (f *fakeS3) failWith(status int, code string) {
	f.mu.Lock()
	f.failNext, f.failCode = status, code
	f.mu.Unlock()
}

func (f *fakeS3) authFailures() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.authErrors...)
}

func s3Err(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, msg)
}

func md5hex(b []byte) string { s := md5.Sum(b); return hex.EncodeToString(s[:]) }

func (f *fakeS3) nextID() string { f.counter++; return fmt.Sprintf("ver-%04d", f.counter) }

func current(vs []*fver) *fver {
	if len(vs) == 0 {
		return nil
	}
	return vs[len(vs)-1]
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, frec{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone()})
	if msg := f.verify(r, body); msg != "" {
		f.authErrors = append(f.authErrors, msg)
		s3Err(w, 403, "SignatureDoesNotMatch", msg)
		return
	}
	if f.failNext != 0 {
		s3Err(w, f.failNext, f.failCode, "injected failure")
		return
	}
	trim := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(trim, "/")
	q := r.URL.Query()
	switch {
	case bucket == "" && r.Method == http.MethodGet:
		names := make([]string, 0, len(f.buckets))
		for n := range f.buckets {
			names = append(names, n)
		}
		sort.Strings(names)
		var sb strings.Builder
		sb.WriteString(`<ListAllMyBucketsResult><Buckets>`)
		for _, n := range names {
			sb.WriteString("<Bucket><Name>" + n + "</Name></Bucket>")
		}
		sb.WriteString(`</Buckets></ListAllMyBucketsResult>`)
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, sb.String())
	case key == "":
		f.bucketOp(w, r, bucket, q, body)
	default:
		f.objectOp(w, r, bucket, key, q, body)
	}
}

func (f *fakeS3) bucketOp(w http.ResponseWriter, r *http.Request, name string, q url.Values, body []byte) {
	b := f.buckets[name]
	switch r.Method {
	case http.MethodPut:
		if _, ok := q["versioning"]; ok {
			if b == nil {
				s3Err(w, 404, "NoSuchBucket", "no bucket")
				return
			}
			b.versioning = strings.Contains(string(body), "Enabled")
			return
		}
		if b == nil {
			f.buckets[name] = &fbucket{objs: map[string][]*fver{}}
		}
		return
	case http.MethodHead:
		if b == nil {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Date", f.clk.Now().Add(f.dateSkew).UTC().Format(http.TimeFormat))
		return
	case http.MethodGet:
		if b == nil {
			s3Err(w, 404, "NoSuchBucket", "no bucket")
			return
		}
		if _, ok := q["versions"]; ok {
			f.listVersions(w, b, q)
			return
		}
		f.listV2(w, b, q)
	default:
		s3Err(w, 405, "MethodNotAllowed", "bad method")
	}
}

func (f *fakeS3) pageLimit() int {
	if f.pageSize > 0 {
		return f.pageSize
	}
	return 1000
}

func (f *fakeS3) listV2(w http.ResponseWriter, b *fbucket, q url.Values) {
	prefix, after := q.Get("prefix"), q.Get("continuation-token")
	var keys []string
	for k, vs := range b.objs {
		if c := current(vs); c != nil && !c.marker && strings.HasPrefix(k, prefix) && k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	trunc := false
	if len(keys) > f.pageLimit() {
		keys, trunc = keys[:f.pageLimit()], true
	}
	var sb strings.Builder
	sb.WriteString(`<ListBucketResult><IsTruncated>` + strconv.FormatBool(trunc) + `</IsTruncated>`)
	if trunc {
		sb.WriteString("<NextContinuationToken>" + xmlEsc(keys[len(keys)-1]) + "</NextContinuationToken>")
	}
	for _, k := range keys {
		c := current(b.objs[k])
		sb.WriteString("<Contents><Key>" + xmlEsc(k) + "</Key><ETag>&quot;" + c.etag + "&quot;</ETag><Size>" + strconv.Itoa(len(c.data)) + "</Size></Contents>")
	}
	sb.WriteString(`</ListBucketResult>`)
	w.Header().Set("Content-Type", "application/xml")
	io.WriteString(w, sb.String())
}

func xmlEsc(s string) string {
	var sb strings.Builder
	_ = xml.EscapeText(&sb, []byte(s))
	return sb.String()
}

type flat struct {
	key string
	v   *fver
	idx int // 0 = newest of the key
}

func (f *fakeS3) listVersions(w http.ResponseWriter, b *fbucket, q url.Values) {
	prefix, km, vm := q.Get("prefix"), q.Get("key-marker"), q.Get("version-id-marker")
	var keys []string
	for k := range b.objs {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var all []flat
	for _, k := range keys {
		vs := b.objs[k]
		for i := len(vs) - 1; i >= 0; i-- {
			all = append(all, flat{key: k, v: vs[i], idx: len(vs) - 1 - i})
		}
	}
	start := 0
	if km != "" {
		for i, e := range all {
			if e.key == km && e.v.id == vm {
				start = i + 1
			}
		}
	}
	rest := all[start:]
	trunc := false
	if len(rest) > f.pageLimit() {
		rest, trunc = rest[:f.pageLimit()], true
	}
	var sb strings.Builder
	sb.WriteString(`<ListVersionsResult><IsTruncated>` + strconv.FormatBool(trunc) + `</IsTruncated>`)
	if trunc {
		last := rest[len(rest)-1]
		sb.WriteString("<NextKeyMarker>" + xmlEsc(last.key) + "</NextKeyMarker><NextVersionIdMarker>" + last.v.id + "</NextVersionIdMarker>")
	}
	for _, e := range rest {
		tag := "Version"
		if e.v.marker {
			tag = "DeleteMarker"
		}
		fmt.Fprintf(&sb, "<%s><Key>%s</Key><VersionId>%s</VersionId><IsLatest>%t</IsLatest><LastModified>%s</LastModified>", tag, xmlEsc(e.key), e.v.id, e.idx == 0, e.v.mod.UTC().Format("2006-01-02T15:04:05.000Z"))
		if !e.v.marker {
			fmt.Fprintf(&sb, "<ETag>&quot;%s&quot;</ETag><Size>%d</Size>", e.v.etag, len(e.v.data))
		}
		fmt.Fprintf(&sb, "</%s>", tag)
	}
	sb.WriteString(`</ListVersionsResult>`)
	w.Header().Set("Content-Type", "application/xml")
	io.WriteString(w, sb.String())
}

func (f *fakeS3) put(b *fbucket, key string, data []byte) *fver {
	v := &fver{data: append([]byte(nil), data...), etag: md5hex(data), mod: f.clk.Now()}
	if b.versioning {
		v.id = f.nextID()
		b.objs[key] = append(b.objs[key], v)
	} else {
		v.id = "null"
		b.objs[key] = []*fver{v}
	}
	f.clk.Advance(time.Second)
	return v
}

func (f *fakeS3) objectOp(w http.ResponseWriter, r *http.Request, bname, key string, q url.Values, body []byte) {
	b := f.buckets[bname]
	if b == nil {
		s3Err(w, 404, "NoSuchBucket", "no bucket")
		return
	}
	vid := q.Get("versionId")
	find := func() *fver {
		vs := b.objs[key]
		if vid == "" {
			if c := current(vs); c != nil && !c.marker {
				return c
			}
			return nil
		}
		for _, v := range vs {
			if v.id == vid {
				return v
			}
		}
		return nil
	}
	switch r.Method {
	case http.MethodPut:
		if src := r.Header.Get("x-amz-copy-source"); src != "" {
			u, err := url.Parse(src)
			if err != nil {
				s3Err(w, 400, "InvalidArgument", "bad copy source")
				return
			}
			sb, sk, _ := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
			sbk := f.buckets[sb]
			var sv *fver
			if sbk != nil {
				for _, v := range sbk.objs[sk] {
					if v.id == u.Query().Get("versionId") {
						sv = v
					}
				}
			}
			if sv == nil {
				s3Err(w, 404, "NoSuchVersion", "no such version")
				return
			}
			nv := f.put(b, key, sv.data)
			w.Header().Set("x-amz-version-id", nv.id)
			io.WriteString(w, `<CopyObjectResult><ETag>&quot;`+nv.etag+`&quot;</ETag></CopyObjectResult>`)
			return
		}
		cur := current(b.objs[key])
		exists := cur != nil && !cur.marker
		if r.Header.Get("If-None-Match") == "*" && exists {
			if f.status409 {
				s3Err(w, 409, "ConditionalRequestConflict", "conflict")
			} else {
				s3Err(w, 412, "PreconditionFailed", "At least one of the pre-conditions you specified did not hold")
			}
			return
		}
		if im := r.Header.Get("If-Match"); im != "" && (!exists || im != `"`+cur.etag+`"`) {
			s3Err(w, 412, "PreconditionFailed", "At least one of the pre-conditions you specified did not hold")
			return
		}
		if cs := r.Header.Get("x-amz-checksum-sha256"); cs != "" {
			sum := sha256.Sum256(body)
			if cs != base64.StdEncoding.EncodeToString(sum[:]) {
				s3Err(w, 400, "BadDigest", "checksum mismatch")
				return
			}
		}
		nv := f.put(b, key, body)
		w.Header().Set("ETag", `"`+nv.etag+`"`)
		if b.versioning {
			w.Header().Set("x-amz-version-id", nv.id)
		}
	case http.MethodGet, http.MethodHead:
		v := find()
		if v == nil || v.marker {
			if r.Method == http.MethodHead {
				w.WriteHeader(404)
				return
			}
			s3Err(w, 404, "NoSuchKey", "no such key")
			return
		}
		w.Header().Set("ETag", `"`+v.etag+`"`)
		w.Header().Set("x-amz-version-id", v.id)
		w.Header().Set("Last-Modified", v.mod.UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", strconv.Itoa(len(v.data)))
		if r.Method == http.MethodGet {
			w.Write(v.data)
		}
	case http.MethodDelete:
		cur := current(b.objs[key])
		if im := r.Header.Get("If-Match"); im != "" && (cur == nil || im != `"`+cur.etag+`"`) {
			s3Err(w, 412, "PreconditionFailed", "At least one of the pre-conditions you specified did not hold")
			return
		}
		if vid != "" {
			vs := b.objs[key]
			for i, v := range vs {
				if v.id == vid {
					b.objs[key] = append(vs[:i:i], vs[i+1:]...)
					break
				}
			}
			if len(b.objs[key]) == 0 {
				delete(b.objs, key)
			}
		} else if b.versioning {
			b.objs[key] = append(b.objs[key], &fver{id: f.nextID(), marker: true, mod: f.clk.Now()})
			f.clk.Advance(time.Second)
		} else {
			delete(b.objs, key)
		}
		w.WriteHeader(204)
	default:
		s3Err(w, 405, "MethodNotAllowed", "bad method")
	}
}

// --- independent SigV4 verification (AWS general reference, "Signature Version 4 signing process")

func hm(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}

func uriEncode(s string, slash bool) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			sb.WriteByte(c)
		case c == '/' && !slash:
			sb.WriteByte(c)
		default:
			fmt.Fprintf(&sb, "%%%02X", c)
		}
	}
	return sb.String()
}

// verify returns "" when the request carries a valid SigV4 signature of this fake's credentials.
func (f *fakeS3) verify(r *http.Request, body []byte) string {
	auth := r.Header.Get("Authorization")
	const pfx = "AWS4-HMAC-SHA256 "
	if !strings.HasPrefix(auth, pfx) {
		return "missing or malformed Authorization header"
	}
	parts := map[string]string{}
	for _, p := range strings.Split(strings.TrimPrefix(auth, pfx), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
		parts[k] = v
	}
	scope := strings.Split(parts["Credential"], "/")
	if len(scope) != 5 || scope[0] != f.access || scope[2] != f.region || scope[3] != "s3" || scope[4] != "aws4_request" {
		return "bad credential scope " + parts["Credential"]
	}
	date := r.Header.Get("x-amz-date")
	if date != f.clk.Now().UTC().Format("20060102T150405Z") || !strings.HasPrefix(date, scope[1]) {
		return "x-amz-date " + date + " is not the client clock's time"
	}
	sum := sha256.Sum256(body)
	ph := r.Header.Get("x-amz-content-sha256")
	if ph != hex.EncodeToString(sum[:]) {
		return "x-amz-content-sha256 does not match the body"
	}
	signed := strings.Split(parts["SignedHeaders"], ";")
	need := map[string]bool{"host": true, "x-amz-content-sha256": true, "x-amz-date": true}
	for _, h := range signed {
		delete(need, h)
	}
	if len(need) != 0 {
		return "SignedHeaders lacks a required header: " + parts["SignedHeaders"]
	}
	var ch strings.Builder
	for _, h := range signed {
		var v string
		switch h {
		case "host":
			v = r.Host
		case "content-length":
			v = strconv.FormatInt(r.ContentLength, 10)
		default:
			v = strings.Join(r.Header.Values(http.CanonicalHeaderKey(h)), ",")
		}
		ch.WriteString(h + ":" + strings.Join(strings.Fields(v), " ") + "\n")
	}
	var qs []string
	for k, vs := range r.URL.Query() {
		for _, v := range vs {
			qs = append(qs, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	sort.Strings(qs)
	cr := strings.Join([]string{r.Method, uriEncode(r.URL.Path, false), strings.Join(qs, "&"), ch.String(), parts["SignedHeaders"], ph}, "\n")
	crh := sha256.Sum256([]byte(cr))
	sts := strings.Join([]string{"AWS4-HMAC-SHA256", date, strings.Join(scope[1:], "/"), hex.EncodeToString(crh[:])}, "\n")
	k := hm([]byte("AWS4"+f.secret), scope[1])
	k = hm(k, scope[2])
	k = hm(k, scope[3])
	k = hm(k, "aws4_request")
	want := hex.EncodeToString(hm(k, sts))
	if parts["Signature"] != want {
		return "signature mismatch; canonical request:\n" + cr
	}
	return ""
}
