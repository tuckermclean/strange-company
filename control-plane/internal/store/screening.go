package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrSpecChanged is returned when a screening result is recorded against a
// document that is no longer the current one.
//
// That means the specification was edited while the model call was in flight.
// Storing the result anyway would mark the NEW text as screened using the OLD
// text's answer -- a stale score on a document nobody screened.
var ErrSpecChanged = errors.New("store: specification changed while it was being screened")

// PendingScreening is one specification awaiting §10.1 screening.
type PendingScreening struct {
	CardID        uuid.UUID
	Content       string
	ContentSHA256 string

	// Score is the recorded screening score. It is meaningful only for
	// rows from ListSpecsAwaitingConversation, where the screening already
	// happened; it is zero for rows still awaiting one.
	Score int
}

// ListSpecsNeedingScreening returns up to limit specifications whose current
// content has not been screened.
//
// The limit is load-bearing, not advisory: without it one pass over a large
// backlog would issue a model call per card at once.
func (s *Store) ListSpecsNeedingScreening(ctx context.Context, limit int) ([]PendingScreening, error) {
	if limit <= 0 {
		return nil, nil
	}

	// The comparison is against the CURRENT content's hash, computed in the
	// query, so an edited specification becomes pending again without any
	// bookkeeping at write time. A NULL screened_sha256 (never screened) is
	// pending by the same expression.
	const q = `
		SELECT card_id, content, encode(sha256(content::bytea), 'hex')
		  FROM card_specs
		 WHERE content <> ''
		   AND (screened_sha256 IS NULL
		        OR screened_sha256 IS DISTINCT FROM encode(sha256(content::bytea), 'hex'))
		 ORDER BY updated_at
		 LIMIT $1`

	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("store: listing specifications to screen: %w", err)
	}
	defer rows.Close()

	var pending []PendingScreening
	for rows.Next() {
		var p PendingScreening
		if err := rows.Scan(&p.CardID, &p.Content, &p.ContentSHA256); err != nil {
			return nil, fmt.Errorf("store: reading specification to screen: %w", err)
		}
		pending = append(pending, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing specifications to screen: %w", err)
	}
	return pending, nil
}

// RecordScreening stores the score screening produced for a specific document.
//
// contentSHA256 must be the hash of the document that was actually screened;
// see ErrSpecChanged for why a mismatch is refused rather than overwritten.
func (s *Store) RecordScreening(ctx context.Context, cardID uuid.UUID, contentSHA256 string, score int) error {
	const q = `
		UPDATE card_specs
		   SET screened_sha256 = $2,
		       screened_score  = $3,
		       screened_at     = now()
		 WHERE card_id = $1
		   AND encode(sha256(content::bytea), 'hex') = $2`

	tag, err := s.pool.Exec(ctx, q, cardID, contentSHA256, score)
	if err != nil {
		return fmt.Errorf("store: recording screening: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	// Nothing updated: either there is no specification for this card, or
	// the one there is no longer matches the hash that was screened. Those
	// are different problems for the caller, so distinguish them.
	var exists bool
	err = s.pool.QueryRow(ctx, `SELECT true FROM card_specs WHERE card_id = $1`, cardID).Scan(&exists)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSpecNotFound
		}
		return fmt.Errorf("store: recording screening: %w", err)
	}
	return ErrSpecChanged
}

// ListSpecsAwaitingConversation returns specifications that screening already
// judged to need a human, but which have no conversation open.
//
// This is the retry path for a card whose screening succeeded and whose
// session could not be created -- a gateway outage, most likely. Without it,
// the only way back would be to re-screen, paying for the same answer again.
func (s *Store) ListSpecsAwaitingConversation(ctx context.Context, limit int) ([]PendingScreening, error) {
	if limit <= 0 {
		return nil, nil
	}

	// screened_sha256 must still match the current content: a specification
	// edited since screening belongs on the screening path, not this one.
	const q = `
		SELECT s.card_id, s.content, s.screened_sha256, s.screened_score
		  FROM card_specs s
		  JOIN cards c ON c.id = s.card_id
		 WHERE s.screened_score >= $1
		   AND s.screened_sha256 = encode(sha256(s.content::bytea), 'hex')
		   AND c.spec_session_id IS NULL
		 ORDER BY s.screened_at
		 LIMIT $2`

	rows, err := s.pool.Query(ctx, q, scoreRequiringHuman, limit)
	if err != nil {
		return nil, fmt.Errorf("store: listing specifications awaiting a conversation: %w", err)
	}
	defer rows.Close()

	var pending []PendingScreening
	for rows.Next() {
		var p PendingScreening
		if err := rows.Scan(&p.CardID, &p.Content, &p.ContentSHA256, &p.Score); err != nil {
			return nil, fmt.Errorf("store: reading specification awaiting a conversation: %w", err)
		}
		pending = append(pending, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing specifications awaiting a conversation: %w", err)
	}
	return pending, nil
}

// scoreRequiringHuman mirrors spec 10.1: scores 2 and 3 need a human. It lives
// here only so this query can filter in SQL; the authority on what the score
// means is ambiguity.Report.RequiresHuman, and specsession.Opener re-checks it
// before opening anything.
const scoreRequiringHuman = 2
