package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrSpecNotFound is returned when an operation references a card that has
// no specification document stored for it.
var ErrSpecNotFound = errors.New("specification not found")

// ErrUnattributedApproval is returned by ApproveSpec when approvedBy is
// empty. Spec §21 requires every audit record to name an actor_id; an
// approval nobody is named on would corrupt that trail, so it is refused
// outright rather than stored as an approval by "".
var ErrUnattributedApproval = errors.New("store: approval must be attributed to someone")

// CardSpec is the specification document stored for one card, together with
// whatever is known about a human approving it.
//
// Approved is computed, not stored directly: it is true only when
// approved_sha256 in the database matches sha256(Content) as it reads right
// now. ApprovedBy and ApprovedAt are reported only when Approved is true --
// spec §10.2 requires a human to approve "the completed spec" (a specific
// document), so an approval whose document has since changed is not
// meaningfully "by" anyone as far as this type's callers are concerned, even
// if a row bypassing PutSpec left stale values sitting in the columns.
type CardSpec struct {
	CardID    uuid.UUID
	Content   string
	UpdatedAt time.Time
	UpdatedBy string

	// ApprovedBy is "" when unapproved.
	ApprovedBy string
	ApprovedAt *time.Time

	// Approved is true iff approved_sha256 matches sha256(Content).
	Approved bool
}

// contentHash returns the hex-encoded sha256 digest of content -- the value
// stored in approved_sha256 by ApproveSpec and recomputed by GetSpec to
// decide whether a stored approval still applies to the current document.
func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// PutSpec upserts the specification document for cardID.
//
// Writing new content always clears any existing approval (approved_by,
// approved_at and approved_sha256 are reset to NULL), whether this call is
// creating the spec for the first time or editing one that already exists.
// Spec §10.2 says a human approves "the completed spec" -- a specific
// document -- so editing it after approval must revoke that approval, or
// approval could be obtained on one document (a harmless draft) and then
// used to promote a different one.
func (s *Store) PutSpec(ctx context.Context, cardID uuid.UUID, content, updatedBy string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO card_specs (card_id, content, updated_at, updated_by, approved_by, approved_at, approved_sha256)
		VALUES ($1::uuid, $2, now(), $3, NULL, NULL, NULL)
		ON CONFLICT (card_id) DO UPDATE
		SET content         = EXCLUDED.content,
		    updated_at      = EXCLUDED.updated_at,
		    updated_by      = EXCLUDED.updated_by,
		    approved_by     = NULL,
		    approved_at     = NULL,
		    approved_sha256 = NULL
	`, cardID.String(), content, updatedBy)
	if err != nil {
		return fmt.Errorf("store: put spec for card %s: %w", cardID, err)
	}
	return nil
}

// GetSpec loads the specification document for cardID, or ErrSpecNotFound
// if none has ever been written.
func (s *Store) GetSpec(ctx context.Context, cardID uuid.UUID) (*CardSpec, error) {
	var (
		content     string
		updatedAt   time.Time
		updatedBy   string
		approvedBy  *string
		approvedAt  *time.Time
		approvedSHA *string
	)

	err := s.pool.QueryRow(ctx, `
		SELECT content, updated_at, updated_by, approved_by, approved_at, approved_sha256
		FROM card_specs
		WHERE card_id = $1::uuid
	`, cardID.String()).Scan(&content, &updatedAt, &updatedBy, &approvedBy, &approvedAt, &approvedSHA)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSpecNotFound
		}
		return nil, fmt.Errorf("store: get spec for card %s: %w", cardID, err)
	}

	spec := &CardSpec{
		CardID:    cardID,
		Content:   content,
		UpdatedAt: updatedAt,
		UpdatedBy: updatedBy,
		Approved:  approvedSHA != nil && *approvedSHA == contentHash(content),
	}

	// Approval details are surfaced only when the approval still applies to
	// the current content -- see the Approved field's doc comment.
	if spec.Approved {
		if approvedBy != nil {
			spec.ApprovedBy = *approvedBy
		}
		spec.ApprovedAt = approvedAt
	}

	return spec, nil
}

// ApproveSpec records that approvedBy has approved cardID's specification as
// it currently reads, storing the sha256 of that exact content (spec §10.2:
// "Human approves the completed spec. Only then may the control plane
// promote the card to Ready.").
//
// approvedBy must be non-empty: spec §21 requires every audit record to name
// an actor_id, and an approval with no attributed approver would corrupt
// that trail. Approving a card with no stored specification is
// ErrSpecNotFound.
func (s *Store) ApproveSpec(ctx context.Context, cardID uuid.UUID, approvedBy string) error {
	if strings.TrimSpace(approvedBy) == "" {
		return fmt.Errorf("store: approve spec for card %s: %w", cardID, ErrUnattributedApproval)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin approve-spec transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var content string
	err = tx.QueryRow(ctx, `
		SELECT content
		FROM card_specs
		WHERE card_id = $1::uuid
		FOR UPDATE
	`, cardID.String()).Scan(&content)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSpecNotFound
		}
		return fmt.Errorf("store: load spec %s for approval: %w", cardID, err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE card_specs
		SET approved_by     = $1,
		    approved_at     = $2,
		    approved_sha256 = $3
		WHERE card_id = $4::uuid
	`, approvedBy, time.Now(), contentHash(content), cardID.String()); err != nil {
		return fmt.Errorf("store: approve spec for card %s: %w", cardID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit approve-spec transaction: %w", err)
	}

	return nil
}
