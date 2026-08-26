package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeChecker is a Checker whose behaviour is fully controlled by the
// test: it optionally sleeps for `delay` (or until the context is done,
// whichever comes first) before reporting the configured outcome.
type fakeChecker struct {
	name   string
	ok     bool
	detail string
	delay  time.Duration
}

func (f *fakeChecker) Name() string { return f.name }

func (f *fakeChecker) Check(ctx context.Context) Status {
	if f.delay > 0 {
		timer := time.NewTimer(f.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
		}
	}
	return Status{
		Name:      f.name,
		OK:        f.ok,
		Detail:    f.detail,
		CheckedAt: time.Now(),
	}
}

func TestHTTPReachableTreats401AsReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	checker := HTTPReachable("hermes", srv.URL, srv.Client())
	status := checker.Check(context.Background())

	if !status.OK {
		t.Fatalf("wanted OK=true for a 401 response, got OK=false (detail=%q)", status.Detail)
	}
	if want := "HTTP 401"; status.Detail != want {
		t.Errorf("wanted Detail %q, got %q", want, status.Detail)
	}
	if status.CheckedAt.IsZero() {
		t.Errorf("wanted a non-zero CheckedAt, got zero value")
	}
}

func TestHTTPReachableTreats404AsReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	checker := HTTPReachable("some-service", srv.URL, srv.Client())
	status := checker.Check(context.Background())

	if !status.OK {
		t.Fatalf("wanted OK=true for a 404 response, got OK=false (detail=%q)", status.Detail)
	}
	if want := "HTTP 404"; status.Detail != want {
		t.Errorf("wanted Detail %q, got %q", want, status.Detail)
	}
}

func TestHTTPReachableTreats200AsReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	checker := HTTPReachable("some-service", srv.URL, srv.Client())
	status := checker.Check(context.Background())

	if !status.OK {
		t.Fatalf("wanted OK=true for a 200 response, got OK=false (detail=%q)", status.Detail)
	}
	if want := "HTTP 200"; status.Detail != want {
		t.Errorf("wanted Detail %q, got %q", want, status.Detail)
	}
}

func TestHTTPReachableConnectionErrorReturnsNotOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := srv.URL
	srv.Close() // nothing is listening on url any more

	checker := HTTPReachable("dead-service", url, nil)
	status := checker.Check(context.Background())

	if status.OK {
		t.Fatalf("wanted OK=false for a closed server, got OK=true (detail=%q)", status.Detail)
	}
	if status.Detail == "" {
		t.Errorf("wanted Detail to contain the connection error text, got empty string")
	}
	if strings.HasPrefix(status.Detail, "HTTP ") {
		t.Errorf("wanted Detail to describe a transport error, got an HTTP-status-shaped Detail %q", status.Detail)
	}
	if status.CheckedAt.IsZero() {
		t.Errorf("wanted a non-zero CheckedAt, got zero value")
	}
}

func TestHTTPReachableWorksWithNilClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	checker := HTTPReachable("svc", srv.URL, nil)
	status := checker.Check(context.Background())

	if !status.OK {
		t.Fatalf("wanted OK=true when passing a nil *http.Client, got OK=false (detail=%q)", status.Detail)
	}
}

func TestHTTPReachableAppliesFiveSecondTimeout(t *testing.T) {
	// The handler blocks until the client gives up (i.e. until the
	// request context is cancelled), so this proves HTTPReachable
	// enforces its own deadline rather than hanging forever, while
	// still letting httptest.Server.Close() return promptly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	checker := HTTPReachable("hung-service", srv.URL, nil)

	start := time.Now()
	status := checker.Check(context.Background())
	elapsed := time.Since(start)

	if status.OK {
		t.Fatalf("wanted OK=false for a dependency that never responds, got OK=true (detail=%q)", status.Detail)
	}
	if elapsed < 4*time.Second {
		t.Errorf("wanted the check to wait close to the 5s timeout before failing, returned after only %s", elapsed)
	}
	if elapsed > 10*time.Second {
		t.Errorf("wanted the check to give up around its 5s timeout, took %s", elapsed)
	}
	if !strings.Contains(status.Detail, "deadline exceeded") {
		t.Errorf("wanted Detail to mention the timeout (deadline exceeded), got %q", status.Detail)
	}
}

func TestAggregateReadyTrueWhenAllChecksOK(t *testing.T) {
	checks := []Checker{
		&fakeChecker{name: "a", ok: true},
		&fakeChecker{name: "b", ok: true},
	}

	ready, statuses := Aggregate(context.Background(), checks)

	if !ready {
		t.Errorf("wanted ready=true when every check is OK, got ready=false")
	}
	if len(statuses) != 2 {
		t.Fatalf("wanted 2 statuses, got %d", len(statuses))
	}
	for _, s := range statuses {
		if !s.OK {
			t.Errorf("wanted status %q to be OK, got not-OK", s.Name)
		}
		if s.CheckedAt.IsZero() {
			t.Errorf("wanted status %q to have a non-zero CheckedAt, got zero value", s.Name)
		}
	}
}

func TestAggregateReadyFalseWhenAnyCheckFails(t *testing.T) {
	checks := []Checker{
		&fakeChecker{name: "a", ok: true},
		&fakeChecker{name: "b", ok: false, detail: "boom"},
		&fakeChecker{name: "c", ok: true},
	}

	ready, statuses := Aggregate(context.Background(), checks)

	if ready {
		t.Errorf("wanted ready=false when one check fails, got ready=true")
	}
	if len(statuses) != 3 {
		t.Fatalf("wanted 3 statuses, got %d", len(statuses))
	}
}

func TestAggregateEmptySliceReturnsReadyTrue(t *testing.T) {
	ready, statuses := Aggregate(context.Background(), nil)

	if !ready {
		t.Errorf("wanted ready=true for a nil checks slice, got ready=false")
	}
	if len(statuses) != 0 {
		t.Errorf("wanted an empty statuses slice, got %d entries", len(statuses))
	}

	ready, statuses = Aggregate(context.Background(), []Checker{})

	if !ready {
		t.Errorf("wanted ready=true for an empty checks slice, got ready=false")
	}
	if len(statuses) != 0 {
		t.Errorf("wanted an empty statuses slice, got %d entries", len(statuses))
	}
}

func TestAggregatePreservesInputOrder(t *testing.T) {
	// Deliberately give the checks different delays so they complete out
	// of order; Aggregate must still place each result at the index of
	// its corresponding input checker.
	checks := []Checker{
		&fakeChecker{name: "slow", ok: true, delay: 60 * time.Millisecond},
		&fakeChecker{name: "fast", ok: true, delay: 5 * time.Millisecond},
		&fakeChecker{name: "medium", ok: true, delay: 30 * time.Millisecond},
	}

	_, statuses := Aggregate(context.Background(), checks)

	want := []string{"slow", "fast", "medium"}
	if len(statuses) != len(want) {
		t.Fatalf("wanted %d statuses, got %d", len(want), len(statuses))
	}
	for i, name := range want {
		if statuses[i].Name != name {
			t.Errorf("wanted statuses[%d].Name = %q, got %q", i, name, statuses[i].Name)
		}
	}
}

func TestAggregateRunsChecksConcurrently(t *testing.T) {
	const (
		n     = 5
		delay = 100 * time.Millisecond
	)

	checks := make([]Checker, n)
	for i := range checks {
		checks[i] = &fakeChecker{name: "c", ok: true, delay: delay}
	}

	start := time.Now()
	Aggregate(context.Background(), checks)
	elapsed := time.Since(start)

	serial := time.Duration(n) * delay
	if elapsed >= serial {
		t.Errorf("wanted concurrent execution to finish well under the serial time of %s, took %s", serial, elapsed)
	}
}
