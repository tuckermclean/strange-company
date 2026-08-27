package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/specgate"
)

func inProgressCard(t *testing.T, s *Store) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := seedBacklogCard(t, s)
	if err := s.PutSpec(ctx, id, "# Context\n\nx", "someone"); err != nil {
		t.Fatal(err)
	}
	// Backlog -> Ready only goes through the gate, which is the guard doing
	// its job: nothing may promote a card by transitioning around it.
	if err := s.PromoteToReady(ctx, id, specgate.Result{Passed: true}, card.ActorHuman, "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.Transition(ctx, id, card.InProgress, card.ActorAgent, "worker-1", "claimed"); err != nil {
		t.Fatal(err)
	}
	return id
}

// §11's phases all happen while a card is InProgress, and the state machine
// has no InProgress -> InProgress transition. Finishing a phase therefore has
// to be representable without a state change, or planning could not end.
func TestAPhaseAdvancesWithoutChangingState(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	id := inProgressCard(t, s)

	if err := s.AdvancePhase(ctx, id, card.PhasePlanning, card.ActorAgent, "worker-1", "plan written"); err != nil {
		t.Fatalf("AdvancePhase: %v", err)
	}

	c, err := s.GetCard(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if c.Phase != card.PhasePlanning {
		t.Errorf("phase = %q, want planning", c.Phase)
	}
	if c.State != card.InProgress {
		t.Errorf("state = %q; advancing a phase moved the card", c.State)
	}
}

// §21: every change to a card is explained in the audit log. A phase advancing
// with nothing on record would leave "what happened to card X?" with a gap
// exactly where the expensive work happens.
func TestAdvancingAPhaseIsRecordedInHistory(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	id := inProgressCard(t, s)

	if err := s.AdvancePhase(ctx, id, card.PhasePlanning, card.ActorAgent, "worker-1", "plan written"); err != nil {
		t.Fatal(err)
	}

	var reasons []string
	rows, err := s.pool.Query(ctx,
		`SELECT reason FROM card_history WHERE card_id = $1 ORDER BY id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var r *string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		if r != nil {
			reasons = append(reasons, *r)
		}
	}

	var found bool
	for _, r := range reasons {
		if r == "plan written" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no history row explains the phase change; reasons = %v", reasons)
	}
}

// Phases are a fixed sequence. An unknown one would leave a card in a phase
// nothing knows how to run, claimed and quietly stuck.
func TestAnUnknownPhaseIsRefused(t *testing.T) {
	s := migrated(t)
	id := inProgressCard(t, s)

	err := s.AdvancePhase(context.Background(), id, card.Phase("thinking"), card.ActorAgent, "worker-1", "x")
	if err == nil {
		t.Fatal("expected an error")
	}
}

// A phase only advances while work is in progress. Advancing one on a Backlog
// card would claim work is happening on a card nobody has claimed.
func TestAPhaseOnlyAdvancesWhileInProgress(t *testing.T) {
	s := migrated(t)
	id := seedBacklogCard(t, s)

	err := s.AdvancePhase(context.Background(), id, card.PhasePlanning, card.ActorAgent, "worker-1", "x")
	if !errors.Is(err, ErrPhaseNotInProgress) {
		t.Fatalf("error = %v, want ErrPhaseNotInProgress", err)
	}
}

func TestAdvancingAPhaseOnAMissingCardIsNotFound(t *testing.T) {
	s := migrated(t)

	err := s.AdvancePhase(context.Background(), uuid.New(), card.PhasePlanning, card.ActorAgent, "w", "x")
	if !errors.Is(err, ErrCardNotFound) {
		t.Fatalf("error = %v, want ErrCardNotFound", err)
	}
}
