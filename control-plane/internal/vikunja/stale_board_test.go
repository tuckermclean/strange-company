package vikunja

import (
	"context"
	"testing"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

func synced(c *card.Card, s card.State) *card.Card {
	v := string(s)
	c.VikunjaSyncedState = &v
	return c
}

// The escalation this protects is the whole point of NeedsHuman. Read as a
// human move, Ready -> NeedsHuman is validated in reverse as NeedsHuman ->
// Ready, which is legal for a human -- so the reconciler used to un-escalate
// the exact card that had asked for a human, and the loop picked it up again.
func TestAnAgentEscalationIsNotUndoneByTheStaleBoard(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(901)
	board.seedTask(bucketReady, taskID, "escalated")

	// The card was Ready when we last projected it; an agent has since
	// escalated it. The board has not caught up.
	c := synced(newCard("escalated", card.NeedsHuman, int64Ptr(taskID)), card.Ready)
	repo := &memRepo{cards: []*card.Card{c}}

	r := newTestReconciler(t, board, repo)
	result, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if len(repo.transitions) != 0 {
		t.Fatalf("the card was transitioned %v; an agent's escalation is not a human move to validate", repo.transitions)
	}
	if c.State != card.NeedsHuman {
		t.Errorf("card state = %s, want %s: the escalation was undone", c.State, card.NeedsHuman)
	}
	if result.Projected != 1 || result.Accepted != 0 || result.Rejected != 0 {
		t.Errorf("result = %+v, want exactly one projection", result)
	}
	if got := board.tasksInBucket(bucketNeedsHuman); len(got) != 1 || got[0] != taskID {
		t.Errorf("task did not reach the NeedsHuman column: %v", got)
	}
}

// The same shape, one step earlier: Ready -> Blocked reversed is Blocked ->
// Ready, also legal for a human.
func TestAnAgentBlockIsNotUndoneByTheStaleBoard(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(902)
	board.seedTask(bucketReady, taskID, "blocked")
	c := synced(newCard("blocked", card.Blocked, int64Ptr(taskID)), card.Ready)
	repo := &memRepo{cards: []*card.Card{c}}

	if _, err := newTestReconciler(t, board, repo).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if c.State != card.Blocked {
		t.Errorf("card state = %s, want %s", c.State, card.Blocked)
	}
	if got := board.tasksInBucket(bucketBlocked); len(got) != 1 {
		t.Errorf("task did not reach the Blocked column: %v", got)
	}
}

// The projection must not swallow the case it was carved out of: when the
// board has moved AWAY from what we projected, that is a human, and it still
// gets validated.
func TestAHumanMoveIsStillReadAsOne(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(903)
	board.seedTask(bucketDone, taskID, "approved")
	// We projected Review; the human dragged it to Done.
	c := synced(newCard("approved", card.Review, int64Ptr(taskID)), card.Review)
	repo := &memRepo{cards: []*card.Card{c}}

	result, err := newTestReconciler(t, board, repo).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Accepted != 1 {
		t.Fatalf("result = %+v, want the human move accepted", result)
	}
	if c.State != card.Done {
		t.Errorf("card state = %s, want %s", c.State, card.Done)
	}
}

// An illegal human move is still reverted, and the revert is recorded as the
// new projection -- otherwise the next pass sees the board matching nothing it
// projected and re-validates the same rejected move forever.
func TestARejectedMoveRecordsWhereTheBoardNowIs(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(904)
	board.seedTask(bucketDone, taskID, "not yours to finish")
	c := synced(newCard("not yours to finish", card.Backlog, int64Ptr(taskID)), card.Backlog)
	repo := &memRepo{cards: []*card.Card{c}}

	result, err := newTestReconciler(t, board, repo).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Rejected != 1 {
		t.Fatalf("result = %+v, want the illegal move rejected", result)
	}
	if c.VikunjaSyncedState == nil || *c.VikunjaSyncedState != string(card.Backlog) {
		t.Errorf("synced state = %v, want %s", c.VikunjaSyncedState, card.Backlog)
	}
}

// Rows written before the synced-state column exists must behave exactly as
// they did, or upgrading the control plane would change how every existing
// card is interpreted on the next tick.
func TestANeverSyncedCardKeepsTheOldBehaviour(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(905)
	board.seedTask(bucketDone, taskID, "legacy")
	c := newCard("legacy", card.Review, int64Ptr(taskID)) // VikunjaSyncedState nil
	repo := &memRepo{cards: []*card.Card{c}}

	result, err := newTestReconciler(t, board, repo).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if result.Accepted != 1 {
		t.Errorf("result = %+v, want a never-synced card read as a human move, as before", result)
	}
}
