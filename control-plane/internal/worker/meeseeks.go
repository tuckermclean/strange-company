// Package worker implements the Meeseeks worker described in spec section 7:
// a deliberately short-lived process that claims exactly one Ready card,
// performs exactly one workflow step for that card's current phase, records
// why, and exits.
//
//	Claim one thing. Make the thing stop being your problem. Cease to exist.
//
// A Meeseeks must never loop over cards, must never become long-lived, and
// must never hold a card across steps -- a card needing several steps is
// handled by several successive Meeseeks over its lifetime; that is the
// design (spec 7.1), not a limitation. Workflow state lives in the system
// (the cards table, via CardStore), never in a worker's memory.
//
// This package performs no model calls of its own (spec 2.4: don't spend
// intelligence on questions software can answer). The single unit of work a
// RunOnce performs is supplied by the caller as a Step, so a later milestone
// can inject a real coding-agent invocation without this package changing.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
)

// ErrNoWork is the error CardStore.ClaimReady must satisfy errors.Is against
// when there is currently no claimable card. RunOnce treats it as a normal,
// quiet exit (OutcomeNoWork, nil) rather than a failure -- a Meeseeks finding
// nothing to do is not an error, and must not log at Warn or spin (spec 7).
//
// CardStore is defined independently of *store.Store (mirroring the
// decoupling internal/mcp.CardService already uses against store.ErrNoWork),
// so this package never needs to import internal/store. Whoever wires a real
// *store.Store in as a CardStore is responsible for translating
// store.ErrNoWork into this sentinel.
var ErrNoWork = errors.New("worker: no claimable card")

// Outcome reports how one RunOnce call ended.
type Outcome string

const (
	// OutcomeNoWork means ClaimReady found nothing to do. A normal, quiet
	// exit -- not an error.
	OutcomeNoWork Outcome = "no_work"
	// OutcomeCompleted means the step succeeded, evidence was attached, and
	// the card was transitioned to the state the step named.
	OutcomeCompleted Outcome = "completed"
	// OutcomeReleased means a card was claimed but had to be handed back:
	// the step failed, evidence could not be attached, or the step's
	// requested transition was illegal. The claim is always released in
	// this case.
	OutcomeReleased Outcome = "released"
	// OutcomeEscalated means the phase's escalation ladder (spec 12.3) was
	// exhausted for this card's attempt count, and the card was moved to
	// NeedsHuman instead of being handed to another Meeseeks.
	OutcomeEscalated Outcome = "escalated"
)

// Step performs exactly one unit of work for a card's current phase and
// returns evidence to attach. It must not transition the card itself --
// RunOnce owns the evidence-then-transition ordering (spec 21: a card must
// never arrive in a new state with no evidence explaining why).
//
// Spec 2.4 forbids spending intelligence on questions software can answer;
// M2's Step implementations must not make model calls. A later milestone can
// satisfy this same interface with one that does.
type Step interface {
	// Do performs exactly one unit of work for the card's current phase and
	// returns evidence to attach. It must not transition the card itself.
	Do(ctx context.Context, c *card.Card, res *policy.Resolution) (Evidence, error)
}

// Evidence is what a Step attaches to a card to explain what happened, and
// where it believes the card should go next.
type Evidence struct {
	Summary   string
	Detail    map[string]any
	NextState card.State // where the step believes the card should go
}

// CardStore is the narrow slice of *store.Store's persistence operations a
// Meeseeks needs: claim, heartbeat, evidence, transition, release. It is
// defined here rather than depending on internal/store directly -- the same
// pattern internal/mcp.CardService uses -- so this package's dependency
// footprint stays stdlib + uuid + card + policy.
type CardStore interface {
	// ClaimReady must return an error satisfying errors.Is(err, ErrNoWork)
	// when nothing is currently claimable.
	ClaimReady(ctx context.Context, workerID string, lease time.Duration) (*card.Card, error)

	// Heartbeat renews workerID's lease on cardID.
	Heartbeat(ctx context.Context, cardID uuid.UUID, workerID string, lease time.Duration) error

	// Release hands cardID back (to Ready) and clears workerID's claim on
	// it, recording reason in the audit log.
	Release(ctx context.Context, cardID uuid.UUID, workerID, reason string) error

	// Transition moves cardID to state `to`, subject to the card state
	// machine (card.CanTransition). An illegal move must be rejected and
	// must not corrupt the card.
	Transition(ctx context.Context, cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error

	// AttachEvidence records ev against cardID -- e.g. as a card comment --
	// so the audit log (spec 21) can answer "what happened to card X?"
	// without exposing model chain-of-thought.
	AttachEvidence(ctx context.Context, cardID uuid.UUID, ev Evidence) error
}

// Meeseeks is one short-lived worker: spawn it, call RunOnce exactly once,
// let it exit. See spec 7.1 for the lifecycle this implements.
type Meeseeks struct {
	id    string
	cards CardStore
	pol   *policy.Policy
	step  Step
	log   *slog.Logger
	lease time.Duration
}

// New constructs a Meeseeks. id identifies this worker as a claimant (spec
// 6's claimed_by) and appears in every card_history row it produces. lease
// is the claim lease duration; RunOnce renews it roughly every lease/3 while
// its step runs.
func New(id string, cards CardStore, pol *policy.Policy, step Step, log *slog.Logger, lease time.Duration) *Meeseeks {
	if log == nil {
		log = slog.Default()
	}
	return &Meeseeks{
		id:    id,
		cards: cards,
		pol:   pol,
		step:  step,
		log:   log,
		lease: lease,
	}
}

// RunOnce implements spec 7.1's lifecycle: claim one card, resolve policy
// for its phase and attempt, perform exactly one step, attach evidence,
// transition, release, return. It never loops over cards and never retains
// the card after returning.
func (m *Meeseeks) RunOnce(ctx context.Context) (Outcome, error) {
	c, err := m.cards.ClaimReady(ctx, m.id, m.lease)
	if err != nil {
		if errors.Is(err, ErrNoWork) {
			// Spec 7: no claimable work is a normal, quiet exit -- not an
			// error, and must never be treated as one worth spinning or
			// warning over.
			return OutcomeNoWork, nil
		}
		return OutcomeNoWork, fmt.Errorf("worker %s: claim ready card: %w", m.id, err)
	}

	log := m.log.With("worker", m.id, "card", c.ID.String(), "phase", string(c.Phase))

	// Spec 12: an implementation_attempt of N means N attempts have already
	// been spent; the next one to resolve is N+1.
	attempt := c.ImplementationAttempt + 1
	res, err := m.pol.Resolve(string(c.Phase), attempt)
	if err != nil {
		if errors.Is(err, policy.ErrLadderExhausted) {
			reason := fmt.Sprintf(
				"spec 12.3: implementation escalation ladder for phase %q exhausted at attempt %d: %v",
				c.Phase, attempt, err,
			)
			if terr := m.cards.Transition(ctx, c.ID, card.NeedsHuman, card.ActorSystem, m.id, reason); terr != nil {
				return OutcomeEscalated, fmt.Errorf("worker %s: escalate card %s to NeedsHuman: %w", m.id, c.ID, terr)
			}
			log.Info("escalated to NeedsHuman: ladder exhausted", "reason", reason)
			return OutcomeEscalated, nil
		}

		// An unexpected policy error (unknown phase/alias/provider, or a
		// malformed attempt count) is not the ladder being exhausted -- it
		// is something the operator needs to fix in YAML. Give the card
		// back rather than escalate a human into a config bug.
		reason := fmt.Sprintf("policy resolution failed: %v", err)
		if rerr := m.cards.Release(ctx, c.ID, m.id, reason); rerr != nil {
			log.Error("release after policy resolve failure also failed", "error", rerr)
		}
		return OutcomeReleased, fmt.Errorf("worker %s: resolve policy for card %s phase %q: %w", m.id, c.ID, c.Phase, err)
	}

	// From here on, the claim MUST be released on every path except a
	// successful transition (which moves the card out of InProgress
	// itself). Guarded with a flag rather than relying on a single return
	// statement, since a step failure, an evidence-attach failure and an
	// illegal transition all need the same guarantee -- and it must hold
	// even if something above this line panics or the step's goroutine
	// setup fails.
	var released bool
	release := func(reason string) error {
		if released {
			return nil
		}
		released = true
		return m.cards.Release(ctx, c.ID, m.id, reason)
	}
	defer func() {
		if rerr := release("meeseeks exiting: spec 7 forbids holding a card across steps"); rerr != nil {
			log.Error("release on exit failed", "error", rerr)
		}
	}()

	// Heartbeat for the duration of the step only (spec 7.1's "perform
	// exactly one workflow step"). It must not outlive RunOnce: cancelHB is
	// called the instant the step returns, and we wait for the goroutine to
	// actually stop before doing anything else.
	hbCtx, cancelHB := context.WithCancel(ctx)
	var hbWG sync.WaitGroup
	hbWG.Add(1)
	go func() {
		defer hbWG.Done()
		m.heartbeat(hbCtx, c.ID, log)
	}()

	ev, stepErr := m.step.Do(ctx, c, res)

	cancelHB()
	hbWG.Wait()

	if stepErr != nil {
		failure := Evidence{
			Summary: fmt.Sprintf("step failed: %v", stepErr),
			Detail: map[string]any{
				"error":   stepErr.Error(),
				"phase":   string(c.Phase),
				"attempt": attempt,
			},
		}
		if aerr := m.cards.AttachEvidence(ctx, c.ID, failure); aerr != nil {
			log.Error("attach failure evidence failed", "error", aerr)
		}
		if rerr := release(fmt.Sprintf("step failed: %v", stepErr)); rerr != nil {
			log.Error("release after step failure also failed", "error", rerr)
		}
		return OutcomeReleased, stepErr
	}

	// Evidence before transition, always: spec 21 requires the audit log to
	// explain every state change, so a card must never arrive in a new
	// state with nothing on record about why.
	if err := m.cards.AttachEvidence(ctx, c.ID, ev); err != nil {
		if rerr := release(fmt.Sprintf("attach evidence failed: %v", err)); rerr != nil {
			log.Error("release after attach-evidence failure also failed", "error", rerr)
		}
		return OutcomeReleased, fmt.Errorf("worker %s: attach evidence for card %s: %w", m.id, c.ID, err)
	}

	if err := m.cards.Transition(ctx, c.ID, ev.NextState, card.ActorAgent, m.id, ev.Summary); err != nil {
		// The step asked for a move the state machine forbids. Evidence is
		// already on record explaining the attempt; the card itself must
		// not be corrupted, so hand it back exactly like any other failure.
		if rerr := release(fmt.Sprintf("step's requested transition was rejected: %v", err)); rerr != nil {
			log.Error("release after illegal transition also failed", "error", rerr)
		}
		return OutcomeReleased, fmt.Errorf("worker %s: transition card %s to %q: %w", m.id, c.ID, ev.NextState, err)
	}

	released = true // the transition moved the card on; nothing left to release.
	return OutcomeCompleted, nil
}

// heartbeat renews the card's lease roughly every lease/3 until ctx is
// cancelled. It must never outlive the RunOnce call that started it.
func (m *Meeseeks) heartbeat(ctx context.Context, cardID uuid.UUID, log *slog.Logger) {
	interval := m.lease / 3
	if interval <= 0 {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.cards.Heartbeat(ctx, cardID, m.id, m.lease); err != nil {
				log.Warn("heartbeat failed", "error", err)
			}
		}
	}
}
