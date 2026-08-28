package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// CardEvidence is a worker's account of one step (spec §21).
//
// Separate from an Artifact (§20): an artifact is a thing a run produced and
// is addressable on its own; evidence exists to make a transition legible.
type CardEvidence struct {
	ActorID string
	Summary string

	// Detail holds facts about the step -- phase, attempt, error. §21
	// forbids exposing model chain-of-thought, so reasoning never goes
	// here.
	Detail map[string]any
}

// AttachEvidence records a worker's account of a step.
//
// Called before a transition, which is why it cannot live on the history row:
// at that moment there is no history row yet, and §21 requires that a card
// never arrive in a new state with nothing explaining why.
func (s *Store) AttachEvidence(ctx context.Context, cardID uuid.UUID, ev CardEvidence) error {
	if strings.TrimSpace(ev.ActorID) == "" {
		return errors.New("store: evidence needs an actor")
	}
	if strings.TrimSpace(ev.Summary) == "" {
		return errors.New("store: evidence needs a summary; evidence that explains nothing is the one thing it cannot be")
	}

	var detail []byte
	if len(ev.Detail) > 0 {
		var err error
		if detail, err = json.Marshal(ev.Detail); err != nil {
			return fmt.Errorf("store: encoding evidence detail: %w", err)
		}
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO card_evidence (card_id, actor_id, summary, detail)
		VALUES ($1, $2, $3, $4)`, cardID, ev.ActorID, ev.Summary, detail)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrCardNotFound
		}
		return fmt.Errorf("store: attaching evidence: %w", err)
	}
	return nil
}

// ListEvidence returns a card's evidence in the order it was recorded.
func (s *Store) ListEvidence(ctx context.Context, cardID uuid.UUID) ([]CardEvidence, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT actor_id, summary, detail
		  FROM card_evidence WHERE card_id = $1 ORDER BY id`, cardID)
	if err != nil {
		return nil, fmt.Errorf("store: listing evidence: %w", err)
	}
	defer rows.Close()

	var out []CardEvidence
	for rows.Next() {
		var ev CardEvidence
		var detail []byte
		if err := rows.Scan(&ev.ActorID, &ev.Summary, &detail); err != nil {
			return nil, fmt.Errorf("store: reading evidence: %w", err)
		}
		if len(detail) > 0 {
			if err := json.Unmarshal(detail, &ev.Detail); err != nil {
				return nil, fmt.Errorf("store: decoding evidence detail: %w", err)
			}
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}
