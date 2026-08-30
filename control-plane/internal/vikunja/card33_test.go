package vikunja

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

type fixedLadder int

func (f fixedLadder) AttemptsFor(string) int { return int(f) }

func specWithCriteria() *store.CardSpec {
	return &store.CardSpec{Content: `# Context

A thing.

# Task

Do the thing.

# Acceptance criteria

- AC1: returns 200 — verified by: ` + "`go test ./...`" + `
`}
}

// §33 fixes what a card carries. Before this it carried a state, a source link
// and one line of evidence -- which is most of what a reader already knew.
func TestTheCardCarriesWhatSection33Asks(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(700)
	board.seedTask(bucketInProgress, taskID, "the work")

	c := newCard("the work", card.InProgress, int64Ptr(taskID))
	c.Phase = card.PhaseImplementation
	c.ImplementationAttempt = 2
	claimed := "meeseeks-control-plane-abc123"
	c.ClaimedBy = &claimed
	c.CostUSD = 0.41

	repo := &memRepo{
		cards: []*card.Card{c},
		spec:  specWithCriteria(),
		history: []store.HistoryEntry{
			{At: time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC), From: "Ready", To: "InProgress",
				ActorType: "agent", ActorID: "meeseeks-1", Reason: "claimed"},
		},
		artifacts: []*store.Artifact{
			{ID: uuid.New(), Type: store.ArtifactRunLog, ContentType: "text/plain", Content: "assistant: hello"},
		},
	}

	r := newTestReconciler(t, board, repo).WithLadder(fixedLadder(3))
	got := descriptionText(r.describe(context.Background(), c))

	for _, want := range []string{
		"InProgress",                 // 2. state
		"implementation",             // 2. phase
		"abc123",                     // 5. current worker
		"attempt 3/3",                // 5. how much rope is left, in §33's own format
		"$0.41 so far",               // 7. cost
		"AC1",                        // 4. acceptance criteria
		"returns 200",                //
		"run-log",                    // 8. artifacts
		"2026-08-30 09:00:00Z",       // 9. history with timestamps
		"claimed",                    //
	} {
		if !strings.Contains(got, want) {
			t.Errorf("card is missing %q\ngot: %s", want, got)
		}
	}
}

// The denominator is the point. "attempt 2" tells a reader nothing; "2 of 3"
// tells them how much rope is left.
func TestWithoutALadderTheDenominatorIsOmittedRatherThanInvented(t *testing.T) {
	board := newFakeBoard(t)
	c := newCard("x", card.InProgress, int64Ptr(701))
	c.ImplementationAttempt = 2
	repo := &memRepo{cards: []*card.Card{c}}

	got := descriptionText(newTestReconciler(t, board, repo).describe(context.Background(), c))
	if !strings.Contains(got, "Implementation attempts: 2") {
		t.Errorf("attempts missing: %s", got)
	}
	if strings.Contains(got, " of 0") {
		t.Errorf("a meaningless denominator was printed: %s", got)
	}
}

// Every opencode run is unpriced until an operator configures rates. A card
// reading "$0.00" would have a reader conclude the work was free rather than
// unmeasured -- the same lie /cards/{id}/cost was built to stop telling.
func TestAnUnpricedCardSaysSoRatherThanShowingZero(t *testing.T) {
	board := newFakeBoard(t)
	c := newCard("x", card.Review, int64Ptr(702))
	repo := &memRepo{cards: []*card.Card{c}}

	got := descriptionText(newTestReconciler(t, board, repo).describe(context.Background(), c))
	if !strings.Contains(got, "unpriced") {
		t.Errorf("card does not say the cost is unknown: %s", got)
	}
	if strings.Contains(got, "$0.00") {
		t.Errorf("card shows $0.00, which reads as free: %s", got)
	}
}

// A card claiming criteria it does not have is worse than one admitting it has
// none.
func TestACardWithNoSpecClaimsNoCriteria(t *testing.T) {
	board := newFakeBoard(t)
	c := newCard("x", card.Backlog, int64Ptr(703))
	repo := &memRepo{cards: []*card.Card{c}}

	got := descriptionText(newTestReconciler(t, board, repo).describe(context.Background(), c))
	if strings.Contains(got, "Acceptance criteria") {
		t.Errorf("an empty criteria heading was rendered: %s", got)
	}
}

// §21 governs this surface absolutely: the description names artifacts, it
// never inlines what a model said.
func TestTheDescriptionNamesArtifactsWithoutInliningThem(t *testing.T) {
	board := newFakeBoard(t)
	c := newCard("x", card.Review, int64Ptr(704))
	repo := &memRepo{
		cards: []*card.Card{c},
		artifacts: []*store.Artifact{{
			ID: uuid.New(), Type: store.ArtifactRunLog, ContentType: "text/plain",
			Content: "assistant: I think the user probably wants me to rewrite everything",
		}},
	}

	got := descriptionText(newTestReconciler(t, board, repo).describe(context.Background(), c))
	if !strings.Contains(got, "run-log") {
		t.Errorf("artifact not listed: %s", got)
	}
	if strings.Contains(got, "I think the user probably wants") {
		t.Error("model reasoning was inlined into the stakeholder view (§21)")
	}
}
