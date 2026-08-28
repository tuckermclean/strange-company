package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrSpecSessionExists is returned when a card already points at a different
// specification conversation.
//
// Opening a second one would split the human's context across two dashboard
// sessions while the card could only ever point at one of them, so this is a
// refusal rather than an overwrite.
var ErrSpecSessionExists = errors.New("store: card already has a specification session")

// RecordSpecSession points a card at the Hermes session holding its
// specification conversation.
//
// Recording the same id twice succeeds: that is what a retry after a lost
// response looks like, and the outcome the caller wanted is already true.
func (s *Store) RecordSpecSession(ctx context.Context, cardID uuid.UUID, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("store: specification session id is required")
	}

	// One statement, so a concurrent recorder cannot slip between a read
	// and a write and leave the card pointing at a session nobody was told
	// about. The WHERE clause is what makes it a claim rather than an
	// overwrite; a row that fails it is either absent or already taken, and
	// the follow-up read distinguishes those two for the caller.
	const q = `
		UPDATE cards
		   SET spec_session_id = $2,
		       updated_at = now()
		 WHERE id = $1
		   AND (spec_session_id IS NULL OR spec_session_id = $2)`

	tag, err := s.pool.Exec(ctx, q, cardID, sessionID)
	if err != nil {
		return fmt.Errorf("store: recording specification session: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	existing, err := s.GetSpecSession(ctx, cardID)
	if err != nil {
		return err
	}
	if existing == "" {
		return ErrCardNotFound
	}
	return fmt.Errorf("%w: %s", ErrSpecSessionExists, existing)
}

// GetSpecSession returns the Hermes session id recorded for a card, or the
// empty string when no conversation has been opened.
func (s *Store) GetSpecSession(ctx context.Context, cardID uuid.UUID) (string, error) {
	const q = `SELECT coalesce(spec_session_id, '') FROM cards WHERE id = $1`

	var sessionID string
	if err := s.pool.QueryRow(ctx, q, cardID).Scan(&sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrCardNotFound
		}
		return "", fmt.Errorf("store: reading specification session: %w", err)
	}
	return sessionID, nil
}

// CardTask pairs a card with the Vikunja task projecting it.
type CardTask struct {
	CardID uuid.UUID
	TaskID int64
}

// ListUnapprovedWithTasks returns Backlog cards that have a specification, are
// projected onto a Vikunja task, and whose specification no human has approved
// as it currently reads.
//
// These are exactly the cards a human could approve from the board, and no
// others: a card already approved must not be looked at again, or the approval
// label would be re-applied to text that has since changed.
func (s *Store) ListUnapprovedWithTasks(ctx context.Context, limit int) ([]CardTask, error) {
	if limit <= 0 {
		return nil, nil
	}

	const q = `
		SELECT c.id, c.vikunja_task_id
		  FROM cards c
		  JOIN card_specs s ON s.card_id = c.id
		 WHERE c.state = 'Backlog'
		   AND c.vikunja_task_id IS NOT NULL
		   AND s.content <> ''
		   AND (s.approved_sha256 IS NULL
		        OR s.approved_sha256 IS DISTINCT FROM encode(sha256(s.content::bytea), 'hex'))
		 ORDER BY s.updated_at
		 LIMIT $1`

	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("store: listing cards awaiting approval: %w", err)
	}
	defer rows.Close()

	var out []CardTask
	for rows.Next() {
		var ct CardTask
		if err := rows.Scan(&ct.CardID, &ct.TaskID); err != nil {
			return nil, fmt.Errorf("store: reading a card awaiting approval: %w", err)
		}
		out = append(out, ct)
	}
	return out, rows.Err()
}
