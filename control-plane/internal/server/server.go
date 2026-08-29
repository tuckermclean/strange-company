// Package server exposes the control plane over HTTP.
//
// Handlers here are deliberately thin: they translate HTTP to a call and a
// status code. Workflow rules live in internal/card and internal/store, never
// in a handler.
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/config"
	"github.com/tuckermclean/strange-company/control-plane/internal/health"
)

// Server wires configuration and dependency checks to routes.
type Server struct {
	cfg     *config.Config
	checks  []health.Checker
	version string
	log     *slog.Logger
	cards   *cardsDeps

	// mcp is the Company MCP server (spec §9), mounted under /mcp when set.
	mcp http.Handler
}

// SetMCP mounts the Company MCP server under /mcp.
func (s *Server) SetMCP(h http.Handler) *Server {
	s.mcp = h
	return s
}

// New builds a Server. checks may be empty, which makes readiness trivially true.
func New(cfg *config.Config, checks []health.Checker, version string) *Server {
	return &Server{
		cfg:     cfg,
		checks:  checks,
		version: version,
		log:     slog.Default(),
	}
}

// Handler returns the routed handler.
//
// Both spellings of each probe are served: the Helm chart probes /healthz and
// /readyz, while the specification names /health and /ready. They are aliases,
// not variants — a divergence between them would be a silent outage.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.HandleFunc("GET /config", s.handleConfig)
	mux.HandleFunc("GET /version", s.handleVersion)

	mux.HandleFunc("GET /cards", s.handleListCards)
	mux.HandleFunc("GET /cards/{id}", s.handleGetCard)
	mux.HandleFunc("POST /cards/{id}/claim", s.handleClaim)
	mux.HandleFunc("POST /cards/{id}/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("POST /cards/{id}/release", s.handleRelease)
	mux.HandleFunc("POST /cards/{id}/transition", s.handleTransition)
	mux.HandleFunc("POST /cards/{id}/approve-spec", s.handleApproveSpec)
	mux.HandleFunc("GET /cards/{id}/artifacts", s.handleListArtifacts)
	mux.HandleFunc("GET /cards/{id}/attempts", s.handleListAttempts)
	mux.HandleFunc("GET /cards/{id}/cost", s.handleCardCost)

	// The Company MCP server (spec §9). Mounted here rather than on its own
	// listener so it shares this server's lifecycle -- it had a package and
	// tests and nothing serving it, which meant Hermes could not reach any
	// of it.
	if s.mcp != nil {
		mux.Handle("/mcp/", http.StripPrefix("/mcp", s.mcp))
	}

	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Default().Error("failed to encode response", "error", err)
	}
}

// requestTimeout bounds readiness work so a hung dependency cannot hold the
// probe open past the kubelet's own timeout.
const requestTimeout = 10 * time.Second
