package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

// SourceCard is one work item as its external source describes it.
//
// It carries only what the source owns. Everything about execution -- state,
// phase, claim, attempt counters, cost -- belongs to the control plane and is
// deliberately absent here, so ingestion has nothing to overwrite.
type SourceCard struct {
	// SourceType and ExternalID together identify the item. ExternalID must
	// be unique within SourceType, so it includes the repository:
	// "owner/repo#123", not "123".
	SourceType string
	ExternalID string

	URL     string
	Title   string
	Body    string
	RepoURL string
	RepoRef string

	// PermittedActions is the allowlist to stamp on the card at creation
	// (spec §5, §24). Written only when the card is new: re-ingestion
	// overwriting it would silently re-widen an allowlist someone narrowed
	// by hand, once per reconcile pass.
	PermittedActions []byte
}

func (s SourceCard) validate() error {
	var missing []string
	if strings.TrimSpace(s.SourceType) == "" {
		missing = append(missing, "source type")
	}
	if strings.TrimSpace(s.ExternalID) == "" {
		missing = append(missing, "external id")
	}
	if strings.TrimSpace(s.Title) == "" {
		missing = append(missing, "title")
	}
	if len(missing) > 0 {
		return fmt.Errorf("store: source card needs a %s", strings.Join(missing, " and a "))
	}
	return nil
}

// UpsertSourceCard creates or updates the card for one external work item and
// reports whether it was newly created.
//
// Ingestion runs on a timer, so this is called with the same item on every
// pass. Two properties make that safe, and both are load-bearing:
//
// It writes only the columns the source owns. A card that has been promoted,
// claimed, and attempted three times must not be dragged back to Backlog
// because its issue still exists upstream -- that would be a poller undoing
// work, silently, once a minute.
//
// It rewrites the specification only when the body actually changed. PutSpec
// revokes approval on every write (spec §10.2: approval is of a document), so
// an unconditional write would clear every human approval once a minute and
// nothing would ever reach Ready.
func (s *Store) UpsertSourceCard(ctx context.Context, in SourceCard) (uuid.UUID, bool, error) {
	if err := in.validate(); err != nil {
		return uuid.Nil, false, err
	}

	// ON CONFLICT against the partial unique index makes the second sighting
	// an update rather than a duplicate, without a read-then-write that two
	// concurrent passes could interleave.
	const q = `
		INSERT INTO cards (id, title, source_type, source_url, source_external_id,
		                   repo_url, repo_base_ref, state, phase, permitted_actions)
		VALUES ($1, $2, $3, nullif($4,''), $5, nullif($6,''), nullif($7,''), 'Backlog', 'specification',
		        nullif($8,'')::jsonb)
		ON CONFLICT (source_type, source_external_id) WHERE source_external_id IS NOT NULL
		DO UPDATE SET
		    title         = EXCLUDED.title,
		    source_url    = EXCLUDED.source_url,
		    repo_url      = EXCLUDED.repo_url,
		    repo_base_ref = EXCLUDED.repo_base_ref,
		    updated_at    = now()
		RETURNING id, (xmax = 0) AS created`

	var (
		id      uuid.UUID
		created bool
	)
	err := s.pool.QueryRow(ctx, q, uuid.New(), in.Title, in.SourceType, in.URL, in.ExternalID,
		in.RepoURL, in.RepoRef, string(in.PermittedActions)).Scan(&id, &created)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("store: ingesting %s %s: %w", in.SourceType, in.ExternalID, err)
	}

	if strings.TrimSpace(in.Body) != "" {
		if err := s.putSpecIfChanged(ctx, id, in.Body, in.SourceType); err != nil {
			return id, created, err
		}
	}

	return id, created, nil
}

// putSpecIfChanged writes the specification only when it differs from what is
// stored, because PutSpec revokes approval unconditionally.
func (s *Store) putSpecIfChanged(ctx context.Context, cardID uuid.UUID, content, updatedBy string) error {
	existing, err := s.GetSpec(ctx, cardID)
	switch {
	case err == nil && existing.Content == content:
		return nil
	case err != nil && !errors.Is(err, ErrSpecNotFound) && !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	return s.PutSpec(ctx, cardID, content, updatedBy)
}

// HasPermittedActions reports whether a card carries an allowlist.
//
// §10's gate needs only this boolean; the block's contents are the sandbox's
// business, not the gate's.
func (s *Store) HasPermittedActions(ctx context.Context, cardID uuid.UUID) (bool, error) {
	var has bool
	err := s.pool.QueryRow(ctx,
		`SELECT permitted_actions IS NOT NULL FROM cards WHERE id = $1`, cardID).Scan(&has)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrCardNotFound
		}
		return false, fmt.Errorf("store: reading permitted actions: %w", err)
	}
	return has, nil
}

// PermittedActions returns a card's allowlist as stored, or nil.
func (s *Store) PermittedActions(ctx context.Context, cardID uuid.UUID) ([]byte, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT coalesce(permitted_actions::text, '')::bytea FROM cards WHERE id = $1`, cardID).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCardNotFound
		}
		return nil, fmt.Errorf("store: reading permitted actions: %w", err)
	}
	return raw, nil
}

// ListApprovedAwaitingPromotion returns Backlog cards whose specification a
// human has approved as it currently reads.
//
// The hash comparison is what makes approval an approval OF A DOCUMENT: an
// edit after approval withdraws the card from this queue rather than promoting
// new text on an old approval.
func (s *Store) ListApprovedAwaitingPromotion(ctx context.Context, limit int) ([]uuid.UUID, error) {
	if limit <= 0 {
		return nil, nil
	}

	const q = `
		SELECT c.id
		  FROM cards c
		  JOIN card_specs s ON s.card_id = c.id
		 WHERE c.state = 'Backlog'
		   AND s.approved_sha256 IS NOT NULL
		   AND s.approved_sha256 = encode(sha256(s.content::bytea), 'hex')
		 ORDER BY s.approved_at
		 LIMIT $1`

	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("store: listing cards awaiting promotion: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: reading card awaiting promotion: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListDependencies returns the cards cardID depends on.
//
// §10's gate refuses a card whose dependencies are not Done. Passing an empty
// slice regardless would promote a card whose prerequisites are unfinished and
// nothing downstream would notice, so this is loaded rather than assumed.
func (s *Store) ListDependencies(ctx context.Context, cardID uuid.UUID) ([]*card.Card, error) {
	const q = `
		SELECT d.depends_on
		  FROM card_dependencies d
		 WHERE d.card_id = $1
		 ORDER BY d.depends_on`

	rows, err := s.pool.Query(ctx, q, cardID)
	if err != nil {
		return nil, fmt.Errorf("store: listing dependencies: %w", err)
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: reading dependency: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing dependencies: %w", err)
	}

	deps := make([]*card.Card, 0, len(ids))
	for _, id := range ids {
		c, err := s.GetCard(ctx, id)
		if err != nil {
			return nil, err
		}
		deps = append(deps, c)
	}
	return deps, nil
}

// ListUnapprovedWithSpec returns Backlog cards that have a specification but
// no valid approval of it.
//
// The mirror of ListApprovedAwaitingPromotion: same queue, opposite side of
// the human gate. A card whose spec was approved and then edited appears here
// again, because the approval no longer describes the document.
func (s *Store) ListUnapprovedWithSpec(ctx context.Context, limit int) ([]uuid.UUID, error) {
	if limit <= 0 {
		return nil, nil
	}

	const q = `
		SELECT c.id::text
		  FROM cards c
		  JOIN card_specs s ON s.card_id = c.id
		 WHERE c.state = 'Backlog'
		   AND coalesce(s.content, '') <> ''
		   AND (s.approved_sha256 IS NULL
		        OR s.approved_sha256 <> encode(sha256(s.content::bytea), 'hex'))
		 ORDER BY c.created_at
		 LIMIT $1`

	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list unapproved cards: %w", err)
	}
	defer rows.Close()

	var out []uuid.UUID
	for rows.Next() {
		var idText string
		if err := rows.Scan(&idText); err != nil {
			return nil, fmt.Errorf("store: scan unapproved card: %w", err)
		}
		id, err := uuid.Parse(idText)
		if err != nil {
			return nil, fmt.Errorf("store: parse card id %q: %w", idText, err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AddDependency records that cardID must wait for dependsOn.
//
// §10's gate already refuses to promote a card whose dependencies are not Done,
// so writing an edge is the whole of sequencing -- there is no scheduler and no
// second rule. The table has existed since the first migration and nothing ever
// wrote to it until decomposition did.
func (s *Store) AddDependency(ctx context.Context, cardID, dependsOn uuid.UUID) error {
	if cardID == dependsOn {
		// A card waiting for itself never promotes, and the gate would
		// report it as an unmet dependency forever with no way to tell it
		// from a real one.
		return fmt.Errorf("store: card %s cannot depend on itself", cardID)
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO card_dependencies (card_id, depends_on)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, cardID, dependsOn)
	if err != nil {
		return fmt.Errorf("store: recording that %s depends on %s: %w", cardID, dependsOn, err)
	}
	return nil
}

// CreateChild makes a card carrying its own specification, already approved,
// and records it as a piece of parent.
//
// Approved on creation because the parent's specification passed the human gate
// and a split of approved work does not silently become unapproved work: §10.2's
// approval is of the intent, and splitting does not change the intent. Asking a
// human to approve each fragment of something they already approved would make
// decomposition cost more attention than it saves.
func (s *Store) CreateChild(ctx context.Context, parent uuid.UUID, title, specText string) (uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, fmt.Errorf("store: begin child creation: %w", err)
	}
	defer tx.Rollback(ctx)

	// The child inherits where the work happens and what it is allowed to
	// do. A child with no repository or no allowlist would be refused by the
	// gate for a reason that has nothing to do with the split.
	var (
		id                       = uuid.New()
		repoURL, repoRef, source *string
		actions                  *string
	)
	err = tx.QueryRow(ctx, `
		SELECT repo_url, repo_base_ref, source_url, permitted_actions::text
		  FROM cards WHERE id = $1
	`, parent).Scan(&repoURL, &repoRef, &source, &actions)
	if err != nil {
		return uuid.Nil, fmt.Errorf("store: reading parent %s: %w", parent, err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO cards (id, title, source_type, source_url, repo_url, repo_base_ref,
		                   state, phase, risk_class, effective_priority, permitted_actions)
		VALUES ($1, $2, 'decomposed', $3, $4, $5, 'Backlog', 'specification', 'R1', 100, $6::jsonb)
	`, id, title, source, repoURL, repoRef, actions); err != nil {
		return uuid.Nil, fmt.Errorf("store: inserting child of %s: %w", parent, err)
	}

	sum := sha256.Sum256([]byte(specText))
	if _, err := tx.Exec(ctx, `
		INSERT INTO card_specs (card_id, content, approved_sha256, approved_by, approved_at)
		VALUES ($1, $2, $3, 'decomposition', now())
	`, id, specText, hex.EncodeToString(sum[:])); err != nil {
		return uuid.Nil, fmt.Errorf("store: writing the child's specification: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO card_dependencies (card_id, depends_on) VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, parent, id); err != nil {
		return uuid.Nil, fmt.Errorf("store: recording %s as a piece of %s: %w", id, parent, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, fmt.Errorf("store: commit child creation: %w", err)
	}
	return id, nil
}
