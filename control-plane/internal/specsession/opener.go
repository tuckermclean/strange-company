package specsession

import (
	"context"
	"errors"
	"fmt"

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

	// model is the literal model string for the specification alias,
	// resolved from policy by the caller. This package never picks a model.
	model string
}

// NewOpener builds an Opener. model is the specification alias's model
// string, resolved from policy.
func NewOpener(g Gateway, s Store, model string) *Opener {
	return &Opener{gateway: g, store: s, model: model}
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

	session, err := o.gateway.CreateSession(ctx, hermes.SpecSession{
		Title:        Title(c),
		Model:        o.model,
		SystemPrompt: prompt,
	})
	if err != nil {
		// Deliberately records nothing: a card pointing at a session that
		// was never created cannot be recovered by a later pass, while a
		// card pointing at nothing simply gets retried.
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
