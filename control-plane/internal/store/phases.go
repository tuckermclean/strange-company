package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

// ErrPhaseNotInProgress is returned when a phase advance is attempted on a
// card that is not being worked on.
//
// A phase describes what a worker is doing right now (spec §11). Advancing one
// on a Backlog card would claim work is happening on a card nobody has
// claimed.
var ErrPhaseNotInProgress = errors.New("store: a phase only advances while a card is InProgress")

// AdvancePhase moves a card to its next §11 phase without changing its state.
//
// §11's phases -- planning, tests, implementation -- all happen while the card
// is InProgress, and the state machine deliberately has no InProgress ->
// InProgress transition. So finishing a phase cannot be expressed as a state
// change, and this is how it is expressed instead.
//
// The move and its history row are one transaction: §21 requires every change
// to be explained, and a crash between them would leave a card in a phase with
// nothing on record about how it got there -- exactly the gap "what happened to
// card X?" cannot have.
func (s *Store) AdvancePhase(ctx context.Context, cardID uuid.UUID, to card.Phase, actor card.ActorType, actorID, reason string) error {
	if !card.ValidPhase(to) {
		return fmt.Errorf("store: unknown phase %q", to)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: advancing phase: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var state, from string
	err = tx.QueryRow(ctx,
		`SELECT state, phase FROM cards WHERE id = $1 FOR UPDATE`, cardID).Scan(&state, &from)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCardNotFound
		}
		return fmt.Errorf("store: advancing phase: %w", err)
	}
	if card.State(state) != card.InProgress {
		return fmt.Errorf("%w (card is %s)", ErrPhaseNotInProgress, state)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE cards SET phase = $2, updated_at = now() WHERE id = $1`, cardID, string(to)); err != nil {
		return fmt.Errorf("store: advancing phase: %w", err)
	}

	// from_state and to_state are both the unchanged state: this row exists
	// to explain a phase move, and reporting a state change that did not
	// happen would be worse than saying nothing.
	if _, err := tx.Exec(ctx, `
		INSERT INTO card_history (card_id, from_state, to_state, actor_type, actor_id, reason)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		cardID, state, state, string(actor), actorID, reason); err != nil {
		return fmt.Errorf("store: recording phase change: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: advancing phase: %w", err)
	}
	return nil
}
