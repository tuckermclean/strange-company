// Package mcp implements the Company MCP server described in spec section 9:
// a deliberately small, named tool surface, and nothing else.
//
// This is the system's core security boundary. Hermes is never handed a
// generic Kubernetes tool, a shell, an exec, or arbitrary HTTP access. It can
// only ask for a named tool such as coding.implement(card_id); the control
// plane -- not Hermes -- decides what pod, image, credentials, model and
// sandbox that implies. The registry built in tools.go is the enforcement
// point: ToolNames reports exactly what is reachable, and
// TestTheToolSurfaceIsExactlyTheSpecifiedSet in server_test.go fails loudly
// the moment anyone registers something resembling kubernetes.*, exec,
// shell, or generic HTTP access.
//
// Transport is a small JSON-RPC-style protocol served over a single HTTP
// endpoint:
//
//	POST / {"method":"tools/list"}
//	POST / {"method":"tools/call","params":{"name":"cards.claim","arguments":{...}}}
//
// An unknown method, an unknown tool, invalid arguments, or even a panicking
// handler always produce a JSON-RPC error in the response body over HTTP 200
// -- never an HTTP 500, and never a crash -- so a caller can parse every
// response the same way.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

// CardService is the narrow slice of persistence operations the cards.* and
// artifacts.* tools need. Its methods mirror *store.Store's signatures
// exactly (see internal/store/cards.go), so *store.Store satisfies it
// directly -- but this package depends only on this interface, never on
// *store.Store, the same dependency style internal/server/cards.go uses to
// keep a transport layer free of persistence-engine details (there, so the
// server package need not import pgx; here, for the same reason).
//
// ClaimReady's "the Ready queue is empty" case is expected to be reported as
// an error satisfying errors.Is(err, ErrNoWork). Wiring *store.Store in as a
// CardService therefore requires a thin adapter that translates
// store.ErrNoWork into ErrNoWork, exactly as main.go's storeErrorClassifier
// already translates store's sentinel errors for the HTTP server. That
// adapter is wiring, not policy, and belongs with whoever mounts this server
// (out of scope for this package).
type CardService interface {
	ListCards(ctx context.Context) ([]*card.Card, error)
	GetCard(ctx context.Context, id uuid.UUID) (*card.Card, error)
	ClaimReady(ctx context.Context, workerID string, lease time.Duration) (*card.Card, error)
	Heartbeat(ctx context.Context, cardID uuid.UUID, workerID string, lease time.Duration) error
	Release(ctx context.Context, cardID uuid.UUID, workerID, reason string) error
	Transition(ctx context.Context, cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error
}

// ErrNoWork is the error CardService.ClaimReady must satisfy errors.Is
// against when the Ready queue is empty. cards.claim treats it as "no work
// available" -- a normal, expected outcome -- rather than a call failure, so
// it never becomes a JSON-RPC error.
var ErrNoWork = errors.New("mcp: no claimable card")

// --- in-memory records for cards.comment and artifacts.* -------------------
//
// Spec section 20 describes artifacts (and, implicitly, comments) as
// eventually durable records. For M2 they live only in an in-memory map
// behind the small recordStore interface below. This is deliberate, not an
// oversight: inventing a database table for them now, before the coding
// pipeline that would actually populate most of their metadata (attempt_id,
// model, commit_sha) exists, would be speculative. A later milestone can
// replace memoryRecords with a Postgres-backed implementation of the same
// interface without touching a single tool handler in tools.go.

// Comment is one cards.comment entry.
type Comment struct {
	ID        uuid.UUID `json:"id"`
	CardID    uuid.UUID `json:"card_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// Artifact is one artifacts.attach entry: the subset of spec section 20's
// metadata that makes sense before the coding pipeline (attempt_id, model,
// commit_sha) exists to populate the rest.
type Artifact struct {
	ID          uuid.UUID `json:"id"`
	CardID      uuid.UUID `json:"card_id"`
	Type        string    `json:"type"`
	ContentType string    `json:"content_type,omitempty"`
	Actor       string    `json:"actor,omitempty"`
	StorageURI  string    `json:"storage_uri,omitempty"`
	SHA256      string    `json:"sha256,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// recordStore is the small interface behind which cards.comment and
// artifacts.* store their data in M2.
type recordStore interface {
	addComment(c Comment)
	listComments(cardID uuid.UUID) []Comment
	addArtifact(a Artifact)
	listArtifacts(cardID uuid.UUID) []Artifact
}

// memoryRecords is the M2 recordStore: a mutex-guarded map. It is never
// exposed outside this package; only the recordStore interface is.
type memoryRecords struct {
	mu        sync.Mutex
	comments  map[uuid.UUID][]Comment
	artifacts map[uuid.UUID][]Artifact
}

func newMemoryRecords() *memoryRecords {
	return &memoryRecords{
		comments:  make(map[uuid.UUID][]Comment),
		artifacts: make(map[uuid.UUID][]Artifact),
	}
}

func (m *memoryRecords) addComment(c Comment) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.comments[c.CardID] = append(m.comments[c.CardID], c)
}

func (m *memoryRecords) listComments(cardID uuid.UUID) []Comment {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Comment, len(m.comments[cardID]))
	copy(out, m.comments[cardID])
	return out
}

func (m *memoryRecords) addArtifact(a Artifact) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.artifacts[a.CardID] = append(m.artifacts[a.CardID], a)
}

func (m *memoryRecords) listArtifacts(cardID uuid.UUID) []Artifact {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Artifact, len(m.artifacts[cardID]))
	copy(out, m.artifacts[cardID])
	return out
}

// --- server ------------------------------------------------------------

// Server is the Company MCP server. It holds only what tools.go's handlers
// need: a CardService and the M2 in-memory records store.
type Server struct {
	cards    CardService
	records  recordStore
	evidence Evidence
}

// Evidence records durable notes against a card (spec §21). Optional: without
// one, cards.comment falls back to the in-memory map M2 shipped -- which is
// what a comment nobody can read after a restart is worth.
type Evidence interface {
	AttachEvidence(ctx context.Context, cardID uuid.UUID, ev store.CardEvidence) error
}

// SetEvidence gives the server somewhere durable to record comments.
func (s *Server) SetEvidence(e Evidence) *Server {
	s.evidence = e
	return s
}

// NewServer builds a Server backed by cards. cards is typically *store.Store
// (via a thin adapter, see CardService's doc comment) in production and a
// hand-written fake in tests.
func NewServer(cards CardService) *Server {
	return &Server{
		cards:   cards,
		records: newMemoryRecords(),
	}
}

// Handler returns the routed handler: a single JSON-RPC-style endpoint.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /", s.handleRPC)
	return mux
}

// --- JSON-RPC-style envelope -------------------------------------------

type rpcRequest struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Result any             `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

const (
	methodToolsList = "tools/list"
	methodToolsCall = "tools/call"
)

// JSON-RPC-ish error codes. The first four follow the JSON-RPC 2.0
// convention; codeNotImplemented is an application-specific extension for
// tools that are declared but deliberately unimplemented in M2.
const (
	codeParseError     = -32700
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
	codeNotImplemented = -32040
)

// handleRPC is the single entry point for both tools/list and tools/call.
// It never returns a non-200 HTTP status and never lets a panic escape: any
// failure, at any stage, becomes a JSON-RPC error in a 200 response.
func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPCError(w, nil, codeParseError, "request body must be a valid JSON-RPC request", err.Error())
		return
	}

	switch req.Method {
	case methodToolsList:
		s.handleToolsList(w, req.ID)
	case methodToolsCall:
		s.handleToolsCall(w, r.Context(), req.ID, req.Params)
	default:
		writeRPCError(w, req.ID, codeMethodNotFound, fmt.Sprintf("unknown method %q", req.Method), nil)
	}
}

func (s *Server) handleToolsList(w http.ResponseWriter, id json.RawMessage) {
	writeRPCResult(w, id, toolsListResult{Tools: toolDescriptors()})
}

type toolsListResult struct {
	Tools []ToolDescriptor `json:"tools"`
}

type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) handleToolsCall(w http.ResponseWriter, ctx context.Context, id json.RawMessage, params json.RawMessage) {
	var callParams toolsCallParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &callParams); err != nil {
			writeRPCError(w, id, codeInvalidParams, `params must be a JSON object with "name" and "arguments"`, err.Error())
			return
		}
	}
	if callParams.Name == "" {
		writeRPCError(w, id, codeInvalidParams, "params.name is required", nil)
		return
	}

	spec, ok := lookupTool(callParams.Name)
	if !ok {
		writeRPCError(w, id, codeMethodNotFound, fmt.Sprintf("unknown tool %q", callParams.Name), nil)
		return
	}

	result, err := s.invokeTool(ctx, spec, callParams.Arguments)
	if err != nil {
		s.writeToolError(w, id, callParams.Name, err)
		return
	}
	writeRPCResult(w, id, result)
}

// invokeTool calls a tool's handler, converting any panic into an error
// instead of letting it crash the process. This is part of this package's
// "never a panic" guarantee: a bug in one tool must not take the whole MCP
// server down.
func (s *Server) invokeTool(ctx context.Context, spec toolSpec, args json.RawMessage) (result any, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("tool %q panicked: %v", spec.Name, rec)
		}
	}()
	return spec.Handler(ctx, s, args)
}

// writeToolError classifies an error returned by a tool handler and writes
// the matching JSON-RPC error. A *ToolError (argument validation failure)
// always carries structured data naming the offending field, so a caller
// learns what to fix rather than receiving an opaque failure.
func (s *Server) writeToolError(w http.ResponseWriter, id json.RawMessage, name string, err error) {
	var toolErr *ToolError
	switch {
	case errors.Is(err, ErrNotImplementedYet):
		writeRPCError(w, id, codeNotImplemented,
			fmt.Sprintf("tool %q is declared but not implemented until a later milestone", name), nil)
	case errors.As(err, &toolErr):
		writeRPCError(w, id, codeInvalidParams, toolErr.Error(),
			map[string]string{"field": toolErr.Field, "message": toolErr.Message})
	default:
		writeRPCError(w, id, codeInternalError, err.Error(), nil)
	}
}

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeJSONRPC(w, rpcResponse{ID: id, Result: result})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string, data any) {
	writeJSONRPC(w, rpcResponse{ID: id, Error: &rpcError{Code: code, Message: message, Data: data}})
}

// writeJSONRPC always answers with HTTP 200: JSON-RPC represents failure in
// the response body, not the HTTP status line, which is what lets an
// unknown method, an unknown tool, a validation failure and a not-yet-M3
// tool all be reported without ever producing a non-200/500 response.
func writeJSONRPC(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
