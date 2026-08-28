package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// §21: a card must never arrive in a new state with nothing explaining why.
// The worker attaches evidence before it transitions, so this is what makes
// that possible.
func TestEvidenceIsStoredAndReadBackInOrder(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	id := seedBacklogCard(t, s)

	for _, summary := range []string{"claimed", "planned"} {
		if err := s.AttachEvidence(ctx, id, CardEvidence{
			ActorID: "worker-1", Summary: summary,
			Detail: map[string]any{"phase": "planning"},
		}); err != nil {
			t.Fatalf("AttachEvidence(%q): %v", summary, err)
		}
	}

	got, err := s.ListEvidence(ctx, id)
	if err != nil {
		t.Fatalf("ListEvidence: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows", len(got))
	}
	if got[0].Summary != "claimed" || got[1].Summary != "planned" {
		t.Fatalf("out of order: %q then %q", got[0].Summary, got[1].Summary)
	}
	if got[0].Detail["phase"] != "planning" {
		t.Errorf("detail = %v", got[0].Detail)
	}
}

// Evidence with no summary explains nothing, which is the one thing it exists
// to do.
func TestEvidenceNeedsASummaryAndAnActor(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	id := seedBacklogCard(t, s)

	for _, ev := range []CardEvidence{
		{ActorID: "worker-1"},
		{Summary: "did a thing"},
	} {
		if err := s.AttachEvidence(ctx, id, ev); err == nil {
			t.Errorf("accepted evidence %+v", ev)
		}
	}
}

// Nil detail is normal -- most steps have nothing structured to add -- and
// must not be an error or a stored "null" string.
func TestEvidenceWithoutDetailIsFine(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	id := seedBacklogCard(t, s)

	if err := s.AttachEvidence(ctx, id, CardEvidence{ActorID: "w", Summary: "s"}); err != nil {
		t.Fatalf("AttachEvidence: %v", err)
	}
	got, err := s.ListEvidence(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Detail != nil {
		t.Fatalf("evidence = %+v", got)
	}
}

func TestEvidenceForAMissingCardIsNotFound(t *testing.T) {
	s := migrated(t)

	err := s.AttachEvidence(context.Background(), uuid.New(), CardEvidence{ActorID: "w", Summary: "s"})
	if !errors.Is(err, ErrCardNotFound) {
		t.Fatalf("error = %v, want ErrCardNotFound", err)
	}
}
