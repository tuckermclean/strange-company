package vikunja

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

func runLog(content string) *store.Artifact {
	return &store.Artifact{
		ID: uuid.New(), Type: store.ArtifactRunLog,
		ContentType: "text/plain", Content: content,
	}
}

// The operator surface. The run log among the attachments is the raw discourse
// §21 keeps out of the description, and downloading it is the "click".
func TestArtifactsReachTheCardAsAttachments(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(710)
	board.seedTask(bucketReview, taskID, "done")
	c := wasSynced(newCard("done", card.Review, int64Ptr(taskID)), card.Review)
	repo := &memRepo{cards: []*card.Card{c}, artifacts: []*store.Artifact{runLog("assistant: wrote it")}}

	if _, err := newTestReconciler(t, board, repo).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := board.attachmentsOn(taskID); len(got) != 1 {
		t.Fatalf("attachments = %v, want the run log", got)
	}
}

// Idempotence is the whole story. Without it the reconciler re-uploads every
// artifact on every tick and grows the operator's Vikunja without bound --
// worse than the description-rewrite loop, which only churned a timestamp.
func TestAnArtifactIsAttachedOnceNotEveryTick(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(711)
	board.seedTask(bucketReview, taskID, "done")
	c := wasSynced(newCard("done", card.Review, int64Ptr(taskID)), card.Review)
	repo := &memRepo{cards: []*card.Card{c}, artifacts: []*store.Artifact{runLog("x"), runLog("y")}}

	r := newTestReconciler(t, board, repo)
	for i := 0; i < 4; i++ {
		if _, err := r.RunOnce(context.Background()); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}

	if got := board.attachmentsOn(taskID); len(got) != 2 {
		t.Errorf("after four passes the task carries %d attachments, want 2: %v", len(got), got)
	}
}

// A card missing an attachment is worse than one with it, and far better than a
// board that has stopped tracking reality.
func TestAnInstanceWithAttachmentsOffStillReconciles(t *testing.T) {
	board := newFakeBoard(t)
	board.attachmentsDisabled = true
	taskID := int64(712)
	board.seedTask(bucketInProgress, taskID, "moving")
	c := wasSynced(newCard("moving", card.Review, int64Ptr(taskID)), card.InProgress)
	repo := &memRepo{cards: []*card.Card{c}, artifacts: []*store.Artifact{runLog("x")}}

	result, err := newTestReconciler(t, board, repo).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v; attachments being disabled must not fail the pass", err)
	}
	if result.Projected != 1 {
		t.Errorf("result = %+v, want the move projected anyway", result)
	}
}

// Names must be stable across passes, or idempotence is a fiction: anything
// varying at render time re-uploads the same file forever.
func TestAnAttachmentNameIsStableForTheSameArtifact(t *testing.T) {
	a := runLog("x")
	if attachmentName(a) != attachmentName(a) {
		t.Fatal("the same artifact produced two names")
	}
	if attachmentName(a) == attachmentName(runLog("x")) {
		t.Error("two distinct artifacts share a name; one would hide the other")
	}
}
