package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func migrated(t *testing.T) *Store {
	t.Helper()
	s := openTestStore(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

// Screening costs a model call, so a card whose specification has not changed
// since it was screened must not come back around on the next pass. This is
// the whole reason the hash is stored.
func TestASpecIsOfferedForScreeningOnlyUntilItIsScreened(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	id := seedBacklogCard(t, s)

	if err := s.PutSpec(ctx, id, "# Problem\n\nsomething", "someone"); err != nil {
		t.Fatalf("PutSpec: %v", err)
	}

	pending, err := s.ListSpecsNeedingScreening(ctx, 10)
	if err != nil {
		t.Fatalf("ListSpecsNeedingScreening: %v", err)
	}
	if len(pending) != 1 || pending[0].CardID != id {
		t.Fatalf("pending = %+v, want the one unscreened card", pending)
	}

	if err := s.RecordScreening(ctx, id, pending[0].ContentSHA256, 2); err != nil {
		t.Fatalf("RecordScreening: %v", err)
	}

	pending, err = s.ListSpecsNeedingScreening(ctx, 10)
	if err != nil {
		t.Fatalf("ListSpecsNeedingScreening: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("an already-screened spec came back around: %+v", pending)
	}
}

// An edit after screening has to be re-screened: the answer was about the old
// text, and treating it as current is how a materially ambiguous rewrite
// slips through on a stale score.
func TestEditingAScreenedSpecMakesItPendingAgain(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	id := seedBacklogCard(t, s)

	if err := s.PutSpec(ctx, id, "first version", "someone"); err != nil {
		t.Fatalf("PutSpec: %v", err)
	}
	pending, _ := s.ListSpecsNeedingScreening(ctx, 10)
	if err := s.RecordScreening(ctx, id, pending[0].ContentSHA256, 0); err != nil {
		t.Fatalf("RecordScreening: %v", err)
	}

	if err := s.PutSpec(ctx, id, "second version", "someone"); err != nil {
		t.Fatalf("PutSpec: %v", err)
	}

	pending, err := s.ListSpecsNeedingScreening(ctx, 10)
	if err != nil {
		t.Fatalf("ListSpecsNeedingScreening: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("an edited spec was not re-offered: %+v", pending)
	}
}

// Recording against a hash that is no longer current means the document was
// edited while the model call was in flight. Storing it would mark the NEW
// text as screened using the OLD text's answer.
func TestRecordScreeningRefusesAStaleHash(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	id := seedBacklogCard(t, s)

	if err := s.PutSpec(ctx, id, "first version", "someone"); err != nil {
		t.Fatalf("PutSpec: %v", err)
	}
	pending, _ := s.ListSpecsNeedingScreening(ctx, 10)
	stale := pending[0].ContentSHA256

	if err := s.PutSpec(ctx, id, "edited mid-flight", "someone"); err != nil {
		t.Fatalf("PutSpec: %v", err)
	}

	err := s.RecordScreening(ctx, id, stale, 0)
	if !errors.Is(err, ErrSpecChanged) {
		t.Fatalf("error = %v, want ErrSpecChanged", err)
	}

	pending, _ = s.ListSpecsNeedingScreening(ctx, 10)
	if len(pending) != 1 {
		t.Fatal("the edited spec should still be pending")
	}
}

// The limit is what stops one pass from screening a thousand-card backlog at
// once, so it has to be honoured rather than advisory.
func TestListSpecsNeedingScreeningHonoursTheLimit(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		id := seedBacklogCard(t, s)
		if err := s.PutSpec(ctx, id, "spec text", "someone"); err != nil {
			t.Fatalf("PutSpec: %v", err)
		}
	}

	pending, err := s.ListSpecsNeedingScreening(ctx, 2)
	if err != nil {
		t.Fatalf("ListSpecsNeedingScreening: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("got %d specs, want the limit of 2", len(pending))
	}
}

// A card with no specification at all has nothing to screen; screening an
// empty document would spend a model call to be told it is empty, which the
// deterministic gate already knows.
func TestACardWithNoSpecIsNeverOfferedForScreening(t *testing.T) {
	s := migrated(t)
	seedBacklogCard(t, s)

	pending, err := s.ListSpecsNeedingScreening(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListSpecsNeedingScreening: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %+v, want nothing", pending)
	}
}

func TestRecordScreeningOnAMissingSpecIsNotFound(t *testing.T) {
	s := migrated(t)

	err := s.RecordScreening(context.Background(), uuid.New(), "deadbeef", 1)
	if !errors.Is(err, ErrSpecNotFound) {
		t.Fatalf("error = %v, want ErrSpecNotFound", err)
	}
}
