package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

// defaultLease is used when a claim or heartbeat request omits
// lease_seconds, matching the 10-minute lease in spec section 6.
const defaultLease = 600 * time.Second

// CardStore is everything the server needs from persistence to serve the
// /cards routes. It is defined here, not imported from internal/store, so
// that this package never depends on the concrete storage engine (see the
// package doc in server.go). *store.Store satisfies this interface.
type CardStore interface {
	ListCards(ctx context.Context) ([]*card.Card, error)
	GetCard(ctx context.Context, id uuid.UUID) (*card.Card, error)
	ClaimReady(ctx context.Context, workerID string, lease time.Duration) (*card.Card, error)
	Heartbeat(ctx context.Context, cardID uuid.UUID, workerID string, lease time.Duration) error
	Release(ctx context.Context, cardID uuid.UUID, workerID, reason string) error
	Transition(ctx context.Context, cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error

	// ApproveSpec records that a human approved the card's specification as
	// it currently reads (spec §10.2). Promotion to Ready is the control
	// plane's consequence of this, not a second human action.
	ApproveSpec(ctx context.Context, cardID uuid.UUID, approvedBy string) error
}

// ErrorClassifier lets the server map a store error to an HTTP status
// without importing the store package (and therefore without importing
// pgx). main wires up an implementation backed by errors.Is against the
// store's sentinel errors.
type ErrorClassifier interface {
	IsNoWork(error) bool
	IsNotClaimant(error) bool
	IsNotFound(error) bool
}

// cardsDeps bundles the two collaborators the /cards routes need. Keeping
// them behind one struct field on Server means adding a card-related
// dependency later doesn't require another field-and-setter pair.
type cardsDeps struct {
	store CardStore
	errs  ErrorClassifier
}

// SetCards wires a card store and error classifier into the server,
// enabling the /cards routes. It returns the Server so it can be chained
// onto New, e.g. server.New(cfg, checks, version).SetCards(st, ec).
func (s *Server) SetCards(cs CardStore, ec ErrorClassifier) *Server {
	s.cards = &cardsDeps{store: cs, errs: ec}
	return s
}

// errorBody is the JSON shape of every error response from the /cards
// routes: a short machine-matchable code plus a human-readable detail.
type errorBody struct {
	Error  string `json:"error"`
	Detail string `json:"detail"`
}

func writeError(w http.ResponseWriter, status int, errCode, detail string) {
	writeJSON(w, status, errorBody{Error: errCode, Detail: detail})
}

// cardsOrError fetches the configured store/classifier pair, or writes a 500
// and reports false if the server was never given one. This guards against a
// programming error (routes registered without SetCards) rather than
// anything a caller can trigger.
func (s *Server) cardsOrError(w http.ResponseWriter) (*cardsDeps, bool) {
	if s.cards == nil {
		writeError(w, http.StatusInternalServerError, "not_configured", "card store is not configured")
		return nil, false
	}
	return s.cards, true
}

// parseCardID extracts and validates the {id} path value. An unparseable id
// is a client error, not a lookup miss, so it is reported as 400 rather than
// 404.
func parseCardID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	raw := r.PathValue("id")
	id, err := uuid.Parse(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", fmt.Sprintf("%q is not a valid card id", raw))
		return uuid.Nil, false
	}
	return id, true
}

// decodeBody reads and decodes a required JSON request body. A missing or
// malformed body is always a 400: none of these routes have optional bodies.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be valid JSON: "+err.Error())
		return false
	}
	return true
}

// writeStoreError classifies an error returned by the store and writes the
// matching HTTP response. It is used for failures that are not already
// special-cased by the caller (claim's "no work" case is handled before this
// is reached, since it is a 204 with no error body at all).
func (s *Server) writeStoreError(w http.ResponseWriter, cd *cardsDeps, err error) {
	switch {
	case cd.errs != nil && cd.errs.IsNotFound(err):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case cd.errs != nil && cd.errs.IsNotClaimant(err):
		writeError(w, http.StatusForbidden, "forbidden", err.Error())
	case cd.errs != nil && cd.errs.IsNoWork(err):
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

// handleListCards serves GET /cards.
func (s *Server) handleListCards(w http.ResponseWriter, r *http.Request) {
	cd, ok := s.cardsOrError(w)
	if !ok {
		return
	}

	cards, err := cd.store.ListCards(r.Context())
	if err != nil {
		s.writeStoreError(w, cd, err)
		return
	}
	writeJSON(w, http.StatusOK, cards)
}

// handleGetCard serves GET /cards/{id}.
func (s *Server) handleGetCard(w http.ResponseWriter, r *http.Request) {
	cd, ok := s.cardsOrError(w)
	if !ok {
		return
	}
	id, ok := parseCardID(w, r)
	if !ok {
		return
	}

	c, err := cd.store.GetCard(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, cd, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

type claimRequest struct {
	WorkerID     string `json:"worker_id"`
	LeaseSeconds int    `json:"lease_seconds"`
}

// handleClaim serves POST /cards/{id}/claim.
//
// Claiming is atomic across the whole Ready queue (spec section 6), not
// scoped to a single card, so the CardStore.ClaimReady signature takes no
// card id. The {id} path segment is still validated for consistency with
// the other /cards/{id}/... routes and because a malformed id is a client
// error regardless of what the route does with it, but it is otherwise
// unused: the caller learns which card it got from the response body.
func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	cd, ok := s.cardsOrError(w)
	if !ok {
		return
	}
	if _, ok := parseCardID(w, r); !ok {
		return
	}

	var req claimRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.WorkerID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "worker_id is required")
		return
	}

	lease := defaultLease
	if req.LeaseSeconds > 0 {
		lease = time.Duration(req.LeaseSeconds) * time.Second
	}

	c, err := cd.store.ClaimReady(r.Context(), req.WorkerID, lease)
	if err != nil {
		if cd.errs != nil && cd.errs.IsNoWork(err) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.writeStoreError(w, cd, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

type heartbeatRequest struct {
	WorkerID     string `json:"worker_id"`
	LeaseSeconds int    `json:"lease_seconds"`
}

type ackResponse struct {
	Status string `json:"status"`
}

// handleHeartbeat serves POST /cards/{id}/heartbeat.
func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	cd, ok := s.cardsOrError(w)
	if !ok {
		return
	}
	id, ok := parseCardID(w, r)
	if !ok {
		return
	}

	var req heartbeatRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.WorkerID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "worker_id is required")
		return
	}

	lease := defaultLease
	if req.LeaseSeconds > 0 {
		lease = time.Duration(req.LeaseSeconds) * time.Second
	}

	if err := cd.store.Heartbeat(r.Context(), id, req.WorkerID, lease); err != nil {
		s.writeStoreError(w, cd, err)
		return
	}
	// Heartbeat does not hand back the card, so there is nothing to echo
	// beyond an acknowledgement.
	writeJSON(w, http.StatusOK, ackResponse{Status: "ok"})
}

type releaseRequest struct {
	WorkerID string `json:"worker_id"`
	Reason   string `json:"reason"`
}

// handleRelease serves POST /cards/{id}/release.
func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	cd, ok := s.cardsOrError(w)
	if !ok {
		return
	}
	id, ok := parseCardID(w, r)
	if !ok {
		return
	}

	var req releaseRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.WorkerID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "worker_id is required")
		return
	}

	if err := cd.store.Release(r.Context(), id, req.WorkerID, req.Reason); err != nil {
		s.writeStoreError(w, cd, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type transitionRequest struct {
	To        string `json:"to"`
	ActorType string `json:"actor_type"`
	ActorID   string `json:"actor_id"`
	Reason    string `json:"reason"`
}

// handleTransition serves POST /cards/{id}/transition.
//
// A rejection from the state machine is reported as 409 with the rejecting
// rule's own message in the body (card.CanTransition's errors already say
// exactly which rule fired and why), so a caller can learn why a move was
// refused without re-deriving the state table itself.
func (s *Server) handleTransition(w http.ResponseWriter, r *http.Request) {
	cd, ok := s.cardsOrError(w)
	if !ok {
		return
	}
	id, ok := parseCardID(w, r)
	if !ok {
		return
	}

	var req transitionRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.ActorID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "actor_id is required")
		return
	}
	if req.To == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "to is required")
		return
	}

	err := cd.store.Transition(r.Context(), id, card.State(req.To), card.ActorType(req.ActorType), req.ActorID, req.Reason)
	if err != nil {
		if errors.Is(err, card.ErrIllegalTransition) {
			writeError(w, http.StatusConflict, "illegal_transition", err.Error())
			return
		}
		s.writeStoreError(w, cd, err)
		return
	}

	// Transition reports success/failure only; fetch the card so the
	// response body reflects its new state, consistent with every other
	// success response on these routes.
	c, err := cd.store.GetCard(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, cd, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// approveSpecRequest is the body of POST /cards/{id}/approve-spec.
type approveSpecRequest struct {
	ApprovedBy string `json:"approved_by"`
}

// handleApproveSpec serves POST /cards/{id}/approve-spec.
//
// Spec §10.2: "Human approves the completed spec. Only then may the control
// plane promote the card to Ready." Approval is the human input; promotion is
// the control plane's consequence of it, and happens on the next reconcile
// pass once the deterministic gate also passes. There is deliberately no
// endpoint to promote directly -- that would be a way around the gate.
func (s *Server) handleApproveSpec(w http.ResponseWriter, r *http.Request) {
	cd, ok := s.cardsOrError(w)
	if !ok {
		return
	}
	id, ok := parseCardID(w, r)
	if !ok {
		return
	}

	var req approveSpecRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ApprovedBy) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request",
			"approved_by is required: an approval that names nobody is not an approval")
		return
	}

	if err := cd.store.ApproveSpec(r.Context(), id, strings.TrimSpace(req.ApprovedBy)); err != nil {
		s.writeStoreError(w, cd, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
