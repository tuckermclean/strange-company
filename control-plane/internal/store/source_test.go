package store

import (
	"context"
	"testing"

	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/runner"
)

func issue(n string) SourceCard {
	return SourceCard{
		SourceType: "github",
		ExternalID: n,
		URL:        "https://github.com/example/repo/issues/" + n,
		Title:      "Add a health endpoint",
		Body:       "# Problem\n\nhealth is unobservable\n",
		RepoURL:    "https://github.com/example/repo",
		RepoRef:    "main",
	}
}

// Ingestion runs on a timer and sees the same issue every pass. Creating a
// card each time would fill the board with duplicates of one piece of work.
func TestIngestingTheSameIssueTwiceYieldsOneCard(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()

	first, created, err := s.UpsertSourceCard(ctx, issue("7"))
	if err != nil {
		t.Fatalf("UpsertSourceCard: %v", err)
	}
	if !created {
		t.Fatal("first ingestion did not report the card as new")
	}

	second, created, err := s.UpsertSourceCard(ctx, issue("7"))
	if err != nil {
		t.Fatalf("UpsertSourceCard: %v", err)
	}
	if created {
		t.Error("second ingestion reported the card as new")
	}
	if second != first {
		t.Fatalf("got a different card: %s then %s", first, second)
	}
}

// Two issues that happen to share a number in different repositories are
// different work. Identity has to include the repository.
func TestDifferentIssuesAreDifferentCards(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()

	a, _, err := s.UpsertSourceCard(ctx, issue("7"))
	if err != nil {
		t.Fatal(err)
	}
	other := issue("7")
	other.ExternalID = "other/repo#7"
	b, _, err := s.UpsertSourceCard(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two distinct issues collapsed into one card")
	}
}

// The single most dangerous thing a poller can do: drag a card that is being
// worked on back to the start because the issue still exists upstream.
func TestReingestionNeverDisturbsExecutionState(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()

	id, _, err := s.UpsertSourceCard(ctx, issue("7"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PromoteToReady(ctx, id, "test"); err != nil {
		t.Fatalf("PromoteToReady: %v", err)
	}
	if _, err := s.RecordAttempt(ctx, recordOf(id, "run-1", resultOf(runner.StatusFailed))); err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}

	renamed := issue("7")
	renamed.Title = "Add a health endpoint (revised)"
	if _, _, err := s.UpsertSourceCard(ctx, renamed); err != nil {
		t.Fatalf("re-ingest: %v", err)
	}

	c, err := s.GetCard(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if c.State != card.Ready {
		t.Errorf("state = %s, want Ready; ingestion moved a card that was in flight", c.State)
	}
	if c.ImplementationAttempt != 1 {
		t.Errorf("implementation_attempt = %d, want 1; ingestion reset the ladder", c.ImplementationAttempt)
	}
	if c.Title != "Add a health endpoint (revised)" {
		t.Errorf("title = %q; the editable half was not updated", c.Title)
	}
}

// Vikunja owns the title and description (§4.3), and the control plane owns
// execution state. Ingestion writes only what the source owns.
func TestIngestionStoresTheIssueBodyAsTheSpecification(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()

	id, _, err := s.UpsertSourceCard(ctx, issue("7"))
	if err != nil {
		t.Fatal(err)
	}

	spec, err := s.GetSpec(ctx, id)
	if err != nil {
		t.Fatalf("GetSpec: %v", err)
	}
	if spec.Content != "# Problem\n\nhealth is unobservable\n" {
		t.Fatalf("spec content = %q", spec.Content)
	}
}

// Approval is of a document. An issue edited after a human approved it is no
// longer that document, and promoting on the strength of the old approval
// would run work nobody agreed to.
func TestEditingTheIssueAfterApprovalRevokesIt(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()

	id, _, err := s.UpsertSourceCard(ctx, issue("7"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveSpec(ctx, id, "a-human"); err != nil {
		t.Fatalf("ApproveSpec: %v", err)
	}

	edited := issue("7")
	edited.Body = "# Problem\n\nsomething else entirely\n"
	if _, _, err := s.UpsertSourceCard(ctx, edited); err != nil {
		t.Fatal(err)
	}

	spec, err := s.GetSpec(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if spec.ApprovedBy != nil {
		t.Fatalf("approval survived an edit to the issue: %+v", spec.ApprovedBy)
	}
}

// An unchanged body must not revoke an approval -- ingestion runs every
// minute, and re-writing an identical spec would clear approval forever.
func TestReingestingAnUnchangedIssueKeepsTheApproval(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()

	id, _, err := s.UpsertSourceCard(ctx, issue("7"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveSpec(ctx, id, "a-human"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertSourceCard(ctx, issue("7")); err != nil {
		t.Fatal(err)
	}

	spec, err := s.GetSpec(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if spec.ApprovedBy == nil {
		t.Fatal("an unchanged re-ingestion revoked the approval")
	}
}

func TestIngestionRequiresAnIdentityAndATitle(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		in   SourceCard
	}{
		{"no source type", SourceCard{ExternalID: "x", Title: "t"}},
		{"no external id", SourceCard{SourceType: "github", Title: "t"}},
		{"no title", SourceCard{SourceType: "github", ExternalID: "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := s.UpsertSourceCard(ctx, tc.in); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
