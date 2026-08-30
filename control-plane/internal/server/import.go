package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tuckermclean/strange-company/control-plane/internal/onboard"
)

// Importer prepares a repository for the engine (day-0).
type Importer interface {
	Import(ctx context.Context, repository string) (*onboard.Result, error)
}

// SetImporter wires day-0 setup. Left unset when no credential carrying
// GitHub's `workflow` scope is configured, in which case the endpoint reports
// that plainly rather than failing in a way that looks like a bug.
func (s *Server) SetImporter(i Importer) *Server {
	s.importer = i
	return s
}

// handleImportRepo serves POST /repos/import.
//
// Day-0 cannot be done by an agent: the runner refuses to commit anything under
// .github/workflows, because an agent that can rewrite CI can weaken the checks
// that gate it. So this runs from outside the loop, with a credential the
// agents never see.
func (s *Server) handleImportRepo(w http.ResponseWriter, r *http.Request) {
	if s.importer == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error": "repository import is not configured",
			"detail": "day-0 setup writes .github/workflows, which needs a GitHub credential " +
				"carrying the `workflow` scope. It is deliberately separate from the agents' " +
				"contents-only credential and is not configured on this install.",
		})
		return
	}

	var req struct {
		Repository string `json:"repository"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxImportBodyBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "expected a JSON body with a repository"})
		return
	}
	req.Repository = strings.TrimSpace(req.Repository)
	if !strings.Contains(req.Repository, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": `repository must be "owner/name"`})
		return
	}

	res, err := s.importer.Import(r.Context(), req.Repository)
	if err != nil {
		// Not a 500: the common failures here are a missing permission or a
		// repository the credential cannot see, both of which are the
		// operator's to fix and neither of which is a fault in this service.
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// maxImportBodyBytes bounds the request: it carries one repository name.
const maxImportBodyBytes = 4 << 10
