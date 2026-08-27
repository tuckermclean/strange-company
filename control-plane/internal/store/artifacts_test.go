package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func artifactOf(cardID uuid.UUID, kind, content string) Artifact {
	return Artifact{
		CardID:      cardID,
		Type:        kind,
		Actor:       "control-plane",
		ContentType: "text/plain",
		Content:     content,
	}
}

func TestAnArtifactIsStoredAndReadBack(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	id := seedBacklogCard(t, s)

	stored, err := s.PutArtifact(ctx, artifactOf(id, ArtifactImplementationPlan, "step one"))
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}

	got, err := s.GetArtifact(ctx, stored.ID)
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if got.Content != "step one" || got.Type != ArtifactImplementationPlan {
		t.Fatalf("artifact = %+v", got)
	}
	if got.SizeBytes != int64(len("step one")) {
		t.Errorf("size = %d", got.SizeBytes)
	}
	if got.Truncated {
		t.Error("a short artifact was marked truncated")
	}
}

// §20 lists the types. An unknown one is a caller bug, and accepting it would
// let a typo produce evidence nothing ever looks for.
func TestAnUnknownArtifactTypeIsRefused(t *testing.T) {
	s := migrated(t)
	id := seedBacklogCard(t, s)

	_, err := s.PutArtifact(context.Background(), artifactOf(id, "implementation_plan", "x"))
	if !errors.Is(err, ErrUnknownArtifactType) {
		t.Fatalf("error = %v, want ErrUnknownArtifactType", err)
	}
}

// The hash has to describe what the run PRODUCED. Hashing the stored copy
// would certify a truncated artifact as the whole thing, which is worse than
// storing nothing: it looks verifiable.
func TestTheHashCoversTheCompleteContentEvenWhenTruncated(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	id := seedBacklogCard(t, s)

	full := strings.Repeat("x", MaxArtifactBytes+4096)
	sum := sha256.Sum256([]byte(full))
	want := hex.EncodeToString(sum[:])

	stored, err := s.PutArtifact(ctx, artifactOf(id, ArtifactTestOutput, full))
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}

	got, err := s.GetArtifact(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != want {
		t.Errorf("sha256 describes the stored copy, not what was produced")
	}
	if !got.Truncated {
		t.Error("a capped artifact is not marked truncated, so a partial log reads as a complete one")
	}
	if got.SizeBytes != int64(len(full)) {
		t.Errorf("size = %d, want the complete length %d", got.SizeBytes, len(full))
	}
	if len(got.Content) > MaxArtifactBytes {
		t.Errorf("stored %d bytes, above the cap", len(got.Content))
	}
}

// Attempt 4's diff must not replace attempt 3's, or the escalation record
// describes a history that no longer exists.
func TestArtifactsAccumulateRatherThanReplace(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	id := seedBacklogCard(t, s)

	for _, body := range []string{"first", "second"} {
		if _, err := s.PutArtifact(ctx, artifactOf(id, ArtifactDiff, body)); err != nil {
			t.Fatalf("PutArtifact(%q): %v", body, err)
		}
	}

	list, err := s.ListArtifacts(ctx, id)
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d artifacts, want both", len(list))
	}
	if list[0].Content != "first" || list[1].Content != "second" {
		t.Fatalf("artifacts are not in the order they were produced: %q then %q",
			list[0].Content, list[1].Content)
	}
}

// "What happened to card X?" is answered per card, so an artifact belonging to
// another card must never appear.
func TestArtifactsAreScopedToTheirCard(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	mine := seedBacklogCard(t, s)
	theirs := seedBacklogCard(t, s)

	if _, err := s.PutArtifact(ctx, artifactOf(theirs, ArtifactReview, "not mine")); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListArtifacts(ctx, mine)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("saw another card's artifacts: %+v", list)
	}
}

func TestAnArtifactRequiresACardAnActorAndAContentType(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	id := seedBacklogCard(t, s)

	for _, tc := range []struct {
		name string
		mut  func(*Artifact)
	}{
		{"no actor", func(a *Artifact) { a.Actor = "" }},
		{"no content type", func(a *Artifact) { a.ContentType = "" }},
		{"no card", func(a *Artifact) { a.CardID = uuid.Nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := artifactOf(id, ArtifactDiff, "x")
			tc.mut(&a)
			if _, err := s.PutArtifact(ctx, a); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestAnArtifactForAMissingCardIsNotFound(t *testing.T) {
	s := migrated(t)

	_, err := s.PutArtifact(context.Background(), artifactOf(uuid.New(), ArtifactDiff, "x"))
	if !errors.Is(err, ErrCardNotFound) {
		t.Fatalf("error = %v, want ErrCardNotFound", err)
	}
}
