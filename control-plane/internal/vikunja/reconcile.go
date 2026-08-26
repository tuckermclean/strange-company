package vikunja

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

// CardRepo is the persistence dependency Reconciler needs. It is satisfied
// by the control plane's card store; it is expressed as an interface here
// so tests can supply an in-memory implementation without a database.
type CardRepo interface {
	// ListCards returns every card the reconciler should consider. There is
	// no paging here in M1 — the control plane's card volume is expected to
	// stay well within a single page for the foreseeable future.
	ListCards(ctx context.Context) ([]*card.Card, error)

	// Transition applies a validated state change to the card identified by
	// id, recording actor/actorID/reason in its immutable history. Callers
	// must have already validated the move with card.CanTransition;
	// Transition itself is not expected to re-validate.
	Transition(ctx context.Context, id uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error

	// SetVikunjaTaskID links a previously-unlinked card to the Vikunja task
	// created for it.
	SetVikunjaTaskID(ctx context.Context, id uuid.UUID, taskID int64) error
}

// Result summarizes the outcome of a single reconciliation pass.
type Result struct {
	// Checked is the number of cards examined plus the number of Vikunja
	// tasks found on the board with no matching card.
	Checked int
	// Pushed is the number of previously-unlinked cards for which a new
	// Vikunja task was created.
	Pushed int
	// Accepted is the number of legal human moves (card state and bucket
	// disagreed, and the move was a valid transition) that were applied to
	// the card as canonical.
	Accepted int
	// Rejected is the number of illegal human moves that were reverted by
	// moving the task back to the bucket matching the card's real state.
	Rejected int
}

// Reconciler periodically reconciles the control plane's canonical card
// state against Vikunja's Kanban board, which is only ever a projection of
// it (spec section 4.3). Vikunja webhooks are treated purely as hints that
// something may have changed sooner than the next scheduled run; this type
// is the actual source of truth for what becomes canonical.
type Reconciler struct {
	client *Client
	board  *Board
	repo   CardRepo
	log    *slog.Logger
}

// NewReconciler returns a Reconciler that syncs cards from repo against the
// Kanban board described by board, using c to talk to Vikunja. If log is
// nil, slog.Default() is used.
func NewReconciler(c *Client, board *Board, repo CardRepo, log *slog.Logger) *Reconciler {
	if log == nil {
		log = slog.Default()
	}
	return &Reconciler{client: c, board: board, repo: repo, log: log}
}

// Run calls RunOnce every `every` until ctx is done, logging (but not
// otherwise acting on) any error RunOnce returns. It blocks until ctx is
// canceled.
func (r *Reconciler) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := r.RunOnce(ctx); err != nil {
				r.log.Error("vikunja reconcile: run failed", "error", err)
			}
		}
	}
}

// RunOnce performs a single reconciliation pass:
//
//  1. Loads every card and the current bucket contents of the Kanban view.
//  2. A card with no linked Vikunja task gets one created, in the bucket
//     matching its current state, and is linked via SetVikunjaTaskID.
//  3. A linked card whose task sits in the bucket matching its state needs
//     no action.
//  4. A linked card whose task sits in a *different* bucket means a human
//     moved it in the Vikunja UI. That move is validated as a human
//     transition via card.CanTransition: if legal, it is applied to the
//     card as canonical; if illegal, the task is moved back to the bucket
//     matching the card's real state and the move never becomes canonical.
//  5. Vikunja tasks with no matching card are ignored (M1 does not create
//     cards from bare tasks); they are only counted and logged once, at
//     Debug, per run.
//
// A failure handling one card is logged and does not stop the run from
// processing the rest; RunOnce returns the first error encountered (or
// nil) only after every card has been considered.
func (r *Reconciler) RunOnce(ctx context.Context) (Result, error) {
	var result Result
	var firstErr error
	noteErr := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	cards, err := r.repo.ListCards(ctx)
	if err != nil {
		return result, fmt.Errorf("vikunja reconcile: list cards: %w", err)
	}

	buckets, err := r.client.ListBoardTasks(ctx, r.board.ProjectID, r.board.KanbanViewID)
	if err != nil {
		return result, fmt.Errorf("vikunja reconcile: list board tasks: %w", err)
	}

	// taskBucket maps every task currently on the board to the id of the
	// bucket it sits in.
	taskBucket := make(map[int64]int64)
	for _, b := range buckets {
		for _, t := range b.Tasks {
			taskBucket[t.ID] = b.ID
		}
	}

	// linkedTaskIDs tracks which board tasks are claimed by a card, so the
	// remainder (after the main loop) are tasks with no matching card.
	linkedTaskIDs := make(map[int64]bool, len(cards))

	for _, cd := range cards {
		result.Checked++

		if cd.VikunjaTaskID == nil {
			if err := r.push(ctx, cd); err != nil {
				r.log.Warn("vikunja reconcile: failed to push unlinked card",
					"card_id", cd.ID, "error", err)
				noteErr(err)
				continue
			}
			result.Pushed++
			continue
		}

		taskID := *cd.VikunjaTaskID
		linkedTaskIDs[taskID] = true

		bucketID, onBoard := taskBucket[taskID]
		if !onBoard {
			r.log.Warn("vikunja reconcile: linked task not found on board",
				"card_id", cd.ID, "vikunja_task_id", taskID)
			continue
		}

		boardState, known := r.board.StateForBucket(bucketID)
		if !known {
			r.log.Warn("vikunja reconcile: task sits in a bucket outside the managed board",
				"card_id", cd.ID, "vikunja_task_id", taskID, "bucket_id", bucketID)
			continue
		}

		if boardState == cd.State {
			// Board and database agree: nothing to do.
			continue
		}

		accepted, err := r.reconcileDisagreement(ctx, cd, boardState)
		if err != nil {
			r.log.Warn("vikunja reconcile: failed to reconcile disagreement",
				"card_id", cd.ID, "card_state", cd.State, "board_state", boardState, "error", err)
			noteErr(err)
			continue
		}

		if accepted {
			result.Accepted++
		} else {
			result.Rejected++
		}
	}

	var orphaned int
	for taskID := range taskBucket {
		if !linkedTaskIDs[taskID] {
			orphaned++
		}
	}
	if orphaned > 0 {
		result.Checked += orphaned
		r.log.Debug("vikunja reconcile: ignoring tasks with no matching card", "count", orphaned)
	}

	return result, firstErr
}

// push creates a new Vikunja task for cd, places it in the bucket matching
// cd's current state, and links the two via SetVikunjaTaskID.
func (r *Reconciler) push(ctx context.Context, cd *card.Card) error {
	bucketID, ok := r.board.BucketByState[cd.State]
	if !ok {
		return fmt.Errorf("no bucket mapped for state %q", cd.State)
	}

	task, err := r.client.CreateTask(ctx, r.board.ProjectID, cd.Title)
	if err != nil {
		return fmt.Errorf("create task: %w", err)
	}

	if err := r.client.MoveTaskToBucket(ctx, r.board.ProjectID, r.board.KanbanViewID, bucketID, task.ID); err != nil {
		return fmt.Errorf("move new task %d to bucket %d: %w", task.ID, bucketID, err)
	}

	if err := r.repo.SetVikunjaTaskID(ctx, cd.ID, task.ID); err != nil {
		return fmt.Errorf("link card %s to task %d: %w", cd.ID, task.ID, err)
	}

	return nil
}

// reconcileDisagreement handles a linked card whose board bucket disagrees
// with its canonical state: cd.State -> boardState is validated as a human
// move. A legal move is applied to the card via repo.Transition and
// reported as accepted; an illegal one is reverted by moving the Vikunja
// task back to the bucket matching cd's real (unchanged) state and reported
// as not accepted.
func (r *Reconciler) reconcileDisagreement(ctx context.Context, cd *card.Card, boardState card.State) (accepted bool, err error) {
	if err := card.CanTransition(cd.State, boardState, card.ActorHuman); err != nil {
		r.log.Warn("vikunja reconcile: rejecting illegal human move",
			"card_id", cd.ID, "from", cd.State, "to", boardState, "reason", err)
		return false, r.revert(ctx, cd)
	}

	if err := r.repo.Transition(ctx, cd.ID, boardState, card.ActorHuman, "vikunja", "moved in Vikunja"); err != nil {
		return false, fmt.Errorf("apply accepted human move to %s: %w", boardState, err)
	}
	return true, nil
}

// revert moves cd's linked task back to the bucket matching cd's real
// (canonical) state, undoing an illegal move made in the Vikunja UI.
func (r *Reconciler) revert(ctx context.Context, cd *card.Card) error {
	bucketID, ok := r.board.BucketByState[cd.State]
	if !ok {
		return fmt.Errorf("no bucket mapped for state %q", cd.State)
	}

	if err := r.client.MoveTaskToBucket(ctx, r.board.ProjectID, r.board.KanbanViewID, bucketID, *cd.VikunjaTaskID); err != nil {
		return fmt.Errorf("move task %d back to bucket %d: %w", *cd.VikunjaTaskID, bucketID, err)
	}
	return nil
}
