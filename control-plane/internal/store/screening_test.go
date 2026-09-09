package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// `text::bytea` parses the text as a bytea LITERAL rather than encoding it.
// In escape format a backslash begins an escape sequence, so a specification
// containing one -- a fenced code block with \n, a regex, a Windows path --
// makes the whole list query fail with 22P02, and promotion and screening are
// both dead for every card on the board, not just that one.
//
// Reported on a fresh 0.22.0 install (#135). It looked schema-dependent
// because it reproduced only after a clean-slate database; it is really
// content-dependent, and the clean slate simply ingested a different first
// issue.
func TestASpecificationContainingABackslashDoesNotBreakTheBoard(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	id := seedBacklogCard(t, s)

	// Every one of these is ordinary in a specification and every one of
	// them is an invalid bytea literal.
	for _, content := range []string{
		"# Problem\n\nMatch `\\d+` in the input.",
		"Use a path like C:\\Users\\agent\\thing.",
		"The prefix \\x is not followed by hex here.",
		"A trailing backslash \\",
	} {
		if err := s.PutSpec(ctx, id, content, "someone"); err != nil {
			t.Fatalf("PutSpec(%q): %v", content, err)
		}

		if _, err := s.ListSpecsNeedingScreening(ctx, 10); err != nil {
			t.Fatalf("ListSpecsNeedingScreening with content %q: %v", content, err)
		}
		if _, err := s.ListUnapprovedWithSpec(ctx, 10); err != nil {
			t.Fatalf("ListUnapprovedWithSpec with content %q: %v", content, err)
		}
		if _, err := s.ListSpecsAwaitingConversation(ctx, 10); err != nil {
			t.Fatalf("ListSpecsAwaitingConversation with content %q: %v", content, err)
		}
		if _, err := s.ListUnapprovedWithTasks(ctx, 10); err != nil {
			t.Fatalf("ListUnapprovedWithTasks with content %q: %v", content, err)
		}
	}
}

// The hash must not change for content that already worked, or every
// previously screened specification would come back around and cost a model
// call. Escape-format bytea passes non-backslash bytes through unchanged, so
// convert_to agrees with the old cast everywhere the old cast succeeded --
// which is the whole argument that this fix is free.
func TestTheContentHashIsUnchangedForContentThatAlreadyWorked(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	id := seedBacklogCard(t, s)

	const content = "# Problem\n\nplain ASCII, and some UTF-8: café — ✓"
	if err := s.PutSpec(ctx, id, content, "someone"); err != nil {
		t.Fatalf("PutSpec: %v", err)
	}

	pending, err := s.ListSpecsNeedingScreening(ctx, 10)
	if err != nil {
		t.Fatalf("ListSpecsNeedingScreening: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("got %d pending, want 1", len(pending))
	}

	// sha256 of the UTF-8 bytes, which is what the old cast also produced
	// for content with no backslashes.
	want := sha256Hex(content)
	if pending[0].ContentSHA256 != want {
		t.Errorf("hash = %s, want %s -- previously screened specs would all be re-screened",
			pending[0].ContentSHA256, want)
	}
}

func sha256Hex(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}
