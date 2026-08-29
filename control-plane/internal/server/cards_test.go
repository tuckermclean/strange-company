package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

// --- fakes -----------------------------------------------------------------

// fakeStore is a hand-written CardStore test double: each method delegates
// to a function field, so a test only wires up the behaviour it exercises.
// A method whose function field is nil panics, which fails the calling test
// loudly rather than silently returning a zero value it never asked for.
type fakeStore struct {
	listCardsFn  func(ctx context.Context) ([]*card.Card, error)
	getCardFn    func(ctx context.Context, id uuid.UUID) (*card.Card, error)
	claimReadyFn func(ctx context.Context, workerID string, lease time.Duration) (*card.Card, error)
	heartbeatFn  func(ctx context.Context, cardID uuid.UUID, workerID string, lease time.Duration) error
	releaseFn    func(ctx context.Context, cardID uuid.UUID, workerID, reason string) error
	transitionFn func(ctx context.Context, cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error
	// approveSpecFn is unset in the tests that predate approval; the
	// method below treats nil as "nothing to record".
	approveSpecFn func(ctx context.Context, cardID uuid.UUID, approvedBy string) error
}

func (f *fakeStore) ListCards(ctx context.Context) ([]*card.Card, error) {
	if f.listCardsFn == nil {
		panic("fakeStore: ListCards not configured")
	}
	return f.listCardsFn(ctx)
}

func (f *fakeStore) GetCard(ctx context.Context, id uuid.UUID) (*card.Card, error) {
	if f.getCardFn == nil {
		panic("fakeStore: GetCard not configured")
	}
	return f.getCardFn(ctx, id)
}

func (f *fakeStore) ClaimReady(ctx context.Context, workerID string, lease time.Duration) (*card.Card, error) {
	if f.claimReadyFn == nil {
		panic("fakeStore: ClaimReady not configured")
	}
	return f.claimReadyFn(ctx, workerID, lease)
}

func (f *fakeStore) Heartbeat(ctx context.Context, cardID uuid.UUID, workerID string, lease time.Duration) error {
	if f.heartbeatFn == nil {
		panic("fakeStore: Heartbeat not configured")
	}
	return f.heartbeatFn(ctx, cardID, workerID, lease)
}

func (f *fakeStore) Release(ctx context.Context, cardID uuid.UUID, workerID, reason string) error {
	if f.releaseFn == nil {
		panic("fakeStore: Release not configured")
	}
	return f.releaseFn(ctx, cardID, workerID, reason)
}

func (f *fakeStore) Transition(ctx context.Context, cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error {
	if f.transitionFn == nil {
		panic("fakeStore: Transition not configured")
	}
	return f.transitionFn(ctx, cardID, to, actor, actorID, reason)
}

// ListArtifacts satisfies CardStore for tests that predate artifacts.
// artifactStore in artifacts_test.go overrides it.
func (f *fakeStore) ListArtifacts(context.Context, uuid.UUID) ([]Artifact, error) {
	return nil, nil
}

// ListAttempts satisfies CardStore for tests that predate the attempt ledger.
// attemptStore in attempts_test.go overrides it.
func (f *fakeStore) ListAttempts(context.Context, uuid.UUID) ([]Attempt, error) {
	return nil, nil
}

// ApproveSpec satisfies CardStore for the tests written before approval
// existed. approvingStore in approve_test.go overrides it.
func (f *fakeStore) ApproveSpec(ctx context.Context, cardID uuid.UUID, approvedBy string) error {
	if f.approveSpecFn == nil {
		panic("fakeStore: ApproveSpec not configured")
	}
	return f.approveSpecFn(ctx, cardID, approvedBy)
}

// Sentinel test errors, classified by fakeClassifier below via errors.Is —
// mirroring how main's real classifier compares against the store's
// sentinels, without this package depending on the store package.
var (
	errFakeNoWork      = errors.New("no ready card is available")
	errFakeNotClaimant = errors.New("caller does not hold the lease")
	errFakeNotFound    = errors.New("card not found")
)

type fakeClassifier struct{}

func (fakeClassifier) IsNoWork(err error) bool      { return errors.Is(err, errFakeNoWork) }
func (fakeClassifier) IsNotClaimant(err error) bool { return errors.Is(err, errFakeNotClaimant) }
func (fakeClassifier) IsNotFound(err error) bool    { return errors.Is(err, errFakeNotFound) }

// --- helpers -----------------------------------------------------------------

func newCardsTestServer(t *testing.T, cs CardStore) *Server {
	t.Helper()
	s := New(testConfig(t), nil, "test")
	s.SetCards(cs, fakeClassifier{})
	return s
}

func postJSON(t *testing.T, s *Server, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeErrorBody(t *testing.T, rec *httptest.ResponseRecorder) errorBody {
	t.Helper()
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error response must be JSON {error, detail}, got %q: %v", rec.Body.String(), err)
	}
	return body
}

// --- tests -----------------------------------------------------------------

func TestClaimReturns204WhenNoWorkIsAvailable(t *testing.T) {
	cs := &fakeStore{
		claimReadyFn: func(ctx context.Context, workerID string, lease time.Duration) (*card.Card, error) {
			return nil, errFakeNoWork
		},
	}
	s := newCardsTestServer(t, cs)

	rec := postJSON(t, s, "/cards/"+uuid.New().String()+"/claim", map[string]any{"worker_id": "worker-1"})

	if got, want := rec.Code, http.StatusNoContent; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", rec.Body.String())
	}
}

func TestClaimReturnsTheClaimedCard(t *testing.T) {
	want := &card.Card{ID: uuid.New(), State: card.InProgress}
	cs := &fakeStore{
		claimReadyFn: func(ctx context.Context, workerID string, lease time.Duration) (*card.Card, error) {
			if workerID != "worker-1" {
				t.Fatalf("ClaimReady called with workerID = %q, want %q", workerID, "worker-1")
			}
			return want, nil
		},
	}
	s := newCardsTestServer(t, cs)

	rec := postJSON(t, s, "/cards/"+uuid.New().String()+"/claim", map[string]any{"worker_id": "worker-1"})

	if got, wantCode := rec.Code, http.StatusOK; got != wantCode {
		t.Fatalf("status = %d, want %d (body: %s)", got, wantCode, rec.Body.String())
	}
	var got card.Card
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response body must decode as a card: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("card.ID = %s, want %s", got.ID, want.ID)
	}
}

func TestHeartbeatFromNonClaimantReturns403(t *testing.T) {
	cs := &fakeStore{
		heartbeatFn: func(ctx context.Context, cardID uuid.UUID, workerID string, lease time.Duration) error {
			return errFakeNotClaimant
		},
	}
	s := newCardsTestServer(t, cs)

	rec := postJSON(t, s, "/cards/"+uuid.New().String()+"/heartbeat", map[string]any{"worker_id": "not-the-claimant"})

	if got, want := rec.Code, http.StatusForbidden; got != want {
		t.Fatalf("status = %d, want %d (body: %s)", got, want, rec.Body.String())
	}
}

func TestTransitionRejectedByTheStateMachineReturns409(t *testing.T) {
	rejection := card.CanTransition(card.Done, card.Ready, card.ActorAgent)
	if rejection == nil {
		t.Fatal("test setup: Done -> Ready must be illegal for this test to mean anything")
	}
	cs := &fakeStore{
		transitionFn: func(ctx context.Context, cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error {
			return rejection
		},
	}
	s := newCardsTestServer(t, cs)

	rec := postJSON(t, s, "/cards/"+uuid.New().String()+"/transition", map[string]any{
		"to":         "Ready",
		"actor_type": "agent",
		"actor_id":   "meeseeks-1",
	})

	if got, want := rec.Code, http.StatusConflict; got != want {
		t.Fatalf("status = %d, want %d (body: %s)", got, want, rec.Body.String())
	}
}

func TestTransition409BodyExplainsWhy(t *testing.T) {
	rejection := card.CanTransition(card.Done, card.Ready, card.ActorAgent)
	if rejection == nil {
		t.Fatal("test setup: Done -> Ready must be illegal for this test to mean anything")
	}
	cs := &fakeStore{
		transitionFn: func(ctx context.Context, cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error {
			return rejection
		},
	}
	s := newCardsTestServer(t, cs)

	rec := postJSON(t, s, "/cards/"+uuid.New().String()+"/transition", map[string]any{
		"to":         "Ready",
		"actor_type": "agent",
		"actor_id":   "meeseeks-1",
	})

	body := decodeErrorBody(t, rec)
	if body.Detail != rejection.Error() {
		t.Fatalf("body.detail = %q, want the rejecting rule's message %q", body.Detail, rejection.Error())
	}
}

func TestUnknownCardReturns404(t *testing.T) {
	cs := &fakeStore{
		getCardFn: func(ctx context.Context, id uuid.UUID) (*card.Card, error) {
			return nil, errFakeNotFound
		},
	}
	s := newCardsTestServer(t, cs)

	rec := get(t, s, "/cards/"+uuid.New().String())

	if got, want := rec.Code, http.StatusNotFound; got != want {
		t.Fatalf("status = %d, want %d (body: %s)", got, want, rec.Body.String())
	}
}

func TestMalformedUUIDReturns400(t *testing.T) {
	// No store method should even be reached: parsing the id fails first.
	s := newCardsTestServer(t, &fakeStore{})

	rec := get(t, s, "/cards/not-a-uuid")

	if got, want := rec.Code, http.StatusBadRequest; got != want {
		t.Fatalf("status = %d, want %d (body: %s)", got, want, rec.Body.String())
	}
}

func TestClaimWithEmptyWorkerIDReturns400(t *testing.T) {
	// No store method should even be reached: validation fails first.
	s := newCardsTestServer(t, &fakeStore{})

	rec := postJSON(t, s, "/cards/"+uuid.New().String()+"/claim", map[string]any{"worker_id": ""})

	if got, want := rec.Code, http.StatusBadRequest; got != want {
		t.Fatalf("status = %d, want %d (body: %s)", got, want, rec.Body.String())
	}
}

func TestGetCardReturnsJSON(t *testing.T) {
	want := &card.Card{ID: uuid.New(), State: card.Ready}
	cs := &fakeStore{
		getCardFn: func(ctx context.Context, id uuid.UUID) (*card.Card, error) {
			if id != want.ID {
				t.Fatalf("GetCard called with id = %s, want %s", id, want.ID)
			}
			return want, nil
		},
	}
	s := newCardsTestServer(t, cs)

	rec := get(t, s, "/cards/"+want.ID.String())

	if got, wantCode := rec.Code, http.StatusOK; got != wantCode {
		t.Fatalf("status = %d, want %d (body: %s)", got, wantCode, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got card.Card
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response body must decode as a card: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("card.ID = %s, want %s", got.ID, want.ID)
	}
}
