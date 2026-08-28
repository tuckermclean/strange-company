package github_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tuckermclean/strange-company/control-plane/internal/github"
)

type checksStub struct {
	body  string
	paths []string
}

func (c *checksStub) client(t *testing.T) *github.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.paths = append(c.paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, c.body)
	}))
	t.Cleanup(srv.Close)
	cl, err := github.New(srv.URL, "ghp_token", nil)
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

func runs(entries ...string) string {
	return `{"total_count":` + itoa(len(entries)) + `,"check_runs":[` + strings.Join(entries, ",") + `]}`
}

func run(status, conclusion string) string {
	c := "null"
	if conclusion != "" {
		c = `"` + conclusion + `"`
	}
	return `{"name":"test","status":"` + status + `","conclusion":` + c + `}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

func TestGreenChecksReportExitZero(t *testing.T) {
	s := &checksStub{body: runs(run("completed", "success"), run("completed", "skipped"))}

	got, err := s.client(t).ChecksFor(context.Background(), "example/repo", "abc123")
	if err != nil {
		t.Fatalf("ChecksFor: %v", err)
	}
	if !got.Completed || got.ExitCode != 0 {
		t.Fatalf("outcome = %+v", got)
	}
	if s.paths[0] != "/repos/example/repo/commits/abc123/check-runs" {
		t.Fatalf("path = %q", s.paths[0])
	}
}

// A neutral or skipped check is not a failure -- a lint job that had nothing to
// look at must not fail a card.
func TestNeutralAndSkippedAreNotFailures(t *testing.T) {
	s := &checksStub{body: runs(run("completed", "neutral"), run("completed", "skipped"))}

	got, err := s.client(t).ChecksFor(context.Background(), "example/repo", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.ExitCode != 0 {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestAFailingCheckReportsNonZero(t *testing.T) {
	for _, conclusion := range []string{"failure", "timed_out", "action_required"} {
		s := &checksStub{body: runs(run("completed", "success"), run("completed", conclusion))}

		got, err := s.client(t).ChecksFor(context.Background(), "example/repo", "abc")
		if err != nil {
			t.Fatal(err)
		}
		if got.ExitCode == 0 {
			t.Errorf("%q read as a pass: %+v", conclusion, got)
		}
	}
}

// Checks still running are not a verdict. redgate reads an incomplete run as
// inconclusive, which is the honest answer while CI is mid-flight.
func TestChecksStillRunningAreNotComplete(t *testing.T) {
	for _, status := range []string{"queued", "in_progress", "waiting", "pending", "requested"} {
		s := &checksStub{body: runs(run("completed", "success"), run(status, ""))}

		got, err := s.client(t).ChecksFor(context.Background(), "example/repo", "abc")
		if err != nil {
			t.Fatal(err)
		}
		if got.Completed {
			t.Errorf("status %q reported a verdict: %+v", status, got)
		}
	}
}

// A cancelled check says nothing about the code. Reading it as failure would
// blame the work for someone pressing stop.
func TestACancelledCheckIsNotAVerdict(t *testing.T) {
	s := &checksStub{body: runs(run("completed", "success"), run("completed", "cancelled"))}

	got, err := s.client(t).ChecksFor(context.Background(), "example/repo", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Completed {
		t.Fatalf("a cancelled check produced a verdict: %+v", got)
	}
}

// No checks at all is the most dangerous case: it looks like nothing failed.
// Almost always it means the workflow does not trigger on agent/* branches, and
// treating it as green would promote work nothing verified.
func TestARefWithNoChecksIsRefused(t *testing.T) {
	s := &checksStub{body: `{"total_count":0,"check_runs":[]}`}

	_, err := s.client(t).ChecksFor(context.Background(), "example/repo", "abc")
	if !errors.Is(err, github.ErrNoChecks) {
		t.Fatalf("error = %v, want ErrNoChecks", err)
	}
}
