// Package reviewstep implements §18's automated review and §19's pull request.
//
// §18 ends in bold: "Automated review cannot move a card to Done." Nothing in
// this package can produce that transition, and a test asserts it for every
// verdict including one nobody can read.
package reviewstep

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/github"
	"github.com/tuckermclean/strange-company/control-plane/internal/modelclient"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/spec"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
	"github.com/tuckermclean/strange-company/control-plane/internal/worker"
)

// verdictPrefix is how the reviewer states its result.
//
// Structural, like the planner's SPEC_INSUFFICIENT: reading a verdict out of
// prose would make "passes review" depend on phrasing, and a reviewer that
// merely sounded approving would ship code.
const verdictPrefix = "VERDICT:"

// See internal/ambiguity: a reasoning model's thinking is billed against this
// budget, so it is sized for thinking plus the answer, not the answer alone.
const maxReviewTokens = 8192

// Completer performs one model completion.
type Completer interface {
	Complete(ctx context.Context, req modelclient.CompleteRequest) (*modelclient.Completion, error)
}

// ClientFor builds a Completer for a resolved rung.
type ClientFor func(*policy.Resolution) (Completer, error)

// Board reads a card's inputs and records the review.
type Board interface {
	GetSpec(ctx context.Context, cardID uuid.UUID) (*store.CardSpec, error)
	ListArtifacts(ctx context.Context, cardID uuid.UUID) ([]*store.Artifact, error)
}

// Artifacts records evidence.
type Artifacts interface {
	PutArtifact(ctx context.Context, a store.Artifact) (*store.Artifact, error)
}

// Pulls opens the pull request a human reviews.
type Pulls interface {
	EnsurePullRequest(ctx context.Context, pr github.PullRequest) (*github.OpenPullRequest, error)
}

// Step is §18 and §19 as a worker step.
type Step struct {
	board     Board
	artifacts Artifacts
	pulls     Pulls
	clientFor ClientFor
	log       *slog.Logger
}

// New builds the review step.
func New(b Board, a Artifacts, p Pulls, clientFor ClientFor, log *slog.Logger) *Step {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Step{board: b, artifacts: a, pulls: p, clientFor: clientFor, log: log}
}

// Do reviews one card.
func (s *Step) Do(ctx context.Context, c *card.Card, res *policy.Resolution) (worker.Evidence, error) {
	cardSpec, err := s.board.GetSpec(ctx, c.ID)
	if err != nil || cardSpec == nil {
		return worker.Evidence{}, errors.New("reviewstep: the card has no specification to review against")
	}
	artifacts, err := s.board.ListArtifacts(ctx, c.ID)
	if err != nil {
		return worker.Evidence{}, fmt.Errorf("reviewstep: reading artifacts: %w", err)
	}

	client, err := s.clientFor(res)
	if err != nil {
		return worker.Evidence{}, fmt.Errorf("reviewstep: no model client: %w", err)
	}

	completion, err := client.Complete(ctx, modelclient.CompleteRequest{
		Messages: []modelclient.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: reviewInput(c, cardSpec.Content, artifacts)},
		},
		MaxTokens: maxReviewTokens,
	})
	if err != nil {
		return worker.Evidence{}, fmt.Errorf("reviewstep: %w", err)
	}

	text := strings.TrimSpace(completion.Text)
	if _, aerr := s.artifacts.PutArtifact(ctx, store.Artifact{
		CardID: c.ID, Type: store.ArtifactReview, Actor: res.ProviderName,
		Model: completion.Model, ContentType: "text/markdown", Content: text,
	}); aerr != nil {
		// Not fatal, but loud: a card in Review with no record of what the
		// reviewer said leaves the human approving it nothing to read.
		s.log.Error("could not record the review", "card_id", c.ID, "error", aerr)
	}

	switch verdict(text) {
	case "PASS":
		pr, err := s.openPullRequest(ctx, c, cardSpec.Content, text)
		if err != nil {
			return worker.Evidence{}, fmt.Errorf("reviewstep: %w", err)
		}
		// §19: the human is the final merge authority. The card stops here
		// and waits for one; Review -> Done is human-only in the state
		// machine, so this cannot go further even by mistake.
		return worker.Evidence{
			Summary:   fmt.Sprintf("review passed; pull request %s", pr.URL),
			Detail:    map[string]any{"pull_request": pr.URL, "model": completion.Model},
			NextState: card.Review,
		}, nil

	case "CORRECTABLE":
		return worker.Evidence{
			Summary:   "review found correctable problems; returning to implementation",
			Detail:    map[string]any{"model": completion.Model},
			NextPhase: card.PhaseImplementation,
		}, nil

	case "BLOCKING":
		return worker.Evidence{
			Summary:   "review is blocking; a human needs to look",
			Detail:    map[string]any{"model": completion.Model},
			NextState: card.NeedsHuman,
		}, nil

	default:
		// Fail closed. A verdict nobody can read is not a pass, and
		// guessing which of three outcomes was meant would eventually
		// guess "ship it".
		return worker.Evidence{
			Summary:   "the reviewer did not state a readable verdict; a human needs to look",
			Detail:    map[string]any{"model": completion.Model},
			NextState: card.NeedsHuman,
		}, nil
	}
}

// verdict extracts the stated result, or "" when there is not one.
func verdict(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, verdictPrefix) {
			continue
		}
		switch v := strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(line, verdictPrefix))); v {
		case "PASS", "CORRECTABLE", "BLOCKING":
			return v
		}
	}
	return ""
}

func (s *Step) openPullRequest(ctx context.Context, c *card.Card, specText, review string) (*github.OpenPullRequest, error) {
	repo, err := repositorySlug(c)
	if err != nil {
		return nil, err
	}
	base := "main"
	if c.RepoBaseRef != nil && *c.RepoBaseRef != "" {
		base = *c.RepoBaseRef
	}
	return s.pulls.EnsurePullRequest(ctx, github.PullRequest{
		Repository: repo,
		Head:       fmt.Sprintf("agent/%s", c.ID),
		Base:       base,
		Title:      strings.TrimSpace(c.Title),
		Body:       pullRequestBody(c, specText, review),
	})
}

// repositorySlug recovers "owner/name" from the card.
func repositorySlug(c *card.Card) (string, error) {
	if c.SourceExternalID != nil {
		if slug, _, ok := strings.Cut(*c.SourceExternalID, "#"); ok && slug != "" {
			return slug, nil
		}
	}
	if c.RepoURL != nil {
		trimmed := strings.TrimSuffix(strings.TrimPrefix(*c.RepoURL, "https://github.com/"), ".git")
		if strings.Count(trimmed, "/") == 1 {
			return trimmed, nil
		}
	}
	return "", errors.New("reviewstep: cannot tell which repository this card belongs to")
}

// pullRequestBody is §25's outbound content: card link, acceptance-criterion
// checklist, verification summary and the review result.
func pullRequestBody(c *card.Card, specText, review string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Card `%s` — %s\n\n", c.ID, strings.TrimSpace(c.Title))

	doc, _ := spec.Parse(c.ID.String(), []byte(specText))
	if doc != nil && len(doc.Criteria) > 0 {
		b.WriteString("## Acceptance criteria\n\n")
		for _, cr := range doc.Criteria {
			// Checked because the green gate passed: §19 only reaches a
			// pull request when the deterministic verification is green.
			//
			// The id is optional -- a criterion written without an "AC1:"
			// prefix parses with an empty one -- so it is only rendered
			// when there is one. Otherwise every line began "- [x] : ".
			if id := strings.TrimSpace(cr.ID); id != "" {
				fmt.Fprintf(&b, "- [x] %s: %s\n", id, strings.TrimSpace(cr.Text))
				continue
			}
			fmt.Fprintf(&b, "- [x] %s\n", strings.TrimSpace(cr.Text))
		}
		b.WriteString("\n")
	}

	b.WriteString("## Automated review\n\n")
	b.WriteString(strings.TrimSpace(review))
	b.WriteString("\n\nA human is the final merge authority (spec §19).\n")
	return b.String()
}

// systemPrompt states §18's two stages and its one prohibition.
const systemPrompt = `You are reviewing a completed change, independently of whoever wrote it.

Review in two stages, in this order:
1. Specification compliance: does the change do what the specification asked,
   and only that?
2. Code quality: is it correct, clear, and consistent with the codebase?

The deterministic verification already passed. You are not re-running tests;
you are judging whether the work is right.

State your result on its own line, exactly one of:

VERDICT: PASS
VERDICT: CORRECTABLE
VERDICT: BLOCKING

PASS means it should go to a human to merge. CORRECTABLE means an implementer
can fix it from your notes. BLOCKING means something is wrong that another
implementation attempt should not try to work around.

Then explain, briefly, in terms of the change itself.`

// reviewInput assembles what §18 says the reviewer receives -- and nothing
// else.
//
// The exclusion is the point: "The reviewer does NOT receive the implementer's
// private reasoning." Failure summaries carry a model's own account of itself,
// so they are deliberately not gathered here.
func reviewInput(c *card.Card, specText string, artifacts []*store.Artifact) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Card\n\n%s\n\n# Specification\n\n%s\n", strings.TrimSpace(c.Title), strings.TrimSpace(specText))
	if plan := latest(artifacts, store.ArtifactImplementationPlan); plan != "" {
		fmt.Fprintf(&b, "\n# Implementation plan\n\n%s\n", plan)
	}
	if diff := latest(artifacts, store.ArtifactDiff); diff != "" {
		fmt.Fprintf(&b, "\n# Final diff\n\n```diff\n%s\n```\n", diff)
	}
	if out := latest(artifacts, store.ArtifactTestOutput); out != "" {
		fmt.Fprintf(&b, "\n# Verification (passing)\n\n```\n%s\n```\n", out)
	}
	return b.String()
}

func latest(artifacts []*store.Artifact, kind string) string {
	var found string
	for _, a := range artifacts {
		if a.Type == kind {
			found = a.Content
		}
	}
	return strings.TrimSpace(found)
}
