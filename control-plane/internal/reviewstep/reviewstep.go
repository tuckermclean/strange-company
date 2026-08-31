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
	"time"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/github"
	"github.com/tuckermclean/strange-company/control-plane/internal/modelclient"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/runner"
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
//
// 8192 was not enough. On a live card DeepSeek spent the entire budget on
// reasoning and returned no answer, twice, before a retry happened to fit --
// and a limit that trips only sometimes reads as an intermittent provider
// fault rather than as a number set too low. No fixed number is safe against a
// model that thinks as long as it likes, so the step also retries once with a
// larger budget; see review().
const maxReviewTokens = 32768

// retryReviewTokens is the second and final budget. A model that cannot answer
// within this is not going to, and spending more is throwing money at it.
const retryReviewTokens = 4 * maxReviewTokens

// reviewTimeout is how long a reviewer may take.
//
// The client's three-minute default is right for a screening question and
// wrong here: a reasoning model reading a 400-line diff spends minutes
// thinking before it writes anything, and on every large card the read was cut
// off mid-answer. Because an aborted read is an infrastructure failure and
// infrastructure failures burn no attempt, the card was released and re-claimed
// on a loop -- spending a full reasoning call every four minutes, forever,
// while opening no pull request and escalating to nobody.
//
// Ten minutes is chosen against the lease rather than against a guess at how
// long a model thinks: the worker heartbeats the card for the whole step, so a
// call inside the lease cannot orphan it, and a provider that is genuinely hung
// still fails inside one lease period.
const reviewTimeout = 10 * time.Minute

// reviewIdleTimeout is how long the reviewer may go SILENT before the run is
// treated as dead.
//
// Not how long it may take. A model reasoning over a large diff is working the
// whole time and emits as it goes; one that has stopped emits nothing. Bounding
// the silence rather than the duration is what lets review scale to a diff of
// any size without a number in this file having to anticipate it.
const reviewIdleTimeout = 2 * time.Minute

// Completer performs one model completion.
//
// CompleteStreaming is preferred and Complete is the fallback: a reviewer
// reading a large diff thinks for minutes before writing anything, and a
// whole-call deadline has to be guessed against the biggest diff anyone will
// ever submit. Streaming replaces that guess with "has it gone quiet".
type Completer interface {
	Complete(ctx context.Context, req modelclient.CompleteRequest) (*modelclient.Completion, error)
}

// StreamingCompleter is implemented by clients that can stream. *modelclient.Client
// does; a fake in a test need not, and reviewstep falls back for anything that
// does not.
type StreamingCompleter interface {
	CompleteStreaming(ctx context.Context, req modelclient.CompleteRequest) (*modelclient.Completion, error)
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

// Attempts is the run ledger (§12, §22).
type Attempts interface {
	RecordAttempt(ctx context.Context, rec store.AttemptRecord) (*store.AttemptOutcome, error)
}

// Pulls opens the pull request a human reviews, and supplies the diff §18
// requires the reviewer to see.
type Pulls interface {
	EnsurePullRequest(ctx context.Context, pr github.PullRequest) (*github.OpenPullRequest, error)
	CompareDiff(ctx context.Context, repository, base, head string) (string, error)
}

// Step is §18 and §19 as a worker step.
type Step struct {
	board     Board
	artifacts Artifacts
	attempts  Attempts
	pulls     Pulls
	clientFor ClientFor
	log       *slog.Logger
}

// New builds the review step.
func New(b Board, a Artifacts, at Attempts, p Pulls, clientFor ClientFor, log *slog.Logger) *Step {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Step{board: b, artifacts: a, attempts: at, pulls: p, clientFor: clientFor, log: log}
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

	// §18: the reviewer receives the final diff. Fetched and refused-without,
	// because a reviewer with no code in front of it does not decline to
	// review -- it reviews the specification and describes an implementation
	// it has never seen. The first run to reach here passed a change while
	// confidently describing an export that was not in it.
	diff, err := s.diffFor(ctx, c)
	if err != nil {
		return worker.Evidence{}, fmt.Errorf("reviewstep: %w", err)
	}

	if _, aerr := s.artifacts.PutArtifact(ctx, store.Artifact{
		CardID: c.ID, Type: store.ArtifactDiff, Actor: "control-plane",
		ContentType: "text/x-diff", Content: diff,
	}); aerr != nil {
		s.log.Error("could not record the diff", "card_id", c.ID, "error", aerr)
	}

	messages := []modelclient.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: reviewInput(c, cardSpec.Content, artifacts, diff)},
	}
	completion, err := s.review(ctx, client, messages)

	// The exchange in full, whether or not it produced a verdict. §18's claim
	// is that the reviewer saw the diff; storing only the verdict leaves that
	// unverifiable, and a call that failed is exactly the one someone comes
	// looking for.
	if _, aerr := s.artifacts.PutArtifact(ctx, store.Artifact{
		CardID: c.ID, Type: store.ArtifactModelExchange, Actor: res.ProviderName,
		Model: res.Model, ContentType: "text/markdown",
		Content: modelclient.Transcript(
			modelclient.CompleteRequest{Messages: messages, MaxTokens: maxReviewTokens, Timeout: reviewTimeout},
			completion, err),
	}); aerr != nil {
		s.log.Error("could not record the review exchange", "card_id", c.ID, "error", aerr)
	}

	s.record(ctx, c, res, completion, err)
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
		// A correctable verdict spends an implementation attempt.
		//
		// Without this the card loops forever, and it did: implementation
		// completes, review says CORRECTABLE, implementation completes
		// again. §12.1 burns an attempt only on a run that FAILED, and both
		// of these runs succeed -- the coding run reached its terminal event
		// and the review call returned a verdict. So nothing counted, the
		// ladder never advanced, and a live card went round three times in
		// forty minutes spending a coding Job and a reasoning call each
		// time, with implementation_attempt still reading zero.
		//
		// The honest reading is that the work was not good enough. §19's
		// green gate passed and §18's review gate did not, and a gate
		// refusing the work is exactly what an implementation attempt
		// measures. Recording it here means the existing ladder bounds this:
		// after the last rung, §12.3 sends the card to a human.
		s.spendAttempt(ctx, c, res, completion)

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
func reviewInput(c *card.Card, specText string, artifacts []*store.Artifact, diff string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Card\n\n%s\n\n# Specification\n\n%s\n", strings.TrimSpace(c.Title), strings.TrimSpace(specText))
	if plan := latest(artifacts, store.ArtifactImplementationPlan); plan != "" {
		fmt.Fprintf(&b, "\n# Implementation plan\n\n%s\n", plan)
	}
	fmt.Fprintf(&b, "\n# Final diff\n\n```diff\n%s\n```\n", diff)
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

// diffFor returns the change this card actually made.
func (s *Step) diffFor(ctx context.Context, c *card.Card) (string, error) {
	repo, err := repositorySlug(c)
	if err != nil {
		return "", err
	}
	base := "main"
	if c.RepoBaseRef != nil && *c.RepoBaseRef != "" {
		base = *c.RepoBaseRef
	}
	return s.pulls.CompareDiff(ctx, repo, base, fmt.Sprintf("agent/%s", c.ID))
}

// review asks for the verdict, and asks once more with a bigger budget if the
// model spent the whole of the first one thinking.
//
// A reasoning model bills its thinking against max_tokens, so an exhausted
// budget is not the model failing -- it is the model not having been given
// room to answer. Retrying with the same number would just fail again.
func (s *Step) review(ctx context.Context, client Completer, msgs []modelclient.Message) (*modelclient.Completion, error) {
	completion, err := complete(ctx, client, modelclient.CompleteRequest{
		Messages: msgs, MaxTokens: maxReviewTokens, Timeout: reviewTimeout,
	})
	if !errors.Is(err, modelclient.ErrBudgetExhausted) {
		return completion, err
	}

	s.log.Warn("the reviewer spent its whole budget thinking; retrying with a larger one",
		"first", maxReviewTokens, "retry", retryReviewTokens, "error", err)
	return complete(ctx, client, modelclient.CompleteRequest{
		Messages: msgs, MaxTokens: retryReviewTokens, Timeout: reviewTimeout,
	})
}

// record puts the review on the run ledger (§12, §22).
//
// A review is a model call that costs money like any other. Recording only the
// implementation phase left the ledger answering "what has this card cost?"
// with a fraction of the truth, and left a card that failed review three times
// looking like it had never been reviewed at all.
func (s *Step) record(ctx context.Context, c *card.Card, res *policy.Resolution, completion *modelclient.Completion, runErr error) {
	if s.attempts == nil {
		return
	}

	result := &runner.CodingRunResult{
		Status:  runner.StatusCompleted,
		Harness: string(res.Harness),
		Summary: "review completed",
	}
	if completion != nil {
		result.Model = completion.Model
		result.Usage.InputTokens = completion.Usage.PromptTokens
		result.Usage.OutputTokens = completion.Usage.CompletionTokens
	}
	if runErr != nil {
		// §12.1: a provider that could not answer is not the model failing
		// the work, and must not burn a rung of the escalation ladder.
		result.Status = runner.StatusInfraError
		result.Summary = fmt.Sprintf("review did not complete: %v", runErr)
	}
	if result.Model == "" {
		result.Model = res.Model
	}

	// The gateway reports no cost at all, so this phase counted thousands of
	// tokens against nothing until the alias carried a rate card.
	runner.Price(result, res.Pricing)

	if _, err := s.attempts.RecordAttempt(ctx, store.AttemptRecord{
		CardID: c.ID, RunID: fmt.Sprintf("review-%s-%d", shortID(c.ID), res.Attempt),
		Phase: string(card.PhaseReview), ModelAlias: res.Alias, Provider: res.ProviderName,
		Harness: result.Harness, Model: result.Model, Result: result,
	}); err != nil {
		s.log.Error("could not record the review attempt", "card_id", c.ID, "error", err)
	}
}

func shortID(id uuid.UUID) string { return strings.SplitN(id.String(), "-", 2)[0] }

// complete streams when the client can, and falls back when it cannot.
//
// The idle timeout is what makes review scale to any diff: a whole-call
// deadline fails the large cards and only the large cards, which is how a
// green 400-line branch spent an evening being retried instead of shipped.
func complete(ctx context.Context, client Completer, req modelclient.CompleteRequest) (*modelclient.Completion, error) {
	if s, ok := client.(StreamingCompleter); ok {
		req.IdleTimeout = reviewIdleTimeout
		return s.CompleteStreaming(ctx, req)
	}
	return client.Complete(ctx, req)
}

// spendAttempt records that the review gate refused this implementation.
//
// Deliberately attributed to the IMPLEMENTATION phase rather than to review:
// what was found wanting is the implementation, the ladder it advances is the
// implementation ladder, and a reader looking at why a card escalated needs to
// see attempts against the work rather than against the judging of it.
func (s *Step) spendAttempt(ctx context.Context, c *card.Card, res *policy.Resolution, completion *modelclient.Completion) {
	if s.attempts == nil {
		return
	}

	model := res.Model
	if completion != nil && completion.Model != "" {
		model = completion.Model
	}

	if _, err := s.attempts.RecordAttempt(ctx, store.AttemptRecord{
		CardID: c.ID,
		RunID:  fmt.Sprintf("review-correctable-%s-%d", shortID(c.ID), res.Attempt),
		Phase:  string(card.PhaseImplementation),
		ModelAlias: res.Alias, Provider: res.ProviderName,
		Harness: string(res.Harness), Model: model,
		Result: &runner.CodingRunResult{
			Status:  runner.StatusFailed,
			Harness: string(res.Harness),
			Model:   model,
			Summary: "the review gate returned CORRECTABLE: the implementation passed its tests but did not pass review",
		},
	}); err != nil {
		s.log.Error("could not record the correctable verdict as an attempt",
			"card_id", c.ID, "error", err)
	}
}
