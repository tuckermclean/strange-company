package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

// --- fake CardService --------------------------------------------------
//
// Hand-written, no mocking library: each method delegates to a function
// field, and a method whose function field is nil panics rather than
// silently returning a zero value nobody asked for. This mirrors
// internal/server/cards_test.go's fakeStore.
type fakeCards struct {
	listCardsFn  func(ctx context.Context) ([]*card.Card, error)
	getCardFn    func(ctx context.Context, id uuid.UUID) (*card.Card, error)
	claimReadyFn func(ctx context.Context, workerID string, lease time.Duration) (*card.Card, error)
	heartbeatFn  func(ctx context.Context, cardID uuid.UUID, workerID string, lease time.Duration) error
	releaseFn    func(ctx context.Context, cardID uuid.UUID, workerID, reason string) error
	transitionFn func(ctx context.Context, cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error
}

func (f *fakeCards) ListCards(ctx context.Context) ([]*card.Card, error) {
	if f.listCardsFn == nil {
		panic("fakeCards: ListCards not configured")
	}
	return f.listCardsFn(ctx)
}

func (f *fakeCards) GetCard(ctx context.Context, id uuid.UUID) (*card.Card, error) {
	if f.getCardFn == nil {
		panic("fakeCards: GetCard not configured")
	}
	return f.getCardFn(ctx, id)
}

func (f *fakeCards) ClaimReady(ctx context.Context, workerID string, lease time.Duration) (*card.Card, error) {
	if f.claimReadyFn == nil {
		panic("fakeCards: ClaimReady not configured")
	}
	return f.claimReadyFn(ctx, workerID, lease)
}

func (f *fakeCards) Heartbeat(ctx context.Context, cardID uuid.UUID, workerID string, lease time.Duration) error {
	if f.heartbeatFn == nil {
		panic("fakeCards: Heartbeat not configured")
	}
	return f.heartbeatFn(ctx, cardID, workerID, lease)
}

func (f *fakeCards) Release(ctx context.Context, cardID uuid.UUID, workerID, reason string) error {
	if f.releaseFn == nil {
		panic("fakeCards: Release not configured")
	}
	return f.releaseFn(ctx, cardID, workerID, reason)
}

func (f *fakeCards) Transition(ctx context.Context, cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error {
	if f.transitionFn == nil {
		panic("fakeCards: Transition not configured")
	}
	return f.transitionFn(ctx, cardID, to, actor, actorID, reason)
}

// --- helpers -------------------------------------------------------------

func callRPC(t *testing.T, s *Server, method string, params any) rpcResponse {
	t.Helper()

	body := map[string]any{"method": method}
	if params != nil {
		body["params"] = params
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200 (JSON-RPC reports failure in the body, not the status line), got %d: %s", rec.Code, rec.Body.String())
	}

	var resp rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response body must be valid JSON-RPC: %v (body=%s)", err, rec.Body.String())
	}
	return resp
}

func callTool(t *testing.T, s *Server, name string, args map[string]any) rpcResponse {
	t.Helper()
	return callRPC(t, s, methodToolsCall, map[string]any{"name": name, "arguments": args})
}

func decodeResult(t *testing.T, resp rpcResponse, target any) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("expected a result, got error %+v", resp.Error)
	}
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("re-marshal result: %v", err)
	}
	if err := json.Unmarshal(b, target); err != nil {
		t.Fatalf("decode result into %T: %v (result=%s)", target, err, b)
	}
}

// --- the security boundary -------------------------------------------------

// TestTheToolSurfaceIsExactlyTheSpecifiedSet is the load-bearing test: spec
// section 9 says Hermes must never be handed a generic Kubernetes tool, a
// shell, an exec, or arbitrary HTTP. This asserts both halves of that
// promise -- the exact allowed set is present, and nothing resembling a
// dangerous generic tool has snuck in.
func TestTheToolSurfaceIsExactlyTheSpecifiedSet(t *testing.T) {
	want := []string{
		"artifacts.attach",
		"artifacts.list",
		"cards.claim",
		"cards.comment",
		"cards.get",
		"cards.heartbeat",
		"cards.list_ready",
		"cards.release",
		"cards.request_human",
		"cards.transition",
		"coding.implement",
		"coding.plan",
		"coding.review",
		"coding.write_tests",
		"cost.get_card",
		"cost.get_run",
		"portfolio.get_policy",
		"portfolio.scan",
		"scrum.get_policy",
		"verification.run",
	}

	got := ToolNames()

	for _, name := range got {
		if looksLikeAForbiddenTool(name) {
			t.Fatalf("spec section 9: Hermes must never be handed %q", name)
		}
	}

	if len(got) != len(want) {
		t.Fatalf("tool surface has %d tools, want %d\n got:  %v\n want: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tool surface mismatch at index %d: got %q, want %q\n got:  %v\n want: %v", i, got[i], want[i], got, want)
		}
	}
}

// looksLikeAForbiddenTool reports whether name resembles a generic
// Kubernetes tool, a shell, an exec, or arbitrary HTTP access -- exactly
// what spec section 9 forbids handing to Hermes.
func looksLikeAForbiddenTool(name string) bool {
	lower := strings.ToLower(name)

	exact := map[string]bool{
		"exec": true, "shell": true, "http": true, "kubectl": true, "kubernetes": true,
	}
	if exact[lower] {
		return true
	}

	for _, prefix := range []string{"kubernetes.", "k8s.", "kubectl.", "exec.", "shell.", "http."} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// --- named tests from the plan -------------------------------------------

func TestClaimReturnsNoWorkRatherThanAnErrorWhenTheQueueIsEmpty(t *testing.T) {
	fc := &fakeCards{
		claimReadyFn: func(ctx context.Context, workerID string, lease time.Duration) (*card.Card, error) {
			return nil, ErrNoWork
		},
	}
	s := NewServer(fc)

	resp := callTool(t, s, "cards.claim", map[string]any{"worker_id": "meeseeks-1"})
	if resp.Error != nil {
		t.Fatalf("an empty queue must not be reported as a JSON-RPC error, got %+v", resp.Error)
	}

	var result cardsClaimResult
	decodeResult(t, resp, &result)
	if !result.NoWork {
		t.Fatalf("expected no_work=true, got %+v", result)
	}
	if result.Card != nil {
		t.Fatalf("expected no card when the queue is empty, got %+v", result.Card)
	}
}

func TestTransitionSurfacesTheStateMachinesRejection(t *testing.T) {
	rejection := fmt.Errorf("%w: Done -> Ready is not a permitted transition (actor=agent)", card.ErrIllegalTransition)
	fc := &fakeCards{
		transitionFn: func(ctx context.Context, cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error {
			return rejection
		},
	}
	s := NewServer(fc)

	resp := callTool(t, s, "cards.transition", map[string]any{
		"card_id":    uuid.New().String(),
		"to":         "Ready",
		"actor_type": "agent",
		"actor_id":   "meeseeks-1",
	})

	if resp.Error == nil {
		t.Fatalf("expected the state machine's rejection to surface as an error, got result %+v", resp.Result)
	}
	if !strings.Contains(resp.Error.Message, rejection.Error()) {
		t.Fatalf("caller cannot learn WHY: error message %q does not contain the rejection %q", resp.Error.Message, rejection.Error())
	}
}

// TestEveryToolValidatesItsArguments checks, for each implemented tool, that
// an invalid or missing required field is rejected -- naming that field --
// before the tool ever reaches the CardService. The fake below panics if any
// of its methods are called, so a case that slips past validation fails
// loudly rather than silently.
func TestEveryToolValidatesItsArguments(t *testing.T) {
	s := NewServer(&fakeCards{})

	someID := uuid.New().String()

	cases := []struct {
		tool  string
		args  map[string]any
		field string
	}{
		{"cards.get", map[string]any{}, "card_id"},
		{"cards.get", map[string]any{"card_id": "not-a-uuid"}, "card_id"},

		{"cards.claim", map[string]any{}, "worker_id"},
		{"cards.claim", map[string]any{"worker_id": "   "}, "worker_id"},

		{"cards.heartbeat", map[string]any{"worker_id": "w1"}, "card_id"},
		{"cards.heartbeat", map[string]any{"card_id": someID}, "worker_id"},

		{"cards.release", map[string]any{"worker_id": "w1"}, "card_id"},
		{"cards.release", map[string]any{"card_id": someID}, "worker_id"},

		{"cards.transition", map[string]any{"to": "Ready", "actor_type": "agent", "actor_id": "a1"}, "card_id"},
		{"cards.transition", map[string]any{"card_id": someID, "actor_type": "agent", "actor_id": "a1"}, "to"},
		{"cards.transition", map[string]any{"card_id": someID, "to": "Ready", "actor_id": "a1"}, "actor_type"},
		{"cards.transition", map[string]any{"card_id": someID, "to": "Ready", "actor_type": "agent"}, "actor_id"},

		{"cards.comment", map[string]any{"author": "a", "body": "b"}, "card_id"},
		{"cards.comment", map[string]any{"card_id": someID, "body": "b"}, "author"},
		{"cards.comment", map[string]any{"card_id": someID, "author": "a"}, "body"},

		{"cards.request_human", map[string]any{}, "card_id"},
		{"cards.request_human", map[string]any{"card_id": someID}, "reason"},

		{"artifacts.attach", map[string]any{"type": "diff", "content": "x"}, "card_id"},
		{"artifacts.attach", map[string]any{"card_id": someID, "content": "x"}, "type"},
		{"artifacts.attach", map[string]any{"card_id": someID, "type": "diff"}, "content"},

		{"artifacts.list", map[string]any{}, "card_id"},
	}

	for _, tc := range cases {
		t.Run(tc.tool+"/"+tc.field, func(t *testing.T) {
			resp := callTool(t, s, tc.tool, tc.args)
			if resp.Error == nil {
				t.Fatalf("%s: expected a validation error, got result %+v", tc.tool, resp.Result)
			}
			data, ok := resp.Error.Data.(map[string]any)
			if !ok {
				t.Fatalf("%s: expected error.data to name the offending field, got %#v (message=%q)", tc.tool, resp.Error.Data, resp.Error.Message)
			}
			if data["field"] != tc.field {
				t.Fatalf("%s: expected offending field %q, got %v (message=%q)", tc.tool, tc.field, data["field"], resp.Error.Message)
			}
		})
	}
}

func TestUnknownToolIsRejected(t *testing.T) {
	s := NewServer(&fakeCards{})

	resp := callTool(t, s, "kubernetes.exec", map[string]any{})
	if resp.Error == nil {
		t.Fatalf("expected an error for an unknown tool, got result %+v", resp.Result)
	}
	if resp.Result != nil {
		t.Fatalf("an unknown tool must not also carry a result, got %+v", resp.Result)
	}
}

// --- supporting coverage ---------------------------------------------------

func TestUnknownMethodIsRejected(t *testing.T) {
	s := NewServer(&fakeCards{})

	resp := callRPC(t, s, "tools/frobnicate", nil)
	if resp.Error == nil {
		t.Fatalf("expected an error for an unknown method, got result %+v", resp.Result)
	}
}

func TestMalformedRequestBodyNeverProducesA500(t *testing.T) {
	s := NewServer(&fakeCards{})

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{not valid json"))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("malformed input must not produce a non-200 HTTP status, got %d", rec.Code)
	}
	var resp rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response must still be valid JSON-RPC: %v (body=%s)", err, rec.Body.String())
	}
	if resp.Error == nil {
		t.Fatalf("expected a JSON-RPC error for malformed input")
	}
}

// TestAPanickingHandlerBecomesAJSONRPCErrorNotACrash proves the transport's
// "never a panic" guarantee: a bug inside one tool handler must not take
// the whole MCP server down.
func TestAPanickingHandlerBecomesAJSONRPCErrorNotACrash(t *testing.T) {
	fc := &fakeCards{
		getCardFn: func(ctx context.Context, id uuid.UUID) (*card.Card, error) {
			panic("boom")
		},
	}
	s := NewServer(fc)

	resp := callTool(t, s, "cards.get", map[string]any{"card_id": uuid.New().String()})
	if resp.Error == nil {
		t.Fatalf("expected a JSON-RPC error, not a propagated panic; result=%+v", resp.Result)
	}
}

func TestToolsListReturnsADescriptorForEveryRegisteredTool(t *testing.T) {
	s := NewServer(&fakeCards{})

	resp := callRPC(t, s, methodToolsList, nil)
	var result toolsListResult
	decodeResult(t, resp, &result)

	if len(result.Tools) != len(ToolNames()) {
		t.Fatalf("tools/list returned %d tools, want %d", len(result.Tools), len(ToolNames()))
	}
	for _, td := range result.Tools {
		if td.Name == "" || td.Description == "" || td.InputSchema == nil {
			t.Fatalf("tool descriptor missing name/description/schema: %+v", td)
		}
	}
}

func TestNotImplementedToolsReturnErrNotImplementedYet(t *testing.T) {
	notYet := []string{
		"coding.plan", "coding.write_tests", "coding.implement", "coding.review",
		"verification.run", "cost.get_card", "cost.get_run",
		"portfolio.scan", "portfolio.get_policy", "scrum.get_policy",
	}
	s := NewServer(&fakeCards{})

	for _, name := range notYet {
		t.Run(name, func(t *testing.T) {
			resp := callTool(t, s, name, map[string]any{
				"card_id": uuid.New().String(),
				"run_id":  uuid.New().String(),
			})
			if resp.Error == nil {
				t.Fatalf("%s: expected an ErrNotImplementedYet error, got result %+v", name, resp.Result)
			}
			if resp.Error.Code != codeNotImplemented {
				t.Fatalf("%s: expected not-implemented error code %d, got %d (%s)", name, codeNotImplemented, resp.Error.Code, resp.Error.Message)
			}
		})
	}
}

func TestArtifactsAttachAndListRoundTrip(t *testing.T) {
	id := uuid.New()
	fc := &fakeCards{
		getCardFn: func(ctx context.Context, gid uuid.UUID) (*card.Card, error) {
			return &card.Card{ID: gid}, nil
		},
	}
	s := NewServer(fc)

	attachResp := callTool(t, s, "artifacts.attach", map[string]any{
		"card_id": id.String(),
		"type":    "diff",
		"content": "diff --git a/foo b/foo",
	})
	var attached Artifact
	decodeResult(t, attachResp, &attached)
	if attached.SHA256 == "" {
		t.Fatalf("expected a sha256 to be computed for inline content, got %+v", attached)
	}
	if attached.CardID != id {
		t.Fatalf("expected artifact card_id %s, got %s", id, attached.CardID)
	}

	listResp := callTool(t, s, "artifacts.list", map[string]any{"card_id": id.String()})
	var listed artifactsListResult
	decodeResult(t, listResp, &listed)
	if len(listed.Artifacts) != 1 || listed.Artifacts[0].ID != attached.ID {
		t.Fatalf("expected the attached artifact back from artifacts.list, got %+v", listed.Artifacts)
	}
}

func TestCardsCommentIsStoredInMemory(t *testing.T) {
	id := uuid.New()
	fc := &fakeCards{
		getCardFn: func(ctx context.Context, gid uuid.UUID) (*card.Card, error) {
			return &card.Card{ID: gid}, nil
		},
	}
	s := NewServer(fc)

	resp := callTool(t, s, "cards.comment", map[string]any{
		"card_id": id.String(),
		"author":  "hermes",
		"body":    "spec looks ambiguous",
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	stored := s.records.listComments(id)
	if len(stored) != 1 || stored[0].Body != "spec looks ambiguous" || stored[0].Author != "hermes" {
		t.Fatalf("expected the comment to be recorded, got %+v", stored)
	}
}

func TestCardsRequestHumanTransitionsToNeedsHumanAsAgent(t *testing.T) {
	id := uuid.New()
	var gotTo card.State
	var gotActor card.ActorType
	var gotReason string
	fc := &fakeCards{
		transitionFn: func(ctx context.Context, cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error {
			gotTo, gotActor, gotReason = to, actor, reason
			return nil
		},
		getCardFn: func(ctx context.Context, gid uuid.UUID) (*card.Card, error) {
			return &card.Card{ID: gid, State: card.NeedsHuman}, nil
		},
	}
	s := NewServer(fc)

	resp := callTool(t, s, "cards.request_human", map[string]any{
		"card_id": id.String(),
		"reason":  "specification is ambiguous",
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if gotTo != card.NeedsHuman {
		t.Fatalf("expected a transition to NeedsHuman, got %s", gotTo)
	}
	if gotActor != card.ActorAgent {
		t.Fatalf("expected actor_type agent, got %s", gotActor)
	}
	if gotReason != "specification is ambiguous" {
		t.Fatalf("expected the reason to be forwarded, got %q", gotReason)
	}
}
