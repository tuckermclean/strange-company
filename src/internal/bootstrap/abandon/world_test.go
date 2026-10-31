package abandon_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"strange.company/sc/internal/bootstrap/install"
	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

// tickClock is the injected clock of every test: each Now moves it forward one second, so two instants read in
// causal order are always strictly ordered (50-AC-30 compares a confirmation with the first CreateBucket without
// reading a wall clock). Sleep advances it by the slept duration.
type tickClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTickClock() *tickClock {
	return &tickClock{t: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)}
}

func (c *tickClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

func (c *tickClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
	return nil
}

var _ clock.Clock = (*tickClock)(nil)

// s3World is an in-memory S3 for the fake install (plan 50 section 22; ADR-0205 emulates no IAM, so the
// account model is the smallest faithful one: each access key is one account, bucket names are global, and a
// bucket another account owns answers AccessDenied). It records every bucket creation with the clock reading.
type s3World struct {
	mu      sync.Mutex
	clk     clock.Clock
	keys    map[string]bool     // known access key ids
	buckets map[string]*wBucket // by name
	created []bucketEvent       // in creation order
	// ignoreIfNoneMatch is the stripping proxy of 06-AC-2: If-None-Match never reaches the store.
	ignoreIfNoneMatch bool
	ctr               int
}

type bucketEvent struct {
	Name, AccessKey string
	At              time.Time
}

type wBucket struct {
	owner     string
	versioned bool
	objs      map[string][]*wVersion // oldest first
}

type wVersion struct {
	id     string
	data   []byte
	mod    time.Time
	marker bool
}

func newS3World(clk clock.Clock, keys ...string) *s3World {
	w := &s3World{clk: clk, keys: map[string]bool{}, buckets: map[string]*wBucket{}}
	for _, k := range keys {
		w.keys[k] = true
	}
	return w
}

// open is Deps.NewS3: a client for the credential.
func (w *s3World) open(cfg s3client.Config) (install.Account, error) {
	return &wAccount{w: w, key: cfg.AccessKey}, nil
}

// createBucket is what the fake storage module does for a bucket (versioning Enabled, as plan 14 requires).
func (w *s3World) createBucket(key, name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.keys[key] {
		return &s3client.Error{Status: 403, Code: "InvalidAccessKeyId"}
	}
	if b, ok := w.buckets[name]; ok {
		if b.owner == key {
			return nil
		}
		return &s3client.Error{Status: 409, Code: "BucketAlreadyExists", Kind: store.Exists}
	}
	w.buckets[name] = &wBucket{owner: key, versioned: true, objs: map[string][]*wVersion{}}
	w.created = append(w.created, bucketEvent{Name: name, AccessKey: key, At: w.clk.Now()})
	return nil
}

// bucketsOf lists the buckets an access key owns.
func (w *s3World) bucketsOf(key string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for n, b := range w.buckets {
		if b.owner == key {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// allBuckets lists every bucket whose name starts with prefix, whoever owns it.
func (w *s3World) allBuckets(prefix string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for n := range w.buckets {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// objects lists the current keys of a bucket below prefix, bypassing access control (the test's own view).
func (w *s3World) objects(bucket, prefix string) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.buckets[bucket]
	if b == nil {
		return nil
	}
	var out []string
	for k, vs := range b.objs {
		if strings.HasPrefix(k, prefix) && len(vs) > 0 && !vs[len(vs)-1].marker {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// read returns the current content of an object, bypassing access control.
func (w *s3World) read(bucket, key string) ([]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.buckets[bucket]
	if b == nil {
		return nil, false
	}
	vs := b.objs[key]
	if len(vs) == 0 || vs[len(vs)-1].marker {
		return nil, false
	}
	return append([]byte(nil), vs[len(vs)-1].data...), true
}

// write puts an object as the owner of the bucket, bypassing preconditions (the fake storage module's migration).
func (w *s3World) write(key, bucket, objKey string, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.buckets[bucket]
	if b == nil {
		return &s3client.Error{Status: 404, Code: "NoSuchBucket", Kind: store.NotFound}
	}
	if b.owner != key {
		return &s3client.Error{Status: 403, Code: "AccessDenied"}
	}
	w.put(b, objKey, data)
	return nil
}

func md5hex(b []byte) string { s := md5.Sum(b); return hex.EncodeToString(s[:]) }

// put appends a version; w.mu is held.
func (w *s3World) put(b *wBucket, key string, data []byte) *wVersion {
	w.ctr++
	v := &wVersion{id: fmt.Sprintf("v%05d", w.ctr), data: append([]byte(nil), data...), mod: w.clk.Now()}
	b.objs[key] = append(b.objs[key], v)
	return v
}

func (b *wBucket) cur(key string) *wVersion {
	vs := b.objs[key]
	if len(vs) == 0 || vs[len(vs)-1].marker {
		return nil
	}
	return vs[len(vs)-1]
}

// --- account

type wAccount struct {
	w   *s3World
	key string
}

var _ install.Account = (*wAccount)(nil)

func (a *wAccount) check(name string) (*wBucket, error) {
	if !a.w.keys[a.key] {
		return nil, &s3client.Error{Status: 403, Code: "InvalidAccessKeyId"}
	}
	b := a.w.buckets[name]
	if b == nil {
		return nil, &s3client.Error{Status: 404, Code: "NoSuchBucket", Kind: store.NotFound}
	}
	if b.owner != a.key {
		return nil, &s3client.Error{Status: 403, Code: "AccessDenied"}
	}
	return b, nil
}

func (a *wAccount) HeadBucket(_ context.Context, name string) error {
	a.w.mu.Lock()
	defer a.w.mu.Unlock()
	_, err := a.check(name)
	return err
}

func (a *wAccount) Versioning(_ context.Context, name string) (bool, error) {
	a.w.mu.Lock()
	defer a.w.mu.Unlock()
	b, err := a.check(name)
	if err != nil {
		return false, err
	}
	return b.versioned, nil
}

func (a *wAccount) ListBuckets(context.Context) ([]string, error) {
	if !a.w.keys[a.key] {
		return nil, &s3client.Error{Status: 403, Code: "InvalidAccessKeyId"}
	}
	return a.w.bucketsOf(a.key), nil
}

func (a *wAccount) Bucket(name string) tofustate.Store { return &wStore{a: a, name: name} }

// --- bucket (tofustate.Store)

type wStore struct {
	a    *wAccount
	name string
}

var _ tofustate.Store = (*wStore)(nil)

func (s *wStore) with(fn func(b *wBucket) error) error {
	s.a.w.mu.Lock()
	defer s.a.w.mu.Unlock()
	b, err := s.a.check(s.name)
	if err != nil {
		return err
	}
	return fn(b)
}

func errPrecondition(kind store.Kind) error {
	return &s3client.Error{Status: 412, Code: "PreconditionFailed", Kind: kind}
}

func (s *wStore) Get(_ context.Context, key string) (store.Object, error) {
	var out store.Object
	err := s.with(func(b *wBucket) error {
		v := b.cur(key)
		if v == nil {
			return &s3client.Error{Status: 404, Code: "NoSuchKey", Kind: store.NotFound}
		}
		out = store.Object{Data: append([]byte(nil), v.data...), ETag: md5hex(v.data)}
		return nil
	})
	return out, err
}

func (s *wStore) PutIfAbsent(ctx context.Context, key string, data []byte) (string, error) {
	r, err := s.PutObject(ctx, key, data, s3client.PutOptions{IfNoneMatch: true})
	return r.ETag, err
}

func (s *wStore) Put(ctx context.Context, key string, data []byte, ifMatch string) (string, error) {
	r, err := s.PutObject(ctx, key, data, s3client.PutOptions{IfMatch: ifMatch})
	return r.ETag, err
}

func (s *wStore) PutObject(_ context.Context, key string, data []byte, o s3client.PutOptions) (s3client.PutResult, error) {
	var out s3client.PutResult
	err := s.with(func(b *wBucket) error {
		cur := b.cur(key)
		if o.IfNoneMatch && !s.a.w.ignoreIfNoneMatch && cur != nil {
			return errPrecondition(store.Exists)
		}
		if o.IfMatch != "" && (cur == nil || md5hex(cur.data) != o.IfMatch) {
			return errPrecondition(store.PreconditionFailed)
		}
		v := s.a.w.put(b, key, data)
		out = s3client.PutResult{ETag: md5hex(data), VersionID: v.id}
		return nil
	})
	return out, err
}

func (s *wStore) List(_ context.Context, prefix string) ([]string, error) {
	var out []string
	err := s.with(func(b *wBucket) error {
		for k := range b.objs {
			if strings.HasPrefix(k, prefix) && b.cur(k) != nil {
				out = append(out, k)
			}
		}
		sort.Strings(out)
		return nil
	})
	return out, err
}

func (s *wStore) Delete(_ context.Context, key, _ string) error {
	return s.with(func(b *wBucket) error {
		if b.cur(key) == nil {
			return nil
		}
		s.a.w.ctr++
		b.objs[key] = append(b.objs[key], &wVersion{id: fmt.Sprintf("v%05d", s.a.w.ctr), marker: true, mod: s.a.w.clk.Now()})
		return nil
	})
}

func (s *wStore) ListVersions(_ context.Context, prefix string) ([]s3client.ObjectInfo, error) {
	var out []s3client.ObjectInfo
	err := s.with(func(b *wBucket) error {
		var keys []string
		for k := range b.objs {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			vs := b.objs[k]
			for i := len(vs) - 1; i >= 0; i-- {
				v := vs[i]
				out = append(out, s3client.ObjectInfo{Key: k, ETag: md5hex(v.data), VersionID: v.id, Size: int64(len(v.data)),
					LastModified: v.mod, IsLatest: i == len(vs)-1, IsDeleteMarker: v.marker})
			}
		}
		return nil
	})
	return out, err
}

func (s *wStore) GetVersion(_ context.Context, key, versionID string) (store.Object, error) {
	var out store.Object
	err := s.with(func(b *wBucket) error {
		for _, v := range b.objs[key] {
			if v.id == versionID && !v.marker {
				out = store.Object{Data: append([]byte(nil), v.data...), ETag: md5hex(v.data)}
				return nil
			}
		}
		return &s3client.Error{Status: 404, Code: "NoSuchVersion", Kind: store.NotFound}
	})
	return out, err
}

func (s *wStore) DeleteVersion(_ context.Context, key, versionID string) error {
	return s.with(func(b *wBucket) error {
		vs := b.objs[key]
		for i, v := range vs {
			if v.id == versionID {
				b.objs[key] = append(vs[:i:i], vs[i+1:]...)
				if len(b.objs[key]) == 0 {
					delete(b.objs, key)
				}
				return nil
			}
		}
		return &s3client.Error{Status: 404, Code: "NoSuchVersion", Kind: store.NotFound}
	})
}

func (s *wStore) CopyVersion(_ context.Context, key, versionID string) (s3client.PutResult, error) {
	var out s3client.PutResult
	err := s.with(func(b *wBucket) error {
		for _, v := range b.objs[key] {
			if v.id == versionID && !v.marker {
				n := s.a.w.put(b, key, v.data)
				out = s3client.PutResult{ETag: md5hex(v.data), VersionID: n.id}
				return nil
			}
		}
		return &s3client.Error{Status: 404, Code: "NoSuchVersion", Kind: store.NotFound}
	})
	return out, err
}

func (s *wStore) Head(_ context.Context, key string) (s3client.ObjectInfo, error) {
	var out s3client.ObjectInfo
	err := s.with(func(b *wBucket) error {
		v := b.cur(key)
		if v == nil {
			return &s3client.Error{Status: 404, Code: "NoSuchKey", Kind: store.NotFound}
		}
		out = s3client.ObjectInfo{Key: key, ETag: md5hex(v.data), VersionID: v.id, Size: int64(len(v.data)), LastModified: v.mod, IsLatest: true}
		return nil
	})
	return out, err
}

func (s *wStore) ServerTime(context.Context) (time.Time, error) { return s.a.w.clk.Now(), nil }

// goodProbe is the C7 probe of an OpenTofu that honors a hand-written lock file.
type goodProbe struct{}

func (goodProbe) Probe(_ context.Context, p tofustate.ProbeParams) (tofustate.ProbeResult, error) {
	return tofustate.ProbeResult{ExitCode: 1, Stderr: "Error: Error acquiring the state lock\n\nLock Info:\n  ID:        " + p.LockID + "\n  Operation: OperationTypePlan\n"}, nil
}

// fakeEncrypt stands in for sops: the ciphertext carries a digest of the plaintext and the recipients, never the plaintext.
func fakeEncrypt(plain []byte, recipients []string) ([]byte, error) {
	if len(recipients) == 0 {
		return nil, errors.New("no recipients")
	}
	sum := md5hex(append(bytes.Clone(plain), []byte(strings.Join(recipients, ","))...))
	return []byte("sops:\n  fixture-ciphertext: " + sum + "\n  recipients: [" + strings.Join(recipients, ", ") + "]\n"), nil
}
