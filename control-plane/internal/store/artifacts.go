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
	"github.com/jackc/pgx/v5/pgconn"
)

// Artifact types, spec §20. The list is the contract: an unknown type is a
// caller bug, and accepting one would let a typo produce evidence nothing ever
// looks for.
const (
	ArtifactSpec               = "spec"
	ArtifactAmbiguityReport    = "ambiguity-report"
	ArtifactImplementationPlan = "implementation-plan"
	ArtifactTestMapping        = "test-mapping"
	ArtifactTestOutput         = "test-output"

	// ArtifactRunLog is a harness run's complete output, stored for EVERY
	// run rather than only failing ones.
	//
	// It is the raw discourse: what the agent actually said and did. §21
	// keeps model reasoning out of the stakeholder view, and this is why
	// that rule needs a second surface rather than a deletion -- when
	// something breaks, this is the only record of it. The Job is deleted
	// as soon as its logs are read, so nothing else survives.
	ArtifactRunLog = "run-log"

	// ArtifactModelExchange is one model call in full: what was asked and
	// what came back.
	//
	// The coding phases keep their harness transcript; the model phases kept
	// only the answer, so planning and review recorded a verdict with no
	// record of the question that produced it. Reviewing that is guesswork:
	// §18's whole claim is that the reviewer saw the diff, and nothing
	// stored what it actually saw.
	ArtifactModelExchange = "model-exchange"
	ArtifactDiff               = "diff"
	ArtifactCompilerOutput     = "compiler-output"
	ArtifactLinterOutput       = "linter-output"
	ArtifactSecurityOutput     = "security-output"
	ArtifactReview             = "review"
	ArtifactCostReport         = "cost-report"
	ArtifactFailureSummary     = "failure-summary"
	ArtifactHumanDecision      = "human-decision"
)

var artifactTypes = map[string]bool{
	ArtifactSpec: true, ArtifactAmbiguityReport: true, ArtifactImplementationPlan: true,
	ArtifactTestMapping: true, ArtifactTestOutput: true, ArtifactDiff: true,
	ArtifactCompilerOutput: true, ArtifactLinterOutput: true, ArtifactSecurityOutput: true,
	ArtifactReview: true, ArtifactCostReport: true, ArtifactFailureSummary: true,
	ArtifactHumanDecision: true,
}

// MaxArtifactBytes caps what is stored inline.
//
// §20: small text artifacts may live in PostgreSQL and large logs are capped.
// A megabyte holds any plan, diff or review worth reading, and a test log
// longer than that is being read for its tail anyway.
const MaxArtifactBytes = 1 << 20

// ErrUnknownArtifactType is returned for a type §20 does not define.
var ErrUnknownArtifactType = errors.New("store: unknown artifact type")

// Artifact is one piece of evidence about a card.
//
// §21: the stakeholder view is built from these and must answer "what happened
// to card X?" WITHOUT exposing chain-of-thought. These are outputs -- plans,
// diffs, logs, reviews -- never reasoning.
type Artifact struct {
	ID        uuid.UUID
	CardID    uuid.UUID
	AttemptID *int64

	Type        string
	Actor       string
	Model       string
	CommitSHA   string
	ContentType string
	StorageURI  string
	Content     string

	// SHA256 and SizeBytes describe the COMPLETE content, even when
	// Content holds only the capped prefix of it.
	SHA256    string
	SizeBytes int64
	Truncated bool
}

// PutArtifact stores one artifact and returns it as stored.
//
// Artifacts are never updated. Attempt 4's diff does not replace attempt 3's,
// or the cost ledger and the escalation record describe a history that no
// longer exists.
func (s *Store) PutArtifact(ctx context.Context, a Artifact) (*Artifact, error) {
	if !artifactTypes[a.Type] {
		return nil, fmt.Errorf("%w: %q", ErrUnknownArtifactType, a.Type)
	}
	if a.CardID == uuid.Nil {
		return nil, errors.New("store: artifact needs a card")
	}
	if strings.TrimSpace(a.Actor) == "" {
		return nil, errors.New("store: artifact needs an actor")
	}
	if strings.TrimSpace(a.ContentType) == "" {
		return nil, errors.New("store: artifact needs a content type")
	}

	// Hashed and measured BEFORE capping: the hash has to describe what the
	// run produced, not the prefix that fitted.
	sum := sha256.Sum256([]byte(a.Content))
	a.SHA256 = hex.EncodeToString(sum[:])
	a.SizeBytes = int64(len(a.Content))

	stored := a.Content
	if len(stored) > MaxArtifactBytes {
		stored = stored[:MaxArtifactBytes]
		a.Truncated = true
	}

	a.ID = uuid.New()

	const q = `
		INSERT INTO artifacts (id, card_id, attempt_id, type, actor, model, commit_sha,
		                       content_type, storage_uri, content, sha256, size_bytes, truncated)
		VALUES ($1,$2,$3,$4,$5,nullif($6,''),nullif($7,''),$8,$9,$10,$11,$12,$13)`

	_, err := s.pool.Exec(ctx, q, a.ID, a.CardID, a.AttemptID, a.Type, a.Actor, a.Model,
		a.CommitSHA, a.ContentType, a.StorageURI, stored, a.SHA256, a.SizeBytes, a.Truncated)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return nil, ErrCardNotFound
		}
		return nil, fmt.Errorf("store: storing artifact: %w", err)
	}

	a.Content = stored
	return &a, nil
}

// GetArtifact returns one artifact.
func (s *Store) GetArtifact(ctx context.Context, id uuid.UUID) (*Artifact, error) {
	const q = `
		SELECT id, card_id, attempt_id, type, actor, coalesce(model,''), coalesce(commit_sha,''),
		       content_type, storage_uri, content, sha256, size_bytes, truncated
		  FROM artifacts WHERE id = $1`

	var a Artifact
	err := s.pool.QueryRow(ctx, q, id).Scan(&a.ID, &a.CardID, &a.AttemptID, &a.Type, &a.Actor,
		&a.Model, &a.CommitSHA, &a.ContentType, &a.StorageURI, &a.Content, &a.SHA256,
		&a.SizeBytes, &a.Truncated)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("store: artifact not found")
		}
		return nil, fmt.Errorf("store: reading artifact: %w", err)
	}
	return &a, nil
}

// ListArtifacts returns a card's artifacts in the order they were produced,
// which is the order "what happened to card X?" wants them in.
func (s *Store) ListArtifacts(ctx context.Context, cardID uuid.UUID) ([]*Artifact, error) {
	const q = `
		SELECT id, card_id, attempt_id, type, actor, coalesce(model,''), coalesce(commit_sha,''),
		       content_type, storage_uri, content, sha256, size_bytes, truncated
		  FROM artifacts
		 WHERE card_id = $1
		 ORDER BY created_at, id`

	rows, err := s.pool.Query(ctx, q, cardID)
	if err != nil {
		return nil, fmt.Errorf("store: listing artifacts: %w", err)
	}
	defer rows.Close()

	var out []*Artifact
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.CardID, &a.AttemptID, &a.Type, &a.Actor, &a.Model,
			&a.CommitSHA, &a.ContentType, &a.StorageURI, &a.Content, &a.SHA256,
			&a.SizeBytes, &a.Truncated); err != nil {
			return nil, fmt.Errorf("store: reading artifact: %w", err)
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}
