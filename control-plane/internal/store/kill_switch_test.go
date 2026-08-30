package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

// An operator watching a card burn a reasoning call every four minutes needs a
// lever that is not "ship a new image". Blocked is that lever, and these tests
// exist because "I believe Blocked stops it" is not something to tell an
// operator without proof.
func TestABlockedCardIsNotClaimedAgain(t *testing.T) {
	s := openTestStore(t)
	id := seedReadyCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.Transition(ctx, id, card.Blocked, card.ActorHuman, "operator", "stop burning money on this"); err != nil {
		t.Fatalf("block: %v", err)
	}

	if _, err := s.ClaimReady(ctx, "worker-a", time.Minute); !errors.Is(err, ErrNoWork) {
		t.Fatalf("ClaimReady returned %v; a blocked card must not be picked up again", err)
	}
}

// The same lever from mid-flight: a worker is holding the card when the
// operator blocks it.
func TestBlockingACardInFlightTakesItFromTheWorker(t *testing.T) {
	s := openTestStore(t)
	id := seedReadyCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := s.ClaimReady(ctx, "worker-a", time.Minute); err != nil {
		t.Fatalf("ClaimReady: %v", err)
	}
	if err := s.Transition(ctx, id, card.Blocked, card.ActorHuman, "operator", "stop"); err != nil {
		t.Fatalf("block: %v", err)
	}

	c, err := s.GetCard(ctx, id)
	if err != nil {
		t.Fatalf("GetCard: %v", err)
	}
	if c.ClaimedBy != nil {
		t.Errorf("claimed_by = %q on a blocked card", *c.ClaimedBy)
	}

	// The worker's step finishes and asks for the move it wanted. It must
	// not be able to undo the block: an operator's stop that a worker can
	// overwrite half a second later is not a stop.
	err = s.Transition(ctx, id, card.Review, card.ActorAgent, "worker-a", "review passed")
	if err == nil {
		t.Fatal("a worker moved a blocked card to Review, overwriting the operator")
	}
	if !errors.Is(err, card.ErrIllegalTransition) {
		t.Errorf("error = %v, want an illegal-transition rejection", err)
	}
}

// Stopping a card must not mean losing it. Blocked -> Ready is human-only, so
// the operator who stopped it is the one who can start it again.
func TestABlockedCardCanBeResumedByAHuman(t *testing.T) {
	s := openTestStore(t)
	id := seedReadyCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.Transition(ctx, id, card.Blocked, card.ActorHuman, "operator", "stop"); err != nil {
		t.Fatalf("block: %v", err)
	}
	if err := s.Transition(ctx, id, card.Ready, card.ActorHuman, "operator", "cause fixed"); err != nil {
		t.Fatalf("resume: %v", err)
	}

	c, err := s.ClaimReady(ctx, "worker-b", time.Minute)
	if err != nil {
		t.Fatalf("ClaimReady after resume: %v", err)
	}
	if c == nil || c.ID != id {
		t.Fatalf("claimed %v, want the resumed card", c)
	}
}

// An agent must not be able to park a card out of reach on its own: Blocked is
// escapable only by a human, so an agent blocking a card would be an agent
// deciding a human is needed without saying so.
func TestAnAgentCannotResumeABlockedCard(t *testing.T) {
	s := openTestStore(t)
	id := seedReadyCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.Transition(ctx, id, card.Blocked, card.ActorHuman, "operator", "stop"); err != nil {
		t.Fatalf("block: %v", err)
	}
	if err := s.Transition(ctx, id, card.Ready, card.ActorAgent, "worker-a", "I would like to continue"); err == nil {
		t.Fatal("an agent released its own block; only a human may")
	}
}
