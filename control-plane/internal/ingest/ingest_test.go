package ingest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/github"
	"github.com/tuckermclean/strange-company/control-plane/internal/ingest"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
)

type fakeSource struct {
	byRepo map[string][]github.Issue
	errs   map[string]error
	asked  []string
	label  string
}

func (f *fakeSource) ListLabeledIssues(_ context.Context, repo, label string) ([]github.Issue, error) {
	f.asked = append(f.asked, repo)
	f.label = label
	if err := f.errs[repo]; err != nil {
		return nil, err
	}
	return f.byRepo[repo], nil
}

type fakeBoard struct {
	upserted []store.SourceCard
	err      error
}

func (f *fakeBoard) UpsertSourceCard(_ context.Context, in store.SourceCard) (uuid.UUID, bool, error) {
	if f.err != nil {
		return uuid.Nil, false, f.err
	}
	f.upserted = append(f.upserted, in)
	return uuid.New(), true, nil
}

func issue(repo string, n int) github.Issue {
	return github.Issue{
		Repository: repo,
		Number:     n,
		Title:      "Add a health endpoint",
		Body:       "# Problem\n\nx",
		HTMLURL:    "https://github.com/" + repo + "/issues/7",
	}
}

func TestAnIssueBecomesACard(t *testing.T) {
	src := &fakeSource{byRepo: map[string][]github.Issue{"example/repo": {issue("example/repo", 7)}}}
	board := &fakeBoard{}

	res, err := ingest.New(src, board, []string{"example/repo"}, "agent-ready", []byte(`{"files":{"include":["**"]}}`), nil).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(board.upserted) != 1 {
		t.Fatalf("upserted %d cards", len(board.upserted))
	}
	got := board.upserted[0]
	if got.SourceType != "github" {
		t.Errorf("source type = %q", got.SourceType)
	}
	if got.ExternalID != "example/repo#7" {
		t.Errorf("external id = %q", got.ExternalID)
	}
	if got.Body != "# Problem\n\nx" {
		t.Errorf("body = %q; the issue text is the specification", got.Body)
	}
	// The coding runner has to know which repository to clone. An issue
	// that becomes a card with no repository is a card nothing can act on.
	if got.RepoURL != "https://github.com/example/repo" {
		t.Errorf("repo url = %q", got.RepoURL)
	}
	// A card with no allowlist can never pass 10's gate, so ingestion has
	// to stamp one at creation or the card is unworkable forever.
	if len(got.PermittedActions) == 0 {
		t.Error("ingested a card with no permitted-actions block")
	}
	if res.Created != 1 || res.Failed != 0 {
		t.Errorf("result = %+v", res)
	}
	if src.label != "agent-ready" {
		t.Errorf("asked for label %q", src.label)
	}
}

// One unreachable repository -- renamed, permissions revoked, typo in values
// -- must not stop every other repository from being ingested.
func TestOneUnreadableRepositoryDoesNotStopTheRest(t *testing.T) {
	src := &fakeSource{
		byRepo: map[string][]github.Issue{"good/repo": {issue("good/repo", 1)}},
		errs:   map[string]error{"bad/repo": errors.New("404")},
	}
	board := &fakeBoard{}

	res, err := ingest.New(src, board, []string{"bad/repo", "good/repo"}, "agent-ready", nil, nil).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce should not fail the pass: %v", err)
	}
	if len(board.upserted) != 1 {
		t.Fatalf("the healthy repository was skipped: %+v", board.upserted)
	}
	if res.Failed != 1 {
		t.Errorf("result = %+v", res)
	}
}

// A card that cannot be written is a card that is not ingested, and the next
// pass must try again rather than the pass aborting.
func TestAFailedWriteIsCountedAndThePassContinues(t *testing.T) {
	src := &fakeSource{byRepo: map[string][]github.Issue{
		"example/repo": {issue("example/repo", 1), issue("example/repo", 2)},
	}}
	board := &fakeBoard{err: errors.New("database down")}

	res, err := ingest.New(src, board, []string{"example/repo"}, "agent-ready", []byte(`{"files":{"include":["**"]}}`), nil).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Failed != 2 || res.Created != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestNoRepositoriesIsANoOp(t *testing.T) {
	src := &fakeSource{}
	board := &fakeBoard{}

	res, err := ingest.New(src, board, nil, "agent-ready", nil, nil).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(src.asked) != 0 || len(board.upserted) != 0 {
		t.Fatalf("did work with nothing configured: asked=%v upserted=%v", src.asked, board.upserted)
	}
	if res != (ingest.Result{}) {
		t.Fatalf("result = %+v", res)
	}
}
