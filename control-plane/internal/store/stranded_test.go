package store

import (
	"context"
	"testing"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

// A human dragging Ready -> InProgress on the Vikunja board takes the legal
// transition the board invites. It sets the state without setting a claim or a
// lease, and the card was then neither Ready nor lease-expired: nothing could
// ever pick it up again. The card simply stopped, with no error anywhere and a
// column that looked like work in progress.
func TestAHumanDraggingACardIntoInProgressDoesNotStrandIt(t *testing.T) {
	s := openTestStore(t)
	id := seedReadyCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.Transition(ctx, id, card.InProgress, card.ActorHuman, "vikunja", "moved in Vikunja"); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	c, err := s.ClaimReady(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimReady after a human move into InProgress: %v; the card is stranded", err)
	}
	if c == nil || c.ID != id {
		t.Fatalf("ClaimReady returned %v, want the card the human moved", c)
	}
}

// §7.1 ends every lifecycle with "release claim -> EXIT". A transition out of
// InProgress is that exit, and abandoning the claim instead makes the board and
// the API report a worker that is not there.
func TestMovingACardOutOfInProgressReleasesItsClaim(t *testing.T) {
	s := openTestStore(t)
	id := seedReadyCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := s.ClaimReady(ctx, "worker-a", time.Minute); err != nil {
		t.Fatalf("ClaimReady: %v", err)
	}
	if err := s.Transition(ctx, id, card.Review, card.ActorAgent, "worker-a", "review passed"); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	c, err := s.GetCard(ctx, id)
	if err != nil {
		t.Fatalf("GetCard: %v", err)
	}
	if c.ClaimedBy != nil {
		t.Errorf("claimed_by = %q on a card in %s; nobody is working on it", *c.ClaimedBy, c.State)
	}
	if c.LeaseExpiresAt != nil {
		t.Errorf("lease_expires_at = %v on a card in %s", c.LeaseExpiresAt, c.State)
	}
}

// The claim must survive a move that keeps the card in progress, or a worker
// would lose its own card mid-step.
func TestAClaimSurvivesWhileTheCardIsStillInProgress(t *testing.T) {
	s := openTestStore(t)
	id := seedReadyCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := s.ClaimReady(ctx, "worker-a", time.Minute); err != nil {
		t.Fatalf("ClaimReady: %v", err)
	}
	if err := s.AdvancePhase(ctx, id, card.PhasePlanning, card.ActorAgent, "worker-a", "spec done"); err != nil {
		t.Fatalf("AdvancePhase: %v", err)
	}

	c, err := s.GetCard(ctx, id)
	if err != nil {
		t.Fatalf("GetCard: %v", err)
	}
	if c.ClaimedBy == nil || *c.ClaimedBy != "worker-a" {
		t.Errorf("claimed_by = %v, want worker-a still holding its own card mid-step", c.ClaimedBy)
	}
}
