package specsession

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/ambiguity"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/hermes"
	"github.com/tuckermclean/strange-company/control-plane/internal/spec"
)

// ErrNoHumanNeeded is returned for a card screening did not send here.
//
// Spec §10.1: only scores 2 and 3 require a human. Opening a conversation for
// a mechanical card spends someone's attention on a question nobody asked, so
// this is the Opener's refusal rather than the caller's to remember.
var ErrNoHumanNeeded = errors.New("specsession: this card does not require a human conversation")

// Gateway is the part of the Hermes API this package uses.
type Gateway interface {
	CreateSession(context.Context, hermes.SpecSession) (*hermes.Session, error)
	DeleteSession(context.Context, string) error

	// ListSessions is how a conversation that exists but was never recorded
	// is found again. The gateway refuses a duplicate title, so without it
	// the only evidence such a session exists is an error saying one does.
	ListSessions(context.Context) ([]*hermes.Session, error)
}

// Store is the part of the card store this package uses.
type Store interface {
	GetSpecSession(context.Context, uuid.UUID) (string, error)
	RecordSpecSession(context.Context, uuid.UUID, string) error
}

// Opener starts the §10.2 specification conversation for a card, exactly
// once, and points the card at it.
type Opener struct {
	gateway Gateway
	store   Store
	log     *slog.Logger

	// model is the literal model string for the specification alias,
	// resolved from policy by the caller. This package never picks a model.
	model string
}

// NewOpener builds an Opener. model is the specification alias's model
// string, resolved from policy.
func NewOpener(g Gateway, s Store, model string) *Opener {
	return &Opener{gateway: g, store: s, model: model, log: slog.Default()}
}

// WithLogger replaces the logger. Adoption of an existing conversation is
// worth saying out loud: it means an earlier pass left one behind.
func (o *Opener) WithLogger(l *slog.Logger) *Opener {
	if l != nil {
		o.log = l
	}
	return o
}

// Open returns the id of the card's specification conversation, starting one
// if there is not already one.
//
// It is safe to call on every pass over a card, and safe to call concurrently:
// the store's record is the single arbiter of which session a card points at,
// and a caller that loses that race deletes the session it created rather than
// leaving it orphaned in someone's dashboard.
func (o *Opener) Open(ctx context.Context, c *card.Card, doc *spec.Document, report *ambiguity.Report) (string, error) {
	if c == nil {
		return "", ErrNoTitle
	}
	if report == nil {
		return "", ErrNoReport
	}
	if !report.RequiresHuman() {
		return "", fmt.Errorf("%w (ambiguity score %d)", ErrNoHumanNeeded, int(report.Score))
	}

	if existing, err := o.store.GetSpecSession(ctx, c.ID); err != nil {
		return "", err
	} else if existing != "" {
		return existing, nil
	}

	prompt, err := BuildSystemPrompt(c, doc, report)
	if err != nil {
		return "", err
	}

	title := Title(c)
	session, err := o.gateway.CreateSession(ctx, hermes.SpecSession{
		Title:        title,
		Model:        o.model,
		SystemPrompt: prompt,
	})
	if err != nil {
		// The conversation may already exist. The gateway refuses a
		// duplicate title, and the title is derived from the card -- so a
		// session created on an earlier pass whose id was never recorded
		// makes every later pass fail identically, forever. That is not
		// hypothetical: a rollout between creating a session and recording
		// it leaves exactly this state, and it retried once a minute all
		// night.
		//
		// So look for it before giving up. Adopting what exists is the same
		// move the coding runner makes with a Job it finds already running.
		if adopted, aerr := o.adopt(ctx, c, title); aerr == nil && adopted != "" {
			return adopted, nil
		}

		// Otherwise records nothing: a card pointing at a session that was
		// never created cannot be recovered by a later pass, while a card
		// pointing at nothing simply gets retried.
		return "", fmt.Errorf("specsession: opening the conversation: %w", err)
	}

	if err := o.store.RecordSpecSession(ctx, c.ID, session.ID); err != nil {
		winner, readErr := o.store.GetSpecSession(ctx, c.ID)
		if readErr != nil || winner == "" || winner == session.ID {
			// The record genuinely failed rather than being lost to a
			// race. Still clean up, so a session nobody can find does
			// not accumulate.
			o.discard(ctx, session.ID)
			return "", fmt.Errorf("specsession: recording the conversation: %w", err)
		}
		o.discard(ctx, session.ID)
		return winner, nil
	}

	return session.ID, nil
}

// discard removes a session this Opener created but could not point a card at.
// A failure to delete is not reported: the caller's outcome does not depend on
// it, and the worst case is one stale conversation rather than a lost card.
func (o *Opener) discard(ctx context.Context, id string) {
	_ = o.gateway.DeleteSession(ctx, id)
}

// adopt finds a conversation that already carries this card's title and records
// it against the card.
//
// Matching on the title rather than parsing the gateway's error: the title is
// something this package chose and can recompute, while an error message is
// prose that changes between versions.
func (o *Opener) adopt(ctx context.Context, c *card.Card, title string) (string, error) {
	sessions, err := o.gateway.ListSessions(ctx)
	if err != nil {
		return "", err
	}
	for _, s := range sessions {
		if s == nil || s.Title != title {
			continue
		}
		if err := o.store.RecordSpecSession(ctx, c.ID, s.ID); err != nil {
			return "", err
		}
		o.log.Info("adopted a specification conversation that already existed",
			"card_id", c.ID, "session_id", s.ID)
		return s.ID, nil
	}
	return "", nil
}
