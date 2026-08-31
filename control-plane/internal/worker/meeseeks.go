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
	// OutcomeAdvanced means the step finished a phase rather than moving
	// the card: the phase moved on and the card was handed back, so the
	// next Meeseeks continues from there. §11's phases all happen while a
	// card is InProgress and the state machine has no InProgress ->
	// InProgress transition, so this is how "planning is done, write the
	// tests next" is expressed without one worker carrying the card
	// through every phase (§7.1).
	OutcomeAdvanced Outcome = "advanced"
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
	Summary string
	Detail  map[string]any

	// NextState is where the step believes the card should go. Exactly one
	// of NextState and NextPhase must be set: they have opposite
	// consequences for the claim -- one hands the card on, the other keeps
	// it -- and guessing between them is worse than refusing.
	NextState card.State

	// NextPhase is the §11 phase to move to, leaving the card InProgress
	// and claimed. Set when a step finished its phase and the work
	// continues.
	NextPhase card.Phase
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

	// NoteStepOutcome records whether a step ran to completion. It is what
	// bounds a card retrying something impossible: every step outcome goes
	// through here, including the failures that never reach a model.
	NoteStepOutcome(ctx context.Context, cardID uuid.UUID, ran bool) error

	// AttachEvidence records ev against cardID -- e.g. as a card comment --
	// so the audit log (spec 21) can answer "what happened to card X?"
	// without exposing model chain-of-thought.
	AttachEvidence(ctx context.Context, cardID uuid.UUID, ev Evidence) error

	// AdvancePhase moves cardID to its next §11 phase without changing its
	// state, for a step that finished a phase rather than the card.
	AdvancePhase(ctx context.Context, cardID uuid.UUID, to card.Phase, actor card.ActorType, actorID, reason string) error
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

	// maxInfraFailures bounds §12.1's infrastructure_failures. Zero disables
	// the bound, which is how every caller behaved before it existed.
	maxInfraFailures int
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
		id:               id,
		cards:            cards,
		pol:              pol,
		step:             step,
		log:              log,
		lease:            lease,
		maxInfraFailures: DefaultMaxInfraFailures,
	}
}

// DefaultMaxInfraFailures is how many non-model failures in a row a card may
// collect before a human is asked to look at it.
//
// Consecutive, not lifetime. The store clears the count on any run that
// reached the model, and clears it again when a card leaves NeedsHuman, so
// this measures "cannot run right now" rather than "has had a bad day". The
// difference is not academic: as a lifetime total it escalated a card whose
// cause had already been FIXED, on the card's very next claim, without ever
// retrying it under the fix -- and the count only went up, so the card could
// never come back.
//
// Five rather than one, because a single provider hiccup or an evicted pod is
// ordinary and recovering from it without troubling anyone is the entire point
// of not counting infrastructure against the ladder. Five in a row is not
// weather. It is something that will not fix itself on its own, and every
// further retry spends a real model call to learn the same thing again.
const DefaultMaxInfraFailures = 5

// WithMaxInfraFailures overrides the bound. Zero disables it entirely, which
// restores the unbounded behaviour and is never what an operator wants running
// unattended.
func (m *Meeseeks) WithMaxInfraFailures(n int) *Meeseeks {
	m.maxInfraFailures = n
	return m
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

	// §12.1 says an infrastructure failure must not burn an attempt, and it
	// is right: a provider outage is not the model failing the work. But
	// nothing ever read the counter it increments, and "does not burn an
	// attempt" with no bound on top of it means a card that CANNOT run
	// retries forever.
	//
	// That is not hypothetical. A reviewer whose read deadline was too short
	// for a large diff timed out, was released without burning an attempt,
	// was re-promoted a minute later, and timed out again -- spending a full
	// reasoning call every four minutes, indefinitely, while opening no pull
	// request and telling nobody. The work was finished and green; only the
	// step reporting on it could not complete.
	//
	// So the counter is now read. A card that has failed this many times in a
	// row for reasons that are not the model's goes to a human, which is what
	// NeedsHuman is for.
	//
	// What this is NOT is a judgement on the work. The step could not be run;
	// the branch may be perfectly good, and on the card that prompted this it
	// was. The reason string says so, because "escalated to NeedsHuman" with
	// a bare count reads as "the machine gave up on your code" -- and a human
	// who believes that will not simply send it back in, which is all it
	// needs.
	if m.maxInfraFailures > 0 && c.InfrastructureFailures >= m.maxInfraFailures {
		reason := fmt.Sprintf(
			"%d consecutive infrastructure failures in phase %q: the step could not be RUN, which says nothing about whether the work is good. "+
				"See the card's latest evidence for what failed. Moving this card out of NeedsHuman clears the count and it will be retried.",
			c.InfrastructureFailures, c.Phase,
		)
		if terr := m.cards.Transition(ctx, c.ID, card.NeedsHuman, card.ActorSystem, m.id, reason); terr != nil {
			return OutcomeEscalated, fmt.Errorf("worker %s: escalate card %s to NeedsHuman: %w", m.id, c.ID, terr)
		}
		log.Warn("escalated to NeedsHuman: too many infrastructure failures",
			"infrastructure_failures", c.InfrastructureFailures)
		return OutcomeEscalated, nil
	}

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

		// An unexpected policy error -- an unknown phase, alias or provider,
		// or a malformed attempt count -- is something the operator must fix
		// in YAML, and it is the one class of failure that CANNOT get better
		// by trying again: the file does not change because a worker read it
		// twice.
		//
		// This used to release the card, on the reasoning that a human
		// should not be escalated into a config bug. That was wrong. The
		// card came straight back, failed identically, and did so once a
		// reconcile interval forever -- and because a policy failure records
		// no attempt, infrastructure_failures never moved and the bound that
		// exists to stop exactly this never saw it. Quietly looping is not
		// kinder to a human than telling them; it is the same stall with
		// nobody informed.
		//
		// Adding a phase to the pipeline is how this happens in practice: an
		// operator-supplied policy that predates the phase has no rung for
		// it, and every card reaching it stops.
		reason := fmt.Sprintf(
			"policy has nothing to run for phase %q: %v. This is configuration, not the work: "+
				"add a rung for this phase to models.yaml, then move this card out of NeedsHuman to retry it.",
			c.Phase, err)
		if terr := m.cards.Transition(ctx, c.ID, card.NeedsHuman, card.ActorSystem, m.id, reason); terr != nil {
			return OutcomeEscalated, fmt.Errorf("worker %s: escalate card %s after a policy failure: %w", m.id, c.ID, terr)
		}
		log.Error("escalated to NeedsHuman: the policy cannot resolve this phase",
			"phase", string(c.Phase), "error", err)
		return OutcomeEscalated, nil
	}

	// From here on, the claim MUST be released on every path except a
	// successful transition (which moves the card out of InProgress
	// itself). A phase advance releases too -- §7.1 ends every lifecycle
	// with "release claim → EXIT" -- it just advances the phase first, so
	// the next Meeseeks picks up where this one stopped.
	// Guarded with a flag rather than relying on a single return
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

	// Counted before anything else is decided. Every step failure lands
	// here, including the ones that fail before reaching a model, which is
	// exactly the class that used to loop forever unseen.
	if err := m.cards.NoteStepOutcome(ctx, c.ID, stepErr == nil); err != nil {
		log.Error("could not record the step outcome", "error", err)
	}

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

	// Exactly one of the two, checked after evidence is recorded so the
	// attempt is on the audit log either way.
	hasState, hasPhase := ev.NextState != "", ev.NextPhase != ""
	if hasState == hasPhase {
		reason := "step named neither a next state nor a next phase"
		if hasState {
			reason = "step named both a next state and a next phase"
		}
		if rerr := release(reason); rerr != nil {
			log.Error("release after an ambiguous step also failed", "error", rerr)
		}
		return OutcomeReleased, fmt.Errorf("worker %s: card %s: %s", m.id, c.ID, reason)
	}

	if hasPhase {
		if err := m.cards.AdvancePhase(ctx, c.ID, ev.NextPhase, card.ActorAgent, m.id, ev.Summary); err != nil {
			if rerr := release(fmt.Sprintf("advance to phase %q failed: %v", ev.NextPhase, err)); rerr != nil {
				log.Error("release after a failed phase advance also failed", "error", rerr)
			}
			return OutcomeReleased, fmt.Errorf("worker %s: advance card %s to phase %q: %w", m.id, c.ID, ev.NextPhase, err)
		}
		// Hand the card back. §7.1: "perform exactly one workflow ...
		// release claim → EXIT", and "a card may require several Meeseeks
		// over its lifetime. That is desirable." Keeping the claim would
		// make one worker carry a card through planning, tests and
		// implementation -- the long-running assistant with an
		// ever-growing pile of context §7 forbids -- and would park the
		// card under a live lease no other worker could take.
		//
		// Released AFTER the advance, so the next Meeseeks claims a card
		// already in its new phase rather than re-running the one that
		// just finished.
		if rerr := release(fmt.Sprintf("phase advanced to %q; next phase needs a fresh Meeseeks", ev.NextPhase)); rerr != nil {
			log.Error("release after a phase advance failed", "error", rerr)
			return OutcomeReleased, fmt.Errorf("worker %s: release card %s after advancing to %q: %w", m.id, c.ID, ev.NextPhase, rerr)
		}
		return OutcomeAdvanced, nil
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
