// Package s3client is the standard-library SigV4 S3 client of sc-bootstrap (CD-70a, plan 06 section 8, plan 14
// section 7.3). It replaces aws-sdk-go-v2 for the handful of calls the state library and the engine need.
package s3client

import (
	"context"
	"net/http"
	"time"

	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/clock"
)

// Config configures a Client.
// SC-STUB(F08 06-AC-1)
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
// SC-STUB(F08 06-AC-1)
type Client struct{}

// Bucket is one bucket of a Client; it implements store.S3 for keys inside the bucket.
// SC-STUB(F08 06-AC-1)
type Bucket struct{}

// Signer signs requests with AWS Signature Version 4.
// SC-STUB(F08 06-AC-1)
type Signer struct {
	AccessKey string
	SecretKey string
	Region    string
	Service   string
}

// Error is an S3 failure: the HTTP status, the S3 error code, and the store.Kind it maps to. It unwraps to a
// *store.Error of that Kind.
// SC-STUB(F08 06-AC-1)
type Error struct {
	Status  int
	Code    string
	Message string
	Kind    store.Kind
}

// PutOptions are the per-request options of PutObject.
// SC-STUB(F08 06-AC-1)
type PutOptions struct {
	IfNoneMatch bool
	IfMatch     string
	Checksum    bool
}

// PutResult is what a successful PutObject or CopyVersion returns.
// SC-STUB(F08 06-AC-1)
type PutResult struct {
	ETag      string
	VersionID string
}

// ObjectInfo describes one object version.
// SC-STUB(F08 06-AC-8)
type ObjectInfo struct {
	Key            string
	ETag           string
	VersionID      string
	Size           int64
	LastModified   time.Time
	IsLatest       bool
	IsDeleteMarker bool
}

// New validates cfg and returns a Client.
// SC-STUB(F08 06-AC-1)
func New(cfg Config) (*Client, error) { return nil, nil }

// Bucket returns the bucket handle.
// SC-STUB(F08 06-AC-1)
func (c *Client) Bucket(name string) *Bucket { return nil }

// CreateBucket creates a bucket.
// SC-STUB(F08 06-AC-1)
func (c *Client) CreateBucket(ctx context.Context, name string) error { return nil }

// SetVersioning enables or suspends versioning on a bucket.
// SC-STUB(F08 06-AC-1)
func (c *Client) SetVersioning(ctx context.Context, name string, enabled bool) error { return nil }

// HeadBucket returns a NotFound error when the bucket does not exist.
// SC-STUB(F08 06-AC-1)
func (c *Client) HeadBucket(ctx context.Context, name string) error { return nil }

// ListBuckets lists the bucket names.
// SC-STUB(F08 06-AC-1)
func (c *Client) ListBuckets(ctx context.Context) ([]string, error) { return nil, nil }

// Sign adds x-amz-date, x-amz-content-sha256 and Authorization to req.
// SC-STUB(F08 06-AC-1)
func (s Signer) Sign(req *http.Request, payloadSHA256 string, now time.Time) error { return nil }

// Error returns the message.
// SC-STUB(F08 06-AC-1)
func (e *Error) Error() string { return "" }

// Unwrap returns the *store.Error of the same Kind.
// SC-STUB(F08 06-AC-1)
func (e *Error) Unwrap() error { return nil }

// Get reads the current version of key.
// SC-STUB(F08 06-AC-1)
func (b *Bucket) Get(ctx context.Context, key string) (store.Object, error) {
	return store.Object{}, nil
}

// PutIfAbsent is the conditional create.
// SC-STUB(F08 06-AC-1)
func (b *Bucket) PutIfAbsent(ctx context.Context, key string, data []byte) (string, error) {
	return "", nil
}

// Put writes key, with If-Match when ifMatch is not empty.
// SC-STUB(F08 06-AC-1)
func (b *Bucket) Put(ctx context.Context, key string, data []byte, ifMatch string) (string, error) {
	return "", nil
}

// List lists the keys with the prefix, following every page.
// SC-STUB(F08 06-AC-1)
func (b *Bucket) List(ctx context.Context, prefix string) ([]string, error) { return nil, nil }

// Delete removes the current version of key.
// SC-STUB(F08 06-AC-1)
func (b *Bucket) Delete(ctx context.Context, key string, ifMatch string) error { return nil }

// PutObject writes key with explicit options.
// SC-STUB(F08 06-AC-1)
func (b *Bucket) PutObject(ctx context.Context, key string, data []byte, o PutOptions) (PutResult, error) {
	return PutResult{}, nil
}

// ListVersions lists every version and delete marker whose key starts with prefix, following every page.
// SC-STUB(F08 06-AC-8)
func (b *Bucket) ListVersions(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	return nil, nil
}

// GetVersion reads one version of key.
// SC-STUB(F08 06-AC-8)
func (b *Bucket) GetVersion(ctx context.Context, key, versionID string) (store.Object, error) {
	return store.Object{}, nil
}

// DeleteVersion permanently deletes one version of key.
// SC-STUB(F08 06-AC-1)
func (b *Bucket) DeleteVersion(ctx context.Context, key, versionID string) error { return nil }

// CopyVersion server-side copies one version of key onto the same key, creating a new current version.
// SC-STUB(F08 06-AC-8)
func (b *Bucket) CopyVersion(ctx context.Context, key, versionID string) (PutResult, error) {
	return PutResult{}, nil
}

// Head returns the metadata of the current version of key.
// SC-STUB(F08 06-AC-8)
func (b *Bucket) Head(ctx context.Context, key string) (ObjectInfo, error) { return ObjectInfo{}, nil }

// ServerTime returns the server's Date header.
// SC-STUB(F08 06-AC-1)
func (b *Bucket) ServerTime(ctx context.Context) (time.Time, error) { return time.Time{}, nil }
