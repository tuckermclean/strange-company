package vikunja

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

// The description says where a card is now. Nothing said how it got there,
// and that is the question actually asked about any card that has been
// sitting still.
func TestAStateChangeLeavesAnAccountOnTheCard(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(910)
	board.seedTask(bucketInProgress, taskID, "moving")
	c := wasSynced(newCard("moving", card.Review, int64Ptr(taskID)), card.InProgress)
	repo := &memRepo{
		cards:    []*card.Card{c},
		evidence: map[uuid.UUID][]store.CardEvidence{c.ID: {{ActorID: "meeseeks-9", Summary: "12 tests pass"}}},
	}

	if _, err := newTestReconciler(t, board, repo).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	notes := board.commentsOn(taskID)
	if len(notes) != 1 {
		t.Fatalf("got %d comments, want exactly one for one move: %v", len(notes), notes)
	}
	got := descriptionText(notes[0])
	for _, want := range []string{"Review", "InProgress", "12 tests pass", "meeseeks-9"} {
		if !strings.Contains(got, want) {
			t.Errorf("the account is missing %q\ngot: %s", want, got)
		}
	}
}

// A card that snaps back to where it was, with no explanation, leaves the
// human unable to tell a rejection from a bug.
func TestARejectedMoveSaysWhyItSnappedBack(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(911)
	board.seedTask(bucketDone, taskID, "not yours to finish")
	c := wasSynced(newCard("not yours to finish", card.Backlog, int64Ptr(taskID)), card.Backlog)
	repo := &memRepo{cards: []*card.Card{c}}

	if _, err := newTestReconciler(t, board, repo).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	notes := board.commentsOn(taskID)
	if len(notes) != 1 {
		t.Fatalf("got %d comments, want one explaining the revert: %v", len(notes), notes)
	}
	got := descriptionText(notes[0])
	if !strings.Contains(got, "Backlog") || !strings.Contains(got, "Done") {
		t.Errorf("the note does not name the move it refused\ngot: %s", got)
	}
}

// A settled card must not accumulate a comment every tick.
func TestASettledCardIsNotCommentedOnAgain(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(912)
	board.seedTask(bucketInProgress, taskID, "moving")
	c := wasSynced(newCard("moving", card.Review, int64Ptr(taskID)), card.InProgress)
	repo := &memRepo{cards: []*card.Card{c}}
	r := newTestReconciler(t, board, repo)

	for i := 0; i < 3; i++ {
		if _, err := r.RunOnce(context.Background()); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if n := len(board.commentsOn(taskID)); n != 1 {
		t.Errorf("got %d comments after three passes, want 1; the card would fill with noise", n)
	}
}

// Comments are optional in Vikunja (service.enabletaskcomments). Losing the
// running account costs a human some context; failing the pass costs them a
// board that has stopped tracking reality.
func TestAnInstanceWithCommentsOffStillReconciles(t *testing.T) {
	board := newFakeBoard(t)
	board.commentsDisabled = true
	taskID := int64(913)
	board.seedTask(bucketInProgress, taskID, "moving")
	c := wasSynced(newCard("moving", card.Review, int64Ptr(taskID)), card.InProgress)
	repo := &memRepo{cards: []*card.Card{c}}

	result, err := newTestReconciler(t, board, repo).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v; comments being disabled must not fail the pass", err)
	}
	if result.Projected != 1 {
		t.Errorf("result = %+v, want the move projected anyway", result)
	}
	if got := board.tasksInBucket(bucketReview); len(got) != 1 {
		t.Errorf("the card did not reach Review: %v", got)
	}
}

// §7.1 makes every phase claim -> advance -> release -> fresh Meeseeks, so a
// card bounces Ready <-> InProgress five times on its way to Review. A comment
// on each buries the moves that matter under ones that do not, and leaves a
// reader unable to tell progress from thrashing.
func TestTheMeeseeksLifecycleDoesNotFillTheCardWithComments(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(920)
	board.seedTask(bucketReady, taskID, "working")

	// Same phase, the card just came back under a fresh Meeseeks.
	c := newCard("working", card.InProgress, int64Ptr(taskID))
	c.Phase = card.PhaseImplementation
	wasSynced(c, card.Ready)
	repo := &memRepo{cards: []*card.Card{c}}

	result, err := newTestReconciler(t, board, repo).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	// The board must still show the move -- it is real state.
	if result.Projected != 1 {
		t.Errorf("result = %+v, want the move projected to the board", result)
	}
	if got := board.tasksInBucket(bucketInProgress); len(got) != 1 {
		t.Errorf("the card did not move to InProgress: %v", got)
	}
	if notes := board.commentsOn(taskID); len(notes) != 0 {
		t.Errorf("got %d comments for a within-phase flip: %v", len(notes), notes)
	}
}

// The corollary: the moves that are not churn must still be told. Without
// this, suppressing the flips would silence the card entirely.
func TestAPhaseAdvanceIsStillWorthTelling(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(921)
	board.seedTask(bucketReady, taskID, "advancing")

	c := newCard("advancing", card.InProgress, int64Ptr(taskID))
	c.Phase = card.PhaseImplementation
	wasSynced(c, card.Ready)
	// The synced phase is the one before: this flip carries an advance.
	prev := string(card.PhaseTests)
	c.VikunjaSyncedPhase = &prev

	repo := &memRepo{cards: []*card.Card{c}}
	if _, err := newTestReconciler(t, board, repo).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if notes := board.commentsOn(taskID); len(notes) != 1 {
		t.Fatalf("got %d comments for a phase advance, want 1: %v", len(notes), notes)
	}
}

// Moves out of the working loop always speak, whatever the phase does.
func TestReachingReviewIsAlwaysTold(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(922)
	board.seedTask(bucketInProgress, taskID, "done working")

	c := newCard("done working", card.Review, int64Ptr(taskID))
	c.Phase = card.PhaseReview
	wasSynced(c, card.InProgress)

	repo := &memRepo{cards: []*card.Card{c}}
	if _, err := newTestReconciler(t, board, repo).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if notes := board.commentsOn(taskID); len(notes) != 1 {
		t.Errorf("got %d comments for a card reaching Review: %v", len(notes), notes)
	}
}
