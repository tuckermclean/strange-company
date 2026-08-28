// Package dispatch routes a claimed card to the step for its current phase.
//
// §7.1's worker does determine_phase() and then performs exactly one workflow.
// This is determine_phase(), kept out of the worker so that adding §11.2's
// test-writer or §12's implementation ladder is a map entry rather than a
// change to the claim/lease/evidence machinery.
package dispatch

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/worker"
)

// Step dispatches to a per-phase worker.Step.
type Step struct {
	steps map[card.Phase]worker.Step
	log   *slog.Logger
}

// New builds a dispatcher over the steps that exist. Phases absent from the
// map are reported to a human rather than retried; see Do.
func New(steps map[card.Phase]worker.Step, log *slog.Logger) *Step {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Step{steps: steps, log: log}
}

// Do runs the step for the card's phase.
func (s *Step) Do(ctx context.Context, c *card.Card, res *policy.Resolution) (worker.Evidence, error) {
	// A card promoted through the §10 gate still carries phase
	// "specification" -- the gate is what finished it. Moving to planning
	// is bookkeeping, and spending a model call to discover that would be
	// the §2.4 failure: intelligence on a question software can answer.
	if c.Phase == card.PhaseSpecification {
		return worker.Evidence{
			Summary:   "specification complete; starting planning",
			NextPhase: card.PhasePlanning,
		}, nil
	}

	step, ok := s.steps[c.Phase]
	if !ok {
		// Deliberately NOT an error. An error releases the card back to
		// Ready, the next Meeseeks claims it and fails identically, and
		// the board spends every reconcile interval claiming and
		// releasing the same card forever -- which looks like activity.
		// A card a human can see is stuck is strictly better than a card
		// that appears busy.
		s.log.Warn("no step for this phase; sending the card to a human",
			"card_id", c.ID, "phase", c.Phase)
		return worker.Evidence{
			Summary: fmt.Sprintf("no step implemented for phase %q", c.Phase),
			Detail: map[string]any{
				"phase": string(c.Phase),
			},
			NextState: card.NeedsHuman,
		}, nil
	}

	return step.Do(ctx, c, res)
}
