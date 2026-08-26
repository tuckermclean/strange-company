package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestPutSpecThenGetSpecRoundTrips checks that a spec written by PutSpec
// comes back from GetSpec unapproved, with the content and author intact.
func TestPutSpecThenGetSpecRoundTrips(t *testing.T) {
	s := openTestStore(t)
	id := seedBacklogCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.PutSpec(ctx, id, "# Context\nwhy this card exists", "fable"); err != nil {
		t.Fatalf("PutSpec() returned error %v, want nil", err)
	}

	got, err := s.GetSpec(ctx, id)
	if err != nil {
		t.Fatalf("GetSpec() returned error %v, want nil", err)
	}
	if got.CardID != id {
		t.Errorf("CardID = %s, want %s", got.CardID, id)
	}
	if got.Content != "# Context\nwhy this card exists" {
		t.Errorf("Content = %q, want the content just written", got.Content)
	}
	if got.UpdatedBy != "fable" {
		t.Errorf("UpdatedBy = %q, want %q", got.UpdatedBy, "fable")
	}
	if got.Approved {
		t.Error("a freshly written spec must not read as approved: nobody has approved it yet")
	}
	if got.ApprovedBy != "" {
		t.Errorf("ApprovedBy = %q, want empty for an unapproved spec", got.ApprovedBy)
	}
	if got.ApprovedAt != nil {
		t.Errorf("ApprovedAt = %v, want nil for an unapproved spec", got.ApprovedAt)
	}
}

// TestGetSpecOnACardWithNoSpecIsNotFound checks that a card which has never
// had a spec written for it reports ErrSpecNotFound rather than a zero
// value, so callers cannot mistake "never written" for "written but empty".
func TestGetSpecOnACardWithNoSpecIsNotFound(t *testing.T) {
	s := openTestStore(t)
	id := seedBacklogCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := s.GetSpec(ctx, id)
	if !errors.Is(err, ErrSpecNotFound) {
		t.Fatalf("GetSpec() on a card with no spec: got error %v, want ErrSpecNotFound", err)
	}
}

// TestApproveSpecRecordsWhoApproved checks that approving a spec records the
// approver and a timestamp, and that GetSpec then reports it as approved.
func TestApproveSpecRecordsWhoApproved(t *testing.T) {
	s := openTestStore(t)
	id := seedBacklogCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.PutSpec(ctx, id, "the spec", "fable"); err != nil {
		t.Fatalf("PutSpec() returned error %v, want nil", err)
	}
	if err := s.ApproveSpec(ctx, id, "tucker"); err != nil {
		t.Fatalf("ApproveSpec() returned error %v, want nil", err)
	}

	got, err := s.GetSpec(ctx, id)
	if err != nil {
		t.Fatalf("GetSpec() returned error %v, want nil", err)
	}
	if !got.Approved {
		t.Fatal("spec §10.2: a card whose spec was just approved must read as approved")
	}
	if got.ApprovedBy != "tucker" {
		t.Errorf("ApprovedBy = %q, want %q", got.ApprovedBy, "tucker")
	}
	if got.ApprovedAt == nil {
		t.Error("ApprovedAt = nil, want a timestamp once approved")
	}
}

// TestEditingAnApprovedSpecRevokesApproval is the load-bearing test: spec
// §10.2 approves a specific document, and PutSpec must unconditionally
// revoke any existing approval when the content changes.
func TestEditingAnApprovedSpecRevokesApproval(t *testing.T) {
	s := openTestStore(t)
	id := seedBacklogCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.PutSpec(ctx, id, "draft one", "fable"); err != nil {
		t.Fatalf("PutSpec() returned error %v, want nil", err)
	}
	if err := s.ApproveSpec(ctx, id, "tucker"); err != nil {
		t.Fatalf("ApproveSpec() returned error %v, want nil", err)
	}

	if err := s.PutSpec(ctx, id, "draft two, materially different", "fable"); err != nil {
		t.Fatalf("PutSpec() returned error %v, want nil", err)
	}

	got, err := s.GetSpec(ctx, id)
	if err != nil {
		t.Fatalf("GetSpec() returned error %v, want nil", err)
	}
	if got.Approved {
		t.Fatal("editing a spec after approval must revoke it, or approval could be obtained on one document and used for another")
	}
	if got.ApprovedBy != "" {
		t.Errorf("ApprovedBy = %q, want empty: editing a spec after approval must revoke it, or approval could be obtained on one document and used for another", got.ApprovedBy)
	}
	if got.ApprovedAt != nil {
		t.Error("ApprovedAt is still set: editing a spec after approval must revoke it, or approval could be obtained on one document and used for another")
	}
}

// TestApproveSpecRejectsAnUnattributedApproval checks that ApproveSpec
// refuses an empty approver rather than silently recording an approval by
// nobody, which would corrupt the audit trail required by spec §21.
func TestApproveSpecRejectsAnUnattributedApproval(t *testing.T) {
	s := openTestStore(t)
	id := seedBacklogCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.PutSpec(ctx, id, "the spec", "fable"); err != nil {
		t.Fatalf("PutSpec() returned error %v, want nil", err)
	}

	if err := s.ApproveSpec(ctx, id, ""); err == nil {
		t.Fatal("ApproveSpec(\"\") returned nil error, want an error: an unattributed approval would corrupt the audit trail (spec §21)")
	}
	if err := s.ApproveSpec(ctx, id, "   "); err == nil {
		t.Fatal("ApproveSpec(\"   \") returned nil error, want an error: an unattributed approval would corrupt the audit trail (spec §21)")
	}

	got, err := s.GetSpec(ctx, id)
	if err != nil {
		t.Fatalf("GetSpec() returned error %v, want nil", err)
	}
	if got.Approved {
		t.Fatal("a rejected, unattributed approval attempt must not leave the spec looking approved")
	}
}

// TestApproveSpecOnAMissingSpecIsNotFound checks that approving a card which
// has no specification document at all is reported as ErrSpecNotFound.
func TestApproveSpecOnAMissingSpecIsNotFound(t *testing.T) {
	s := openTestStore(t)
	id := seedBacklogCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := s.ApproveSpec(ctx, id, "tucker")
	if !errors.Is(err, ErrSpecNotFound) {
		t.Fatalf("ApproveSpec() on a card with no spec: got error %v, want ErrSpecNotFound", err)
	}
}

// TestApprovalIsOfADocumentNotACard checks that approval is revoked by
// PutSpec unconditionally -- even when the newly written content happens to
// be byte-identical to what was approved -- because PutSpec has no clever
// equality check and approval must be of a document, not of a card.
func TestApprovalIsOfADocumentNotACard(t *testing.T) {
	s := openTestStore(t)
	id := seedBacklogCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const content = "identical content, word for word"

	if err := s.PutSpec(ctx, id, content, "fable"); err != nil {
		t.Fatalf("PutSpec() returned error %v, want nil", err)
	}
	if err := s.ApproveSpec(ctx, id, "tucker"); err != nil {
		t.Fatalf("ApproveSpec() returned error %v, want nil", err)
	}

	// Byte-identical to what was just approved.
	if err := s.PutSpec(ctx, id, content, "fable"); err != nil {
		t.Fatalf("PutSpec() returned error %v, want nil", err)
	}

	got, err := s.GetSpec(ctx, id)
	if err != nil {
		t.Fatalf("GetSpec() returned error %v, want nil", err)
	}
	if got.Approved {
		t.Fatal("approval is of a document, not a card: PutSpec must always clear it, or approval obtained on one document could be used for another written later")
	}
}
