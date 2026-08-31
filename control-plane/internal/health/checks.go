// Package health provides readiness checks for the control plane and the
// services it depends on.
package health

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Status describes the outcome of a single readiness check.
type Status struct {
	Name      string    `json:"name"`
	OK        bool      `json:"ok"`
	Detail    string    `json:"detail"`
	CheckedAt time.Time `json:"checked_at"`

	// Advisory marks a check whose failure is reported but does not make
	// this service unready.
	//
	// The distinction is between what this process NEEDS and what it
	// USES. Losing its database means it cannot answer anything and
	// should leave the load balancer. Losing a provider means some phases
	// stall while the console, the API and every other phase keep working
	// -- and reporting that as unready takes the whole service out over
	// somebody else's outage.
	Advisory bool `json:"advisory,omitempty"`
}

// Checker is anything that can report its own readiness.
type Checker interface {
	Name() string
	Check(ctx context.Context) Status
}

// httpReachableTimeout bounds how long an HTTPReachable check may take,
// regardless of the caller-supplied context, so a hung dependency cannot
// wedge the readiness endpoint.
const httpReachableTimeout = 5 * time.Second

// httpReachableChecker probes a URL with a plain GET to prove the target
// process is alive and accepting connections. It intentionally does not
// care about the response status code: some dependencies (for example an
// authenticated gateway) will reject an unauthenticated probe with 401 or
// 404, but that rejection still proves the socket and the process behind
// it are up, and it avoids making a real (e.g. billable) request.
type httpReachableChecker struct {
	name   string
	url    string
	client *http.Client
}

// HTTPReachable returns a Checker that reports OK=true for any HTTP
// response (including 4xx/5xx) and OK=false only when the request could
// not be completed (DNS failure, connection refused, timeout, etc). If
// client is nil, http.DefaultClient is used.
func HTTPReachable(name, rawURL string, client *http.Client) Checker {
	if client == nil {
		client = http.DefaultClient
	}
	return &httpReachableChecker{name: name, url: rawURL, client: client}
}

func (c *httpReachableChecker) Name() string {
	return c.name
}

func (c *httpReachableChecker) Check(ctx context.Context) Status {
	ctx, cancel := context.WithTimeout(ctx, httpReachableTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return Status{
			Name:      c.name,
			OK:        false,
			Detail:    err.Error(),
			CheckedAt: time.Now(),
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return Status{
			Name:      c.name,
			OK:        false,
			Detail:    err.Error(),
			CheckedAt: time.Now(),
		}
	}
	defer resp.Body.Close()

	return Status{
		Name:      c.name,
		OK:        true,
		Detail:    fmt.Sprintf("HTTP %d", resp.StatusCode),
		CheckedAt: time.Now(),
	}
}

// Aggregate runs every check concurrently and reports overall readiness.
// ready is true only if every check reported OK. statuses is returned in
// the same order as the input checks slice, regardless of the order in
// which the checks complete, so callers get deterministic output (e.g.
// for a JSON readiness endpoint).
func Aggregate(ctx context.Context, checks []Checker) (ready bool, statuses []Status) {
	statuses = make([]Status, len(checks))

	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func(i int, c Checker) {
			defer wg.Done()
			statuses[i] = c.Check(ctx)
		}(i, c)
	}
	wg.Wait()

	ready = true
	for _, s := range statuses {
		if !s.OK && !s.Advisory {
			ready = false
			break
		}
	}

	return ready, statuses
}

// Advisory wraps a Checker so its failure is reported without making the
// service unready.
//
// A dependency being down is worth showing on /readyz. It is not worth having
// Kubernetes pull this pod out of service over: an operator debugging a
// provider outage should still be able to reach the console that would tell
// them about it.
func Advisory(c Checker) Checker { return advisory{c} }

type advisory struct{ Checker }

func (a advisory) Check(ctx context.Context) Status {
	s := a.Checker.Check(ctx)
	s.Advisory = true
	return s
}
