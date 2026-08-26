package server

import (
	"context"
	"net/http"

	"github.com/tuckermclean/strange-company/control-plane/internal/health"
)

type healthResponse struct {
	Status string `json:"status"`
}

type readyResponse struct {
	Ready  bool            `json:"ready"`
	Checks []health.Status `json:"checks"`
}

type versionResponse struct {
	Version string `json:"version"`
}

// handleHealth reports liveness only.
//
// It deliberately ignores dependency state. Liveness answers "should this
// process be restarted?", and restarting the control plane does not fix a
// PostgreSQL outage -- it just adds a crash loop to an existing incident.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// handleReady reports whether this instance can currently do useful work.
//
// Unlike the CI fixture it replaces, a 200 here means the control plane
// actually reached PostgreSQL, Vikunja and the Hermes gateway.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	ready, statuses := health.Aggregate(ctx, s.checks)

	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, readyResponse{Ready: ready, Checks: statuses})
}

// handleConfig renders the resolved contract with secrets redacted, so an
// operator can see which endpoints were resolved without it becoming a way to
// read a credential.
func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Redacted())
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, versionResponse{Version: s.version})
}
