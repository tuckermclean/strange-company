package server

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// Attempt is one recorded run against a card, as this package needs it.
//
// Structurally a subset of store.StoredAttempt; main adapts between them, for
// the same reason CardStore is declared here rather than there.
//
// §21: this is what answers "what happened to card X?", so every field is
// something a run produced or a fact about how it was run. Summary is the
// worker's account of the outcome (§12.2 evidence, not monologue); model
// reasoning never appears here.
type Attempt struct {
	ID     int64
	RunID  string
	Phase  string
	Number *int

	// ModelAlias is the rung of the escalation ladder; Provider, Harness
	// and Model are what that rung actually resolved to, which is the
	// question a cost report is really asking.
	ModelAlias string
	Provider   string
	Harness    string
	Model      string

	Status string
	// CountedAsAttempt is §12.1's classification: an infrastructure failure
	// is not the model failing, and must not burn a rung of the ladder. A
	// human reading a stalled card needs to see which runs counted.
	CountedAsAttempt bool
	Summary          string

	InputTokens  int
	OutputTokens int
	CachedTokens int
	CostUSD      *float64
	DurationMS   int64
	StartedAt    time.Time
	CreatedAt    time.Time
}

// AttemptStore is the read side of the attempt ledger (spec §12, §22) and of
// the audit log (§21).
type AttemptStore interface {
	ListAttempts(ctx context.Context, cardID uuid.UUID) ([]Attempt, error)
	ListHistory(ctx context.Context, cardID uuid.UUID, limit int) ([]HistoryEntry, error)
}

// HistoryEntry is one state change, as this package needs it.
type HistoryEntry struct {
	At        time.Time
	From      string
	To        string
	ActorType string
	ActorID   string
	Reason    string
}

type attemptView struct {
	ID     int64  `json:"id"`
	RunID  string `json:"run_id"`
	Phase  string `json:"phase"`
	Number *int   `json:"attempt_number,omitempty"`

	ModelAlias string `json:"model_alias"`
	Provider   string `json:"provider"`
	Harness    string `json:"harness"`
	Model      string `json:"model"`

	Status           string `json:"status"`
	CountedAsAttempt bool   `json:"counted_as_attempt"`
	Summary          string `json:"summary,omitempty"`

	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	CachedTokens int      `json:"cached_tokens"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`
	DurationMS   int64    `json:"duration_ms"`
	StartedAt    string   `json:"started_at"`
	CreatedAt    string   `json:"created_at"`
}

// handleListAttempts serves GET /cards/{id}/attempts.
//
// §12 records every run against a card and §21 requires that record be
// readable. Until this endpoint existed the ledger was write-only: the data
// was there and nothing outside the database could see it.
func (s *Server) handleListAttempts(w http.ResponseWriter, r *http.Request) {
	cd, ok := s.cardsOrError(w)
	if !ok {
		return
	}
	id, ok := parseCardID(w, r)
	if !ok {
		return
	}

	attempts, err := cd.store.ListAttempts(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, cd, err)
		return
	}

	// Non-nil so a card with no attempts renders as [] rather than null.
	views := make([]attemptView, 0, len(attempts))
	for _, a := range attempts {
		views = append(views, attemptView{
			ID: a.ID, RunID: a.RunID, Phase: a.Phase, Number: a.Number,
			ModelAlias: a.ModelAlias, Provider: a.Provider, Harness: a.Harness, Model: a.Model,
			Status: a.Status, CountedAsAttempt: a.CountedAsAttempt, Summary: a.Summary,
			InputTokens: a.InputTokens, OutputTokens: a.OutputTokens, CachedTokens: a.CachedTokens,
			CostUSD: a.CostUSD, DurationMS: a.DurationMS,
			StartedAt: a.StartedAt.UTC().Format(time.RFC3339),
			CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"attempts": views})
}

type costBreakdown struct {
	Key          string  `json:"key"`
	Attempts     int     `json:"attempts"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CachedTokens int     `json:"cached_tokens"`
}

// handleCardCost serves GET /cards/{id}/cost.
//
// §22 wants spend attributable per card. The card row carries the running
// total the budget is enforced against; the breakdowns come from the attempt
// ledger and answer the follow-up question -- which phase, and which rung of
// the ladder, the money went to.
func (s *Server) handleCardCost(w http.ResponseWriter, r *http.Request) {
	cd, ok := s.cardsOrError(w)
	if !ok {
		return
	}
	id, ok := parseCardID(w, r)
	if !ok {
		return
	}

	c, err := cd.store.GetCard(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, cd, err)
		return
	}

	attempts, err := cd.store.ListAttempts(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, cd, err)
		return
	}

	byPhase := map[string]*costBreakdown{}
	byModel := map[string]*costBreakdown{}
	var ledger float64
	var counted, unpriced int

	for _, a := range attempts {
		var cost float64
		if a.CostUSD != nil {
			cost = *a.CostUSD
		} else {
			// Counted, not silently treated as free. opencode drops the
			// event carrying usage and cost when it runs in a container
			// (the same upstream bug that swallows its narrative output),
			// so a run whose price is simply unknown would otherwise be
			// reported as $0 -- and a ledger that reads zero because it is
			// blind is worse than one that says it cannot see.
			unpriced++
		}
		ledger += cost
		if a.CountedAsAttempt {
			counted++
		}
		add := func(into map[string]*costBreakdown, key string) {
			b, ok := into[key]
			if !ok {
				b = &costBreakdown{Key: key}
				into[key] = b
			}
			b.Attempts++
			b.CostUSD += cost
			b.InputTokens += a.InputTokens
			b.OutputTokens += a.OutputTokens
			b.CachedTokens += a.CachedTokens
		}
		add(byPhase, a.Phase)
		add(byModel, a.ModelAlias)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"card_id": c.ID.String(),
		// The enforced total, from the card row. The budget is checked
		// against this, so this is the number that matters.
		"cost_usd":     c.CostUSD,
		"max_cost_usd": c.MaxCostUSD,
		// The same money seen from the ledger. It can lag the card total
		// when a run has not reported yet, and saying so is better than
		// presenting one number and hoping.
		"attempts_cost_usd": ledger,
		"attempts":          len(attempts),
		"counted_attempts":  counted,
		// How much of the ledger is missing. Non-zero means the totals
		// above are a floor, not a figure -- and a budget checked against
		// them is not being enforced.
		"unpriced_attempts": unpriced,
		"cost_complete":     unpriced == 0,
		"by_phase":          sortedBreakdowns(byPhase),
		"by_model_alias":    sortedBreakdowns(byModel),
	})
}

// sortedBreakdowns renders the map in a stable order -- dearest first, then by
// key -- so a client polling the endpoint does not see rows shuffle.
func sortedBreakdowns(m map[string]*costBreakdown) []costBreakdown {
	out := make([]costBreakdown, 0, len(m))
	for _, b := range m {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CostUSD != out[j].CostUSD {
			return out[i].CostUSD > out[j].CostUSD
		}
		return out[i].Key < out[j].Key
	})
	return out
}

type historyView struct {
	At        string `json:"at"`
	From      string `json:"from,omitempty"`
	To        string `json:"to"`
	ActorType string `json:"actor_type"`
	ActorID   string `json:"actor_id"`
	Reason    string `json:"reason,omitempty"`
}

// handleCardHistory serves GET /cards/{id}/history.
//
// §21 requires the audit log to answer "what happened to card X?". The rows
// have been written since M0 and were reachable only from a psql session --
// which is how every diagnosis of this system has been done so far.
func (s *Server) handleCardHistory(w http.ResponseWriter, r *http.Request) {
	cd, ok := s.cardsOrError(w)
	if !ok {
		return
	}
	id, ok := parseCardID(w, r)
	if !ok {
		return
	}

	limit := 0 // the store's default
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}

	entries, err := cd.store.ListHistory(r.Context(), id, limit)
	if err != nil {
		s.writeStoreError(w, cd, err)
		return
	}

	views := make([]historyView, 0, len(entries))
	for _, e := range entries {
		views = append(views, historyView{
			At: e.At.UTC().Format(time.RFC3339), From: e.From, To: e.To,
			ActorType: e.ActorType, ActorID: e.ActorID, Reason: e.Reason,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": views})
}
