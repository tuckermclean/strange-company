package specsession

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/ambiguity"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/spec"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

// Screener performs one §10.1 screening on a specification document's text.
type Screener interface {
	Screen(ctx context.Context, content string) (*ambiguity.Report, error)
}

// Board is the part of the store the reconciler uses.
type Board interface {
	ListSpecsNeedingScreening(ctx context.Context, limit int) ([]store.PendingScreening, error)
	ListSpecsAwaitingConversation(ctx context.Context, limit int) ([]store.PendingScreening, error)
	RecordScreening(ctx context.Context, cardID uuid.UUID, contentSHA256 string, score int) error
	GetCard(ctx context.Context, cardID uuid.UUID) (*card.Card, error)
}

// Result summarises one pass, for the operator's log.
type Result struct {
	Screened int
	Opened   int
	Failed   int
}

// Reconciler screens unscreened specifications and opens the §10.2
// conversation for the ones that need a human.
type Reconciler struct {
	board    Board
	screener Screener
	opener   *Opener
	limit    int
	log      *slog.Logger
}

// NewReconciler builds a Reconciler. limit bounds how many specifications one
// pass will screen, which is what stops a large backlog from issuing a model
// call per card at once.
func NewReconciler(b Board, s Screener, o *Opener, limit int, log *slog.Logger) *Reconciler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Reconciler{board: b, screener: s, opener: o, limit: limit, log: log}
}

// RunOnce performs one pass.
//
// A failure on one card is counted and logged, never returned: one unreadable
// specification must not wedge the backlog behind it. An error comes back only
// when the pass itself could not be performed.
func (r *Reconciler) RunOnce(ctx context.Context) (Result, error) {
	var res Result

	// Retries first. These cards already cost a model call; finishing them
	// is cheaper than screening anything new.
	awaiting, err := r.board.ListSpecsAwaitingConversation(ctx, r.limit)
	if err != nil {
		return res, err
	}
	for _, p := range awaiting {
		report := &ambiguity.Report{Score: ambiguity.Score(p.Score)}
		if r.converse(ctx, p, report) {
			res.Opened++
		} else {
			res.Failed++
		}
	}

	pending, err := r.board.ListSpecsNeedingScreening(ctx, r.limit)
	if err != nil {
		return res, err
	}
	for _, p := range pending {
		report, err := r.screener.Screen(ctx, p.Content)
		if err != nil {
			r.log.Error("screening failed", "card_id", p.CardID, "error", err)
			res.Failed++
			continue
		}

		// Recorded before the conversation is attempted: screening is the
		// half that costs money, and a gateway outage should cost one
		// model call in total rather than one per pass. The card comes
		// back through ListSpecsAwaitingConversation instead.
		if err := r.board.RecordScreening(ctx, p.CardID, p.ContentSHA256, int(report.Score)); err != nil {
			r.log.Error("recording screening failed", "card_id", p.CardID, "error", err)
			res.Failed++
			continue
		}
		res.Screened++

		if !report.RequiresHuman() {
			continue
		}
		if r.converse(ctx, p, report) {
			res.Opened++
		} else {
			res.Failed++
		}
	}

	return res, nil
}

// converse opens the conversation for one card, reporting only whether it
// succeeded. Every failure here is recoverable on a later pass.
func (r *Reconciler) converse(ctx context.Context, p store.PendingScreening, report *ambiguity.Report) bool {
	c, err := r.board.GetCard(ctx, p.CardID)
	if err != nil {
		r.log.Error("reading the card failed", "card_id", p.CardID, "error", err)
		return false
	}

	// Parse problems are not fatal here: the conversation exists precisely
	// to fix an incomplete specification, and Parse always returns a usable
	// document. The deterministic gate is what refuses to promote it.
	doc, _ := spec.Parse(p.CardID.String(), []byte(p.Content))

	id, err := r.opener.Open(ctx, c, doc, report)
	if err != nil {
		if errors.Is(err, ErrNoHumanNeeded) {
			return true
		}
		r.log.Error("opening the specification conversation failed", "card_id", p.CardID, "error", err)
		return false
	}
	r.log.Info("specification conversation open", "card_id", p.CardID, "session_id", id)
	return true
}
