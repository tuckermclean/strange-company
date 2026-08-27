package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// The session id is the entire handoff: without it, nobody can find the
// conversation a card was sent to, and the control plane would open a second
// one on the next pass.
func TestRecordSpecSessionIsReadBack(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id := seedBacklogCard(t, s)

	if err := s.RecordSpecSession(ctx, id, "api_1787781129_5343ebc5"); err != nil {
		t.Fatalf("RecordSpecSession: %v", err)
	}

	got, err := s.GetSpecSession(ctx, id)
	if err != nil {
		t.Fatalf("GetSpecSession: %v", err)
	}
	if got != "api_1787781129_5343ebc5" {
		t.Fatalf("session id = %q", got)
	}
}

func TestGetSpecSessionIsEmptyBeforeAnyConversation(t *testing.T) {
	s := openTestStore(t)
	id := seedBacklogCard(t, s)

	got, err := s.GetSpecSession(context.Background(), id)
	if err != nil {
		t.Fatalf("GetSpecSession: %v", err)
	}
	if got != "" {
		t.Fatalf("expected no session, got %q", got)
	}
}

// A second conversation for a card whose first one is still open would split
// the human's context across two dashboard sessions, and the card would end
// up pointing at only one of them.
func TestRecordSpecSessionRefusesToReplaceAnOpenConversation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id := seedBacklogCard(t, s)

	if err := s.RecordSpecSession(ctx, id, "api_first"); err != nil {
		t.Fatalf("first RecordSpecSession: %v", err)
	}

	err := s.RecordSpecSession(ctx, id, "api_second")
	if !errors.Is(err, ErrSpecSessionExists) {
		t.Fatalf("error = %v, want ErrSpecSessionExists", err)
	}

	got, _ := s.GetSpecSession(ctx, id)
	if got != "api_first" {
		t.Fatalf("session id = %q, want the original", got)
	}
}

// Recording the same id twice is what a retry after a lost response looks
// like, and it must not be an error.
func TestRecordingTheSameSessionTwiceIsNotAnError(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id := seedBacklogCard(t, s)

	if err := s.RecordSpecSession(ctx, id, "api_same"); err != nil {
		t.Fatalf("first RecordSpecSession: %v", err)
	}
	if err := s.RecordSpecSession(ctx, id, "api_same"); err != nil {
		t.Fatalf("second RecordSpecSession: %v", err)
	}
}

func TestRecordSpecSessionOnAMissingCardIsNotFound(t *testing.T) {
	s := openTestStore(t)
	// openTestStore resets the schema without migrating; the other tests
	// here migrate via seedBacklogCard, and this one has no card to seed.
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	err := s.RecordSpecSession(context.Background(), uuid.New(), "api_x")
	if !errors.Is(err, ErrCardNotFound) {
		t.Fatalf("error = %v, want ErrCardNotFound", err)
	}
}

func TestRecordSpecSessionRejectsAnEmptySessionID(t *testing.T) {
	s := openTestStore(t)
	id := seedBacklogCard(t, s)

	if err := s.RecordSpecSession(context.Background(), id, ""); err == nil {
		t.Fatal("expected an error for an empty session id")
	}
}
