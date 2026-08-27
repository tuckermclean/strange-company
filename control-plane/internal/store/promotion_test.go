package store

import (
	"context"
	"testing"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

const actionsJSON = `{"files":{"include":["**"],"exclude":[]},"commands":["test"],"endpoints":[],"network":[]}`

// A card ingested without an allowlist can never pass §10's gate, so every
// creation path has to stamp one.
func TestIngestionStampsThePermittedActions(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()

	in := issue("7")
	in.PermittedActions = []byte(actionsJSON)
	id, _, err := s.UpsertSourceCard(ctx, in)
	if err != nil {
		t.Fatal(err)
	}

	has, err := s.HasPermittedActions(ctx, id)
	if err != nil {
		t.Fatalf("HasPermittedActions: %v", err)
	}
	if !has {
		t.Fatal("an ingested card has no allowlist and can never be promoted")
	}
}

// Re-ingestion must not overwrite an allowlist someone narrowed by hand --
// that would silently re-widen a card's permissions once a minute.
func TestReingestionDoesNotRestampThePermittedActions(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()

	in := issue("7")
	in.PermittedActions = []byte(`{"files":{"include":["src/**"],"exclude":[]},"commands":["test"],"endpoints":[],"network":[]}`)
	id, _, err := s.UpsertSourceCard(ctx, in)
	if err != nil {
		t.Fatal(err)
	}

	wider := issue("7")
	wider.PermittedActions = []byte(actionsJSON)
	if _, _, err := s.UpsertSourceCard(ctx, wider); err != nil {
		t.Fatal(err)
	}

	got, err := s.PermittedActions(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == actionsJSON {
		t.Fatal("re-ingestion widened the allowlist of an existing card")
	}
}

// The promotion pass must look only at cards a human has actually approved,
// or it would promote on the strength of nothing.
func TestOnlyApprovedBacklogCardsAreOfferedForPromotion(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()

	unapproved := seedBacklogCard(t, s)
	if err := s.PutSpec(ctx, unapproved, "# Problem\n\nx", "someone"); err != nil {
		t.Fatal(err)
	}

	approved := seedBacklogCard(t, s)
	if err := s.PutSpec(ctx, approved, "# Problem\n\ny", "someone"); err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveSpec(ctx, approved, "a-human"); err != nil {
		t.Fatal(err)
	}

	pending, err := s.ListApprovedAwaitingPromotion(ctx, 10)
	if err != nil {
		t.Fatalf("ListApprovedAwaitingPromotion: %v", err)
	}
	if len(pending) != 1 || pending[0] != approved {
		t.Fatalf("pending = %v, want just the approved card", pending)
	}
}

// A card that already left Backlog is not waiting to be promoted; offering it
// again would have the supervisor retry a transition that cannot be legal.
func TestACardPastBacklogIsNotOfferedForPromotion(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()

	id := seedBacklogCard(t, s)
	if err := s.PutSpec(ctx, id, "# Problem\n\nx", "someone"); err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveSpec(ctx, id, "a-human"); err != nil {
		t.Fatal(err)
	}
	if err := s.Transition(ctx, id, card.Blocked, card.ActorHuman, "someone", "waiting"); err != nil {
		t.Fatal(err)
	}

	pending, err := s.ListApprovedAwaitingPromotion(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %v, want nothing", pending)
	}
}

// Approval is of a document: an edit after approval must remove the card from
// the promotion queue, not promote the new text on the old approval.
func TestEditingAnApprovedSpecWithdrawsItFromPromotion(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()

	id := seedBacklogCard(t, s)
	if err := s.PutSpec(ctx, id, "# Problem\n\nx", "someone"); err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveSpec(ctx, id, "a-human"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSpec(ctx, id, "# Problem\n\nrewritten", "someone"); err != nil {
		t.Fatal(err)
	}

	pending, err := s.ListApprovedAwaitingPromotion(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("an edited spec is still queued for promotion: %v", pending)
	}
}
