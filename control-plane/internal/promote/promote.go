// Package promote moves approved cards into Ready.
//
// Spec §10.2: "Human approves the completed spec. Only then may the control
// plane promote the card to Ready." Approval is the human input; promotion is
// the control plane's consequence of it. There is no endpoint to promote
// directly, because that would be a way around the deterministic gate.
package promote

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/spec"
	"github.com/tuckermclean/strange-company/control-plane/internal/specgate"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

// actorID names the control plane in the audit trail. The human's name is
// already recorded against the approval; this records who acted on it.
const actorID = "control-plane"

// Board is the part of the store this package uses.
type Board interface {
	ListApprovedAwaitingPromotion(ctx context.Context, limit int) ([]uuid.UUID, error)
	GetCard(ctx context.Context, id uuid.UUID) (*card.Card, error)
	GetSpec(ctx context.Context, id uuid.UUID) (*store.CardSpec, error)
	ListDependencies(ctx context.Context, id uuid.UUID) ([]*card.Card, error)
	HasPermittedActions(ctx context.Context, id uuid.UUID) (bool, error)
	PromoteToReady(ctx context.Context, id uuid.UUID, gate specgate.Result, actor card.ActorType, actorID string) error
}

// Result summarises one pass.
type Result struct {
	Considered int
	Promoted   int

	// Blocked counts cards a human approved that the gate still refuses.
	// Not a failure -- the gate working -- but worth surfacing, because
	// from the human's side an approval that changed nothing is confusing.
	Blocked int

	Failed int
}

// Reconciler promotes approved cards whose deterministic gate passes.
type Reconciler struct {
	board Board
	limit int
	log   *slog.Logger
}

// New builds a Reconciler.
func New(b Board, limit int, log *slog.Logger) *Reconciler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Reconciler{board: b, limit: limit, log: log}
}

// RunOnce evaluates every approved card once.
func (r *Reconciler) RunOnce(ctx context.Context) (Result, error) {
	var res Result

	ids, err := r.board.ListApprovedAwaitingPromotion(ctx, r.limit)
	if err != nil {
		return res, err
	}

	for _, id := range ids {
		res.Considered++

		gate, ok := r.evaluate(ctx, id)
		if !ok {
			res.Failed++
			continue
		}
		if !gate.Passed {
			// A human approved something the gate refuses. Logged at
			// info with the reasons: it is the gate doing its job, and
			// the person who approved needs to know why nothing moved.
			res.Blocked++
			r.log.Info("approved but not promotable", "card_id", id, "reasons", gate.Error())
			continue
		}

		if err := r.board.PromoteToReady(ctx, id, gate, card.ActorSystem, actorID); err != nil {
			r.log.Error("could not promote an approved card", "card_id", id, "error", err)
			res.Failed++
			continue
		}
		res.Promoted++
		r.log.Info("promoted to Ready", "card_id", id)
	}

	return res, nil
}

// evaluate gathers the gate's inputs for one card.
func (r *Reconciler) evaluate(ctx context.Context, id uuid.UUID) (specgate.Result, bool) {
	c, err := r.board.GetCard(ctx, id)
	if err != nil {
		r.log.Error("could not read a card awaiting promotion", "card_id", id, "error", err)
		return specgate.Result{}, false
	}
	cardSpec, err := r.board.GetSpec(ctx, id)
	if err != nil {
		r.log.Error("could not read the specification", "card_id", id, "error", err)
		return specgate.Result{}, false
	}
	deps, err := r.board.ListDependencies(ctx, id)
	if err != nil {
		r.log.Error("could not read dependencies", "card_id", id, "error", err)
		return specgate.Result{}, false
	}
	hasActions, err := r.board.HasPermittedActions(ctx, id)
	if err != nil {
		r.log.Error("could not read permitted actions", "card_id", id, "error", err)
		return specgate.Result{}, false
	}

	doc, problems := spec.Parse(id.String(), []byte(cardSpec.Content))

	return specgate.Evaluate(specgate.Inputs{
		Card:             c,
		Spec:             doc,
		SpecProblems:     problems,
		Dependencies:     deps,
		PermittedActions: hasActions,
		SpecApproved:     cardSpec.Approved,
	}), true
}
