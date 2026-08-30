package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/runner"
)

func recordStatus(t *testing.T, s *Store, id uuid.UUID, status runner.Status) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := s.RecordAttempt(ctx, AttemptRecord{
		CardID: id, RunID: "r", Phase: "implementation",
		ModelAlias: "a", Provider: "p", Harness: "h", Model: "m",
		Result: &runner.CodingRunResult{Status: status},
	}); err != nil {
		t.Fatalf("RecordAttempt(%s): %v", status, err)
	}
}

func infraCount(t *testing.T, s *Store, id uuid.UUID) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := s.GetCard(ctx, id)
	if err != nil {
		t.Fatalf("GetCard: %v", err)
	}
	return c.InfrastructureFailures
}

// The bound on this counter exists to stop a card retrying something that
// cannot work. A lifetime total answers a different question, and answering it
// badly is how a card whose cause was fixed escalated anyway on its next claim,
// having never once been retried under the fix.
func TestAnInfrastructureRunOfBadLuckIsBrokenByOneGoodRun(t *testing.T) {
	s := openTestStore(t)
	id := seedReadyCard(t, s)

	recordStatus(t, s, id, runner.StatusInfraError)
	recordStatus(t, s, id, runner.StatusInfraError)
	if got := infraCount(t, s, id); got != 2 {
		t.Fatalf("infrastructure_failures = %d, want 2", got)
	}

	// The model was reached, it did the work, the work was wrong. Nothing
	// about that says the card cannot run.
	recordStatus(t, s, id, runner.StatusFailed)
	if got := infraCount(t, s, id); got != 0 {
		t.Errorf("infrastructure_failures = %d after a run that reached the model, want 0", got)
	}
}

func TestACompletedRunAlsoClearsTheCount(t *testing.T) {
	s := openTestStore(t)
	id := seedReadyCard(t, s)

	recordStatus(t, s, id, runner.StatusTimeout)
	recordStatus(t, s, id, runner.StatusCompleted)

	if got := infraCount(t, s, id); got != 0 {
		t.Errorf("infrastructure_failures = %d after a completed run, want 0", got)
	}
}

// A card only leaves NeedsHuman because someone decided it should, and what
// they mean by moving it is "try this again". Carrying the count across that
// decision makes the next claim re-escalate on failures that predate the
// intervention -- a card the human cannot return.
func TestSendingACardBackFromNeedsHumanLetsItActuallyRun(t *testing.T) {
	s := openTestStore(t)
	id := seedReadyCard(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for i := 0; i < 7; i++ {
		recordStatus(t, s, id, runner.StatusInfraError)
	}
	if err := s.Transition(ctx, id, card.NeedsHuman, card.ActorSystem, "worker", "too many infrastructure failures"); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if got := infraCount(t, s, id); got != 7 {
		t.Fatalf("infrastructure_failures = %d while parked at NeedsHuman, want the count kept for a human to read", got)
	}

	if err := s.Transition(ctx, id, card.Ready, card.ActorHuman, "tucker", "fixed the cause; try again"); err != nil {
		t.Fatalf("send back: %v", err)
	}
	if got := infraCount(t, s, id); got != 0 {
		t.Errorf("infrastructure_failures = %d after a human sent the card back, want 0", got)
	}
}
