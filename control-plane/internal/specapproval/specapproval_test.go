package specapproval_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/specapproval"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
	"github.com/tuckermclean/strange-company/control-plane/internal/vikunja"
)

type fakeBoard struct {
	pending  []store.CardTask
	approved []uuid.UUID
	by       string
	err      error
}

func (f *fakeBoard) ListUnapprovedWithTasks(context.Context, int) ([]store.CardTask, error) {
	return f.pending, nil
}
func (f *fakeBoard) ApproveSpec(_ context.Context, id uuid.UUID, by string) error {
	if f.err != nil {
		return f.err
	}
	f.approved = append(f.approved, id)
	f.by = by
	return nil
}

type fakeLabels struct {
	labels  map[int64][]vikunja.Label
	removed []int64
	listErr error
}

func (f *fakeLabels) TaskLabels(_ context.Context, taskID int64) ([]vikunja.Label, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.labels[taskID], nil
}
func (f *fakeLabels) RemoveTaskLabel(_ context.Context, _ int64, labelID int64) error {
	f.removed = append(f.removed, labelID)
	return nil
}

func card1() store.CardTask { return store.CardTask{CardID: uuid.New(), TaskID: 42} }

// The board is a surface no model can reach, which is what makes it a human
// gate: §10.2 requires a human to approve, and MCP is agent-only by
// construction.
func TestTheApprovalLabelApprovesTheSpecification(t *testing.T) {
	ct := card1()
	b := &fakeBoard{pending: []store.CardTask{ct}}
	l := &fakeLabels{labels: map[int64][]vikunja.Label{42: {{ID: 3, Title: "spec-approved"}}}}

	res, err := specapproval.New(b, l, "spec-approved", 10, nil).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(b.approved) != 1 || b.approved[0] != ct.CardID {
		t.Fatalf("approved = %v", b.approved)
	}
	if b.by == "" {
		t.Error("the approval was not attributed to anyone")
	}
	if res.Approved != 1 {
		t.Fatalf("result = %+v", res)
	}
}

// The single most important behaviour here. Editing a specification revokes
// its approval; a label left in place would silently re-approve the new text
// on the next pass, turning one human decision into standing consent for
// every future edit.
func TestTheLabelIsRemovedOnceItHasBeenActedOn(t *testing.T) {
	b := &fakeBoard{pending: []store.CardTask{card1()}}
	l := &fakeLabels{labels: map[int64][]vikunja.Label{42: {{ID: 3, Title: "spec-approved"}}}}

	if _, err := specapproval.New(b, l, "spec-approved", 10, nil).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(l.removed) != 1 || l.removed[0] != 3 {
		t.Fatalf("removed = %v; the label must not survive to approve later edits", l.removed)
	}
}

func TestACardWithoutTheLabelIsNotApproved(t *testing.T) {
	b := &fakeBoard{pending: []store.CardTask{card1()}}
	l := &fakeLabels{labels: map[int64][]vikunja.Label{42: {{ID: 9, Title: "urgent"}}}}

	if _, err := specapproval.New(b, l, "spec-approved", 10, nil).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.approved) != 0 {
		t.Fatalf("approved a card nobody labelled: %v", b.approved)
	}
	if len(l.removed) != 0 {
		t.Fatalf("removed a label it did not act on: %v", l.removed)
	}
}

// If the approval cannot be recorded, the label must stay: removing it would
// throw away a human decision the system never acted on.
func TestAFailedApprovalLeavesTheLabelInPlace(t *testing.T) {
	b := &fakeBoard{pending: []store.CardTask{card1()}, err: errors.New("database down")}
	l := &fakeLabels{labels: map[int64][]vikunja.Label{42: {{ID: 3, Title: "spec-approved"}}}}

	res, err := specapproval.New(b, l, "spec-approved", 10, nil).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce should not fail the pass: %v", err)
	}
	if len(l.removed) != 0 {
		t.Fatal("removed the label after failing to record the approval; the decision would be lost")
	}
	if res.Failed != 1 {
		t.Fatalf("result = %+v", res)
	}
}

// One unreadable task must not stop the rest of the board being approved.
func TestOneUnreadableTaskDoesNotStopTheRest(t *testing.T) {
	b := &fakeBoard{pending: []store.CardTask{card1()}}
	l := &fakeLabels{listErr: errors.New("gone")}

	res, err := specapproval.New(b, l, "spec-approved", 10, nil).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Failed != 1 {
		t.Fatalf("result = %+v", res)
	}
}
