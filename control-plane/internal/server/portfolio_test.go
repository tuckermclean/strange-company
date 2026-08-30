package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

type portfolioStore struct {
	fakeStore
	cards []*card.Card
}

func (p *portfolioStore) ListCards(context.Context) ([]*card.Card, error) { return p.cards, nil }

func aCard(title string, state card.State, cost float64, idle time.Duration) *card.Card {
	return &card.Card{
		ID: uuid.New(), Title: title, State: state, Phase: card.PhaseImplementation,
		CostUSD: cost, UpdatedAt: time.Now().Add(-idle),
	}
}

// §33 puts "what's stuck" and "this week's expensive cards" in a human's hands.
// Answering either meant reading every card and doing the arithmetic by hand.
func TestThePortfolioAnswersWhereTheCompanyIs(t *testing.T) {
	s := newCardsTestServer(t, &portfolioStore{cards: []*card.Card{
		aCard("cheap", card.Review, 0.10, time.Minute),
		aCard("dear", card.InProgress, 4.20, time.Minute),
		aCard("escalated", card.NeedsHuman, 1.00, 48*time.Hour),
	}})

	rec := getJSON(t, s, "/portfolio")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	var got struct {
		Cards        int             `json:"cards"`
		InFlight     int             `json:"in_flight"`
		NeedingHuman int             `json:"needing_human"`
		CostUSD      float64         `json:"cost_usd"`
		Portfolio    []portfolioCard `json:"portfolio"`
		Stuck        []portfolioCard `json:"stuck"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.Cards != 3 || got.NeedingHuman != 1 {
		t.Errorf("cards = %d needing_human = %d, want 3 and 1", got.Cards, got.NeedingHuman)
	}
	if got.CostUSD < 5.29 || got.CostUSD > 5.31 {
		t.Errorf("cost_usd = %v, want the cards summed", got.CostUSD)
	}
	// Dearest first: the row that explains the bill is the row you read.
	if len(got.Portfolio) == 0 || got.Portfolio[0].Title != "dear" {
		t.Errorf("portfolio = %+v, want the expensive card first", got.Portfolio)
	}
}

// A card in NeedsHuman is waiting on a person by design, and one in Done has
// arrived. Calling either stuck buries the cards that genuinely stopped.
func TestACardWaitingOnAHumanIsNotReportedAsStuck(t *testing.T) {
	s := newCardsTestServer(t, &portfolioStore{cards: []*card.Card{
		aCard("waiting for you", card.NeedsHuman, 0, 48*time.Hour),
		aCard("finished", card.Done, 0, 48*time.Hour),
		aCard("stopped on its own", card.InProgress, 0, 48*time.Hour),
	}})

	var got struct {
		Stuck []portfolioCard `json:"stuck"`
	}
	_ = json.Unmarshal(getJSON(t, s, "/portfolio").Body.Bytes(), &got)

	if len(got.Stuck) != 1 {
		t.Fatalf("stuck = %+v, want only the card that stopped on its own", got.Stuck)
	}
	if got.Stuck[0].Title != "stopped on its own" {
		t.Errorf("stuck card = %q", got.Stuck[0].Title)
	}
	if got.Stuck[0].StuckFor == "" {
		t.Error("a stuck card does not say for how long")
	}
}

// Non-zero unpriced cards mean the total is a floor, not a figure -- and a
// portfolio that presented it as a figure would be the same lie the per-card
// endpoint was built to stop telling.
func TestThePortfolioSaysWhenItsTotalIsAFloor(t *testing.T) {
	s := newCardsTestServer(t, &portfolioStore{cards: []*card.Card{
		aCard("priced", card.Review, 1.00, time.Minute),
		aCard("unpriced", card.Review, 0, time.Minute),
	}})

	var got struct {
		Unpriced     int  `json:"unpriced_cards"`
		CostComplete bool `json:"cost_complete"`
	}
	_ = json.Unmarshal(getJSON(t, s, "/portfolio").Body.Bytes(), &got)

	if got.Unpriced != 1 {
		t.Errorf("unpriced_cards = %d, want 1", got.Unpriced)
	}
	if got.CostComplete {
		t.Error("cost_complete is true while a card's spend is unknown")
	}
}
