package tofustate_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"strange.company/sc/internal/bootstrap/store"
	"strange.company/sc/internal/clock"
	"strange.company/sc/internal/s3client"
	"strange.company/sc/internal/tofustate"
)

// --- N5: Restore over a copy that S3 answered with "200 OK" and an <Error> body must not report success.

func TestRestore_CopyError200IsNotSuccess_06AC8(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		switch {
		case r.Method == http.MethodPut && r.Header.Get("x-amz-copy-source") != "":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>InternalError</Code><Message>try again</Message></Error>`))
		case r.Method == http.MethodGet && r.URL.Query().Get("versionId") != "":
			w.Header().Set("x-amz-version-id", r.URL.Query().Get("versionId"))
			_, _ = w.Write([]byte("older state"))
		case r.Method == http.MethodGet:
			w.Header().Set("x-amz-version-id", "v-current")
			_, _ = w.Write([]byte("garbage current state"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, err := s3client.New(s3client.Config{Endpoint: srv.URL, Region: "us-east-1", AccessKey: "AKIA" + "FIXTUREKEY0001", SecretKey: "fixture-secret-" + strings.Repeat("k", 24),
		PathStyle: true, Clock: clock.NewFake(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))})
	if err != nil {
		t.Fatal(err)
	}
	res, err := tofustate.Restore(context.Background(), c.Bucket("state-bkt"), "lab", "compute", "v-old", nil)
	if err == nil {
		t.Fatalf("Restore reported success over a failed copy: %+v", res)
	}
	if res.NewVersionID != "" || res.NoOp {
		t.Errorf("RestoreResult after a failed copy = %+v, want the zero value", res)
	}
}

// --- N2: only "the store ignored or refused the conditional header" maps to the unsupported codes.

// faultStore injects an error into conditional creates; n counts the calls per key.
type faultStore struct {
	*memS3
	mu  sync.Mutex
	n   map[string]int
	err func(key string, call int) error
}

func newFaultStore(m *memS3, f func(key string, call int) error) *faultStore {
	return &faultStore{memS3: m, n: map[string]int{}, err: f}
}

func (f *faultStore) PutIfAbsent(ctx context.Context, key string, data []byte) (string, error) {
	f.mu.Lock()
	f.n[key]++
	call := f.n[key]
	f.mu.Unlock()
	if err := f.err(key, call); err != nil {
		return "", err
	}
	return f.memS3.PutIfAbsent(ctx, key, data)
}

type named[T any] struct {
	name string
	v    T
}

func TestConformance_AccessAndTransportFailuresAreNotUnsupported_06AC1(t *testing.T) {
	faults := []named[error]{
		{"403 AccessDenied", &s3client.Error{Status: 403, Code: "AccessDenied", Message: "denied"}},
		{"503 SlowDown", &s3client.Error{Status: 503, Code: "SlowDown", Kind: store.Unavailable}},
		{"transport", errors.New("dial tcp 192.0.2.1:9000: connect: connection refused")},
	}
	steps := []named[func(key string, call int) bool]{
		{"C1 first create", func(k string, c int) bool { return strings.HasSuffix(k, "/k1") && c == 1 }},
		{"C2 second create", func(k string, c int) bool { return strings.HasSuffix(k, "/k1") && c == 2 }},
		{"C3 concurrent creates", func(k string, c int) bool { return strings.HasSuffix(k, "/k2") }},
	}
	for _, fl := range faults {
		for _, st := range steps {
			fault, step := fl.v, st.v
			t.Run(st.name+"/"+fl.name, func(t *testing.T) {
				m := newMemS3()
				fs := newFaultStore(m, func(k string, c int) error { return when(step(k, c), fault) })
				rep, err := tofustate.Conformance(context.Background(), tofustate.ConformanceParams{Store: fs, Cluster: cluster, RunID: runID, Clock: m.clk})
				if err == nil && len(rep.Fatal) == 0 {
					t.Errorf("the failure was swallowed: no error and fatal = %v", rep.Fatal)
				}
				for _, code := range []string{"S3_CONDITIONAL_PUT_UNSUPPORTED", "PF_S3_LOCK_UNSUPPORTED"} {
					if hasFatal(rep, code) {
						t.Errorf("fatal %v reports %s although the store neither ignored nor refused the header", rep.Fatal, code)
					}
					for _, r := range rep.Tests {
						if r.Code == code {
							t.Errorf("row %s carries %s for %v", r.ID, code, fault)
						}
					}
				}
			})
		}
	}
}

func when(cond bool, e error) error {
	if cond {
		return e
	}
	return nil
}

// --- N1: cleanup runs on its own (admin) store; the store under test (tofu-state) never deletes versions.

// noDeleteStore is the tofu-state credential: it can write and list but holds no DeleteObjectVersion.
type noDeleteStore struct {
	*memS3
	mu      sync.Mutex
	deletes []string
}

func (s *noDeleteStore) DeleteVersion(_ context.Context, key, _ string) error {
	s.mu.Lock()
	s.deletes = append(s.deletes, key)
	s.mu.Unlock()
	return &s3client.Error{Status: 403, Code: "AccessDenied", Message: "no DeleteObjectVersion"}
}

func (s *noDeleteStore) Delete(_ context.Context, key, _ string) error {
	s.mu.Lock()
	s.deletes = append(s.deletes, key)
	s.mu.Unlock()
	return &s3client.Error{Status: 403, Code: "AccessDenied", Message: "no DeleteObject"}
}

// adminStore is the cleanup credential over the same bucket; failKey and failList inject cleanup failures.
type adminStore struct {
	*memS3
	mu       sync.Mutex
	deleted  int
	failKey  string
	failList bool
}

func (a *adminStore) ListVersions(ctx context.Context, prefix string) ([]s3client.ObjectInfo, error) {
	if a.failList {
		return nil, &s3client.Error{Status: 403, Code: "AccessDenied", Message: "list denied"}
	}
	return a.memS3.ListVersions(ctx, prefix)
}

func (a *adminStore) DeleteVersion(ctx context.Context, key, id string) error {
	a.mu.Lock()
	a.deleted++
	a.mu.Unlock()
	if a.failKey != "" && strings.HasSuffix(key, a.failKey) {
		return &s3client.Error{Status: 500, Code: "InternalError", Kind: store.Unavailable}
	}
	return a.memS3.DeleteVersion(ctx, key, id)
}

func TestConformance_CleanupUsesTheSeparateStore_06AC1(t *testing.T) {
	m := newMemS3()
	st := &noDeleteStore{memS3: m}
	adm := &adminStore{memS3: m}
	rep, err := tofustate.Conformance(context.Background(), tofustate.ConformanceParams{Store: st, Cleanup: adm, Cluster: cluster, RunID: runID, Clock: m.clk, Probe: goodProbe(m)})
	if err != nil {
		t.Fatalf("Conformance: %v", err)
	}
	if len(st.deletes) != 0 {
		t.Errorf("the store under test was asked to delete %v; cleanup must use the Cleanup store only", st.deletes)
	}
	if adm.deleted == 0 {
		t.Error("the Cleanup store deleted nothing")
	}
	if n := m.totalVersions(prefix); n != 0 {
		t.Errorf("%d object versions are left under %s", n, prefix)
	}
	if len(rep.Fatal) != 0 || len(rep.CleanupFailures) != 0 {
		t.Errorf("fatal = %v, cleanup failures = %v; want none", rep.Fatal, rep.CleanupFailures)
	}
}

func TestConformance_CleanupFailureIsRecorded_06AC1(t *testing.T) {
	cases := map[string]*adminStore{
		"delete refused": {failKey: "/k3"},
		"list refused":   {failList: true},
	}
	for name, adm := range cases {
		t.Run(name, func(t *testing.T) {
			m := newMemS3()
			adm.memS3 = m
			rep, err := tofustate.Conformance(context.Background(), tofustate.ConformanceParams{Store: m, Cleanup: adm, Cluster: cluster, RunID: runID, Clock: m.clk, Probe: goodProbe(m)})
			if err != nil {
				t.Fatalf("Conformance: %v", err)
			}
			if len(rep.CleanupFailures) == 0 {
				t.Fatalf("a cleanup failure was ignored; report: %+v", rep)
			}
			if name == "delete refused" && !strings.Contains(strings.Join(rep.CleanupFailures, "\n"), prefix+"k3") {
				t.Errorf("cleanup failures %v do not name the object %sk3", rep.CleanupFailures, prefix)
			}
			if len(rep.Fatal) != 0 {
				t.Errorf("a cleanup failure changed fatal to %v; the conformance verdict is unchanged", rep.Fatal)
			}
			b, err := rep.Marshal()
			if err != nil || !strings.Contains(string(b), `"cleanup_failures"`) {
				t.Errorf("the JSON report lacks cleanup_failures: %s (%v)", b, err)
			}
		})
	}
}
