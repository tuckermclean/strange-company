package specsession_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/ambiguity"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/specsession"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

type fakeBoard struct {
	pending   []store.PendingScreening
	awaiting  []store.PendingScreening
	screened  map[uuid.UUID]int
	sessions  map[uuid.UUID]string
	recordErr error
	cards     map[uuid.UUID]*card.Card
}

func newBoard(cards ...*card.Card) *fakeBoard {
	b := &fakeBoard{
		screened: map[uuid.UUID]int{},
		sessions: map[uuid.UUID]string{},
		cards:    map[uuid.UUID]*card.Card{},
	}
	for _, c := range cards {
		b.cards[c.ID] = c
		b.pending = append(b.pending, store.PendingScreening{
			CardID: c.ID, Content: "# Problem\n\ntext", ContentSHA256: "sha-" + c.ID.String(),
		})
	}
	return b
}

func (b *fakeBoard) ListSpecsNeedingScreening(context.Context, int) ([]store.PendingScreening, error) {
	return b.pending, nil
}
func (b *fakeBoard) ListSpecsAwaitingConversation(context.Context, int) ([]store.PendingScreening, error) {
	return b.awaiting, nil
}
func (b *fakeBoard) RecordScreening(_ context.Context, id uuid.UUID, _ string, score int) error {
	if b.recordErr != nil {
		return b.recordErr
	}
	b.screened[id] = score
	return nil
}
func (b *fakeBoard) GetCard(_ context.Context, id uuid.UUID) (*card.Card, error) {
	c, ok := b.cards[id]
	if !ok {
		return nil, errors.New("no such card")
	}
	return c, nil
}
func (b *fakeBoard) GetSpecSession(_ context.Context, id uuid.UUID) (string, error) {
	return b.sessions[id], nil
}
func (b *fakeBoard) RecordSpecSession(_ context.Context, id uuid.UUID, s string) error {
	b.sessions[id] = s
	return nil
}

// scriptedScreener returns a fixed score, and counts how often it was asked.
type scriptedScreener struct {
	score ambiguity.Score
	calls int
	err   error
}

func (s *scriptedScreener) Screen(context.Context, string) (*ambiguity.Report, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &ambiguity.Report{Score: s.score, Rationale: "because", Findings: []ambiguity.Finding{{Section: "Scope", Concern: "unclear"}}}, nil
}

func cardWithID() *card.Card {
	c := testCard()
	c.ID = uuid.New()
	return c
}

func reconciler(b *fakeBoard, s *scriptedScreener, g *fakeGateway) *specsession.Reconciler {
	return specsession.NewReconciler(b, s, specsession.NewOpener(g, b, "anthropic/claude-fable-5"), 10, nil)
}

// A mechanical card is screened and recorded, and no human is called in.
func TestAnUnambiguousSpecIsScreenedButOpensNoConversation(t *testing.T) {
	c := cardWithID()
	b := newBoard(c)
	g := &fakeGateway{nextID: "api_1"}
	scr := &scriptedScreener{score: ambiguity.ScoreMechanical}

	res, err := reconciler(b, scr, g).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if scr.calls != 1 {
		t.Fatalf("screened %d times", scr.calls)
	}
	if got, ok := b.screened[c.ID]; !ok || got != 0 {
		t.Fatalf("screened score = %v (recorded: %v)", got, ok)
	}
	if len(g.created) != 0 {
		t.Fatalf("opened a conversation for a mechanical card")
	}
	if res.Opened != 0 || res.Screened != 1 {
		t.Fatalf("result = %+v", res)
	}
}

func TestAnAmbiguousSpecOpensTheConversation(t *testing.T) {
	c := cardWithID()
	b := newBoard(c)
	g := &fakeGateway{nextID: "api_1"}
	scr := &scriptedScreener{score: ambiguity.ScoreMaterialAmbiguity}

	res, err := reconciler(b, scr, g).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if b.sessions[c.ID] != "api_1" {
		t.Fatalf("session = %q", b.sessions[c.ID])
	}
	if res.Opened != 1 {
		t.Fatalf("result = %+v", res)
	}
}

// The screening is the expensive half. Recording it before the conversation is
// attempted means a gateway outage costs one model call in total, not one per
// pass for as long as the outage lasts.
func TestScreeningIsRecordedEvenWhenTheGatewayIsDown(t *testing.T) {
	c := cardWithID()
	b := newBoard(c)
	g := &fakeGateway{createErr: errors.New("gateway down")}
	scr := &scriptedScreener{score: ambiguity.ScoreFundamentalAmbiguity}

	res, err := reconciler(b, scr, g).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce should not fail the whole pass: %v", err)
	}
	if _, ok := b.screened[c.ID]; !ok {
		t.Fatal("the screening result was thrown away, so the next pass pays for it again")
	}
	if res.Failed != 1 {
		t.Fatalf("result = %+v", res)
	}
}

// A card screened as ambiguous but left without a conversation -- the gateway
// was down -- must be retried without paying to screen it a second time.
func TestACardAwaitingAConversationIsRetriedWithoutRescreening(t *testing.T) {
	c := cardWithID()
	b := newBoard()
	b.cards[c.ID] = c
	b.awaiting = []store.PendingScreening{{
		CardID: c.ID, Content: "# Problem\n\ntext", ContentSHA256: "sha",
		Score: int(ambiguity.ScoreMaterialAmbiguity),
	}}
	b.screened[c.ID] = int(ambiguity.ScoreMaterialAmbiguity)
	g := &fakeGateway{nextID: "api_retry"}
	scr := &scriptedScreener{score: ambiguity.ScoreMechanical}

	res, err := reconciler(b, scr, g).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if scr.calls != 0 {
		t.Fatalf("re-screened a card that was already screened (%d calls)", scr.calls)
	}
	if b.sessions[c.ID] != "api_retry" {
		t.Fatalf("session = %q", b.sessions[c.ID])
	}
	if res.Opened != 1 {
		t.Fatalf("result = %+v", res)
	}
}

// One unreadable card must not wedge the backlog behind it.
func TestOneFailingCardDoesNotStopTheRest(t *testing.T) {
	good := cardWithID()
	b := newBoard(good)
	orphan := uuid.New()
	b.pending = append([]store.PendingScreening{{CardID: orphan, Content: "x", ContentSHA256: "sha"}}, b.pending...)
	g := &fakeGateway{nextID: "api_1"}
	scr := &scriptedScreener{score: ambiguity.ScoreMaterialAmbiguity}

	res, err := reconciler(b, scr, g).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if b.sessions[good.ID] != "api_1" {
		t.Fatal("the healthy card behind the failing one was never handled")
	}
	if res.Failed != 1 {
		t.Fatalf("result = %+v", res)
	}
}
