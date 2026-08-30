package server

import (
	"net/http"
	"sort"
	"time"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
)

// stuckAfter is how long a card may sit untouched before this endpoint calls
// it stuck.
//
// Measured from updated_at, which every transition, claim and phase advance
// moves. A card working normally touches that column several times a minute, so
// an hour of silence is not slowness -- it is something that has stopped and
// has not said so. Both bounds that escalate on their own (the ladder and the
// consecutive infrastructure count) act far sooner than this, so anything still
// here is a case neither of them covers.
const stuckAfter = time.Hour

// portfolioCard is one row of the portfolio view.
type portfolioCard struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	Phase     string   `json:"phase"`
	SourceURL string   `json:"source_url,omitempty"`
	CostUSD   float64  `json:"cost_usd"`
	MaxCost   *float64 `json:"max_cost_usd,omitempty"`
	Attempts  int      `json:"implementation_attempt"`
	InfraFail int      `json:"infrastructure_failures"`
	ClaimedBy string   `json:"claimed_by,omitempty"`
	UpdatedAt string   `json:"updated_at"`
	StuckFor  string   `json:"stuck_for,omitempty"`
}

// handlePortfolio serves GET /portfolio.
//
// §33 puts "what's stuck" and "this week's expensive cards" in the human's
// hands, and answering either meant reading every card and doing the arithmetic
// by hand. This is that arithmetic, done once, over deterministic state.
//
// Deliberately not paginated and deliberately not filtered by query parameters:
// the whole point is one call that says where the company is. A board with
// enough cards to need paging is a different problem and will say so loudly.
func (s *Server) handlePortfolio(w http.ResponseWriter, r *http.Request) {
	cd, ok := s.cardsOrError(w)
	if !ok {
		return
	}

	cards, err := cd.store.ListCards(r.Context())
	if err != nil {
		s.writeStoreError(w, cd, err)
		return
	}

	now := time.Now()
	byState := map[string]int{}
	byPhase := map[string]int{}
	var totalCost float64
	var needingHuman, blocked, inFlight, unpriced int

	rows := make([]portfolioCard, 0, len(cards))
	stuck := make([]portfolioCard, 0)

	for _, c := range cards {
		byState[string(c.State)]++
		byPhase[string(c.Phase)]++
		totalCost += c.CostUSD
		if c.CostUSD <= 0 {
			unpriced++
		}

		switch c.State {
		case card.NeedsHuman:
			needingHuman++
		case card.Blocked:
			blocked++
		case card.InProgress, card.Ready:
			inFlight++
		}

		row := portfolioCard{
			ID: c.ID.String(), Title: c.Title,
			State: string(c.State), Phase: string(c.Phase),
			CostUSD: c.CostUSD, MaxCost: c.MaxCostUSD,
			Attempts: c.ImplementationAttempt, InfraFail: c.InfrastructureFailures,
			UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339),
		}
		if c.SourceURL != nil {
			row.SourceURL = *c.SourceURL
		}
		if c.ClaimedBy != nil {
			row.ClaimedBy = *c.ClaimedBy
		}

		// Terminal states are not stuck. A card in Done has arrived, and one
		// in NeedsHuman is waiting on a person by design -- calling either
		// stuck would bury the cards that genuinely stopped on their own.
		if c.State != card.Done && c.State != card.NeedsHuman && c.State != card.Blocked {
			if idle := now.Sub(c.UpdatedAt); idle > stuckAfter {
				row.StuckFor = idle.Truncate(time.Minute).String()
				stuck = append(stuck, row)
			}
		}

		rows = append(rows, row)
	}

	// Dearest first: the row that explains the bill is the row you read.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CostUSD != rows[j].CostUSD {
			return rows[i].CostUSD > rows[j].CostUSD
		}
		return rows[i].UpdatedAt > rows[j].UpdatedAt
	})
	sort.Slice(stuck, func(i, j int) bool { return stuck[i].UpdatedAt < stuck[j].UpdatedAt })

	writeJSON(w, http.StatusOK, map[string]any{
		"cards":          len(cards),
		"in_flight":      inFlight,
		"needing_human":  needingHuman,
		"blocked":        blocked,
		"by_state":       byState,
		"by_phase":       byPhase,
		"cost_usd":       totalCost,
		// How much of that total is a floor rather than a figure. Non-zero
		// means the spend above is understated, and by an unknown amount.
		"unpriced_cards": unpriced,
		"cost_complete":  unpriced == 0,
		"stuck":          stuck,
		"portfolio":      rows,
	})
}
