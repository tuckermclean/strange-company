package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// approvingStore is a fakeStore that also records approvals.
type approvingStore struct {
	fakeStore
	cardID     uuid.UUID
	approvedBy string
	err        error
}

func (a *approvingStore) ApproveSpec(_ context.Context, cardID uuid.UUID, approvedBy string) error {
	a.cardID = cardID
	a.approvedBy = approvedBy
	return a.err
}

// §10.2: a human approves the completed spec, and only then may the control
// plane promote. Without this endpoint nothing can approve anything, so no
// card can ever reach Ready no matter how healthy the rest of the pipeline is.
func TestApproveSpecRecordsTheApproval(t *testing.T) {
	fake := &approvingStore{}
	s := newCardsTestServer(t, fake)
	id := uuid.New()

	rec := postJSON(t, s, "/cards/"+id.String()+"/approve-spec",
		map[string]string{"approved_by": "a-human"})

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if fake.cardID != id {
		t.Errorf("approved card %s, want %s", fake.cardID, id)
	}
	if fake.approvedBy != "a-human" {
		t.Errorf("approved_by = %q", fake.approvedBy)
	}
}

// An unattributed approval is not an approval. §10.2 requires a human, and
// the record of who approved is the point of storing it at all.
func TestApproveSpecRequiresAnApprover(t *testing.T) {
	fake := &approvingStore{}
	s := newCardsTestServer(t, fake)

	for _, body := range []map[string]string{{}, {"approved_by": "   "}} {
		rec := postJSON(t, s, "/cards/"+uuid.New().String()+"/approve-spec", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d for %v, want 400", rec.Code, body)
		}
	}
	if fake.approvedBy != "" {
		t.Errorf("recorded an unattributed approval as %q", fake.approvedBy)
	}
}

func TestApproveSpecRejectsAMalformedCardID(t *testing.T) {
	s := newCardsTestServer(t, &approvingStore{})

	rec := postJSON(t, s, "/cards/not-a-uuid/approve-spec", map[string]string{"approved_by": "a-human"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

// A card that does not exist must not read as approved.
func TestApproveSpecReportsAMissingCard(t *testing.T) {
	fake := &approvingStore{err: errFakeNotFound}
	s := newCardsTestServer(t, fake)

	rec := postJSON(t, s, "/cards/"+uuid.New().String()+"/approve-spec",
		map[string]string{"approved_by": "a-human"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
