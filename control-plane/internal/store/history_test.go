package store

import (
	"context"
	"testing"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

// card_history has been written since M0 and read by nothing. §21 requires the
// audit log to answer "what happened to card X?"; it was not reachable outside
// a psql session.
func TestHistoryReadsBackInTheOrderItHappened(t *testing.T) {
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

	got, err := s.ListHistory(ctx, id, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("history = %+v, want the claim and the transition", got)
	}
	if got[len(got)-1].To != string(card.Review) {
		t.Errorf("last entry = %+v, want the newest last", got[len(got)-1])
	}
	if got[len(got)-1].Reason != "review passed" {
		t.Errorf("reason = %q, want the reason recorded with the move", got[len(got)-1].Reason)
	}
	if got[len(got)-1].At.IsZero() {
		t.Error("history entry has no timestamp; §33 asks for history WITH timestamps")
	}
}

// A card that has churned for hours has a long history, and the newest entries
// are the ones explaining where it is now.
func TestHistoryIsCappedFromTheNewestEnd(t *testing.T) {
	s := openTestStore(t)
	id := seedReadyCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Ready -> InProgress -> Ready, repeatedly: the Meeseeks lifecycle.
	for i := 0; i < 6; i++ {
		if _, err := s.ClaimReady(ctx, "worker-a", time.Minute); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if err := s.Release(ctx, id, "worker-a", "phase advanced"); err != nil {
			t.Fatalf("release %d: %v", i, err)
		}
	}

	got, err := s.ListHistory(ctx, id, 3)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("history = %d entries, want the cap of 3", len(got))
	}
	// The cap must drop the OLDEST, not the newest.
	if got[len(got)-1].Reason != "phase advanced" {
		t.Errorf("newest entry = %+v; the cap dropped the wrong end", got[len(got)-1])
	}
}
