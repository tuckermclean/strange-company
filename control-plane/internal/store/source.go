package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
		                   repo_url, repo_base_ref, state, phase)
		VALUES ($1, $2, $3, nullif($4,''), $5, nullif($6,''), nullif($7,''), 'Backlog', 'specification')
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
		in.RepoURL, in.RepoRef).Scan(&id, &created)
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
