package vikunja

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

func strPtr(s string) *string { return &s }

// A board of bare titles tells a reader nothing they did not already know
// from the issue. This is the whole complaint.
func TestANewCardArrivesOnTheBoardCarryingItsContext(t *testing.T) {
	board := newFakeBoard(t)
	c := newCard("do the thing", card.Backlog, nil)
	c.SourceURL = strPtr("https://github.com/o/r/issues/7")
	c.RepoURL = strPtr("https://github.com/o/r")
	c.RepoBaseRef = strPtr("main")
	repo := &memRepo{cards: []*card.Card{c}}

	r := newTestReconciler(t, board, repo)
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(repo.setTaskIDs) != 1 {
		t.Fatalf("no task was created")
	}

	got := descriptionText(board.descriptionOf(repo.setTaskIDs[0].taskID))
	for _, want := range []string{
		string(card.Backlog),                // where it is
		string(card.PhaseImplementation),    // what it is doing
		"https://github.com/o/r/issues/7",   // where it came from
		"https://github.com/o/r",            // where it works
		"main",
		c.ID.String(),                       // how to ask the API about it
	} {
		if !strings.Contains(got, want) {
			t.Errorf("description is missing %q\ngot: %s", want, got)
		}
	}
}

// The reason a card is where it is was written to card_evidence and read by
// nothing. This is the one place a human is actually looking.
func TestTheLatestEvidenceReachesTheCard(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(880)
	board.seedTask(bucketReview, taskID, "in review")
	c := newCard("in review", card.Review, int64Ptr(taskID))
	repo := &memRepo{
		cards: []*card.Card{c},
		evidence: map[uuid.UUID][]store.CardEvidence{c.ID: {
			{ActorID: "meeseeks-1", Summary: "acceptance tests written, attempt 1"},
			{ActorID: "meeseeks-2", Summary: "implementation green, 12 tests pass"},
		}},
	}

	r := newTestReconciler(t, board, repo)
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	got := descriptionText(board.descriptionOf(taskID))
	if !strings.Contains(got, "implementation green, 12 tests pass") {
		t.Errorf("the newest evidence did not reach the card\ngot: %s", got)
	}
	if strings.Contains(got, "attempt 1") {
		t.Errorf("stale evidence is on the card; only the latest belongs there\ngot: %s", got)
	}
}

// Counters reading zero on every card train a reader to skip the list.
func TestCountersAppearOnlyOnceTheyHaveMoved(t *testing.T) {
	board := newFakeBoard(t)
	c := newCard("fresh", card.Backlog, nil)
	repo := &memRepo{cards: []*card.Card{c}}
	r := newTestReconciler(t, board, repo)

	if got := r.describe(context.Background(), c); strings.Contains(got, "attempts") {
		t.Errorf("a card that has not been attempted advertises an attempt count: %s", got)
	}

	c.ImplementationAttempt = 2
	if got := descriptionText(r.describe(context.Background(), c)); !strings.Contains(got, "Implementation attempts: 2") {
		t.Errorf("attempts are not shown once they matter: %s", got)
	}
}

// Vikunja sanitises what it stores, so a reconciler comparing raw HTML would
// believe every card was stale and rewrite the whole board every tick.
func TestASanitisedDescriptionIsNotMistakenForAChange(t *testing.T) {
	sent := `<p><strong>review</strong></p><ul><li>Source: https://x/y?a=1&amp;b=2</li></ul>`
	readBack := `<p><strong>review</strong></p>
<ul><li>Source: https://x/y?a=1&b=2</li></ul>`

	if !sameDescription(sent, readBack) {
		t.Errorf("a round-trip through Vikunja reads as a change:\n%q\n%q",
			descriptionText(sent), descriptionText(readBack))
	}
	if sameDescription(sent, strings.Replace(sent, "review", "done", 1)) {
		t.Error("a real change reads as no change")
	}
}

func TestAnUnchangedCardIsNotRewritten(t *testing.T) {
	board := newFakeBoard(t)
	taskID := int64(881)
	board.seedTask(bucketBacklog, taskID, "steady")
	c := newCard("steady", card.Backlog, int64Ptr(taskID))
	repo := &memRepo{cards: []*card.Card{c}}

	r := newTestReconciler(t, board, repo)
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	before := len(board.writeRequests())

	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if after := len(board.writeRequests()); after != before {
		t.Errorf("a second pass rewrote an unchanged card (%d writes, then %d); "+
			"the board would churn every tick", before, after)
	}
}

// Listing every artifact individually is what broke the board. A card that
// spent five hours in a retry loop collected 269 of them, its description grew
// a line for each, and the board listing went past the client's read cap -- so
// the reconciler failed on every pass for days, reporting a truncated body as a
// decode error.
func TestArtifactsAreSummarisedRatherThanEnumerated(t *testing.T) {
	board := newFakeBoard(t)
	c := newCard("looped", card.Review, int64Ptr(950))

	var arts []*store.Artifact
	for i := 0; i < 269; i++ {
		arts = append(arts, &store.Artifact{
			ID: uuid.New(), Type: store.ArtifactTestMapping, ContentType: "text/plain", SizeBytes: 336,
		})
	}
	arts = append(arts, &store.Artifact{ID: uuid.New(), Type: store.ArtifactDiff, ContentType: "text/x-diff"})
	repo := &memRepo{cards: []*card.Card{c}, artifacts: arts}

	got := newTestReconciler(t, board, repo).describe(context.Background(), c)

	// One line per TYPE, not per artifact.
	if n := strings.Count(got, "<li><code>"); n > 8 {
		t.Errorf("the description has %d artifact lines; a card cannot be allowed to grow its own description without bound", n)
	}
	text := descriptionText(got)
	if !strings.Contains(text, "269") {
		t.Errorf("the count is not shown, so a reader cannot tell one from many:\n%s", text)
	}
	if !strings.Contains(text, "diff") {
		t.Error("a type with a single artifact was lost in the summary")
	}

	// The whole point: this must stay small however much a card accumulates.
	if len(got) > 4000 {
		t.Errorf("description is %d bytes for 270 artifacts; it is still unbounded", len(got))
	}
}
