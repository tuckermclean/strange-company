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

func noteOutcome(t *testing.T, s *Store, id uuid.UUID, ran bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.NoteStepOutcome(ctx, id, ran); err != nil {
		t.Fatalf("NoteStepOutcome(%v): %v", ran, err)
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

	noteOutcome(t, s, id, false)
	noteOutcome(t, s, id, false)
	if got := infraCount(t, s, id); got != 2 {
		t.Fatalf("infrastructure_failures = %d, want 2", got)
	}

	// A step that ran clears it. What the bound asks is whether the card can
	// make progress now, not whether it has ever had a bad day.
	noteOutcome(t, s, id, true)
	if got := infraCount(t, s, id); got != 0 {
		t.Errorf("infrastructure_failures = %d after a step that ran, want 0", got)
	}
}

// NoteStepOutcome is the single writer, and that is the point: RecordAttempt
// only ever saw steps that reached a model, so a step failing earlier -- an
// unresolvable policy, a Hermes session whose title already existed, a
// decomposition whose children could not be written -- was invisible to the
// bound built to stop it. Six unbounded loops hid there.
func TestNoteStepOutcomeIsTheSingleCounterOfInfrastructure(t *testing.T) {
	s := openTestStore(t)
	id := seedReadyCard(t, s)

	// A run that reached a model and failed on the merits moves the ladder,
	// never this counter.
	recordStatus(t, s, id, runner.StatusInfraError)
	if got := infraCount(t, s, id); got != 0 {
		t.Errorf("RecordAttempt moved infrastructure_failures to %d; it no longer owns that counter", got)
	}

	noteOutcome(t, s, id, false)
	if got := infraCount(t, s, id); got != 1 {
		t.Errorf("infrastructure_failures = %d after a failed step, want 1", got)
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
		noteOutcome(t, s, id, false)
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
