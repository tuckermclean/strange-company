package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tuckermclean/strange-company/control-plane/internal/runner"
)

// AttemptRecord is one coding run to be recorded against a card, regardless
// of whether the run ends up counting as an implementation attempt.
//
// ModelAlias, Provider, Harness and Model are supplied by the caller rather
// than read off Result: Result.Model is whatever the harness itself reported
// (spec §13), while these four identify the run against the policy that
// launched it (spec §12, §22) and map one-to-one onto the card_attempts
// columns of the same names.
type AttemptRecord struct {
	CardID     uuid.UUID
	RunID      string
	Phase      string
	ModelAlias string
	Provider   string
	Harness    string
	Model      string
	Result     *runner.CodingRunResult
}

// AttemptOutcome reports what RecordAttempt actually did: whether the run
// counted as an implementation attempt, the attempt number assigned to it
// (0 when it did not count), and the card's two escalation counters as they
// read immediately after this record was written.
type AttemptOutcome struct {
	CountedAsAttempt       bool
	AttemptNumber          int
	ImplementationAttempts int
	InfrastructureFailures int
}

// StoredAttempt is one row read back from card_attempts.
type StoredAttempt struct {
	ID                int64
	CardID            uuid.UUID
	RunID             string
	Phase             string
	AttemptNumber     *int
	ModelAlias        string
	Provider          string
	Harness           string
	Model             string
	Status            runner.Status
	CountedAsAttempt  bool
	Summary           string
	InputTokens       int
	OutputTokens      int
	CachedTokens      int
	CostUSD           *float64
	DurationMS        int64
	StartedAt         time.Time
	CreatedAt         time.Time
}

// classifyAttempt derives the spec §12.1 / §32 classification of one run
// from its terminal Status. This is the entire model-tiering thesis in two
// booleans: countsAsAttempt is true only for a genuine implementation
// failure (StatusFailed) -- verification ran and did not pass. countsAsInfra
// is true for StatusInfraError and StatusTimeout: neither is a failed idea,
// so neither may burn a Haiku/Sonnet/Opus rung. StatusCompleted and
// StatusPolicyViolation increment neither counter -- a completed run has
// nothing to retry, and §24 requires a policy violation to block the card
// outright rather than be retried harder.
func classifyAttempt(status runner.Status) (countsAsAttempt, countsAsInfra bool) {
	switch status {
	case runner.StatusFailed:
		return true, false
	case runner.StatusInfraError, runner.StatusTimeout:
		return false, true
	default:
		// runner.StatusCompleted, runner.StatusPolicyViolation, and any
		// future status this package does not yet know about: recorded for
		// audit, but burns neither counter.
		return false, false
	}
}

// RecordAttempt records one coding run against a card and, in the same
// transaction, updates whichever of the card's two escalation counters the
// run's Status implies (see classifyAttempt). Counter update and evidence
// row live in one transaction on purpose: a crash between them would either
// record an attempt without incrementing the ladder or increment the ladder
// with no evidence to show for it, and spec §21 requires the audit trail to
// be trustworthy.
//
// Returns ErrCardNotFound if cardID does not exist.
func (s *Store) RecordAttempt(ctx context.Context, rec AttemptRecord) (*AttemptOutcome, error) {
	if rec.Result == nil {
		return nil, fmt.Errorf("store: record attempt for card %s: nil result", rec.CardID)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: begin record-attempt transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	idText := rec.CardID.String()

	var implementationAttempt, infrastructureFailures int
	err = tx.QueryRow(ctx, `
		SELECT implementation_attempt, infrastructure_failures
		FROM cards
		WHERE id = $1::uuid
		FOR UPDATE
	`, idText).Scan(&implementationAttempt, &infrastructureFailures)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCardNotFound
		}
		return nil, fmt.Errorf("store: load card %s for attempt recording: %w", rec.CardID, err)
	}

	countedAsAttempt, countsAsInfra := classifyAttempt(rec.Result.Status)

	var attemptNumber *int
	if countedAsAttempt {
		implementationAttempt++
		n := implementationAttempt
		attemptNumber = &n
	}
	// The infrastructure counter is NOT touched here.
	//
	// It used to be, and that left it covering only the steps that record
	// attempts. A step failing before it ever reaches a model -- an
	// unresolvable policy, a decomposition whose children could not be
	// written -- recorded nothing, so the bound that exists to stop a card
	// retrying something impossible never saw it. Six separate unbounded
	// loops were found that way, each in a different step, each invisible to
	// the same guard.
	//
	// The worker sees EVERY step outcome, so the worker counts. One writer,
	// uniform coverage, and a step written next year is bounded without
	// having to remember to opt in.
	_ = countsAsInfra

	if _, err := tx.Exec(ctx, `
		UPDATE cards
		SET implementation_attempt = $1,
		    updated_at             = now()
		WHERE id = $2::uuid
	`, implementationAttempt, idText); err != nil {
		return nil, fmt.Errorf("store: update attempt counters for card %s: %w", rec.CardID, err)
	}

	// cached_tokens is the one column standing in for both of Usage's
	// cache-related fields (cache reads and cache-creation writes): the
	// harnesses name the same underlying concept differently (see
	// runner.Usage's doc comment), and this table tracks "tokens served
	// from or written to cache" as a single evidence figure rather than
	// splitting cost accounting across two columns.
	cachedTokens := rec.Result.Usage.CachedInputTokens + rec.Result.Usage.CacheCreationTokens

	if _, err := tx.Exec(ctx, `
		INSERT INTO card_attempts (
			card_id, run_id, phase, attempt_number, model_alias, provider, harness, model,
			status, counted_as_attempt, summary, input_tokens, output_tokens, cached_tokens,
			cost_usd, duration_ms
		) VALUES (
			$1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16
		)
	`,
		idText, rec.RunID, rec.Phase, attemptNumber, rec.ModelAlias, rec.Provider, rec.Harness, rec.Model,
		string(rec.Result.Status), countedAsAttempt, rec.Result.Summary, rec.Result.Usage.InputTokens,
		rec.Result.Usage.OutputTokens, cachedTokens, rec.Result.CostUSD, rec.Result.DurationMS,
	); err != nil {
		return nil, fmt.Errorf("store: insert attempt row for card %s: %w", rec.CardID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("store: commit record-attempt transaction: %w", err)
	}

	outcomeAttemptNumber := 0
	if attemptNumber != nil {
		outcomeAttemptNumber = *attemptNumber
	}

	return &AttemptOutcome{
		CountedAsAttempt:       countedAsAttempt,
		AttemptNumber:          outcomeAttemptNumber,
		ImplementationAttempts: implementationAttempt,
		InfrastructureFailures: infrastructureFailures,
	}, nil
}

// ListAttempts loads every recorded run for cardID, oldest first.
func (s *Store) ListAttempts(ctx context.Context, cardID uuid.UUID) ([]StoredAttempt, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			id, card_id::text, run_id, phase, attempt_number, model_alias, provider, harness,
			model, status, counted_as_attempt, summary, input_tokens, output_tokens,
			cached_tokens, cost_usd::float8, duration_ms, started_at, created_at
		FROM card_attempts
		WHERE card_id = $1::uuid
		ORDER BY created_at, id
	`, cardID.String())
	if err != nil {
		return nil, fmt.Errorf("store: list attempts for card %s: %w", cardID, err)
	}
	defer rows.Close()

	var out []StoredAttempt
	for rows.Next() {
		var (
			a          StoredAttempt
			idText     string
			statusText string
		)

		if err := rows.Scan(
			&a.ID, &idText, &a.RunID, &a.Phase, &a.AttemptNumber, &a.ModelAlias, &a.Provider,
			&a.Harness, &a.Model, &statusText, &a.CountedAsAttempt, &a.Summary, &a.InputTokens,
			&a.OutputTokens, &a.CachedTokens, &a.CostUSD, &a.DurationMS, &a.StartedAt, &a.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("store: scan attempt row for card %s: %w", cardID, err)
		}

		cardUUID, err := uuid.Parse(idText)
		if err != nil {
			return nil, fmt.Errorf("store: parse card id %q: %w", idText, err)
		}
		a.CardID = cardUUID
		a.Status = runner.Status(statusText)

		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list attempts for card %s: %w", cardID, err)
	}

	return out, nil
}

// LadderExhausted reports whether cardID has reached or passed limit
// implementation attempts. This package does not know the ladder's shape
// (spec §12.3: Haiku x3 -> Sonnet x3 -> Opus x1 -> Needs Human) -- the
// caller supplies limit from policy, keeping that shape out of the store.
//
// Returns ErrCardNotFound if cardID does not exist.
func (s *Store) LadderExhausted(ctx context.Context, cardID uuid.UUID, limit int) (bool, error) {
	var implementationAttempt int
	err := s.pool.QueryRow(ctx, `
		SELECT implementation_attempt
		FROM cards
		WHERE id = $1::uuid
	`, cardID.String()).Scan(&implementationAttempt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrCardNotFound
		}
		return false, fmt.Errorf("store: load implementation attempt count for card %s: %w", cardID, err)
	}

	return implementationAttempt >= limit, nil
}

// PhaseAttempts counts the attempts already spent on a card's CURRENT run at
// a phase -- the trailing run of same-phase rows in the ledger.
//
// The ladder index used to come from implementation_attempt, which is
// incremented by every phase that records an attempt and is never reset. So
// one counter indexed every phase's ladder: a card that had spent a single
// implementation attempt resolved its NEXT review at attempt 2, and review's
// ladder is one rung long. The reviewer was refused before it ran, and the
// CORRECTABLE path -- review asks for a fix, implementation makes it, review
// looks again -- could never complete for any card, ever.
//
// Trailing rather than cumulative because models.yaml's rule for review is
// "one independent review pass per verification cycle": a second review with
// an implementation between them is the next cycle, not a retry. Two reviews
// in a row with nothing in between IS a retry, and still exhausts.
//
// The implementation phase does not use this. Its ladder is deliberately
// cumulative across the whole card (§12.3) -- resetting it every time review
// sent work back is what would make the correctable loop unbounded.
func (s *Store) PhaseAttempts(ctx context.Context, cardID uuid.UUID, phase string) (int, error) {
	const q = `
		SELECT phase
		  FROM card_attempts
		 WHERE card_id = $1::uuid
		   AND attempt_number IS NOT NULL
		 ORDER BY created_at DESC, id DESC`

	rows, err := s.pool.Query(ctx, q, cardID.String())
	if err != nil {
		return 0, fmt.Errorf("store: counting %s attempts for card %s: %w", phase, cardID, err)
	}
	defer rows.Close()

	n := 0
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return 0, fmt.Errorf("store: reading an attempt row: %w", err)
		}
		if p != phase {
			break
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("store: counting %s attempts for card %s: %w", phase, cardID, err)
	}
	return n, nil
}
