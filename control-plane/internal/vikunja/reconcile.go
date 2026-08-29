package vikunja

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
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

	// ListEvidence returns what the workers recorded about a card, oldest
	// first. It is the only account of WHY a card is where it is, and until
	// now nothing read it: a card moved column and the reason stayed in the
	// database.
	ListEvidence(ctx context.Context, cardID uuid.UUID) ([]store.CardEvidence, error)
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
	taskByID := make(map[int64]*Task)
	for _, b := range buckets {
		for _, t := range b.Tasks {
			taskBucket[t.ID] = b.ID
			taskByID[t.ID] = t
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

		// Keep the summary current. A description written once at create
		// and never touched again is worse than none: it describes a card
		// that has since moved on, and a reader has no way to tell.
		if task, ok := taskByID[taskID]; ok {
			if want := r.describe(ctx, cd); !sameDescription(task.Description, want) {
				if err := r.client.UpdateTask(ctx, taskID, cd.Title, want); err != nil {
					r.log.Warn("vikunja reconcile: could not refresh a task description",
						"card_id", cd.ID, "vikunja_task_id", taskID, "error", err)
				}
			}
		}

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

	if err := r.client.UpdateTask(ctx, task.ID, cd.Title, r.describe(ctx, cd)); err != nil {
		// Not fatal. A card on the board without its description is worse
		// than one with it, and far better than one that never appeared.
		r.log.Error("could not describe a new task", "card_id", cd.ID, "task", task.ID, "error", err)
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

// describe renders what a human needs to know about a card, for the task body.
//
// A board of bare titles tells a reader nothing they did not already know from
// the issue. This is what the card IS, where it came from, and -- the part
// that was previously unreachable outside the database -- why it is in the
// column it is in.
//
// Vikunja stores descriptions as HTML.
func (r *Reconciler) describe(ctx context.Context, cd *card.Card) string {
	var b strings.Builder

	fmt.Fprintf(&b, "<p><strong>%s</strong> \u00b7 phase <strong>%s</strong></p>",
		html.EscapeString(string(cd.State)), html.EscapeString(string(cd.Phase)))

	b.WriteString("<ul>")
	if cd.SourceURL != nil && *cd.SourceURL != "" {
		fmt.Fprintf(&b, "<li>Source: %s</li>", html.EscapeString(*cd.SourceURL))
	}
	if cd.RepoURL != nil && *cd.RepoURL != "" {
		fmt.Fprintf(&b, "<li>Repository: %s", html.EscapeString(*cd.RepoURL))
		if cd.RepoBaseRef != nil && *cd.RepoBaseRef != "" {
			fmt.Fprintf(&b, " (%s)", html.EscapeString(*cd.RepoBaseRef))
		}
		b.WriteString("</li>")
	}
	// Only when they have moved. A counter reading zero on every card is
	// noise that trains a reader to skip the list.
	if cd.ImplementationAttempt > 0 {
		fmt.Fprintf(&b, "<li>Implementation attempts: %d</li>", cd.ImplementationAttempt)
	}
	if cd.InfrastructureFailures > 0 {
		fmt.Fprintf(&b, "<li>Infrastructure failures: %d</li>", cd.InfrastructureFailures)
	}
	fmt.Fprintf(&b, "<li>Card <code>%s</code></li>", html.EscapeString(cd.ID.String()))
	b.WriteString("</ul>")

	// The most recent account of what happened. §21 wants "what happened to
	// card X?" answerable; this is the one line of it that matters most --
	// the reason this card is sitting where it is.
	if evidence, err := r.repo.ListEvidence(ctx, cd.ID); err != nil {
		r.log.Warn("could not read evidence for a task description", "card_id", cd.ID, "error", err)
	} else if len(evidence) > 0 {
		last := evidence[len(evidence)-1]
		fmt.Fprintf(&b, "<p><em>%s</em><br>%s</p>",
			html.EscapeString(last.Summary), html.EscapeString(last.ActorID))
	}

	return b.String()
}

// sameDescription reports whether two descriptions say the same thing.
//
// Comparing the HTML directly does not work: Vikunja sanitises what it stores,
// so the text read back is never byte-identical to the text sent -- and a
// reconciler that believes every description is stale rewrites every card on
// the board once a tick, churning "recently updated" for a human who is trying
// to use it. Compare what a reader would actually see instead.
func sameDescription(a, b string) bool {
	return descriptionText(a) == descriptionText(b)
}

func descriptionText(s string) string {
	var out strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
			out.WriteRune(' ') // a dropped tag is still a word boundary
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			out.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(html.UnescapeString(out.String())), " ")
}
