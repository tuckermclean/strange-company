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

// Runner performs one coding run.
type Runner interface {
	Run(ctx context.Context, req codingrun.Request) (*runner.CodingRunResult, error)
}

// Step is §11.2 as a worker step.
type Step struct {
	board     Board
	artifacts Artifacts
	runner    Runner
	log       *slog.Logger
}

// New builds the test-writing step.
func New(b Board, a Artifacts, r Runner, log *slog.Logger) *Step {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Step{board: b, artifacts: a, runner: r, log: log}
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

	runID := fmt.Sprintf("tests-%s-%d", shortID(c.ID), res.Attempt)
	result, err := s.runner.Run(ctx, codingrun.Request{
		CardID:     c.ID.String(),
		RunID:      runID,
		Task:       task(c, spec.Content, plan),
		Resolution: res,
		RepoURL:    *c.RepoURL,
		BaseRef:    baseRef,
		// §16.2: agents only ever push their own agent/ branch.
		Branch:  fmt.Sprintf("agent/%s", c.ID),
		Phase:   string(card.PhaseTests),
		Attempt: res.Attempt,
	})
	if err != nil {
		return worker.Evidence{}, fmt.Errorf("teststep: %w", err)
	}

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

	switch result.Status {
	case runner.StatusCompleted:
		return worker.Evidence{
			Summary:   "acceptance tests written",
			Detail:    map[string]any{"harness": result.Harness, "model": result.Model},
			NextPhase: card.PhaseImplementation,
		}, nil

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
func task(c *card.Card, spec, plan string) string {
	var b strings.Builder
	b.WriteString("Write the acceptance tests for this work, using test-driven development.\n\n")
	b.WriteString("You MUST NOT implement the requested feature. Write only tests, and\n")
	b.WriteString("leave them failing against the current, unimplemented state. A test that\n")
	b.WriteString("passes right now is a test that is not testing this work.\n\n")
	b.WriteString("Produce: the test changes themselves, a mapping from each acceptance\n")
	b.WriteString("criterion to the test that covers it, and the command that runs them.\n\n")
	fmt.Fprintf(&b, "Commit the test command as an executable shell script at %s.\n", codingrun.TestCommandPath)
	b.WriteString("Both gates run that script and nothing else: the red gate to prove your\n")
	b.WriteString("tests fail now, and the green gate to prove they pass once the feature\n")
	b.WriteString("exists. Without it neither gate can run and the card stops here.\n\n")
	fmt.Fprintf(&b, "# Card\n\n%s\n\n# Specification\n\n%s\n\n# Implementation plan\n\n%s\n",
		strings.TrimSpace(c.Title), strings.TrimSpace(spec), strings.TrimSpace(plan))
	return b.String()
}
