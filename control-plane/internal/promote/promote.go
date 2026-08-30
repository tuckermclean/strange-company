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

	// ListUnapprovedWithSpec is the same queue on the other side of the
	// human gate: Backlog cards with a specification nobody has approved.
	// Only read when autonomy is set to approve them.
	ListUnapprovedWithSpec(ctx context.Context, limit int) ([]uuid.UUID, error)

	// ApproveSpec records an approval of the card's specification as it
	// currently reads.
	ApproveSpec(ctx context.Context, cardID uuid.UUID, approvedBy string) error
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

	// Approved counts specifications this pass signed on a human's behalf,
	// which happens only when autonomy is configured to allow it.
	Approved int
}

// Reconciler promotes approved cards whose deterministic gate passes.
type Reconciler struct {
	board Board
	limit       int
	autoApprove bool
	log   *slog.Logger
}

// New builds a Reconciler.
func New(b Board, limit int, log *slog.Logger) *Reconciler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Reconciler{board: b, limit: limit, log: log}
}

// WithAutoApproval makes this reconciler sign specifications a human would
// otherwise have to.
//
// Off by default, so an upgrade never changes who is approving what. §19 is
// untouched either way: the human remains the final merge authority, and no
// setting here can alter that without amending the spec.
func (r *Reconciler) WithAutoApproval(on bool) *Reconciler {
	r.autoApprove = on
	return r
}

// RunOnce evaluates every approved card once.
func (r *Reconciler) RunOnce(ctx context.Context) (Result, error) {
	var res Result

	// Sign for the specs a human would otherwise have to, when configured to.
	//
	// This is the front half of §10.2's gate, and the only autonomy setting
	// that can be honoured without amending the spec: §19 keeps the human as
	// the final merge authority regardless of what is set here.
	if r.autoApprove {
		res.Approved = r.approveEligible(ctx)
	}

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
	return r.evaluateAs(ctx, id, false)
}

// evaluateAs runs the gate, optionally pretending the specification is
// approved so a caller can ask "would this pass if someone signed it?".
func (r *Reconciler) evaluateAs(ctx context.Context, id uuid.UUID, assumeApproved bool) (specgate.Result, bool) {
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
		SpecApproved:     cardSpec.Approved || assumeApproved,
	}), true
}

// approveEligible approves every card whose specification would pass the gate
// if a human signed it, and returns how many it signed.
//
// The condition is exact rather than approximate: the gate is evaluated with
// SpecApproved set true, so approval is granted only when the human's signature
// is the ONLY thing missing. Every other check -- specification completeness,
// unverifiable criteria, dependencies, permitted actions -- still has to pass
// on its own. Nothing is bypassed; one signature is delegated.
//
// The approver is recorded as the autonomy setting rather than as a person, so
// §21's audit never shows a human approving something no human read.
func (r *Reconciler) approveEligible(ctx context.Context) int {
	ids, err := r.board.ListUnapprovedWithSpec(ctx, r.limit)
	if err != nil {
		r.log.Error("could not list unapproved cards", "error", err)
		return 0
	}

	signed := 0
	for _, id := range ids {
		gate, ok := r.evaluateAs(ctx, id, true)
		if !ok || !gate.Passed {
			// Not a failure worth logging loudly: an incomplete spec
			// sitting unapproved is the system working.
			continue
		}
		if err := r.board.ApproveSpec(ctx, id, autoApprovalActor); err != nil {
			r.log.Error("could not approve a card automatically", "card_id", id, "error", err)
			continue
		}
		r.log.Info("approved a specification automatically", "card_id", id, "actor", autoApprovalActor)
		signed++
	}
	return signed
}

// autoApprovalActor is what §21's audit records as the approver.
//
// Deliberately not a person's name and deliberately not empty: a reader of the
// history has to be able to tell a specification a human read from one the
// control plane signed on their behalf.
const autoApprovalActor = "autonomy:auto-approve-specs"
