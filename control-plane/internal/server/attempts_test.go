package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

type attemptStore struct {
	fakeStore
	attempts []Attempt
	card     *card.Card
	err      error
}

func (a *attemptStore) ListAttempts(context.Context, uuid.UUID) ([]Attempt, error) {
	return a.attempts, a.err
}

func (a *attemptStore) GetCard(context.Context, uuid.UUID) (*card.Card, error) {
	return a.card, a.err
}

func f64(v float64) *float64 { return &v }

func ledger() []Attempt {
	t0 := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	return []Attempt{
		{ID: 1, RunID: "r1", Phase: "tests", ModelAlias: "cheap", Provider: "deepseek",
			Harness: "opencode", Model: "deepseek-v4", Status: "success", CountedAsAttempt: true,
			Summary: "acceptance tests written", InputTokens: 100, OutputTokens: 40,
			CostUSD: f64(0.10), StartedAt: t0, CreatedAt: t0},
		{ID: 2, RunID: "r2", Phase: "implementation", ModelAlias: "cheap", Provider: "deepseek",
			Harness: "opencode", Model: "deepseek-v4", Status: "infrastructure_failure",
			CountedAsAttempt: false, Summary: "job evicted", InputTokens: 10,
			CostUSD: f64(0.01), StartedAt: t0, CreatedAt: t0},
		{ID: 3, RunID: "r3", Phase: "implementation", ModelAlias: "strong", Provider: "anthropic",
			Harness: "claudecode", Model: "claude-opus-5", Status: "success", CountedAsAttempt: true,
			Summary: "implementation green", InputTokens: 900, OutputTokens: 300,
			CostUSD: f64(2.00), StartedAt: t0, CreatedAt: t0},
	}
}

// §12 records every run and §21 requires that record be readable. The ledger
// was write-only: the rows were there and nothing outside the database could
// see them.
func TestAttemptsAreReadable(t *testing.T) {
	s := newCardsTestServer(t, &attemptStore{attempts: ledger()})

	rec := getJSON(t, s, "/cards/"+uuid.New().String()+"/attempts")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	var got struct {
		Attempts []struct {
			Phase            string   `json:"phase"`
			Harness          string   `json:"harness"`
			Status           string   `json:"status"`
			CountedAsAttempt bool     `json:"counted_as_attempt"`
			CostUSD          *float64 `json:"cost_usd"`
		} `json:"attempts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Attempts) != 3 {
		t.Fatalf("got %d attempts, want 3", len(got.Attempts))
	}
	// §12.1: an infrastructure failure is not the model failing, and a human
	// reading a stalled card needs to see which runs actually counted.
	if got.Attempts[1].CountedAsAttempt {
		t.Error("an infrastructure failure is reported as a counted attempt")
	}
	if got.Attempts[2].Harness != "claudecode" {
		t.Errorf("harness = %q; a cost report needs to know what actually ran", got.Attempts[2].Harness)
	}
}

func TestACardWithNoAttemptsRendersAnEmptyList(t *testing.T) {
	s := newCardsTestServer(t, &attemptStore{})
	rec := getJSON(t, s, "/cards/"+uuid.New().String()+"/attempts")

	if body := rec.Body.String(); !strings.Contains(body, `"attempts":[]`) {
		t.Errorf("body = %s, want an empty list rather than null", body)
	}
}

// §22 wants spend attributable per card, and the follow-up question is always
// which phase and which rung of the ladder it went to.
func TestCostAttributesSpend(t *testing.T) {
	c := &card.Card{ID: uuid.New(), CostUSD: 2.11, MaxCostUSD: f64(5)}
	s := newCardsTestServer(t, &attemptStore{attempts: ledger(), card: c})

	rec := getJSON(t, s, "/cards/"+c.ID.String()+"/cost")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	var got struct {
		CostUSD         float64         `json:"cost_usd"`
		MaxCostUSD      *float64        `json:"max_cost_usd"`
		LedgerCost      float64         `json:"attempts_cost_usd"`
		Attempts        int             `json:"attempts"`
		CountedAttempts int             `json:"counted_attempts"`
		ByPhase         []costBreakdown `json:"by_phase"`
		ByModelAlias    []costBreakdown `json:"by_model_alias"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The enforced total comes from the card row, because that is the number
	// the budget is actually checked against.
	if got.CostUSD != 2.11 {
		t.Errorf("cost_usd = %v, want the card's enforced total 2.11", got.CostUSD)
	}
	if got.MaxCostUSD == nil || *got.MaxCostUSD != 5 {
		t.Errorf("max_cost_usd = %v, want the budget", got.MaxCostUSD)
	}
	if got.CountedAttempts != 2 || got.Attempts != 3 {
		t.Errorf("attempts = %d counted = %d, want 3 and 2", got.Attempts, got.CountedAttempts)
	}

	// Dearest first, so the row that explains the bill is the row you read.
	if len(got.ByPhase) == 0 || got.ByPhase[0].Key != "implementation" {
		t.Fatalf("by_phase = %+v, want implementation first", got.ByPhase)
	}
	if got.ByPhase[0].CostUSD != 2.01 {
		t.Errorf("implementation cost = %v, want 2.01", got.ByPhase[0].CostUSD)
	}
	if len(got.ByModelAlias) == 0 || got.ByModelAlias[0].Key != "strong" {
		t.Fatalf("by_model_alias = %+v, want the expensive rung first", got.ByModelAlias)
	}
}

// A phase and a model alias can share a name. Folding them into one map keyed
// by string would silently lose a breakdown.
func TestAPhaseAndAnAliasSharingANameAreCountedSeparately(t *testing.T) {
	c := &card.Card{ID: uuid.New()}
	s := newCardsTestServer(t, &attemptStore{card: c, attempts: []Attempt{
		{ID: 1, Phase: "review", ModelAlias: "review", CostUSD: f64(1), CountedAsAttempt: true},
	}})

	rec := getJSON(t, s, "/cards/"+c.ID.String()+"/cost")
	var got struct {
		ByPhase      []costBreakdown `json:"by_phase"`
		ByModelAlias []costBreakdown `json:"by_model_alias"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)

	if len(got.ByPhase) != 1 || len(got.ByModelAlias) != 1 {
		t.Fatalf("by_phase = %+v by_model_alias = %+v, want one row each", got.ByPhase, got.ByModelAlias)
	}
	if got.ByPhase[0].CostUSD != 1 || got.ByModelAlias[0].CostUSD != 1 {
		t.Errorf("a shared name collapsed the breakdowns: %+v / %+v", got.ByPhase, got.ByModelAlias)
	}
}

// opencode drops the event carrying usage and cost when it runs in a container
// -- the same upstream bug that swallows its narrative output. A run whose
// price is unknown reported as $0 makes a blind ledger look like a free one,
// and a budget checked against it is not being enforced.
func TestAnUnpricedRunIsNotReportedAsFree(t *testing.T) {
	c := &card.Card{ID: uuid.New()}
	s := newCardsTestServer(t, &attemptStore{card: c, attempts: []Attempt{
		{ID: 1, Phase: "implementation", ModelAlias: "cheap", CostUSD: nil},
		{ID: 2, Phase: "review", ModelAlias: "strong", CostUSD: f64(0.5)},
	}})

	rec := getJSON(t, s, "/cards/"+c.ID.String()+"/cost")
	var got struct {
		Unpriced     int  `json:"unpriced_attempts"`
		CostComplete bool `json:"cost_complete"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Unpriced != 1 {
		t.Errorf("unpriced_attempts = %d, want 1", got.Unpriced)
	}
	if got.CostComplete {
		t.Error("cost_complete is true while a run's price is unknown")
	}
}

func TestAFullyPricedCardSaysSo(t *testing.T) {
	c := &card.Card{ID: uuid.New()}
	s := newCardsTestServer(t, &attemptStore{card: c, attempts: []Attempt{
		{ID: 1, Phase: "implementation", ModelAlias: "cheap", CostUSD: f64(0.25)},
	}})

	rec := getJSON(t, s, "/cards/"+c.ID.String()+"/cost")
	var got struct {
		CostComplete bool `json:"cost_complete"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.CostComplete {
		t.Error("cost_complete is false while every run reported its price")
	}
}
