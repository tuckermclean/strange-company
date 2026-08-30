package ui

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/spec"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

// cardView is one card's whole story.
//
// Ordered as the work happened rather than as the database is shaped: what it
// is, what it promised, what each run cost and produced, and what it left
// behind. The runs are the centrepiece, because a run that failed and a run
// that could not start are the two things that tell a reader this system knows
// what it does not know.
type cardView struct {
	ID        string
	Title     string
	State     string
	Phase     string
	SourceURL string
	RepoURL   string
	Branch    string
	Worker    string

	CostUSD  float64
	Unpriced bool
	MaxCost  string

	Attempts  int
	InfraFail int

	Criteria  []criterionView
	Runs      []runView
	Artifacts []artifactView
	History   []historyView

	// CanApprove and friends decide which buttons render. A button that
	// cannot work is worse than a missing one: it invites a click that
	// produces an error the person cannot act on.
	CanApprove  bool
	CanBlock    bool
	CanSendBack bool

	// Error carries the state machine's own words when a move was refused.
	Error string
}

type criterionView struct {
	ID, Text, Verification string
}

type runView struct {
	Phase    string
	Model    string
	Harness  string
	Status   string
	Counted  bool
	Summary  string
	CostUSD  string
	Tokens   string
	Duration string
	When     string
	Failed   bool
	Infra    bool
}

type artifactView struct {
	Type      string
	Size      int64
	Truncated bool
}

type historyView struct {
	When   string
	From   string
	To     string
	Actor  string
	Reason string
}

func (h *Handler) cardPage(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "not a card id", http.StatusBadRequest)
		return
	}

	c, err := h.store.GetCard(r.Context(), id)
	if err != nil {
		http.Error(w, "no such card", http.StatusNotFound)
		return
	}

	v := cardView{
		ID: c.ID.String(), Title: c.Title,
		State: string(c.State), Phase: string(c.Phase),
		CostUSD: c.CostUSD, Unpriced: c.CostUSD <= 0,
		Attempts: c.ImplementationAttempt, InfraFail: c.InfrastructureFailures,
	}
	if c.SourceURL != nil {
		v.SourceURL = *c.SourceURL
	}
	if c.RepoURL != nil {
		v.RepoURL = *c.RepoURL
	}
	if c.Branch != nil {
		v.Branch = *c.Branch
	}
	if c.ClaimedBy != nil {
		v.Worker = shortWorker(*c.ClaimedBy)
	}
	if c.MaxCostUSD != nil {
		v.MaxCost = money(*c.MaxCostUSD)
	}

	// §19 and the state machine decide these, not the template. Asking
	// CanTransition is what keeps the buttons honest as the rules change.
	v.CanBlock = card.CanTransition(c.State, card.Blocked, card.ActorHuman) == nil
	v.CanSendBack = card.CanTransition(c.State, card.Ready, card.ActorHuman) == nil

	if cardSpec, err := h.store.GetSpec(r.Context(), id); err == nil && cardSpec != nil {
		if strings.TrimSpace(cardSpec.Content) != "" {
			doc, _ := spec.Parse(id.String(), []byte(cardSpec.Content))
			if doc != nil {
				for _, cr := range doc.Criteria {
					v.Criteria = append(v.Criteria, criterionView{ID: cr.ID, Text: cr.Text, Verification: cr.Verification})
				}
			}
			v.CanApprove = !cardSpec.Approved && c.State == card.Backlog
		}
	}

	if attempts, err := h.store.ListAttempts(r.Context(), id); err == nil {
		for _, a := range attempts {
			v.Runs = append(v.Runs, runFrom(a))
		}
	}
	if artifacts, err := h.store.ListArtifacts(r.Context(), id); err == nil {
		for _, a := range artifacts {
			v.Artifacts = append(v.Artifacts, artifactView{Type: a.Type, Size: a.SizeBytes, Truncated: a.Truncated})
		}
	}
	if history, err := h.store.ListHistory(r.Context(), id, 40); err == nil {
		for _, e := range history {
			v.History = append(v.History, historyView{
				When: e.At.UTC().Format("15:04:05Z"), From: e.From, To: e.To,
				Actor: e.ActorType, Reason: e.Reason,
			})
		}
	}

	v.Error = r.URL.Query().Get("error")
	h.render(w, "card.html", v)
}

func runFrom(a store.StoredAttempt) runView {
	r := runView{
		Phase: a.Phase, Model: a.Model, Harness: a.Harness,
		Status: string(a.Status), Counted: a.CountedAsAttempt,
		Summary:  a.Summary,
		Duration: (time.Duration(a.DurationMS) * time.Millisecond).Truncate(time.Second).String(),
		When:     a.CreatedAt.UTC().Format("15:04:05Z"),
	}

	// The two states worth showing loudly. A run that failed on the merits
	// spent a rung of the ladder; one that could not start spent nothing and
	// says something is wrong outside the model. Collapsing them into "error"
	// is what makes an autonomous system look unreliable when it is being
	// careful.
	switch a.Status {
	case "failed":
		r.Failed = true
	case "infra_error", "timeout":
		r.Infra = true
	}

	if a.CostUSD != nil {
		r.CostUSD = money(*a.CostUSD)
	} else {
		r.CostUSD = "unpriced"
	}
	if a.InputTokens > 0 || a.OutputTokens > 0 {
		r.Tokens = formatTokens(a.InputTokens, a.OutputTokens)
	}
	return r
}
