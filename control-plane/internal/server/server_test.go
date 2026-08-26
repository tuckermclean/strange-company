package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/config"
	"github.com/tuckermclean/strange-company/control-plane/internal/health"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	env := map[string]string{
		"DATABASE_HOST":      "pg",
		"DATABASE_PORT":      "5432",
		"DATABASE_NAME":      "strange-company",
		"DATABASE_USER":      "strange-company",
		"DATABASE_PASSWORD":  "hunter2",
		"VIKUNJA_URL":        "http://strange-company-vikunja:3456",
		"HERMES_GATEWAY_URL": "http://strange-company-hermes:8642",
	}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("test configuration must be valid: %v", err)
	}
	return cfg
}

type stubCheck struct {
	name string
	ok   bool
}

func (s stubCheck) Name() string { return s.name }
func (s stubCheck) Check(context.Context) health.Status {
	return health.Status{Name: s.name, OK: s.ok, Detail: "stub", CheckedAt: time.Now()}
}

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// Liveness must not depend on dependencies. If it did, a Vikunja restart would
// make Kubernetes kill a perfectly healthy control plane.
func TestHealthzIsAliveEvenWhenEveryDependencyIsDown(t *testing.T) {
	s := New(testConfig(t), []health.Checker{stubCheck{"postgres", false}}, "test")

	rec := get(t, s, "/healthz")

	if rec.Code != http.StatusOK {
		t.Fatalf("liveness must not depend on dependencies: want 200, got %d", rec.Code)
	}
}

func TestReadyzReports503WhenADependencyIsDown(t *testing.T) {
	s := New(testConfig(t), []health.Checker{
		stubCheck{"postgres", true},
		stubCheck{"vikunja", false},
	}, "test")

	rec := get(t, s, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when a dependency is down, got %d", rec.Code)
	}
}

func TestReadyzReports200WhenEveryDependencyIsUp(t *testing.T) {
	s := New(testConfig(t), []health.Checker{
		stubCheck{"postgres", true},
		stubCheck{"hermes", true},
	}, "test")

	rec := get(t, s, "/readyz")

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 when all dependencies are up, got %d", rec.Code)
	}
}

// The response body has to name which dependency failed, or an operator has to
// go read logs to learn what a 503 meant.
func TestReadyzBodyNamesTheFailingDependency(t *testing.T) {
	s := New(testConfig(t), []health.Checker{stubCheck{"vikunja", false}}, "test")

	rec := get(t, s, "/readyz")

	var body struct {
		Ready  bool `json:"ready"`
		Checks []struct {
			Name string `json:"name"`
			OK   bool   `json:"ok"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("readiness body must be JSON: %v", err)
	}
	if body.Ready {
		t.Error("want ready=false")
	}
	if len(body.Checks) != 1 || body.Checks[0].Name != "vikunja" || body.Checks[0].OK {
		t.Fatalf("body must report the failing dependency by name, got %+v", body.Checks)
	}
}

// The chart probes /healthz and /readyz; the specification names /health and
// /ready. They must be aliases, because a divergence is a silent outage.
func TestBothProbeSpellingsAreServed(t *testing.T) {
	s := New(testConfig(t), nil, "test")

	for _, path := range []string{"/healthz", "/health", "/readyz", "/ready"} {
		if code := get(t, s, path).Code; code != http.StatusOK {
			t.Errorf("%s: want 200, got %d", path, code)
		}
	}
}

func TestReadinessWithNoChecksIsReady(t *testing.T) {
	s := New(testConfig(t), nil, "test")

	if code := get(t, s, "/readyz").Code; code != http.StatusOK {
		t.Fatalf("no configured checks means nothing can be down: want 200, got %d", code)
	}
}

func TestConfigEndpointNeverLeaksTheDatabasePassword(t *testing.T) {
	s := New(testConfig(t), nil, "test")

	rec := get(t, s, "/config")

	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatal("/config leaked the database password")
	}
	if !strings.Contains(rec.Body.String(), "strange-company-vikunja") {
		t.Error("/config should still show resolved non-secret endpoints")
	}
}

func TestVersionEndpointReportsTheBuildVersion(t *testing.T) {
	s := New(testConfig(t), nil, "v1.2.3")

	rec := get(t, s, "/version")

	if !strings.Contains(rec.Body.String(), "v1.2.3") {
		t.Fatalf("want the build version in the body, got %q", rec.Body.String())
	}
}

func TestUnknownPathIs404(t *testing.T) {
	s := New(testConfig(t), nil, "test")

	if code := get(t, s, "/nope").Code; code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", code)
	}
}
