// Package specapproval turns a label on the board into a §10.2 approval.
//
// §10.2 requires a HUMAN to approve a specification. Everything reaching the
// control plane through MCP is an agent by construction, so a model can state
// that a human approved but cannot make it so. The Vikunja board is a surface
// no model has access to, which is what makes a label there a human decision.
package specapproval

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/store"
	"github.com/tuckermclean/strange-company/control-plane/internal/vikunja"
)

// approvedBy is what the approval is attributed to.
//
// Vikunja does not tell us which human added a label, so this records the
// surface rather than inventing a name -- the same attribution the reconciler
// already uses for a human's drag between columns.
const approvedBy = "vikunja"

// Board is the part of the store this package uses.
type Board interface {
	ListUnapprovedWithTasks(ctx context.Context, limit int) ([]store.CardTask, error)
	ApproveSpec(ctx context.Context, cardID uuid.UUID, approvedBy string) error
}

// Labels is the part of Vikunja this package uses.
type Labels interface {
	TaskLabels(ctx context.Context, taskID int64) ([]vikunja.Label, error)
	RemoveTaskLabel(ctx context.Context, taskID, labelID int64) error
}

// Result summarises one pass.
type Result struct {
	Approved int
	Failed   int
}

// Reconciler applies approval labels.
type Reconciler struct {
	board Board
	tasks Labels
	label string
	limit int
	log   *slog.Logger
}

// New builds a Reconciler. label is the label title a human adds to approve.
func New(b Board, l Labels, label string, limit int, log *slog.Logger) *Reconciler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Reconciler{board: b, tasks: l, label: label, limit: limit, log: log}
}

// RunOnce applies every approval label currently on the board.
func (r *Reconciler) RunOnce(ctx context.Context) (Result, error) {
	var res Result

	pending, err := r.board.ListUnapprovedWithTasks(ctx, r.limit)
	if err != nil {
		return res, err
	}

	for _, ct := range pending {
		labels, err := r.tasks.TaskLabels(ctx, ct.TaskID)
		if err != nil {
			r.log.Error("could not read a task's labels", "task_id", ct.TaskID, "error", err)
			res.Failed++
			continue
		}

		label, found := find(labels, r.label)
		if !found {
			continue
		}

		if err := r.board.ApproveSpec(ctx, ct.CardID, approvedBy); err != nil {
			// The label stays. Removing it now would throw away a human
			// decision the system never acted on, and nobody would know
			// to make it again.
			r.log.Error("could not record an approval from the board",
				"card_id", ct.CardID, "task_id", ct.TaskID, "error", err)
			res.Failed++
			continue
		}
		res.Approved++

		// Removed because it has been acted on. Editing a specification
		// revokes its approval, and a label left in place would silently
		// re-approve the new text on the next pass -- turning one human
		// decision into standing consent for every future edit.
		if err := r.tasks.RemoveTaskLabel(ctx, ct.TaskID, label.ID); err != nil {
			r.log.Error("approved, but could not remove the approval label",
				"card_id", ct.CardID, "task_id", ct.TaskID, "error", err)
		}
	}

	return res, nil
}

func find(labels []vikunja.Label, title string) (vikunja.Label, bool) {
	for _, l := range labels {
		if l.Title == title {
			return l, true
		}
	}
	return vikunja.Label{}, false
}
