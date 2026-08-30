package promote_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/promote"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

// Approval is granted only when a human's signature is the ONLY thing missing.
// The gate is evaluated as if signed, so an incomplete spec, an unverifiable
// criterion or a missing allowlist still stops the card on its own.
func TestAutoApprovalSignsOnlyWhatWouldOtherwisePass(t *testing.T) {
	b, good := boardWith(t, goodSpec, false, true)
	bad := uuid.New()
	b.cards[bad] = &card.Card{ID: bad, State: card.Backlog, RiskClass: "R1"}
	b.specs[bad] = &store.CardSpec{Content: "# Context\n\nnot a specification\n"}
	b.actions[bad] = true
	b.unapproved = []uuid.UUID{good, bad}

	res, err := promote.New(b, 10, nil).WithAutoApproval(true).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if res.Approved != 1 {
		t.Fatalf("approved %d, want only the complete specification", res.Approved)
	}
	if !b.approved[good] {
		t.Error("a specification that would pass was not signed")
	}
	if b.approved[bad] {
		t.Error("an incomplete specification was signed; the gate was bypassed rather than delegated")
	}
}

// §21's audit has to distinguish a specification a human read from one the
// control plane signed on their behalf.
func TestAutoApprovalIsAttributedToTheSettingNotToAPerson(t *testing.T) {
	b, id := boardWith(t, goodSpec, false, true)
	b.unapproved = []uuid.UUID{id}

	if _, err := promote.New(b, 10, nil).WithAutoApproval(true).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	actor := b.approvedBy[id]
	if actor == "" {
		t.Fatal("the approval has no actor at all")
	}
	if !strings.Contains(actor, "autonomy") {
		t.Errorf("approver = %q; a reader cannot tell this from a person", actor)
	}
}

// Off by default, so an upgrade never changes who is approving what.
func TestNothingIsSignedWhenAutonomyIsManual(t *testing.T) {
	b, id := boardWith(t, goodSpec, false, true)
	b.unapproved = []uuid.UUID{id}

	res, err := promote.New(b, 10, nil).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Approved != 0 || b.approved[id] {
		t.Error("a specification was signed with autonomy left at manual")
	}
}
