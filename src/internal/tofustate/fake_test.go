package tofustate_test

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

// memS3 is the in-memory S3 fake of plan 06 section 22: versioned, with knobs to ignore If-None-Match, answer 409,
// reject conditional writes, drop versioning, ignore If-Match, reject (or require) SDK checksums, and skew the
// server clock. It implements tofustate.Store.
type memS3 struct {
	mu   sync.Mutex
	clk  *clock.Fake
	objs map[string][]*mver // oldest first
	ctr  int

	IgnoreIfNoneMatch    bool
	Status409            bool
	NoConditional        bool // any If-None-Match or If-Match write answers 501
	NoVersioning         bool // a single null version per key; no version ids
	IgnoreIfMatch        bool
	RejectChecksum       bool // PutOptions.Checksum writes fail with 400
	FailPlainAfterReject bool // after a checksummed write was rejected, the next plain write fails once
	Skew                 time.Duration

	armed    bool
	touched  map[string]bool
	attempts map[string]int // PutObject calls with IfNoneMatch per key
	puts     []string       // keys in PutObject order
}

type mver struct {
	id   string
	data []byte
	mod  time.Time
	mark bool
}

func newMemS3() *memS3 {
	return &memS3{
		clk:      clock.NewFake(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)),
		objs:     map[string][]*mver{},
		touched:  map[string]bool{},
		attempts: map[string]int{},
	}
}

var _ tofustate.Store = (*memS3)(nil)

func md5of(b []byte) string { s := md5.Sum(b); return hex.EncodeToString(s[:]) }

func (m *memS3) cur(key string) *mver {
	vs := m.objs[key]
	if len(vs) == 0 || vs[len(vs)-1].mark {
		return nil
	}
	return vs[len(vs)-1]
}

func (m *memS3) write(key string, data []byte) *mver {
	m.ctr++
	v := &mver{id: fmt.Sprintf("v%03d", m.ctr), data: append([]byte(nil), data...), mod: m.clk.Now()}
	if m.NoVersioning {
		v.id = ""
		m.objs[key] = []*mver{v}
	} else {
		m.objs[key] = append(m.objs[key], v)
	}
	return v
}

func (m *memS3) conflict() error {
	if m.Status409 {
		return &s3client.Error{Status: 409, Code: "ConditionalRequestConflict", Kind: store.Exists}
	}
	return &s3client.Error{Status: 412, Code: "PreconditionFailed", Kind: store.Exists}
}

func (m *memS3) PutObject(_ context.Context, key string, data []byte, o s3client.PutOptions) (s3client.PutResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.touched[key] = true
	m.puts = append(m.puts, key)
	if o.Checksum && m.RejectChecksum {
		m.armed = m.FailPlainAfterReject
		return s3client.PutResult{}, &s3client.Error{Status: 400, Code: "InvalidRequest", Kind: store.Unsupported}
	}
	if !o.Checksum && m.armed {
		m.armed = false
		return s3client.PutResult{}, &s3client.Error{Status: 400, Code: "InvalidRequest", Kind: store.Unsupported}
	}
	if o.IfNoneMatch {
		m.attempts[key]++
		if m.NoConditional {
			return s3client.PutResult{}, &s3client.Error{Status: 501, Code: "NotImplemented", Kind: store.Unsupported}
		}
		if !m.IgnoreIfNoneMatch && m.cur(key) != nil {
			return s3client.PutResult{}, m.conflict()
		}
	}
	if o.IfMatch != "" {
		if m.NoConditional {
			return s3client.PutResult{}, &s3client.Error{Status: 501, Code: "NotImplemented", Kind: store.Unsupported}
		}
		c := m.cur(key)
		if !m.IgnoreIfMatch && (c == nil || strings.Trim(o.IfMatch, `"`) != md5of(c.data)) {
			return s3client.PutResult{}, &s3client.Error{Status: 412, Code: "PreconditionFailed", Kind: store.PreconditionFailed}
		}
	}
	v := m.write(key, data)
	return s3client.PutResult{ETag: md5of(data), VersionID: v.id}, nil
}

func (m *memS3) Get(_ context.Context, key string) (store.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.cur(key)
	if c == nil {
		return store.Object{}, &s3client.Error{Status: 404, Code: "NoSuchKey", Kind: store.NotFound}
	}
	return store.Object{Data: append([]byte(nil), c.data...), ETag: md5of(c.data)}, nil
}

func (m *memS3) PutIfAbsent(ctx context.Context, key string, data []byte) (string, error) {
	r, err := m.PutObject(ctx, key, data, s3client.PutOptions{IfNoneMatch: true})
	return r.ETag, err
}

func (m *memS3) Put(ctx context.Context, key string, data []byte, ifMatch string) (string, error) {
	r, err := m.PutObject(ctx, key, data, s3client.PutOptions{IfMatch: ifMatch})
	return r.ETag, err
}

func (m *memS3) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.objs {
		if strings.HasPrefix(k, prefix) && m.cur(k) != nil {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (m *memS3) Delete(_ context.Context, key, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, key)
	return nil
}

func (m *memS3) ListVersions(_ context.Context, prefix string) ([]s3client.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.objs {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []s3client.ObjectInfo
	for _, k := range keys {
		vs := m.objs[k]
		for i := len(vs) - 1; i >= 0; i-- {
			v := vs[i]
			out = append(out, s3client.ObjectInfo{Key: k, ETag: md5of(v.data), VersionID: v.id, Size: int64(len(v.data)), LastModified: v.mod, IsLatest: i == len(vs)-1, IsDeleteMarker: v.mark})
		}
	}
	return out, nil
}

func (m *memS3) GetVersion(_ context.Context, key, id string) (store.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.objs[key] {
		if v.id == id && !v.mark {
			return store.Object{Data: append([]byte(nil), v.data...), ETag: md5of(v.data)}, nil
		}
	}
	return store.Object{}, &s3client.Error{Status: 404, Code: "NoSuchVersion", Kind: store.NotFound}
}

func (m *memS3) DeleteVersion(_ context.Context, key, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	vs := m.objs[key]
	for i, v := range vs {
		if v.id == id {
			m.objs[key] = append(vs[:i:i], vs[i+1:]...)
			if len(m.objs[key]) == 0 {
				delete(m.objs, key)
			}
			return nil
		}
	}
	return &s3client.Error{Status: 404, Code: "NoSuchVersion", Kind: store.NotFound}
}

func (m *memS3) CopyVersion(_ context.Context, key, id string) (s3client.PutResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.objs[key] {
		if v.id == id && !v.mark {
			nv := m.write(key, v.data)
			return s3client.PutResult{ETag: md5of(nv.data), VersionID: nv.id}, nil
		}
	}
	return s3client.PutResult{}, &s3client.Error{Status: 404, Code: "NoSuchVersion", Kind: store.NotFound}
}

func (m *memS3) Head(_ context.Context, key string) (s3client.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.cur(key)
	if c == nil {
		return s3client.ObjectInfo{}, &s3client.Error{Status: 404, Code: "NoSuchKey", Kind: store.NotFound}
	}
	return s3client.ObjectInfo{Key: key, ETag: md5of(c.data), VersionID: c.id, Size: int64(len(c.data)), LastModified: c.mod, IsLatest: true}, nil
}

func (m *memS3) ServerTime(context.Context) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.clk.Now().Add(m.Skew), nil
}

// seed writes an object without touching the knobs or the touched set.
func (m *memS3) seed(key, content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.write(key, []byte(content))
	m.clk.Advance(time.Second) // distinct modification times; the suite's own writes do not move the clock
}

func (m *memS3) versionCount(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.objs[key])
}

func (m *memS3) totalVersions(prefix string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, vs := range m.objs {
		if strings.HasPrefix(k, prefix) {
			n += len(vs)
		}
	}
	return n
}

func (m *memS3) content(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.cur(key); c != nil {
		return string(c.data)
	}
	return ""
}

func (m *memS3) touchedKeys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.touched {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fakeProbe is a LockProbe test double.
type fakeProbe struct {
	fn    func(tofustate.ProbeParams) (tofustate.ProbeResult, error)
	calls []tofustate.ProbeParams
}

func (f *fakeProbe) Probe(_ context.Context, p tofustate.ProbeParams) (tofustate.ProbeResult, error) {
	f.calls = append(f.calls, p)
	return f.fn(p)
}
