// Package ui serves the operator's view of the engine.
//
// Server-rendered HTML, no framework, no build step, no JavaScript beyond a
// meta refresh. That is not minimalism for its own sake: this UI's whole job is
// to be trusted by someone sceptical, and a page they can read the source of --
// where every number links to the GitHub run or the artifact it came from --
// argues for itself in a way a single-page app cannot.
//
// It replaces the three gestures the Vikunja board currently owns: approving a
// specification, stopping a card, and sending one back. Nothing else about
// Vikunja is retired here; the board keeps working until these have earned it.
package ui

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

//go:embed templates/*.html
var templatesFS embed.FS

// Store is everything the UI reads and does.
//
// Reads only what already has an endpoint, and writes only through transitions
// the state machine already validates -- so this package can show and steer the
// engine without knowing anything about how it works.
type Store interface {
	ListCards(ctx context.Context) ([]*card.Card, error)
	GetCard(ctx context.Context, id uuid.UUID) (*card.Card, error)
	GetSpec(ctx context.Context, cardID uuid.UUID) (*store.CardSpec, error)
	ListArtifacts(ctx context.Context, cardID uuid.UUID) ([]*store.Artifact, error)
	ListAttempts(ctx context.Context, cardID uuid.UUID) ([]store.StoredAttempt, error)
	ListHistory(ctx context.Context, cardID uuid.UUID, limit int) ([]store.HistoryEntry, error)
	ListEvidence(ctx context.Context, cardID uuid.UUID) ([]store.CardEvidence, error)

	// SpecSessionID returns the Hermes conversation opened for this card's
	// specification (§10.2), or "" when none has been.
	GetSpecSession(ctx context.Context, cardID uuid.UUID) (string, error)

	Transition(ctx context.Context, cardID uuid.UUID, to card.State, actor card.ActorType, actorID, reason string) error
	ApproveSpec(ctx context.Context, cardID uuid.UUID, approvedBy string) error
}

// Handler serves the UI.
type Handler struct {
	store Store
	log   *slog.Logger
	tmpl  *template.Template

	// dashboard is where a human continues a specification conversation.
	//
	// §10.2's conversation happens in Hermes, which is a real chat product
	// with streaming, history and its own sign-in. Rebuilding that here
	// would be the one part of this system that duplicates something rather
	// than doing something nothing else does -- so the console links to it.
	// What was missing was never the chat. It was the link: the session id
	// has been stored since M4 and surfaced nowhere, so finding a
	// conversation meant hunting a dashboard list by title.
	dashboard string
}

// WithDashboard sets the public Hermes dashboard URL used to link a card to
// its specification conversation. Without one, the console reports that a
// conversation is open but cannot say where.
func (h *Handler) WithDashboard(url string) *Handler {
	h.dashboard = strings.TrimRight(url, "/")
	return h
}

// New builds the UI handler.
func New(s Store, log *slog.Logger) (*Handler, error) {
	if log == nil {
		log = slog.Default()
	}
	t, err := template.New("").Funcs(funcs()).ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("ui: parsing templates: %w", err)
	}
	return &Handler{store: s, log: log, tmpl: t}, nil
}

// Routes registers the UI on mux.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ui", h.engine)
	mux.HandleFunc("GET /ui/", h.engine)
	mux.HandleFunc("GET /ui/cards/{id}", h.cardPage)
	mux.HandleFunc("GET /ui/cards/{id}/artifacts/{artifact}", h.artifactPage)
	mux.HandleFunc("POST /ui/cards/{id}/approve", h.approve)
	mux.HandleFunc("POST /ui/cards/{id}/block", h.block)
	mux.HandleFunc("POST /ui/cards/{id}/send-back", h.sendBack)
}

// engineView is the home screen: what is running, what needs a person, and
// what has shipped.
//
// Three bands rather than columns, because this engine's cards are not moved by
// people between states -- they move themselves, and a board metaphor makes the
// Meeseeks lifecycle's Ready/InProgress churn read as thrashing when it is the
// system working exactly as designed.
type engineView struct {
	Now      string
	Running  []cardRow
	NeedsYou []cardRow
	Recent   []cardRow

	Cards     int
	CostUSD   float64
	Unpriced  int
	Escalated int
}

type cardRow struct {
	ID       string
	Title    string
	State    string
	Phase    string
	Worker   string
	Elapsed  string
	CostUSD  float64
	Unpriced bool
	Reason   string
}

func (h *Handler) engine(w http.ResponseWriter, r *http.Request) {
	cards, err := h.store.ListCards(r.Context())
	if err != nil {
		h.fail(w, "could not read the board", err)
		return
	}

	v := engineView{Now: time.Now().UTC().Format("15:04:05Z"), Cards: len(cards)}
	now := time.Now()

	for _, c := range cards {
		row := cardRow{
			ID: c.ID.String(), Title: c.Title,
			State: string(c.State), Phase: string(c.Phase),
			CostUSD: c.CostUSD, Unpriced: c.CostUSD <= 0,
			Elapsed: since(now, c.UpdatedAt),
		}
		if c.ClaimedBy != nil {
			row.Worker = shortWorker(*c.ClaimedBy)
		}
		v.CostUSD += c.CostUSD
		if c.CostUSD <= 0 {
			v.Unpriced++
		}

		switch {
		case c.State == card.NeedsHuman, c.State == card.Blocked:
			v.Escalated++
			v.NeedsYou = append(v.NeedsYou, row)
		case c.State == card.Review:
			// Waiting on a human to merge (§19), which is a decision, not a
			// stall -- it belongs with the things asking for attention.
			v.NeedsYou = append(v.NeedsYou, row)
		case c.ClaimedBy != nil:
			v.Running = append(v.Running, row)
		case c.State == card.Done:
			v.Recent = append(v.Recent, row)
		default:
			v.Running = append(v.Running, row)
		}
	}

	sort.Slice(v.Recent, func(i, j int) bool { return v.Recent[i].Title < v.Recent[j].Title })
	h.render(w, "engine.html", v)
}

func (h *Handler) fail(w http.ResponseWriter, what string, err error) {
	h.log.Error("ui: "+what, "error", err)
	http.Error(w, what, http.StatusInternalServerError)
}

func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, name, data); err != nil {
		h.log.Error("ui: rendering", "template", name, "error", err)
	}
}

func since(now, then time.Time) string {
	d := now.Sub(then)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// shortWorker trims a Kubernetes-flavoured worker id to something a human can
// hold in their head. "meeseeks-strange-company-control-plane-6f5-16" is not a
// name; "6f5-16" is.
func shortWorker(id string) string {
	parts := strings.Split(id, "-")
	if len(parts) < 2 {
		return id
	}
	return strings.Join(parts[len(parts)-2:], "-")
}

func funcs() template.FuncMap {
	return template.FuncMap{
		"money": func(v float64) string { return fmt.Sprintf("$%.2f", v) },
		"short": func(s string) string {
			if i := strings.Index(s, "-"); i > 0 {
				return s[:i]
			}
			return s
		},
	}
}

func money(v float64) string { return fmt.Sprintf("$%.2f", v) }

func formatTokens(in, out int) string {
	return fmt.Sprintf("%s in / %s out", compact(in), compact(out))
}

func compact(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func sprint(v any) string { return fmt.Sprint(v) }
