// Package teststep implements §11.2, the test-writing phase.
//
// It runs a coding harness against the real repository to produce failing
// acceptance tests -- and nothing else. §11.2 is in capitals about the one
// thing it must not do: "The test-writing agent MUST NOT implement the
// requested feature."
package teststep

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/tuckermclean/strange-company/control-plane/internal/card"
	"github.com/tuckermclean/strange-company/control-plane/internal/codingrun"
	"github.com/tuckermclean/strange-company/control-plane/internal/policy"
	"github.com/tuckermclean/strange-company/control-plane/internal/redgate"
	"github.com/tuckermclean/strange-company/control-plane/internal/runner"
	"github.com/tuckermclean/strange-company/control-plane/internal/store"
	"github.com/tuckermclean/strange-company/control-plane/internal/worker"
)

// Board is what this step reads and records.
type Board interface {
	GetSpec(ctx context.Context, cardID uuid.UUID) (*store.CardSpec, error)
	ListArtifacts(ctx context.Context, cardID uuid.UUID) ([]*store.Artifact, error)
}

// Artifacts records evidence.
type Artifacts interface {
	PutArtifact(ctx context.Context, a store.Artifact) (*store.Artifact, error)
}

// Attempts is the run ledger (§12, §22).
//
// The tests phase runs a model exactly as the implementation phase does, and
// costs exactly as much. Recording only one of them left the ledger answering
// "what has this card cost?" with a fraction of the truth.
type Attempts interface {
	RecordAttempt(ctx context.Context, rec store.AttemptRecord) (*store.AttemptOutcome, error)
}

// Runner performs one coding run.
type Runner interface {
	Run(ctx context.Context, req codingrun.Request) (*runner.CodingRunResult, error)
}

// Verifier answers "did this ref's tests pass". Either backend satisfies it:
// a script in the repository, or the checks GitHub Actions already produced.
type Verifier interface {
	Verify(ctx context.Context, req codingrun.VerifyRequest) (redgate.RunOutcome, error)

	// TaskRequirement is what this backend needs the repository to provide,
	// in words for the test-writing agent, or "" when it needs nothing.
	//
	// The backend answers because the backend knows: asking unconditionally
	// for a test-command script had the agent commit one into a repository
	// whose gates read GitHub Actions and never open it.
	TaskRequirement() string
}

// Step is §11.2 as a worker step.
type Step struct {
	board     Board
	artifacts Artifacts
	attempts  Attempts
	runner    Runner
	verifier  Verifier
	git       codingrun.GitIdentity
	log       *slog.Logger
}

// New builds the test-writing step.
func New(b Board, a Artifacts, at Attempts, r Runner, v Verifier, git codingrun.GitIdentity, log *slog.Logger) *Step {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Step{board: b, artifacts: a, attempts: at, runner: r, verifier: v, git: git, log: log}
}

// Do writes the acceptance tests for one card.
func (s *Step) Do(ctx context.Context, c *card.Card, res *policy.Resolution) (worker.Evidence, error) {
	spec, err := s.board.GetSpec(ctx, c.ID)
	if err != nil {
		return worker.Evidence{}, fmt.Errorf("teststep: reading the specification: %w", err)
	}
	if spec == nil || strings.TrimSpace(spec.Content) == "" {
		return worker.Evidence{}, errors.New("teststep: the card has no specification")
	}

	plan, err := s.latestPlan(ctx, c.ID)
	if err != nil {
		// Planning runs first for a reason. Without its output the
		// test-writer is guessing, which is what §11.1 forbids of the
		// planner and is no better here.
		return worker.Evidence{}, err
	}

	if c.RepoURL == nil || *c.RepoURL == "" {
		return worker.Evidence{}, errors.New("teststep: the card names no repository to write tests in")
	}
	baseRef := "main"
	if c.RepoBaseRef != nil && *c.RepoBaseRef != "" {
		baseRef = *c.RepoBaseRef
	}

	branch := fmt.Sprintf("agent/%s", c.ID)
	runID := fmt.Sprintf("tests-%s-%d", shortID(c.ID), res.Attempt)
	result, err := s.runner.Run(ctx, codingrun.Request{
		CardID:     c.ID.String(),
		RunID:      runID,
		Task:       task(c, spec.Content, plan, s.verifier.TaskRequirement()),
		Resolution: res,
		RepoURL:    *c.RepoURL,
		BaseRef:    baseRef,
		// §16.2: agents only ever push their own agent/ branch.
		Branch:  branch,
		Phase:   string(card.PhaseTests),
		Attempt: res.Attempt,

		// Without this the Job clones and never pushes: the agent branch
		// is never created, so nothing has checks and the red gate has
		// nothing to compare.
		GitToken: s.git.Token, GitUsername: s.git.Username,
		GitAuthorName: s.git.AuthorName, GitAuthorEmail: s.git.AuthorEmail,
	})
	if err != nil {
		// See implstep: a harness with no adapter is a policy mistake, and
		// handing the card back would spin on it forever.
		if errors.Is(err, codingrun.ErrNoAdapter) {
			return worker.Evidence{
				Summary:   fmt.Sprintf("test-writing cannot run: %v", err),
				Detail:    map[string]any{"provider": res.ProviderName, "harness": string(res.Harness), "alias": res.Alias},
				NextState: card.NeedsHuman,
			}, nil
		}
		return worker.Evidence{}, fmt.Errorf("teststep: %w", err)
	}

	// The complete output, on EVERY run.
	//
	// This used to be kept only when a run went wrong, on the reasoning that
	// a healthy run's stream says nothing a human needs. That was backwards:
	// the cards that shipped cleanly ended up carrying the least evidence,
	// which is exactly the wrong way round for an audit surface. The Job is
	// deleted as soon as its logs are read, so this is the only copy that
	// survives either way.
	if len(result.Raw) > 0 {
		if _, aerr := s.artifacts.PutArtifact(ctx, store.Artifact{
			CardID: c.ID, Type: store.ArtifactRunLog, Actor: res.ProviderName,
			Model: result.Model, ContentType: "text/plain", Content: string(result.Raw),
		}); aerr != nil {
			s.log.Error("could not record the harness output", "card_id", c.ID, "error", aerr)
		}
	}

	// Before the switch below: an infrastructure failure returns an error
	// from here, and a run that cost money must be on the ledger whether or
	// not it produced anything.
	runner.Price(result, res.Pricing)
	if _, rerr := s.attempts.RecordAttempt(ctx, store.AttemptRecord{
		CardID: c.ID, RunID: runID, Phase: string(card.PhaseTests),
		ModelAlias: res.Alias, Provider: res.ProviderName,
		Harness: result.Harness, Model: result.Model, Result: result,
	}); rerr != nil {
		// Never fatal: losing the card would be worse than losing one row
		// of accounting.
		s.log.Error("could not record the attempt", "card_id", c.ID, "run_id", runID, "error", rerr)
	}

	// Only when the run actually produced something. This artifact claims to
	// BE the test mapping, and its content is the run's summary -- which for
	// a run that never completed is an error message.
	//
	// Writing it unconditionally put 262 artifacts on one card, each labelled
	// "test-mapping" and each containing a Kubernetes 404, during a
	// five-hour retry loop. That is not clutter, it is false evidence: an
	// audit surface asserting a mapping exists where nothing was mapped.
	if result.Status == runner.StatusCompleted {
		if _, aerr := s.artifacts.PutArtifact(ctx, store.Artifact{
			CardID:      c.ID,
			Type:        store.ArtifactTestMapping,
			Actor:       res.ProviderName,
			Model:       result.Model,
			ContentType: "text/markdown",
			Content:     result.Summary,
		}); aerr != nil {
			s.log.Error("could not record the test-writing run", "card_id", c.ID, "error", aerr)
		}
	}
	switch result.Status {
	case runner.StatusCompleted:
		return s.redGate(ctx, c, result, baseRef, branch)

	case runner.StatusInfraError, runner.StatusTimeout:
		// Returned as an error so the worker hands the card back for
		// another Meeseeks. §12.1: this is not a failed attempt, and the
		// phase must not advance -- there are no tests for an
		// implementation to be written against.
		return worker.Evidence{}, fmt.Errorf("teststep: run did not complete (%s): %s", result.Status, result.Summary)

	default:
		// The harness ran and did not produce tests. Advancing would send
		// an implementer at a red gate that cannot go green.
		return worker.Evidence{
			Summary:   fmt.Sprintf("test-writing did not complete: %s", result.Summary),
			Detail:    map[string]any{"status": string(result.Status), "harness": result.Harness},
			NextState: card.NeedsHuman,
		}, nil
	}
}

// latestPlan returns the most recent implementation plan for a card.
func (s *Step) latestPlan(ctx context.Context, cardID uuid.UUID) (string, error) {
	artifacts, err := s.board.ListArtifacts(ctx, cardID)
	if err != nil {
		return "", fmt.Errorf("teststep: reading artifacts: %w", err)
	}
	// Artifacts come back oldest first and accumulate, so the last plan is
	// the current one -- an earlier attempt's plan is history, not input.
	var plan string
	for _, a := range artifacts {
		if a.Type == store.ArtifactImplementationPlan {
			plan = a.Content
		}
	}
	if strings.TrimSpace(plan) == "" {
		return "", errors.New("teststep: the card has no implementation plan to write tests from")
	}
	return plan, nil
}

// shortID keeps a Kubernetes object name inside its length limit while staying
// recognisable in a namespace listing.
func shortID(id uuid.UUID) string {
	return strings.SplitN(id.String(), "-", 2)[0]
}

// task is the instruction handed to the harness.
//
// The prohibition is stated first and in the spec's own words. It cannot be
// enforced from here -- the red gate is what actually catches an implementation
// that slipped in, by failing when the new tests pass without one -- but a
// requirement that is never stated cannot be met either.
func task(c *card.Card, spec, plan, requirement string) string {
	var b strings.Builder
	b.WriteString("Write the acceptance tests for this work, using test-driven development.\n\n")
	b.WriteString("You MUST NOT implement the requested feature. Write only tests, and\n")
	b.WriteString("leave them failing against the current, unimplemented state. A test that\n")
	b.WriteString("passes right now is a test that is not testing this work.\n\n")
	b.WriteString("Produce: the test changes themselves, a mapping from each acceptance\n")
	b.WriteString("criterion to the test that covers it, and the command that runs them.\n\n")
	if requirement != "" {
		b.WriteString(requirement)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "# Card\n\n%s\n\n# Specification\n\n%s\n\n# Implementation plan\n\n%s\n",
		strings.TrimSpace(c.Title), strings.TrimSpace(spec), strings.TrimSpace(plan))
	return b.String()
}

// redGate is §11.3: the tests are executed against the unimplemented state, and
// no model grades them.
//
// Two runs of the same verification at two refs. The suite must pass at the
// base ref and fail with the new tests -- the difference is then the tests
// themselves, which is what makes the failure attributable without judgement.
// internal/redgate holds the comparison and states what it cannot catch.
func (s *Step) redGate(ctx context.Context, c *card.Card, result *runner.CodingRunResult, baseRef, branch string) (worker.Evidence, error) {
	repo := repositorySlug(c)

	baseline, err := s.verifier.Verify(ctx, codingrun.VerifyRequest{
		CardID: c.ID.String(), RunID: fmt.Sprintf("redbase-%s", shortID(c.ID)),
		Repository: repo, Ref: baseRef,
		RepoURL: *c.RepoURL, BaseRef: baseRef, Branch: branch,
		Phase: string(card.PhaseTests),
	})
	if err != nil {
		return worker.Evidence{}, fmt.Errorf("teststep: baseline verification: %w", err)
	}

	candidate, err := s.verifier.Verify(ctx, codingrun.VerifyRequest{
		CardID: c.ID.String(), RunID: fmt.Sprintf("redhead-%s", shortID(c.ID)),
		Repository: repo, Ref: branch,
		RepoURL: *c.RepoURL, BaseRef: baseRef, Branch: branch,
		Phase: string(card.PhaseTests),
	})
	if err != nil {
		return worker.Evidence{}, fmt.Errorf("teststep: verification of the new tests: %w", err)
	}

	outcome, why := redgate.Evaluate(baseline, candidate)

	if _, aerr := s.artifacts.PutArtifact(ctx, store.Artifact{
		CardID: c.ID, Type: store.ArtifactTestOutput, Actor: "red-gate",
		ContentType: "text/plain",
		Content:     fmt.Sprintf("red gate: %s\n\n%s", outcome, why),
	}); aerr != nil {
		s.log.Error("could not record the red gate result", "card_id", c.ID, "error", aerr)
	}

	if outcome == redgate.Inconclusive {
		// Not a verdict about the tests. Hand the card back so another
		// Meeseeks tries, rather than stopping it for an outage.
		return worker.Evidence{}, fmt.Errorf("teststep: red gate inconclusive: %s", why)
	}

	if !outcome.Proceeds() {
		// §11.3: the card does not proceed until a valid red state exists.
		return worker.Evidence{
			Summary:   fmt.Sprintf("red gate: %s", why),
			Detail:    map[string]any{"outcome": string(outcome), "harness": result.Harness},
			NextState: card.NeedsHuman,
		}, nil
	}

	return worker.Evidence{
		Summary:   fmt.Sprintf("acceptance tests written and red: %s", why),
		Detail:    map[string]any{"harness": result.Harness, "model": result.Model},
		NextPhase: card.PhaseImplementation,
	}, nil
}

// repositorySlug recovers "owner/name" for the checks API.
func repositorySlug(c *card.Card) string {
	if c.SourceExternalID != nil {
		if slug, _, ok := strings.Cut(*c.SourceExternalID, "#"); ok && slug != "" {
			return slug
		}
	}
	if c.RepoURL != nil {
		return strings.TrimSuffix(strings.TrimPrefix(*c.RepoURL, "https://github.com/"), ".git")
	}
	return ""
}
