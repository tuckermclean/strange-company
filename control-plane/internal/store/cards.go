package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/specgate"
)

// ErrNoWork is returned by ClaimReady when no card is currently claimable:
// there is no card in state Ready whose lease is unheld or expired.
var ErrNoWork = errors.New("no claimable card")

// ErrNotClaimant is returned when a caller attempts to heartbeat or release
// a card it does not currently hold the lease on.
var ErrNotClaimant = errors.New("worker does not hold this card")

// ErrCardNotFound is returned when an operation references a card id that
// does not exist in the cards table.
var ErrCardNotFound = errors.New("card not found")

// cardColumns is the column list, in the order scanCard expects, for
// selecting a full row from the cards table. Numeric columns that are
// nullable/arbitrary-precision in Postgres are cast to float8 so they scan
// directly into Go float64 values without needing pgtype.Numeric, and the
// uuid primary key is cast to text so it scans into a plain string that is
// then parsed with uuid.Parse.
const cardColumns = `
	id::text,
	vikunja_task_id,
	vikunja_synced_state,
	vikunja_synced_phase,
	title,
	source_type,
	source_url,
	source_external_id,
	repo_url,
	repo_base_ref,
	branch,
	state,
	phase,
	spec_uri,
	plan_uri,
	risk_class,
	effective_priority,
	claimed_by,
	lease_expires_at,
	implementation_attempt,
	infrastructure_failures,
	max_cost_usd::float8,
	cost_usd::float8,
	created_at,
	updated_at
`

// rowScanner is satisfied by both pgx.Row and pgx.Rows, letting scanCard
// serve GetCard's single-row query and ListCards' multi-row query alike.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanCard scans one row produced by a query selecting cardColumns (in that
// order) into a *card.Card.
func scanCard(row rowScanner) (*card.Card, error) {
	var (
		c      card.Card
		idText string
		state  string
		phase  string
	)

	err := row.Scan(
		&idText,
		&c.VikunjaTaskID,
		&c.VikunjaSyncedState,
		&c.VikunjaSyncedPhase,
		&c.Title,
		&c.SourceType,
		&c.SourceURL,
		&c.SourceExternalID,
		&c.RepoURL,
		&c.RepoBaseRef,
		&c.Branch,
		&state,
		&phase,
		&c.SpecURI,
		&c.PlanURI,
		&c.RiskClass,
		&c.EffectivePriority,
		&c.ClaimedBy,
		&c.LeaseExpiresAt,
		&c.ImplementationAttempt,
		&c.InfrastructureFailures,
		&c.MaxCostUSD,
		&c.CostUSD,
		&c.CreatedAt,
		&c.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	id, err := uuid.Parse(idText)
	if err != nil {
		return nil, fmt.Errorf("store: parse card id %q: %w", idText, err)
	}

	c.ID = id
	c.State = card.State(state)
	c.Phase = card.Phase(phase)

	return &c, nil
}

// ClaimReady atomically claims one Ready, unclaimed-or-expired-lease card
// for workerID and moves it to InProgress (spec section 6). It MUST be safe
// against at least ten simultaneous callers racing for the same card, and
// guarantees exactly one of them receives it.
//
// The whole operation — select-for-update, state change and history write —
// happens in a single transaction. SELECT ... FOR UPDATE SKIP LOCKED is what
// makes losing callers see no row (and thus ErrNoWork) instead of blocking
// on the winner's row lock; it must not be replaced with NOWAIT, an advisory
// lock, or a retry loop.
func (s *Store) ClaimReady(ctx context.Context, workerID string, lease time.Duration) (*card.Card, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: begin claim transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Three kinds of card are claimable:
	//
	//   1. a Ready card nobody holds;
	//   2. an InProgress card whose lease has expired -- its worker died
	//      without releasing it. Spec section 6 allows reclaiming only after
	//      expiry, and a Meeseeks is expected to die, so this is the normal
	//      recovery path rather than an edge case; and
	//   3. an InProgress card nobody holds at all.
	//
	// The third is not hypothetical, and leaving it out was a deadlock. A
	// human dragging a card from Ready to InProgress on the Vikunja board
	// takes the legal transition the board invites, and it sets the state
	// without setting a claim or a lease. The card is then neither Ready nor
	// lease-expired, so nothing could ever pick it up again -- it simply
	// stopped, with no error anywhere and a column that looked like work in
	// progress. A claimless InProgress card unambiguously means nobody is
	// working on it, whether a human put it there or a worker vanished
	// mid-claim, and the answer is the same: someone should take it.
	//
	// The current state is selected too, so the history row records where the
	// card actually came from instead of assuming Ready.
	var idText, fromState string
	err = tx.QueryRow(ctx, `
		SELECT id::text, state
		FROM cards
		WHERE (state IN ('Ready', 'InProgress') AND claimed_by IS NULL)
		   OR (state = 'InProgress'
		       AND lease_expires_at IS NOT NULL
		       AND lease_expires_at < now())
		ORDER BY effective_priority, created_at
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`).Scan(&idText, &fromState)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoWork
		}
		return nil, fmt.Errorf("store: select claimable card: %w", err)
	}

	leaseExpiresAt := time.Now().Add(lease)

	if _, err := tx.Exec(ctx, `
		UPDATE cards
		SET state = 'InProgress',
		    claimed_by = $1,
		    lease_expires_at = $2,
		    updated_at = now()
		WHERE id = $3::uuid
	`, workerID, leaseExpiresAt, idText); err != nil {
		return nil, fmt.Errorf("store: claim card %s: %w", idText, err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO card_history (card_id, from_state, to_state, actor_type, actor_id, reason)
		VALUES ($1::uuid, $2, $3, $4, $5, $6)
	`, idText, fromState, string(card.InProgress), string(card.ActorAgent), workerID, claimReason(fromState)); err != nil {
		return nil, fmt.Errorf("store: record claim history for card %s: %w", idText, err)
	}

	claimed, err := scanCard(tx.QueryRow(ctx, `SELECT `+cardColumns+` FROM cards WHERE id = $1::uuid`, idText))
	if err != nil {
		return nil, fmt.Errorf("store: reload claimed card %s: %w", idText, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("store: commit claim transaction: %w", err)
	}

	return claimed, nil
}

// Heartbeat extends a card's lease, but only while workerID is still the
// current claimant and its existing lease has not already expired.
func (s *Store) Heartbeat(ctx context.Context, cardID uuid.UUID, workerID string, lease time.Duration) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin heartbeat transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	idText := cardID.String()

	var claimedBy *string
	var leaseExpiresAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT claimed_by, lease_expires_at
		FROM cards
		WHERE id = $1::uuid
		FOR UPDATE
	`, idText).Scan(&claimedBy, &leaseExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCardNotFound
		}
		return fmt.Errorf("store: load card %s for heartbeat: %w", idText, err)
	}

	if claimedBy == nil || *claimedBy != workerID {
		return ErrNotClaimant
	}
	if leaseExpiresAt == nil || !leaseExpiresAt.After(time.Now()) {
		return ErrNotClaimant
	}

	newLease := time.Now().Add(lease)
	if _, err := tx.Exec(ctx, `
		UPDATE cards
		SET lease_expires_at = $1,
		    updated_at = now()
		WHERE id = $2::uuid
	`, newLease, idText); err != nil {
		return fmt.Errorf("store: extend lease for card %s: %w", idText, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit heartbeat transaction: %w", err)
	}

	return nil
}

// Release clears a card's claim and returns it to Ready, but only while
// workerID is still the current claimant. It writes an InProgress -> Ready
// card_history row.
func (s *Store) Release(ctx context.Context, cardID uuid.UUID, workerID, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin release transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	idText := cardID.String()

	var claimedBy *string
	err = tx.QueryRow(ctx, `
		SELECT claimed_by
		FROM cards
		WHERE id = $1::uuid
		FOR UPDATE
	`, idText).Scan(&claimedBy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCardNotFound
		}
		return fmt.Errorf("store: load card %s for release: %w", idText, err)
	}

	if claimedBy == nil || *claimedBy != workerID {
		return ErrNotClaimant
	}

	if _, err := tx.Exec(ctx, `
		UPDATE cards
		SET state = 'Ready',
		    claimed_by = NULL,
		    lease_expires_at = NULL,
		    updated_at = now()
		WHERE id = $1::uuid
	`, idText); err != nil {
		return fmt.Errorf("store: release card %s: %w", idText, err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO card_history (card_id, from_state, to_state, actor_type, actor_id, reason)
		VALUES ($1::uuid, $2, $3, $4, $5, $6)
	`, idText, string(card.InProgress), string(card.Ready), string(card.ActorAgent), workerID, reason); err != nil {
		return fmt.Errorf("store: record release history for card %s: %w", idText, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit release transaction: %w", err)
	}

	return nil
}

// Transition moves a card from its current state to `to`, subject to
// card.CanTransition. If the move is illegal, the error from CanTransition
// is returned unchanged (wrapping card.ErrIllegalTransition) and no
// card_history row is written. On success, the state change and its
// card_history row are written together in one transaction.
// ErrSpecGateRequired is returned when something tries to promote a card to
// Ready through the generic transition path.
//
// Spec §10: a Backlog card cannot become Ready merely because an LLM says it is
// ready. Checking that in the caller would make the gate advisory -- anything
// holding a Store could route around it -- so the generic path refuses and
// PromoteToReady is the only way in.
var ErrSpecGateRequired = errors.New("promotion to Ready must go through the specification gate")

// PromoteToReady moves a Backlog card to Ready, and only with a passing
// specification gate.
//
// The gate result is taken as an argument rather than computed here because
// evaluating it needs the spec document and the card's dependencies, which are
// the caller's to assemble. What this function guarantees is narrower and more
// important: no path through this package reaches Ready without one.
func (s *Store) PromoteToReady(ctx context.Context, cardID uuid.UUID, gate specgate.Result, actor card.ActorType, actorID string) error {
	if !gate.Passed {
		return fmt.Errorf("store: refusing to promote card %s: %w: %s", cardID, ErrSpecGateRequired, gate.Error())
	}
	return s.transition(ctx, cardID, card.Ready, actor, actorID, "specification gate passed", true)
}

func (s *Store) Transition(ctx context.Context, cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error {
	return s.transition(ctx, cardID, to, actor, actorID, reason, false)
}

// transition carries viaGate so that exactly one caller -- PromoteToReady --
// can make the Backlog -> Ready move. Passing it as an argument rather than
// checking a field keeps the exception visible at every call site.
func (s *Store) transition(ctx context.Context, cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string, viaGate bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin transition transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	idText := cardID.String()

	var current string
	err = tx.QueryRow(ctx, `
		SELECT state
		FROM cards
		WHERE id = $1::uuid
		FOR UPDATE
	`, idText).Scan(&current)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCardNotFound
		}
		return fmt.Errorf("store: load card %s for transition: %w", idText, err)
	}

	from := card.State(current)

	// Backlog -> Ready is the specification gate's transition and nothing else's.
	// Keyed on the transition rather than the actor, deliberately: DoD item 2 says
	// a card cannot enter Ready without a specification, and says it without
	// exception -- so a human dragging the card across the board in Vikunja is
	// gated too. Review -> Ready stays open, because that is §19's human
	// rejection of work whose spec already passed.
	if !viaGate && from == card.Backlog && to == card.Ready {
		return fmt.Errorf("store: %w", ErrSpecGateRequired)
	}

	if err := card.CanTransition(from, to, actor); err != nil {
		return err
	}

	// A claim only means anything while a card is InProgress, so leaving
	// one on a card that has moved elsewhere makes the board and the API
	// report a worker that is not there. §7.1 ends every lifecycle with
	// "release claim -> EXIT"; a transition out of InProgress is that exit,
	// and until now it abandoned the claim rather than releasing it.
	// Leaving NeedsHuman clears the infrastructure-failure count.
	//
	// A card only leaves NeedsHuman because someone decided it should, and
	// what they are saying by moving it is "try this again". Carrying the
	// old count across that decision means the very next claim re-escalates
	// on failures that predate the intervention -- so a card that reached
	// NeedsHuman for an infrastructure reason could never be sent back in,
	// even after the cause was fixed. That is not an escalation to a human.
	// It is a card the human cannot return.
	leavingNeedsHuman := from == card.NeedsHuman

	if _, err := tx.Exec(ctx, `
		UPDATE cards
		SET state            = $1,
		    claimed_by       = CASE WHEN $1 = 'InProgress' THEN claimed_by       ELSE NULL END,
		    lease_expires_at = CASE WHEN $1 = 'InProgress' THEN lease_expires_at ELSE NULL END,
		    infrastructure_failures = CASE WHEN $3 THEN 0 ELSE infrastructure_failures END,
		    updated_at       = now()
		WHERE id = $2::uuid
	`, string(to), idText, leavingNeedsHuman); err != nil {
		return fmt.Errorf("store: update state for card %s: %w", idText, err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO card_history (card_id, from_state, to_state, actor_type, actor_id, reason)
		VALUES ($1::uuid, $2, $3, $4, $5, $6)
	`, idText, string(from), string(to), string(actor), actorID, reason); err != nil {
		return fmt.Errorf("store: record transition history for card %s: %w", idText, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit transition transaction: %w", err)
	}

	return nil
}

// GetCard loads a single card by id.
func (s *Store) GetCard(ctx context.Context, id uuid.UUID) (*card.Card, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+cardColumns+` FROM cards WHERE id = $1::uuid`, id.String())
	c, err := scanCard(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCardNotFound
		}
		return nil, fmt.Errorf("store: get card %s: %w", id, err)
	}
	return c, nil
}

// ListCards loads every card, ordered by creation time.
func (s *Store) ListCards(ctx context.Context) ([]*card.Card, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+cardColumns+` FROM cards ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("store: list cards: %w", err)
	}
	defer rows.Close()

	var cards []*card.Card
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan card row: %w", err)
		}
		cards = append(cards, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list cards: %w", err)
	}

	return cards, nil
}


// claimReason distinguishes a normal claim from taking over a card whose
// previous worker's lease expired, so the audit log shows which happened.
func claimReason(fromState string) string {
	if fromState == string(card.InProgress) {
		return "reclaimed after lease expiry"
	}
	return "claimed"
}

// SetVikunjaTaskID links a card to the Vikunja task that represents it.
//
// The link is written once, when the reconciler first pushes a card onto the
// board. It is deliberately not part of Transition: the projection changing has
// nothing to do with the card's workflow state, and should not append a history
// row.
func (s *Store) SetVikunjaTaskID(ctx context.Context, cardID uuid.UUID, taskID int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE cards
		SET vikunja_task_id = $1,
		    updated_at = now()
		WHERE id = $2::uuid
	`, taskID, cardID.String())
	if err != nil {
		return fmt.Errorf("store: link card %s to vikunja task %d: %w", cardID, taskID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrCardNotFound
	}
	return nil
}

// SetVikunjaSyncedState records the state the reconciler has just projected
// onto the Vikunja board.
//
// It is what lets the next pass tell a board a human moved from a board that
// has simply not caught up yet. Like SetVikunjaTaskID it is not a Transition:
// the projection catching up is not a workflow event and must not append a
// history row.
func (s *Store) SetVikunjaSyncedState(ctx context.Context, cardID uuid.UUID, state card.State, phase card.Phase) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE cards
		SET vikunja_synced_state = $1,
		    vikunja_synced_phase = $2
		WHERE id = $3::uuid
	`, string(state), string(phase), cardID.String())
	if err != nil {
		return fmt.Errorf("store: record synced state %q/%q for card %s: %w", state, phase, cardID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrCardNotFound
	}
	return nil
}

// HistoryEntry is one immutable record of a card changing state (§21).
type HistoryEntry struct {
	At        time.Time
	From      string
	To        string
	ActorType string
	ActorID   string
	Reason    string
}

// ListHistory returns a card's transitions, oldest first.
//
// card_history has been written since M0 and read by nothing since. §21
// requires the audit log to answer "what happened to card X?", and §33 puts
// "history with timestamps" on the card -- neither was reachable outside a
// psql session.
//
// Bounded: a card that has churned for hours has a long history, and neither a
// task description nor a human needs all of it. The newest entries are the ones
// that explain where a card is now, so the cap takes from the end.
func (s *Store) ListHistory(ctx context.Context, cardID uuid.UUID, limit int) ([]HistoryEntry, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.pool.Query(ctx, `
		SELECT at, coalesce(from_state, ''), to_state, actor_type, actor_id, coalesce(reason, '')
		FROM card_history
		WHERE card_id = $1::uuid
		ORDER BY at DESC, id DESC
		LIMIT $2
	`, cardID.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("store: list history for card %s: %w", cardID, err)
	}
	defer rows.Close()

	var out []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.At, &e.From, &e.To, &e.ActorType, &e.ActorID, &e.Reason); err != nil {
			return nil, fmt.Errorf("store: scan history row for card %s: %w", cardID, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read history for card %s: %w", cardID, err)
	}

	// Selected newest-first to make the cap take the newest; returned
	// oldest-first because that is how a history reads.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
