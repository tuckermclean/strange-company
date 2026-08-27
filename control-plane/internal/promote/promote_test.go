package promote_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/promote"
	"github.com/tuckermclean/strange-company/control-plane/internal/specgate"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

const goodSpec = `# Problem

Health is unobservable.

# Scope

One endpoint.

# Out of scope

Metrics.

# Acceptance criteria

- AC1: returns 200 when healthy (verified by: curl)

# Risks

None.
`

type board struct {
	queue    []uuid.UUID
	cards    map[uuid.UUID]*card.Card
	specs    map[uuid.UUID]*store.CardSpec
	actions  map[uuid.UUID]bool
	promoted []uuid.UUID
	promErr  error
}

func (b *board) ListApprovedAwaitingPromotion(context.Context, int) ([]uuid.UUID, error) {
	return b.queue, nil
}
func (b *board) GetCard(_ context.Context, id uuid.UUID) (*card.Card, error) {
	c, ok := b.cards[id]
	if !ok {
		return nil, errors.New("no such card")
	}
	return c, nil
}
func (b *board) GetSpec(_ context.Context, id uuid.UUID) (*store.CardSpec, error) {
	s, ok := b.specs[id]
	if !ok {
		return nil, errors.New("no spec")
	}
	return s, nil
}
func (b *board) ListDependencies(context.Context, uuid.UUID) ([]*card.Card, error) { return nil, nil }
func (b *board) HasPermittedActions(_ context.Context, id uuid.UUID) (bool, error) {
	return b.actions[id], nil
}
func (b *board) PromoteToReady(_ context.Context, id uuid.UUID, _ specgate.Result, _ card.ActorType, _ string) error {
	if b.promErr != nil {
		return b.promErr
	}
	b.promoted = append(b.promoted, id)
	return nil
}

func boardWith(t *testing.T, content string, approved, actions bool) (*board, uuid.UUID) {
	t.Helper()
	id := uuid.New()
	repo := "https://github.com/example/repo"
	return &board{
		queue:   []uuid.UUID{id},
		cards:   map[uuid.UUID]*card.Card{id: {ID: id, Title: "t", State: card.Backlog, RepoURL: &repo}},
		specs:   map[uuid.UUID]*store.CardSpec{id: {CardID: id, Content: content, Approved: approved}},
		actions: map[uuid.UUID]bool{id: actions},
	}, id
}

// The whole point: an approved specification that passes the deterministic
// gate becomes Ready without a second human action (spec §10.2).
func TestAnApprovedCardThatPassesTheGateIsPromoted(t *testing.T) {
	b, id := boardWith(t, goodSpec, true, true)

	res, err := promote.New(b, 10, nil).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(b.promoted) != 1 || b.promoted[0] != id {
		t.Fatalf("promoted %v", b.promoted)
	}
	if res.Promoted != 1 {
		t.Fatalf("result = %+v", res)
	}
}

// A card with no allowlist cannot be promoted, and this is the check that
// stops a global default from turning §10's rule into a rubber stamp.
func TestACardWithNoPermittedActionsIsNotPromoted(t *testing.T) {
	b, _ := boardWith(t, goodSpec, true, false)

	res, err := promote.New(b, 10, nil).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(b.promoted) != 0 {
		t.Fatal("promoted a card with no permitted-actions block")
	}
	if res.Blocked != 1 {
		t.Fatalf("result = %+v", res)
	}
}

// A human can approve an incomplete specification. The gate is deterministic
// and does not care, and the card must stay put rather than being promoted on
// the strength of the approval alone.
func TestAnApprovedButIncompleteSpecIsNotPromoted(t *testing.T) {
	b, _ := boardWith(t, "# Problem\n\nnot much else\n", true, true)

	res, err := promote.New(b, 10, nil).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(b.promoted) != 0 {
		t.Fatal("an approval overrode the deterministic gate")
	}
	if res.Blocked != 1 {
		t.Fatalf("result = %+v", res)
	}
}

// One card that cannot be read must not stop the rest of the queue.
func TestOneUnreadableCardDoesNotStopTheQueue(t *testing.T) {
	b, good := boardWith(t, goodSpec, true, true)
	b.queue = append([]uuid.UUID{uuid.New()}, b.queue...)

	res, err := promote.New(b, 10, nil).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(b.promoted) != 1 || b.promoted[0] != good {
		t.Fatalf("promoted %v", b.promoted)
	}
	if res.Failed != 1 {
		t.Fatalf("result = %+v", res)
	}
}
