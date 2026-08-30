package vikunja

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/spec"
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

	// SetVikunjaSyncedState records the state just projected onto the
	// board, so the next pass can tell a board a human moved from a board
	// that has not caught up yet.
	SetVikunjaSyncedState(ctx context.Context, id uuid.UUID, state card.State, phase card.Phase) error

	// GetSpec returns the card's specification, or nil when it has none.
	// §33 puts acceptance criteria on the card, and they are parsed from
	// here rather than re-derived.
	GetSpec(ctx context.Context, cardID uuid.UUID) (*store.CardSpec, error)

	// ListHistory returns the card's transitions, oldest first. §33's last
	// card item is "history with timestamps".
	ListHistory(ctx context.Context, cardID uuid.UUID, limit int) ([]store.HistoryEntry, error)

	// ListArtifacts returns what the card has produced, for §33's artifact
	// list and for the attachments that carry them.
	ListArtifacts(ctx context.Context, cardID uuid.UUID) ([]*store.Artifact, error)
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
	// Projected is the number of cards whose task was moved to catch the
	// board up with a state change made in the database. These are not
	// human moves and are never validated as one.
	Projected int
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
	ladder Ladder
	log    *slog.Logger
}

// Ladder answers how many attempts a phase allows, for §33's "attempt 2/3".
// *policy.Policy satisfies it; nil is fine and simply omits the denominator.
type Ladder interface {
	AttemptsFor(phase string) int
}

// WithLadder supplies the escalation ladder used to render the denominator of
// a card's attempt count.
func (r *Reconciler) WithLadder(l Ladder) *Reconciler {
	r.ladder = l
	return r
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

		// The artifacts themselves, as attachments. This is the operator
		// surface: the run logs among them are the raw discourse §21 keeps
		// out of the description above.
		r.attach(ctx, cd, taskID)

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
			// Board and database agree. Record that, if it is not already
			// recorded: a card that happens to be in the right bucket would
			// otherwise read as never-synced forever, and the first agent
			// move after it would be misread as a human's.
			if !synced(cd) {
				if err := r.repo.SetVikunjaSyncedState(ctx, cd.ID, cd.State, cd.Phase); err != nil {
					r.log.Warn("vikunja reconcile: failed to record synced state",
						"card_id", cd.ID, "state", cd.State, "error", err)
					noteErr(err)
				}
			}
			continue
		}

		// The board disagrees with the database. Before reading that as a
		// human's intent, rule out the far more common cause: an agent
		// moved the card and the board has not caught up. A bucket that
		// still matches what we last projected is stale, not a decision.
		//
		// Read as a human move, a stale board is validated in reverse, and
		// two reversals are legal: Ready->Blocked and Ready->NeedsHuman
		// would both be undone -- the second un-escalating the very card
		// that had asked for a human.
		if cd.VikunjaSyncedState != nil && card.State(*cd.VikunjaSyncedState) == boardState {
			if err := r.project(ctx, cd); err != nil {
				r.log.Warn("vikunja reconcile: failed to project a card onto the board",
					"card_id", cd.ID, "state", cd.State, "error", err)
				noteErr(err)
				continue
			}
			result.Projected++
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

	if err := r.repo.SetVikunjaSyncedState(ctx, cd.ID, cd.State, cd.Phase); err != nil {
		return fmt.Errorf("record synced state for card %s: %w", cd.ID, err)
	}

	return nil
}

// project moves a card's task into the bucket matching the card's real state
// and records that projection.
//
// This is the agent's move reaching the board: the database is canonical here,
// and Vikunja is the view of it.
func (r *Reconciler) project(ctx context.Context, cd *card.Card) error {
	bucketID, ok := r.board.BucketByState[cd.State]
	if !ok {
		return fmt.Errorf("no bucket mapped for state %q", cd.State)
	}
	if err := r.client.MoveTaskToBucket(ctx, r.board.ProjectID, r.board.KanbanViewID, bucketID, *cd.VikunjaTaskID); err != nil {
		return fmt.Errorf("project card %s to bucket %d: %w", cd.ID, bucketID, err)
	}

	if !churn(cd) {
		r.comment(ctx, cd, r.moveNote(ctx, cd))
	}

	return r.repo.SetVikunjaSyncedState(ctx, cd.ID, cd.State, cd.Phase)
}

// synced reports whether the board already reflects where this card is.
func synced(cd *card.Card) bool {
	return cd.VikunjaSyncedState != nil && *cd.VikunjaSyncedState == string(cd.State) &&
		cd.VikunjaSyncedPhase != nil && *cd.VikunjaSyncedPhase == string(cd.Phase)
}

// churn reports whether a move is the Meeseeks lifecycle showing through
// rather than anything a human wants told about.
//
// §7.1 makes every phase claim -> advance -> release -> fresh Meeseeks, so a
// card bounces Ready <-> InProgress five times on its way to Review. Those
// flips are real state and the board must show them; commenting on each one
// buries the four moves that matter under ten that do not, and leaves a reader
// unable to tell progress from thrashing.
//
// A flip within one phase is churn. A phase advance, or a move to Review,
// Blocked, NeedsHuman or Done, is not.
func churn(cd *card.Card) bool {
	if cd.VikunjaSyncedState == nil || cd.VikunjaSyncedPhase == nil {
		return false
	}
	if *cd.VikunjaSyncedPhase != string(cd.Phase) {
		return false
	}
	inLoop := func(s string) bool { return s == string(card.Ready) || s == string(card.InProgress) }
	return inLoop(*cd.VikunjaSyncedState) && inLoop(string(cd.State))
}

// moveNote is the running account of a state change, for a task comment.
//
// The description says where a card is now. This says how it got there, which
// is the question actually asked about any card that has been sitting still.
func (r *Reconciler) moveNote(ctx context.Context, cd *card.Card) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<p>Moved to <strong>%s</strong>", html.EscapeString(string(cd.State)))
	if cd.VikunjaSyncedState != nil {
		fmt.Fprintf(&b, " from <strong>%s</strong>", html.EscapeString(*cd.VikunjaSyncedState))
	}
	fmt.Fprintf(&b, ", phase <strong>%s</strong>.</p>", html.EscapeString(string(cd.Phase)))

	if evidence, err := r.repo.ListEvidence(ctx, cd.ID); err != nil {
		r.log.Warn("vikunja reconcile: could not read evidence for a comment", "card_id", cd.ID, "error", err)
	} else if len(evidence) > 0 {
		last := evidence[len(evidence)-1]
		fmt.Fprintf(&b, "<p>%s<br>%s</p>",
			html.EscapeString(last.Summary), html.EscapeString(last.ActorID))
	}
	return b.String()
}

// comment posts a note against a card's task, and treats failing to do so as
// not worth failing a reconciliation pass over.
//
// A missing comment costs a human some context. A failed pass costs them a
// board that has stopped tracking reality, which is far worse. Installs with
// comments disabled are a deployment choice and are logged once, quietly.
func (r *Reconciler) comment(ctx context.Context, cd *card.Card, note string) {
	if cd.VikunjaTaskID == nil || note == "" {
		return
	}
	err := r.client.CreateTaskComment(ctx, *cd.VikunjaTaskID, note)
	switch {
	case err == nil:
	case errors.Is(err, ErrCommentsDisabled):
		r.log.Debug("vikunja reconcile: not commenting; this instance has task comments disabled",
			"card_id", cd.ID)
	default:
		r.log.Warn("vikunja reconcile: could not comment on a card",
			"card_id", cd.ID, "vikunja_task_id", *cd.VikunjaTaskID, "error", err)
	}
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
		if err := r.revert(ctx, cd); err != nil {
			return false, err
		}
		// A card that snaps back to where it was, with no explanation, is
		// the worst version of this: the human cannot tell a rejection
		// from a bug.
		r.comment(ctx, cd, fmt.Sprintf(
			"<p>Moved back to <strong>%s</strong>. <strong>%s</strong> to <strong>%s</strong> is not a move a human can make on this card.</p>",
			html.EscapeString(string(cd.State)),
			html.EscapeString(string(cd.State)),
			html.EscapeString(string(boardState))))
		return false, r.repo.SetVikunjaSyncedState(ctx, cd.ID, cd.State, cd.Phase)
	}

	if err := r.repo.Transition(ctx, cd.ID, boardState, card.ActorHuman, "vikunja", "moved in Vikunja"); err != nil {
		return false, fmt.Errorf("apply accepted human move to %s: %w", boardState, err)
	}
	return true, r.repo.SetVikunjaSyncedState(ctx, cd.ID, boardState, cd.Phase)
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
// maxHistoryOnCard bounds §33's "history with timestamps".
//
// A card churns Ready/InProgress once per phase, so its history is long and
// mostly lifecycle. The newest entries explain where it is now.
const maxHistoryOnCard = 12

// describe renders §33's card, in §33's order.
//
// §33 fixes both the contents and the sequence: title; state / current phase;
// why it exists; acceptance criteria; current worker; latest result; cost;
// artifacts; history with timestamps. Then: "No chain-of-thought dump. No need
// to watch a terminal scroll by. The product is visibility into work."
//
// So this is the STAKEHOLDER surface and §21 governs it absolutely -- nothing
// here is a model's reasoning. The raw discourse lives in the attachments,
// where a reader has to ask for it and is told what they are getting.
//
// Every section is omitted when it has nothing to say. A card advertising an
// empty heading teaches a reader to skim past headings.
func (r *Reconciler) describe(ctx context.Context, cd *card.Card) string {
	var b strings.Builder

	// 2. State and phase.
	fmt.Fprintf(&b, "<p><strong>%s</strong> \u00b7 phase <strong>%s</strong></p>",
		html.EscapeString(string(cd.State)), html.EscapeString(string(cd.Phase)))

	// 3. Why it exists.
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
	fmt.Fprintf(&b, "<li>Card <code>%s</code></li>", html.EscapeString(cd.ID.String()))
	b.WriteString("</ul>")

	// 5. Current worker, and 7. cost.
	if line := r.workerLine(cd); line != "" {
		b.WriteString(line)
	}
	b.WriteString(r.progressLine(cd))
	b.WriteString(r.costLine(cd))

	// 6. Latest result.
	if evidence, err := r.repo.ListEvidence(ctx, cd.ID); err != nil {
		r.log.Warn("could not read evidence for a task description", "card_id", cd.ID, "error", err)
	} else if len(evidence) > 0 {
		last := evidence[len(evidence)-1]
		fmt.Fprintf(&b, "<p><em>%s</em><br>%s</p>",
			html.EscapeString(last.Summary), html.EscapeString(last.ActorID))
	}

	// 4. Acceptance criteria.
	b.WriteString(r.criteriaSection(ctx, cd))

	// 8. Artifacts.
	b.WriteString(r.artifactSection(ctx, cd))

	// 9. History with timestamps.
	b.WriteString(r.historySection(ctx, cd))

	return b.String()
}

// workerLine renders §33's "Meeseeks #8f2c — Implementation — Haiku attempt 2/3".
//
// The denominator is the point. "attempt 2" tells a reader nothing; "2/3" tells
// them how much rope is left, which is the question actually being asked of a
// card that is taking a while.
func (r *Reconciler) workerLine(cd *card.Card) string {
	if cd.ClaimedBy == nil || *cd.ClaimedBy == "" {
		return ""
	}

	worker := *cd.ClaimedBy
	if i := strings.LastIndex(worker, "-"); i >= 0 && i < len(worker)-1 {
		worker = worker[i+1:]
	}

	attempt := cd.ImplementationAttempt + 1
	of := ""
	if r.ladder != nil {
		if total := r.ladder.AttemptsFor(string(cd.Phase)); total > 0 {
			of = fmt.Sprintf("/%d", total)
		}
	}

	return fmt.Sprintf("<p>Meeseeks <code>%s</code> \u00b7 %s \u00b7 attempt %d%s</p>",
		html.EscapeString(worker), html.EscapeString(string(cd.Phase)), attempt, of)
}

// progressLine reports what a card has spent, whether or not anyone is
// currently holding it.
//
// §33 folds the attempt count into the current-worker line, which is right
// while a card is being worked and useless the rest of the time: a card
// sitting in Ready having burned two of three attempts is exactly the card a
// reader needs to notice, and it has no current worker at all.
//
// Shown only once a counter has moved. Zeroes on every card train a reader to
// skip the line.
func (r *Reconciler) progressLine(cd *card.Card) string {
	var parts []string
	if cd.ImplementationAttempt > 0 {
		of := ""
		if r.ladder != nil {
			if total := r.ladder.AttemptsFor(string(cd.Phase)); total > 0 {
				of = fmt.Sprintf(" of %d", total)
			}
		}
		parts = append(parts, fmt.Sprintf("Implementation attempts: %d%s", cd.ImplementationAttempt, of))
	}
	if cd.InfrastructureFailures > 0 {
		parts = append(parts, fmt.Sprintf("Infrastructure failures: %d in a row", cd.InfrastructureFailures))
	}
	if len(parts) == 0 {
		return ""
	}
	return "<p>" + html.EscapeString(strings.Join(parts, " \u00b7 ")) + "</p>"
}

// costLine renders §33's "$0.41 so far".
//
// An unpriced card says so rather than showing $0.00. Every opencode run is
// unpriced until an operator configures rates, and a card reading "$0.00" would
// have a reader conclude the work was free rather than unmeasured -- which is
// the same lie GET /cards/{id}/cost was built to stop telling.
func (r *Reconciler) costLine(cd *card.Card) string {
	if cd.CostUSD <= 0 {
		return "<p>Cost: unpriced \u2014 no rate card configured for this card's models</p>"
	}
	if cd.MaxCostUSD != nil && *cd.MaxCostUSD > 0 {
		return fmt.Sprintf("<p>Cost: $%.2f so far, of $%.2f</p>", cd.CostUSD, *cd.MaxCostUSD)
	}
	return fmt.Sprintf("<p>Cost: $%.2f so far</p>", cd.CostUSD)
}

// criteriaSection renders §33's acceptance criteria.
//
// Parsed by internal/spec, not scraped: the specification gate already depends
// on those criteria being structured, so this renders what was validated. A
// card with no spec, or a spec with no criteria, shows nothing -- a card
// claiming criteria it does not have is worse than one admitting it has none.
func (r *Reconciler) criteriaSection(ctx context.Context, cd *card.Card) string {
	cardSpec, err := r.repo.GetSpec(ctx, cd.ID)
	if err != nil {
		r.log.Warn("could not read the spec for a task description", "card_id", cd.ID, "error", err)
		return ""
	}
	if cardSpec == nil || strings.TrimSpace(cardSpec.Content) == "" {
		return ""
	}

	doc, _ := spec.Parse(cd.ID.String(), []byte(cardSpec.Content))
	if doc == nil || len(doc.Criteria) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("<p><strong>Acceptance criteria</strong></p><ul>")
	for _, c := range doc.Criteria {
		b.WriteString("<li>")
		if c.ID != "" {
			fmt.Fprintf(&b, "<code>%s</code> ", html.EscapeString(c.ID))
		}
		b.WriteString(html.EscapeString(c.Text))
		if c.Verification != "" {
			fmt.Fprintf(&b, " \u2014 verified by: %s", html.EscapeString(c.Verification))
		}
		b.WriteString("</li>")
	}
	b.WriteString("</ul>")
	return b.String()
}

// artifactSection lists what the card has produced.
//
// Names match the attachments, so a reader looking at the list knows what to
// download. §21 holds here: this names the artifacts, it does not inline them.
func (r *Reconciler) artifactSection(ctx context.Context, cd *card.Card) string {
	artifacts, err := r.repo.ListArtifacts(ctx, cd.ID)
	if err != nil {
		r.log.Warn("could not read artifacts for a task description", "card_id", cd.ID, "error", err)
		return ""
	}
	if len(artifacts) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("<p><strong>Artifacts</strong> (attached to this task)</p><ul>")
	for _, a := range artifacts {
		fmt.Fprintf(&b, "<li><code>%s</code>", html.EscapeString(attachmentName(a)))
		if a.Truncated {
			fmt.Fprintf(&b, " \u2014 truncated; %d bytes in full", a.SizeBytes)
		}
		b.WriteString("</li>")
	}
	b.WriteString("</ul>")
	return b.String()
}

// historySection renders §33's "history with timestamps".
func (r *Reconciler) historySection(ctx context.Context, cd *card.Card) string {
	entries, err := r.repo.ListHistory(ctx, cd.ID, maxHistoryOnCard)
	if err != nil {
		r.log.Warn("could not read history for a task description", "card_id", cd.ID, "error", err)
		return ""
	}
	if len(entries) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("<p><strong>History</strong></p><ul>")
	for _, e := range entries {
		fmt.Fprintf(&b, "<li>%s \u00b7 ", html.EscapeString(e.At.UTC().Format("2006-01-02 15:04:05Z")))
		if e.From != "" {
			fmt.Fprintf(&b, "%s \u2192 ", html.EscapeString(e.From))
		}
		fmt.Fprintf(&b, "<strong>%s</strong> (%s)", html.EscapeString(e.To), html.EscapeString(e.ActorType))
		if e.Reason != "" {
			fmt.Fprintf(&b, "<br>%s", html.EscapeString(e.Reason))
		}
		b.WriteString("</li>")
	}
	b.WriteString("</ul>")
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

// attach uploads any artifact the task does not already carry.
//
// Idempotence is by name: artifacts are immutable, so a name already attached
// is already done. Without that check this re-uploads every artifact on every
// tick and grows the operator's Vikunja without bound -- the same shape as the
// description-rewrite loop, with a worse consequence than a noisy timestamp.
//
// Never fatal. A card missing an attachment is worse than one with it and far
// better than a board that has stopped tracking reality.
func (r *Reconciler) attach(ctx context.Context, cd *card.Card, taskID int64) {
	artifacts, err := r.repo.ListArtifacts(ctx, cd.ID)
	if err != nil {
		r.log.Warn("vikunja reconcile: could not read artifacts to attach", "card_id", cd.ID, "error", err)
		return
	}
	if len(artifacts) == 0 {
		return
	}

	existing, err := r.client.ListAttachments(ctx, taskID)
	switch {
	case errors.Is(err, ErrAttachmentsDisabled):
		r.log.Debug("vikunja reconcile: not attaching; this instance has task attachments disabled",
			"card_id", cd.ID)
		return
	case err != nil:
		// Uploading without knowing what is there would duplicate
		// everything, every tick. Skipping the pass is the safe failure.
		r.log.Warn("vikunja reconcile: could not list attachments", "card_id", cd.ID, "error", err)
		return
	}

	have := make(map[string]bool, len(existing))
	for _, a := range existing {
		have[a.File.Name] = true
	}

	for _, a := range artifacts {
		name := attachmentName(a)
		if have[name] {
			continue
		}
		if err := r.client.UploadAttachment(ctx, taskID, name, []byte(a.Content)); err != nil {
			if errors.Is(err, ErrAttachmentsDisabled) {
				return
			}
			r.log.Warn("vikunja reconcile: could not attach an artifact",
				"card_id", cd.ID, "artifact", name, "error", err)
			continue
		}
		have[name] = true
	}
}
