// Package s3client is the standard-library SigV4 S3 client of sc-bootstrap (CD-70a, plan 06 section 8, plan 14
// section 7.3). It replaces aws-sdk-go-v2 for the handful of calls the state library and the engine need.
package s3client

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/clock"
)

// Config configures a Client.
type Config struct {
	Endpoint  string
	Region    string
	AccessKey string
	SecretKey string
	PathStyle bool
	HTTP      *http.Client
	Clock     clock.Clock
}

// Client talks to one S3-compatible endpoint.
type Client struct {
	base   *url.URL
	signer Signer
	region string
	path   bool
	http   *http.Client
	clk    clock.Clock
}

// Bucket is one bucket of a Client; it implements store.S3 for keys inside the bucket.
type Bucket struct {
	c    *Client
	name string
}

// Signer signs requests with AWS Signature Version 4.
type Signer struct {
	AccessKey string
	SecretKey string
	Region    string
	Service   string
}

// Error is an S3 failure: the HTTP status, the S3 error code, and the store.Kind it maps to. It unwraps to a
// *store.Error of that Kind.
type Error struct {
	Status  int
	Code    string
	Message string
	Kind    store.Kind
}

// PutOptions are the per-request options of PutObject.
type PutOptions struct {
	IfNoneMatch bool
	IfMatch     string
	Checksum    bool
}

// PutResult is what a successful PutObject or CopyVersion returns.
type PutResult struct {
	ETag      string
	VersionID string
}

// ObjectInfo describes one object version.
type ObjectInfo struct {
	Key            string
	ETag           string
	VersionID      string
	Size           int64
	LastModified   time.Time
	IsLatest       bool
	IsDeleteMarker bool
}

const (
	emptySHA256  = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	maxErrorBody = 64 << 10
)

// New validates cfg and returns a Client.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("s3client: endpoint is not a URL: %v", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("s3client: endpoint %q must be an http or https URL with a host", cfg.Endpoint)
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("s3client: access key and secret key are required")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	c := &Client{
		base:   &url.URL{Scheme: u.Scheme, Host: u.Host},
		signer: Signer{AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, Region: region, Service: "s3"},
		region: region,
		path:   cfg.PathStyle,
		http:   cfg.HTTP,
		clk:    cfg.Clock,
	}
	if c.http == nil {
		c.http = &http.Client{}
	}
	if c.clk == nil {
		c.clk = clock.Real{}
	}
	return c, nil
}

// Bucket returns the bucket handle.
func (c *Client) Bucket(name string) *Bucket { return &Bucket{c: c, name: name} }

// --- signing

// uriEncode is the AWS URI encoding: unreserved characters stay, everything else is %XX; "/" stays unless slash.
func uriEncode(s string, slash bool) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			sb.WriteByte(c)
		case c == '/' && !slash:
			sb.WriteByte('/')
		default:
			fmt.Fprintf(&sb, "%%%02X", c)
		}
	}
	return sb.String()
}

func canonicalQuery(q url.Values) string {
	parts := make([]string, 0, len(q))
	for k, vs := range q {
		for _, v := range vs {
			parts = append(parts, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

func hmacSHA256(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}

// signedHeader reports whether a header is part of the signature besides host, x-amz-content-sha256 and x-amz-date.
func signedHeader(lower string) bool {
	switch lower {
	case "date", "range", "content-md5", "content-type", "if-match", "if-none-match", "if-modified-since", "if-unmodified-since":
		return true
	}
	return strings.HasPrefix(lower, "x-amz-")
}

// Sign adds x-amz-date, x-amz-content-sha256 and Authorization to req.
func (s Signer) Sign(req *http.Request, payloadSHA256 string, now time.Time) error {
	if req == nil || req.URL == nil {
		return errors.New("s3client: nothing to sign")
	}
	if s.AccessKey == "" || s.SecretKey == "" || s.Region == "" || s.Service == "" {
		return errors.New("s3client: signer needs access key, secret key, region and service")
	}
	if len(payloadSHA256) != 64 {
		return errors.New("s3client: payload hash must be 64 hex characters")
	}
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadSHA256)
	req.Header.Del("Authorization")

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	vals := map[string]string{"host": host}
	for name, vs := range req.Header {
		lower := strings.ToLower(name)
		if !signedHeader(lower) {
			continue
		}
		clean := make([]string, len(vs))
		for i, v := range vs {
			clean[i] = strings.Join(strings.Fields(v), " ")
		}
		vals[lower] = strings.Join(clean, ",")
	}
	names := make([]string, 0, len(vals))
	for n := range vals {
		names = append(names, n)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, n := range names {
		ch.WriteString(n + ":" + vals[n] + "\n")
	}
	signed := strings.Join(names, ";")
	path := req.URL.Path
	if path == "" {
		path = "/"
	}
	canon := strings.Join([]string{req.Method, uriEncode(path, false), canonicalQuery(req.URL.Query()), ch.String(), signed, payloadSHA256}, "\n")
	ch2 := sha256.Sum256([]byte(canon))
	scope := day + "/" + s.Region + "/" + s.Service + "/aws4_request"
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(ch2[:])
	k := hmacSHA256([]byte("AWS4"+s.SecretKey), day)
	k = hmacSHA256(k, s.Region)
	k = hmacSHA256(k, s.Service)
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, sts))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.AccessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
	return nil
}

// --- errors

// Error returns the message.
func (e *Error) Error() string {
	msg := fmt.Sprintf("s3: HTTP %d %s", e.Status, e.Code)
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// Unwrap returns the *store.Error of the same Kind.
func (e *Error) Unwrap() error { return &store.Error{Kind: e.Kind, Msg: e.Code} }

type xmlError struct {
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

func classify(status int, code string, conditionalCreate bool) store.Kind {
	switch {
	case status == http.StatusNotFound:
		return store.NotFound
	case status == http.StatusPreconditionFailed && conditionalCreate:
		return store.Exists
	case status == http.StatusConflict && conditionalCreate:
		return store.Exists
	case status == http.StatusPreconditionFailed:
		return store.PreconditionFailed
	case status == http.StatusNotImplemented:
		return store.Unsupported
	case status >= 500 || status == http.StatusTooManyRequests:
		return store.Unavailable
	}
	return 0
}

// --- requests

type req struct {
	method string
	bucket string
	key    string
	query  url.Values
	header map[string]string
	body   []byte
	// conditionalCreate marks If-None-Match requests, whose 412 and 409 mean "already exists".
	conditionalCreate bool
}

func (c *Client) url(r req) *url.URL {
	u := &url.URL{Scheme: c.base.Scheme, Host: c.base.Host}
	var path string
	switch {
	case r.bucket == "":
		path = "/"
	case c.path:
		path = "/" + r.bucket
		if r.key != "" {
			path += "/" + r.key
		}
	default:
		u.Host = r.bucket + "." + c.base.Host
		path = "/" + r.key
	}
	u.Path = path
	u.RawPath = uriEncode(path, false)
	u.RawQuery = canonicalQuery(r.query)
	return u
}

func (c *Client) do(ctx context.Context, r req) (*http.Response, error) {
	var body io.Reader
	if len(r.body) > 0 {
		body = bytes.NewReader(r.body)
	}
	hr, err := http.NewRequestWithContext(ctx, r.method, c.url(r).String(), body)
	if err != nil {
		return nil, fmt.Errorf("s3client: %v", err)
	}
	for k, v := range r.header {
		hr.Header.Set(k, v)
	}
	payload := emptySHA256
	if len(r.body) > 0 {
		sum := sha256.Sum256(r.body)
		payload = hex.EncodeToString(sum[:])
	}
	if err := c.signer.Sign(hr, payload, c.clk.Now()); err != nil {
		return nil, err
	}
	resp, err := c.http.Do(hr)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, &store.Error{Kind: store.Unavailable, Msg: err.Error()}
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		var xe xmlError
		_ = xml.Unmarshal(raw, &xe)
		if xe.Code == "" {
			xe.Code = strings.ReplaceAll(http.StatusText(resp.StatusCode), " ", "")
		}
		if len(xe.Message) > 512 {
			xe.Message = xe.Message[:512]
		}
		return nil, &Error{Status: resp.StatusCode, Code: xe.Code, Message: xe.Message, Kind: classify(resp.StatusCode, xe.Code, r.conditionalCreate)}
	}
	return resp, nil
}

// doRead performs the request and returns the body.
func (c *Client) doRead(ctx context.Context, r req) ([]byte, http.Header, error) {
	resp, err := c.do(ctx, r)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, cerr
		}
		return nil, nil, &store.Error{Kind: store.Unavailable, Msg: err.Error()}
	}
	return data, resp.Header, nil
}

func trimETag(s string) string { return strings.Trim(strings.TrimPrefix(s, "W/"), `"`) }

func quoteETag(s string) string {
	if strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) && len(s) >= 2 {
		return s
	}
	return `"` + s + `"`
}

// --- bucket operations

// CreateBucket creates a bucket; a bucket the caller already owns is not an error.
func (c *Client) CreateBucket(ctx context.Context, name string) error {
	r := req{method: http.MethodPut, bucket: name}
	if c.region != "us-east-1" {
		r.body = []byte(`<CreateBucketConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><LocationConstraint>` + c.region + `</LocationConstraint></CreateBucketConfiguration>`)
	}
	_, _, err := c.doRead(ctx, r)
	var se *Error
	if errors.As(err, &se) && se.Code == "BucketAlreadyOwnedByYou" {
		return nil
	}
	return err
}

// SetVersioning enables or suspends versioning on a bucket.
func (c *Client) SetVersioning(ctx context.Context, name string, enabled bool) error {
	status := "Suspended"
	if enabled {
		status = "Enabled"
	}
	body := `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>` + status + `</Status></VersioningConfiguration>`
	_, _, err := c.doRead(ctx, req{method: http.MethodPut, bucket: name, query: url.Values{"versioning": {""}}, body: []byte(body)})
	return err
}

// Versioning reports whether versioning is Enabled on a bucket (GetBucketVersioning).
func (c *Client) Versioning(ctx context.Context, name string) (bool, error) {
	data, _, err := c.doRead(ctx, req{method: http.MethodGet, bucket: name, query: url.Values{"versioning": {""}}})
	if err != nil {
		return false, err
	}
	var doc struct {
		Status string `xml:"Status"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return false, fmt.Errorf("s3client: GetBucketVersioning response: %v", err)
	}
	return doc.Status == "Enabled", nil
}

// HeadBucket returns a NotFound error when the bucket does not exist.
func (c *Client) HeadBucket(ctx context.Context, name string) error {
	_, _, err := c.doRead(ctx, req{method: http.MethodHead, bucket: name})
	return err
}

// ListBuckets lists the bucket names.
func (c *Client) ListBuckets(ctx context.Context) ([]string, error) {
	data, _, err := c.doRead(ctx, req{method: http.MethodGet})
	if err != nil {
		return nil, err
	}
	var doc struct {
		Names []string `xml:"Buckets>Bucket>Name"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("s3client: ListBuckets response: %v", err)
	}
	sort.Strings(doc.Names)
	return doc.Names, nil
}

// --- object operations

func (b *Bucket) req(method, key string) req {
	return req{method: method, bucket: b.name, key: key}
}

// Get reads the current version of key.
func (b *Bucket) Get(ctx context.Context, key string) (store.Object, error) {
	data, h, err := b.c.doRead(ctx, b.req(http.MethodGet, key))
	if err != nil {
		return store.Object{}, err
	}
	return store.Object{Data: data, ETag: trimETag(h.Get("ETag"))}, nil
}

// PutIfAbsent is the conditional create.
func (b *Bucket) PutIfAbsent(ctx context.Context, key string, data []byte) (string, error) {
	r, err := b.PutObject(ctx, key, data, PutOptions{IfNoneMatch: true})
	return r.ETag, err
}

// Put writes key, with If-Match when ifMatch is not empty.
func (b *Bucket) Put(ctx context.Context, key string, data []byte, ifMatch string) (string, error) {
	r, err := b.PutObject(ctx, key, data, PutOptions{IfMatch: ifMatch})
	return r.ETag, err
}

// List lists the keys with the prefix, following every page.
func (b *Bucket) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	token := ""
	for {
		q := url.Values{"list-type": {"2"}}
		if prefix != "" {
			q.Set("prefix", prefix)
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		r := b.req(http.MethodGet, "")
		r.query = q
		data, _, err := b.c.doRead(ctx, r)
		if err != nil {
			return nil, err
		}
		var doc struct {
			Truncated bool     `xml:"IsTruncated"`
			Next      string   `xml:"NextContinuationToken"`
			Keys      []string `xml:"Contents>Key"`
		}
		if err := xml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("s3client: List response: %v", err)
		}
		keys = append(keys, doc.Keys...)
		if !doc.Truncated || doc.Next == "" {
			return keys, nil
		}
		token = doc.Next
	}
}

// Delete removes the current version of key.
func (b *Bucket) Delete(ctx context.Context, key string, ifMatch string) error {
	r := b.req(http.MethodDelete, key)
	if ifMatch != "" {
		r.header = map[string]string{"If-Match": quoteETag(ifMatch)}
	}
	_, _, err := b.c.doRead(ctx, r)
	return err
}

// PutObject writes key with explicit options.
func (b *Bucket) PutObject(ctx context.Context, key string, data []byte, o PutOptions) (PutResult, error) {
	r := b.req(http.MethodPut, key)
	r.body = data
	r.header = map[string]string{}
	if o.IfNoneMatch {
		r.header["If-None-Match"] = "*"
		r.conditionalCreate = true
	}
	if o.IfMatch != "" {
		r.header["If-Match"] = quoteETag(o.IfMatch)
	}
	if o.Checksum {
		sum := sha256.Sum256(data)
		r.header["x-amz-checksum-sha256"] = base64.StdEncoding.EncodeToString(sum[:])
	}
	_, h, err := b.c.doRead(ctx, r)
	if err != nil {
		return PutResult{}, err
	}
	return PutResult{ETag: trimETag(h.Get("ETag")), VersionID: h.Get("x-amz-version-id")}, nil
}

type versionItem struct {
	XMLName      xml.Name
	Key          string `xml:"Key"`
	VersionID    string `xml:"VersionId"`
	IsLatest     bool   `xml:"IsLatest"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
}

// ListVersions lists every version and delete marker whose key starts with prefix, following every page.
func (b *Bucket) ListVersions(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	keyMarker, verMarker := "", ""
	for {
		q := url.Values{"versions": {""}}
		if prefix != "" {
			q.Set("prefix", prefix)
		}
		if keyMarker != "" {
			q.Set("key-marker", keyMarker)
		}
		if verMarker != "" {
			q.Set("version-id-marker", verMarker)
		}
		r := b.req(http.MethodGet, "")
		r.query = q
		data, _, err := b.c.doRead(ctx, r)
		if err != nil {
			return nil, err
		}
		var doc struct {
			Truncated bool          `xml:"IsTruncated"`
			NextKey   string        `xml:"NextKeyMarker"`
			NextVer   string        `xml:"NextVersionIdMarker"`
			Items     []versionItem `xml:",any"`
		}
		if err := xml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("s3client: ListVersions response: %v", err)
		}
		for _, it := range doc.Items {
			var kind bool
			switch it.XMLName.Local {
			case "Version":
			case "DeleteMarker":
				kind = true
			default:
				continue
			}
			mod, _ := time.Parse(time.RFC3339, it.LastModified)
			out = append(out, ObjectInfo{Key: it.Key, ETag: trimETag(it.ETag), VersionID: it.VersionID, Size: it.Size, LastModified: mod, IsLatest: it.IsLatest, IsDeleteMarker: kind})
		}
		if !doc.Truncated || (doc.NextKey == "" && doc.NextVer == "") {
			return out, nil
		}
		keyMarker, verMarker = doc.NextKey, doc.NextVer
	}
}

// GetVersion reads one version of key.
func (b *Bucket) GetVersion(ctx context.Context, key, versionID string) (store.Object, error) {
	r := b.req(http.MethodGet, key)
	r.query = url.Values{"versionId": {versionID}}
	data, h, err := b.c.doRead(ctx, r)
	if err != nil {
		return store.Object{}, err
	}
	return store.Object{Data: data, ETag: trimETag(h.Get("ETag"))}, nil
}

// DeleteVersion permanently deletes one version of key.
func (b *Bucket) DeleteVersion(ctx context.Context, key, versionID string) error {
	r := b.req(http.MethodDelete, key)
	r.query = url.Values{"versionId": {versionID}}
	_, _, err := b.c.doRead(ctx, r)
	return err
}

// CopyVersion server-side copies one version of key onto the same key, creating a new current version.
func (b *Bucket) CopyVersion(ctx context.Context, key, versionID string) (PutResult, error) {
	r := b.req(http.MethodPut, key)
	r.header = map[string]string{
		"x-amz-copy-source": "/" + uriEncode(b.name+"/"+key, false) + "?versionId=" + uriEncode(versionID, true),
	}
	data, h, err := b.c.doRead(ctx, r)
	if err != nil {
		return PutResult{}, err
	}
	// S3 may answer a failed server-side copy with 200 and an <Error> root element.
	var doc struct {
		XMLName xml.Name
		ETag    string `xml:"ETag"`
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	_ = xml.Unmarshal(data, &doc)
	if doc.XMLName.Local == "Error" {
		code := doc.Code
		if code == "" {
			code = "InternalError"
		}
		return PutResult{}, &Error{Status: http.StatusOK, Code: code, Message: doc.Message, Kind: store.Unavailable}
	}
	etag := trimETag(doc.ETag)
	if etag == "" {
		etag = trimETag(h.Get("ETag"))
	}
	return PutResult{ETag: etag, VersionID: h.Get("x-amz-version-id")}, nil
}

// Head returns the metadata of the current version of key.
func (b *Bucket) Head(ctx context.Context, key string) (ObjectInfo, error) {
	resp, err := b.c.do(ctx, b.req(http.MethodHead, key))
	if err != nil {
		return ObjectInfo{}, err
	}
	defer resp.Body.Close()
	info := ObjectInfo{Key: key, ETag: trimETag(resp.Header.Get("ETag")), VersionID: resp.Header.Get("x-amz-version-id"), IsLatest: true}
	if n, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); err == nil {
		info.Size = n
	}
	if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		info.LastModified = t
	}
	return info, nil
}

// ServerTime returns the server's Date header.
func (b *Bucket) ServerTime(ctx context.Context) (time.Time, error) {
	resp, err := b.c.do(ctx, req{method: http.MethodHead, bucket: b.name})
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()
	t, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		return time.Time{}, fmt.Errorf("s3client: server Date header: %v", err)
	}
	return t, nil
}
