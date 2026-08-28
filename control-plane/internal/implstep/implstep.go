// Package implstep implements §12, the implementation phase and its green gate.
//
// This is where the escalation ladder actually moves. §12.1 defines an
// implementation attempt precisely -- the agent did work, the runner regained
// control, verification ran, and verification FAILED -- and this package is
// the only place that decides a run met that definition.
package implstep

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

// Board reads a card's inputs.
type Board interface {
	GetSpec(ctx context.Context, cardID uuid.UUID) (*store.CardSpec, error)
	ListArtifacts(ctx context.Context, cardID uuid.UUID) ([]*store.Artifact, error)
}

// Artifacts records evidence.
type Artifacts interface {
	PutArtifact(ctx context.Context, a store.Artifact) (*store.Artifact, error)
}

// Attempts records what a run counted as.
type Attempts interface {
	RecordAttempt(ctx context.Context, rec store.AttemptRecord) (*store.AttemptOutcome, error)
}

// Runner performs coding and verification runs.
type Runner interface {
	Run(ctx context.Context, req codingrun.Request) (*runner.CodingRunResult, error)
	Verify(ctx context.Context, req codingrun.VerifyRequest) (redgate.RunOutcome, error)
}

// Step is §12 as a worker step.
type Step struct {
	board     Board
	artifacts Artifacts
	attempts  Attempts
	runner    Runner
	log       *slog.Logger
}

// New builds the implementation step.
func New(b Board, a Artifacts, at Attempts, r Runner, log *slog.Logger) *Step {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Step{board: b, artifacts: a, attempts: at, runner: r, log: log}
}

// Do performs one implementation attempt.
func (s *Step) Do(ctx context.Context, c *card.Card, res *policy.Resolution) (worker.Evidence, error) {
	spec, err := s.board.GetSpec(ctx, c.ID)
	if err != nil || spec == nil || strings.TrimSpace(spec.Content) == "" {
		return worker.Evidence{}, errors.New("implstep: the card has no specification to implement")
	}
	artifacts, err := s.board.ListArtifacts(ctx, c.ID)
	if err != nil {
		return worker.Evidence{}, fmt.Errorf("implstep: reading artifacts: %w", err)
	}
	if c.RepoURL == nil || *c.RepoURL == "" {
		return worker.Evidence{}, errors.New("implstep: the card names no repository")
	}

	baseRef := "main"
	if c.RepoBaseRef != nil && *c.RepoBaseRef != "" {
		baseRef = *c.RepoBaseRef
	}
	branch := fmt.Sprintf("agent/%s", c.ID)
	runID := fmt.Sprintf("impl-%s-%d", shortID(c.ID), res.Attempt)

	result, err := s.runner.Run(ctx, codingrun.Request{
		CardID: c.ID.String(), RunID: runID,
		Task:       task(c, spec.Content, artifacts, res.Attempt),
		Resolution: res,
		RepoURL:    *c.RepoURL, BaseRef: baseRef, Branch: branch,
		Phase: string(card.PhaseImplementation), Attempt: res.Attempt,
	})
	if err != nil {
		return worker.Evidence{}, fmt.Errorf("implstep: %w", err)
	}

	// Recorded before the green gate runs. §12.1 classifies by status, and a
	// run that never happened must be on the ledger as infrastructure even
	// though no attempt was spent.
	if result.Status != runner.StatusCompleted {
		s.record(ctx, c, res, runID, result)
		return worker.Evidence{}, fmt.Errorf("implstep: run did not complete (%s): %s", result.Status, result.Summary)
	}

	// §19's green gate: the tests the red gate proved failing must now pass.
	// The model's own account of success is not evidence of it.
	verdict, err := s.runner.Verify(ctx, codingrun.VerifyRequest{
		CardID: c.ID.String(), RunID: runID + "-verify",
		RepoURL: *c.RepoURL, BaseRef: baseRef, Branch: branch,
		Phase: string(card.PhaseImplementation), Attempt: res.Attempt,
	})
	if err != nil {
		return worker.Evidence{}, fmt.Errorf("implstep: verification: %w", err)
	}
	if !verdict.Completed {
		// Not a green light and not a failed attempt: an outage. The
		// ladder must not move on it.
		infra := *result
		infra.Status = runner.StatusInfraError
		infra.Summary = "the verification run did not complete"
		s.record(ctx, c, res, runID, &infra)
		return worker.Evidence{}, errors.New("implstep: the verification run did not complete")
	}

	if verdict.ExitCode == 0 {
		s.record(ctx, c, res, runID, result)
		// To the review PHASE, not the Review state. §18's automated
		// review runs after the green gate and before a human sees
		// anything, and a card in the Review state is not claimable --
		// moving there now would strand it before it was reviewed.
		return worker.Evidence{
			Summary:   "implementation verified: the acceptance tests pass",
			Detail:    map[string]any{"harness": result.Harness, "model": result.Model, "attempt": res.Attempt},
			NextPhase: card.PhaseReview,
		}, nil
	}

	// §12.1's implementation attempt: work happened, verification ran, and
	// it failed. This is the only path that moves the ladder.
	failed := *result
	failed.Status = runner.StatusFailed
	failed.Summary = fmt.Sprintf("the acceptance tests still fail after this attempt: %s", result.Summary)
	s.record(ctx, c, res, runID, &failed)

	return worker.Evidence{
		Summary:   fmt.Sprintf("attempt %d did not make the tests pass", res.Attempt),
		Detail:    map[string]any{"model": result.Model, "attempt": res.Attempt},
		NextPhase: card.PhaseImplementation,
	}, nil
}

// record writes the ledger row. A failure to record is logged, never fatal:
// losing the card would be worse than losing one row of accounting.
func (s *Step) record(ctx context.Context, c *card.Card, res *policy.Resolution, runID string, result *runner.CodingRunResult) {
	if _, err := s.attempts.RecordAttempt(ctx, store.AttemptRecord{
		CardID: c.ID, RunID: runID, Phase: string(card.PhaseImplementation),
		ModelAlias: res.Alias, Provider: res.ProviderName,
		Harness: result.Harness, Model: result.Model, Result: result,
	}); err != nil {
		s.log.Error("could not record the attempt", "card_id", c.ID, "run_id", runID, "error", err)
	}
}

func shortID(id uuid.UUID) string { return strings.SplitN(id.String(), "-", 2)[0] }

// task assembles §12.2's feedback.
//
// "The model does not receive seven pages of previous model monologue. It
// receives evidence." So a retry gets the specification, the plan, and the
// failing test output -- and deliberately not the previous attempt's own
// account of what it was thinking.
func task(c *card.Card, spec string, artifacts []*store.Artifact, attempt int) string {
	var b strings.Builder
	b.WriteString("Implement this work so its acceptance tests pass.\n\n")
	b.WriteString("You MUST NOT change the tests. They are the contract the red gate\n")
	b.WriteString("already proved fails without this feature; an implementation that edits\n")
	b.WriteString("them can make anything pass.\n\n")
	fmt.Fprintf(&b, "# Card\n\n%s\n\n# Specification\n\n%s\n", strings.TrimSpace(c.Title), strings.TrimSpace(spec))

	if plan := latest(artifacts, store.ArtifactImplementationPlan); plan != "" {
		fmt.Fprintf(&b, "\n# Implementation plan\n\n%s\n", plan)
	}

	if attempt > 1 {
		// Evidence, not narrative. Test output and compiler output are
		// deterministic facts about what happened; a previous model's
		// summary of its own reasoning is not.
		if out := latest(artifacts, store.ArtifactTestOutput); out != "" {
			fmt.Fprintf(&b, "\n# Failing test output from the previous attempt\n\n```\n%s\n```\n", out)
		}
		if out := latest(artifacts, store.ArtifactCompilerOutput); out != "" {
			fmt.Fprintf(&b, "\n# Compiler output from the previous attempt\n\n```\n%s\n```\n", out)
		}
		fmt.Fprintf(&b, "\nThis is attempt %d. Earlier attempts did not make the tests pass.\n", attempt)
	}
	return b.String()
}

// latest returns the most recent artifact of a type. Artifacts accumulate, so
// the last one is the current one.
func latest(artifacts []*store.Artifact, kind string) string {
	var found string
	for _, a := range artifacts {
		if a.Type == kind {
			found = a.Content
		}
	}
	return strings.TrimSpace(found)
}
